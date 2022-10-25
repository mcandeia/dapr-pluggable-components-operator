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
	"encoding/json"
	"sort"
	"strings"

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
	checkSumComponentsAnnotation          = "components.dapr.io/checksum"
	daprEnabledAnnotation                 = "dapr.io/enabled"
	appIDAnnotation                       = "dapr.io/app-id"
	pluggableComponentsAnnotation         = "dapr.io/pluggable-components"
	defaultSocketPath                     = "/tmp/dapr-components-sockets"
	daprComponentsSocketVolumeName        = "dapr-components-unix-domain-socket"
	daprdContainerName                    = "daprd"
)

// PodReconciler reconciles a Pod object
type PodReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Presets map[string]corev1.PodSpec
}

func (r *PodReconciler) mergePresets(spec *corev1.PodSpec, appID string, components []componentsapi.Component) (map[string]bool, string, error) {
	componentContainers := make(map[string]bool)
	presetHashes := make([]string, 0)
	for _, component := range components {
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
			bts, err := json.Marshal(podPreset)
			if err != nil {
				return nil, "", err
			}

			presetHashes = append(presetHashes, string(bts))
			for _, container := range podPreset.Containers {
				componentContainers[container.Name] = true
			}

			if err := mergo.Merge(spec, podPreset, mergo.WithAppendSlice); err != nil {
				return nil, "", err
			}
		}
	}

	sort.Strings(presetHashes) // to keep consistency
	h := sha256.New()
	_, err := h.Write([]byte(strings.Join(presetHashes, "")))
	if err != nil {
		return nil, "", err
	}

	return componentContainers, hex.EncodeToString(h.Sum(nil)), nil
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
		return ctrl.Result{}, nil
	}

	// metav1.ListOptions{LabelSelector: "k8s-app=kube-proxy"}
	appID := pod.Annotations[appIDAnnotation]

	var components componentsapi.ComponentList

	if err := r.List(ctx, &components, &client.ListOptions{
		Namespace: pod.Namespace,
	}); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "error getting components")
	}

	spec := pod.Spec
	componentContainers, hash, err := r.mergePresets(&spec, appID, components.Items)

	if err != nil {
		log.Error(err, "error when merging presets")
		return ctrl.Result{}, err
	}

	if hash == pod.Annotations[checkSumComponentsAnnotation] { // nothing has changed
		return ctrl.Result{}, nil
	}

	componentContainersList := make([]string, len(componentContainers))
	idx := 0
	for containerName := range componentContainers {
		componentContainersList[idx] = containerName
		idx++
	}

	originalPod := pod

	pod.Annotations[checkSumComponentsAnnotation] = hash
	pod.Annotations[pluggableComponentsAnnotation] = strings.Join(componentContainersList, ",")
	pod.Spec = spec
	pod.ResourceVersion = ""
	pod.UID = ""
	pod.Name = ""
	// add unix socket volume
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: daprComponentsSocketVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	})

	for idx, container := range pod.Spec.Containers {
		// mount volume in desired containers.
		if componentContainers[container.Name] || container.Name == daprdContainerName {
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      daprComponentsSocketVolumeName,
				MountPath: defaultSocketPath,
			})
			pod.Spec.Containers[idx] = container
		}
	}

	// create first then delete later.
	err = r.Create(ctx, &pod)
	if err != nil {
		log.Error(err, "unable to create pod")
		return ctrl.Result{}, err
	}

	err = r.Delete(ctx, &originalPod)
	if err != nil {
		log.Error(err, "unable to delete pod")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, err
}

// SetupWithManager sets up the controller with the Manager.
func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Complete(r)
}
