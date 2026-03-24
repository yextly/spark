/*
Copyright 2026.

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

package controller

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	computev1alpha1 "spark/api/v1alpha1"

	"k8s.io/apimachinery/pkg/api/errors"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("WorkerInstance Controller", func() {

	Context("When reconciling a resource", func() {

		const resourceName = "test-resource"
		const templateName = "template1"
		const namespace = "default"

		ctx := context.Background()
		workerInstance := &computev1alpha1.WorkerInstance{}
		workerTemplate := &computev1alpha1.WorkerTemplate{}

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: namespace,
		}

		BeforeEach(func() {
			//
			// Create a WorkerTemplate
			//
			jobTemplate := batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:  "main",
								Image: "busybox",
							}},
						},
					},
				},
			}

			raw, err := json.Marshal(jobTemplate)
			Expect(err).ToNot(HaveOccurred())

			workerTemplate = &computev1alpha1.WorkerTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      templateName,
					Namespace: namespace,
				},
				Spec: computev1alpha1.WorkerTemplateSpec{
					Template: runtime.RawExtension{Raw: raw},
				},
			}

			logger := logf.Log.WithName("Resource")
			logger.Info("Create resource", "value", "template")

			Expect(k8sClient.Create(ctx, workerTemplate)).To(Succeed())

			//
			// Create WorkerInstance resource
			//
			By("creating the custom resource for the Kind WorkerInstance")
			err = k8sClient.Get(ctx, typeNamespacedName, workerInstance)
			if err != nil && errors.IsNotFound(err) {
				resource := &computev1alpha1.WorkerInstance{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: namespace,
					},
					Spec: computev1alpha1.WorkerInstanceSpec{
						TemplateName: templateName,
						WorkerId:     "worker-123",
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &computev1alpha1.WorkerInstance{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance WorkerInstance")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")

			controllerReconciler := &WorkerInstanceReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// --- Call reconcile  ---
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// --- Assertions! ---

			// Fetch updated instance
			updated := &computev1alpha1.WorkerInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())

			// Finalizer gets added on first reconcile
			Expect(updated.Finalizers).To(ContainElement("workerinstance.compute.yextly.io"))

			// Status should transition to Creating or Running (depending on step)
			Expect(updated.Status.ProvisioningState).To(BeElementOf(
				computev1alpha1.WorkerProvisioningCreating,
				computev1alpha1.WorkerProvisioningRunning,
			))

			// If a Job was created, JobName should be populated
			if updated.Status.JobName != "" {
				job := &batchv1.Job{}
				Expect(
					k8sClient.Get(ctx, types.NamespacedName{
						Name:      updated.Status.JobName,
						Namespace: namespace,
					}, job),
				).To(Succeed())

				Expect(job.ObjectMeta.Name).To(Equal("aaaaa"))
				Expect(job.ObjectMeta.Namespace).To(Equal(namespace))

				Expect(job.Spec.Template.Spec.RestartPolicy).To(Equal(v1.RestartPolicyNever))
				Expect(job.ObjectMeta.Annotations).To(HaveKeyWithValue(jobAnnotationName, workerInstance.Name))
			}

		})
	})
})
