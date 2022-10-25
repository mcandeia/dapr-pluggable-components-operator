/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	componentsapi "github.com/dapr/dapr/pkg/apis/components/v1alpha1"

	"github.com/pkg/errors"
)

const (
	autoInjectComponentsEnabledAnnotation = "components.dapr.io/enabled"
	checkSumComponentsAnnotation          = "components.dapr.io/checksum"
	containerImageAnnotation              = "components.dapr.io/container-image"
	volumeMountsAnnotation                = "components.dapr.io/container-volume-mounts"
	containerEnvAnnotation                = "components.dapr.io/container-env"
	daprEnabledAnnotation                 = "dapr.io/enabled"
	appIDAnnotation                       = "dapr.io/app-id"
	defaultSocketPath                     = "/tmp/dapr-components-sockets"
	daprComponentsSocketVolumeName        = "dapr-components-unix-domain-socket"
	daprdContainerName                    = "daprd"
)

// PodReconciler reconciles a Pod object
type PodReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// toEnvVariables converts from component env annotations to container env variables.
func toEnvVariables(envAnnotation string) []corev1.EnvVar {
	envVars := make([]corev1.EnvVar, 0)
	if envAnnotation == "" {
		return envVars
	}

	envs := strings.Split(envAnnotation, ",")

	for _, envVar := range envs {
		nameAndValue := strings.Split(envVar, ";")
		if len(nameAndValue) != 2 {
			continue
		}
		envVars = append(envVars, corev1.EnvVar{
			Name:  nameAndValue[0],
			Value: nameAndValue[1],
		})
	}
	return envVars
}

// toVolumeMounts convert from component mount volumes annotation to pod volume mounts and defaults to emptyDir volumes.
func toVolumeMounts(mountAnnotation string) ([]corev1.VolumeMount, []corev1.Volume) {
	volumeMounts := make([]corev1.VolumeMount, 0)
	volumes := make([]corev1.Volume, 0)
	if mountAnnotation == "" {
		return volumeMounts, volumes
	}

	mounts := strings.Split(mountAnnotation, ",")
	for _, mount := range mounts {
		volumeAndPath := strings.Split(mount, ";")
		if len(volumeAndPath) != 2 {
			continue
		}
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      volumeAndPath[0],
			MountPath: volumeAndPath[1],
		})
		volumes = append(volumes, corev1.Volume{
			Name: volumeAndPath[0],
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	}
	return volumeMounts, volumes
}

// getContainers return all required containers for such appID based on declared components.
func (r *PodReconciler) getContainers(sharedSocketVolumeMount corev1.VolumeMount, appID string, components []componentsapi.Component) ([]corev1.Container, []corev1.Volume, string, error) {
	componentContainers := make([]corev1.Container, 0)
	volumes := make([]corev1.Volume, 0)
	presetHashes := make([]string, 0)
	for _, component := range components {
		containerImage := component.Annotations[containerImageAnnotation]
		if containerImage == "" {
			continue
		}

		typeName := strings.Split(component.Spec.Type, ".")
		if len(typeName) < 2 { // invalid name
			continue
		}

		appScopped := len(component.Scopes) == 0
		for _, scoppedApp := range component.Scopes {
			if scoppedApp == appID {
				appScopped = true
				break
			}
		}

		if appScopped {
			volumeMounts, podVolumes := toVolumeMounts(component.Annotations[volumeMountsAnnotation])
			volumes = append(volumes, podVolumes...)
			presetHashes = append(presetHashes, containerImage)
			componentContainers = append(componentContainers, corev1.Container{
				Name:         component.Name,
				Image:        containerImage,
				VolumeMounts: append(volumeMounts, sharedSocketVolumeMount),
			})
		}
	}

	if len(componentContainers) == 0 {
		return componentContainers, volumes, "", nil
	}

	sort.Strings(presetHashes) // to keep consistency
	h := sha256.New()
	_, err := h.Write([]byte(strings.Join(presetHashes, "")))
	if err != nil {
		return nil, nil, "", err
	}

	return componentContainers, volumes, hex.EncodeToString(h.Sum(nil)), nil
}

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dapr.io,resources=components,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *PodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			// we'll ignore not-found errors, since we can get them on deleted requests.
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch Pod")
		return ctrl.Result{}, err
	}

	if pod.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	shouldBeManaged := pod.Annotations[daprEnabledAnnotation] == "true" && pod.Annotations[autoInjectComponentsEnabledAnnotation] == "true"
	if !shouldBeManaged {
		return ctrl.Result{}, nil
	}

	appID := pod.Annotations[appIDAnnotation]

	var components componentsapi.ComponentList

	if err := r.List(ctx, &components, &client.ListOptions{
		Namespace: pod.Namespace,
	}); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, errors.Wrap(err, "error getting components")
	}

	sharedSocketVolumeMount := corev1.VolumeMount{
		Name:      daprComponentsSocketVolumeName,
		MountPath: defaultSocketPath,
	}

	componentContainers, requiredContainersVolumes, hash, err := r.getContainers(sharedSocketVolumeMount, appID, components.Items)
	if err != nil {
		log.Error(err, "error when getting containers")
		return ctrl.Result{}, err
	}

	if hash == pod.Annotations[checkSumComponentsAnnotation] { // nothing has changed
		return ctrl.Result{}, nil
	}

	err = r.Delete(ctx, &pod)
	if err != nil {
		log.Error(err, "unable to delete pod")
		return ctrl.Result{}, err
	}

	pod.Annotations[checkSumComponentsAnnotation] = hash
	pod.ResourceVersion = ""
	pod.UID = ""
	pod.Name = pod.Name + "-patched"
	// add unix socket volume
	requiredContainersVolumes = append(requiredContainersVolumes, corev1.Volume{
		Name: daprComponentsSocketVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	})

	pod.Spec.Containers = append(pod.Spec.Containers, componentContainers...)

	log.WithValues("containers", len(componentContainers)).Info("adding containers")

	for idx, container := range pod.Spec.Containers {
		if container.Name == daprdContainerName {
			isVolumeMounted := false

			for _, volume := range container.VolumeMounts {
				if volume.Name == sharedSocketVolumeMount.Name {
					isVolumeMounted = true
					break
				}
			}

			if !isVolumeMounted {
				container.VolumeMounts = append(container.VolumeMounts, sharedSocketVolumeMount)
				pod.Spec.Containers[idx] = container
			}
		}
	}

	currentPodVolumes := make(map[string]bool, 0)
	for _, volume := range pod.Spec.Volumes {
		currentPodVolumes[volume.Name] = true
	}

	for _, newVolume := range requiredContainersVolumes {
		if !currentPodVolumes[newVolume.Name] {
			pod.Spec.Volumes = append(pod.Spec.Volumes, newVolume)
			currentPodVolumes[newVolume.Name] = true
		}
	}

	err = r.Create(ctx, &pod)
	if err != nil {
		log.Error(err, "unable to create pod")
	}

	return ctrl.Result{
		Requeue: err != nil,
	}, err
}

// SetupWithManager sets up the controller with the Manager.
func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Complete(r)
}
