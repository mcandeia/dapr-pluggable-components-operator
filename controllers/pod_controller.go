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
	autoInjectComponentsEnabledPodLabel = "components.dapr.io/enabled"
	daprEnabledPodLabel                 = "dapr.io/enabled"
	appIdPodLabel                       = "dapr.io/app-id"
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

	shouldBeManaged := pod.Annotations[daprEnabledPodLabel] == "true" && pod.Annotations[autoInjectComponentsEnabledPodLabel] == "true"
	if !shouldBeManaged {
		return ctrl.Result{}, nil
	}

	appID := pod.Annotations[appIdPodLabel]

	var components componentsapi.ComponentList

	if err := r.List(ctx, &components, &client.ListOptions{
		Namespace: pod.Namespace,
	}); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "error getting components")
	}

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
			if err := mergo.MergeWithOverwrite(&pod, r.Presets[typeName[1]]); err != nil {
				log.Error(err, "unable to merge preset")
				return ctrl.Result{}, err
			}
		}
	}

	err := r.Update(ctx, &pod)
	if err != nil {
		log.Error(err, "unable to update pod")
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
