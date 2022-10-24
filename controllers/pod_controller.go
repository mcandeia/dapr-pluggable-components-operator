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
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	componentsapi "github.com/dapr/dapr/pkg/apis/components/v1alpha1"
	"github.com/imdario/mergo"
	"github.com/pkg/errors"
)

const (
	autoInjectComponentsEnabledAnnotation = "components.dapr.io/enabled"
	daprEnabledAnnotation                 = "dapr.io/enabled"
	appIDAnnotation                       = "dapr.io/app-id"
	pluggableComponentsAnnotation         = "dapr.io/pluggable-components"
	defaultSocketPath                     = "/tmp/dapr-components-sockets"
	daprUnixDomainSocketVolumeName        = "dapr-unix-domain-socket"
)

// PodReconciler reconciles a Pod object
type PodReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Presets map[string]corev1.PodSpec
}

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dapr.io,resources=components,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Pod object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.13.0/pkg/reconcile
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
		log.Info("pod should not be managed")
		return ctrl.Result{}, nil
	}

	appID := pod.Annotations[appIDAnnotation]

	var components componentsapi.ComponentList

	if err := r.List(ctx, &components, &client.ListOptions{
		Namespace: pod.Namespace,
	}); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "error getting components")
	}

	componentContainers := make(map[string]bool)

	var annotatedComponents []string
	annotated := pod.Annotations[pluggableComponentsAnnotation]
	if annotated == "" {
		annotatedComponents = make([]string, 0)
	} else {
		annotatedComponents = strings.Split(pod.Annotations[pluggableComponentsAnnotation], ",")
	}

	for _, containerName := range annotatedComponents {
		componentContainers[containerName] = true
	}

	spec := pod.Spec
	for _, component := range components.Items {
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
			podPreset := r.Presets[typeName[1]]
			containsContainer := false
			for _, container := range podPreset.Containers {
				_, ok := componentContainers[container.Name]
				if ok { // already contains the container.
					containsContainer = true
					break
				}
				componentContainers[container.Name] = true
			}

			if containsContainer {
				continue
			}

			if err := mergo.Merge(&spec, podPreset, mergo.WithAppendSlice); err != nil {
				log.Error(err, "unable to merge preset")
				return ctrl.Result{}, err
			}

		}
	}

	componentContainersList := make([]string, len(componentContainers))
	idx := 0
	for containerName := range componentContainers {
		componentContainersList[idx] = containerName
		idx++
	}

	sort.Strings(componentContainersList) // to keep consistency

	newPluggableComponentsAnnotation := strings.Join(componentContainersList, ",")
	if newPluggableComponentsAnnotation == pod.Annotations[pluggableComponentsAnnotation] { // nothing changes
		return ctrl.Result{}, nil
	}

	err := r.Delete(ctx, &pod)
	if apierrors.IsConflict(err) {
		log.Error(err, "conflict while deleting pod")
		return ctrl.Result{}, nil
	}

	pod.Annotations[pluggableComponentsAnnotation] = newPluggableComponentsAnnotation
	pod.Spec = spec
	pod.ResourceVersion = ""
	pod.UID = ""
	pod.Name = ""

	socketVolume := corev1.Volume{
		Name: daprUnixDomainSocketVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, socketVolume)

	for idx, container := range pod.Spec.Containers {
		if componentContainers[container.Name] || container.Name == "daprd" {
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      daprUnixDomainSocketVolumeName,
				MountPath: defaultSocketPath,
			})
			pod.Spec.Containers[idx] = container
		}
	}
	err = r.Create(ctx, &pod)

	if apierrors.IsConflict(err) {
		log.Error(err, "conflict while creating pod")
		return ctrl.Result{}, nil
	}

	if err != nil {
		log.Error(err, "unable to update pod")
		return ctrl.Result{RequeueAfter: time.Second * 10}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Complete(r)
}
