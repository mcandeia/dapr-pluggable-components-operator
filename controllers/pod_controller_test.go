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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	componentsapi "github.com/dapr/dapr/pkg/apis/components/v1alpha1"
)

var _ = Describe("Pod custom controller", func() {
	Context("Pod custom controller test", func() {
		utilruntime.Must(componentsapi.AddToScheme(scheme.Scheme)) // add components api to scheme

		const (
			podName      = "test-pod"
			podNamespace = "test-namespace"
		)

		ctx := context.Background()

		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podNamespace,
				Namespace: podNamespace,
			},
		}

		typeNamespaceName := types.NamespacedName{Name: podName, Namespace: podNamespace}

		BeforeEach(func() {
			By("Creating the Namespace to perform the tests")
			err := k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))
		})

		AfterEach(func() {
			// be aware of the current delete namespace limitations. More info: https://book.kubebuilder.io/reference/envtest.html#testing-considerations
			By("Deleting the Namespace to perform the tests")
			_ = k8sClient.Delete(ctx, namespace)
		})

		It("Should successfully reconcile a Pod", func() {
			By("Creating the Pod with dapr enabled")

			pod := &corev1.Pod{}
			err := k8sClient.Get(ctx, typeNamespaceName, pod)
			if err != nil && apierrors.IsNotFound(err) {
				// Let's mock our custom resource at the same way that we would
				// apply on the cluster the manifest under config/samples
				pod = &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      podName,
						Namespace: namespace.Name,
						Annotations: map[string]string{
							daprEnabledAnnotation:                 "true",
							autoInjectComponentsEnabledAnnotation: "true",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  "test",
							Image: "localhost:5000/state-memory:latest",
						}},
					},
				}
				err = k8sClient.Create(ctx, pod)
				Expect(err).To(Not(HaveOccurred()))
			}

			By("Checking if the pod was successfully created")

			found := &corev1.Pod{}
			Eventually(func() error {
				return k8sClient.Get(ctx, typeNamespaceName, found)
			}, time.Minute, time.Second).Should(Succeed())

			Expect(found.Spec.Containers).To(HaveLen(1))

			By("Reconciling the pod created")
			podReconciler := &PodReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err = podReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespaceName,
			})

			Expect(err).To(Not(HaveOccurred()))

			By("Applying the Dapr component")
			const componentName = "dapr-component-custom"

			componentTypeNamespace := types.NamespacedName{Name: componentName, Namespace: podNamespace}
			component := &componentsapi.Component{}
			err = k8sClient.Get(ctx, componentTypeNamespace, component)
			if err != nil && apierrors.IsNotFound(err) {
				component = &componentsapi.Component{
					ObjectMeta: metav1.ObjectMeta{
						Name:      componentName,
						Namespace: namespace.Name,
						Annotations: map[string]string{
							containerImageAnnotation: "localhost:5000/state-memory:latest",
						},
					},
					Spec: componentsapi.ComponentSpec{
						Type:         "state.memory-pluggable",
						Version:      "v1",
						IgnoreErrors: false,
						Metadata:     []componentsapi.MetadataItem{},
						InitTimeout:  "1m",
					},
				}
				err = k8sClient.Create(ctx, component)
				Expect(err).To(Not(HaveOccurred()))
			}

			componentFound := &componentsapi.Component{}
			Eventually(func() error {
				return k8sClient.Get(ctx, componentTypeNamespace, componentFound)
			}, time.Minute, time.Second).Should(Succeed())

			By("Reconciling the pod again")

			_, err = podReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespaceName,
			})

			Expect(err).To(Not(HaveOccurred()))

			Eventually(func() error {
				return k8sClient.Get(ctx, typeNamespaceName, found)
			}, time.Minute, time.Second).Should(Succeed())

			Expect(found.Spec.Containers).To(HaveLen(2))
		})
	})
})
