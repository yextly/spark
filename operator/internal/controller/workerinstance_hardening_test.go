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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/yaml"

	computev1alpha1 "spark/api/v1alpha1"
)

var _ = Describe("WorkerInstance secret workflow", func() {
	var (
		ctx       context.Context
		namespace string
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespace = newTestNamespace(ctx)
	})

	expectFailed := func(name string, reason string, messageParts ...string) *computev1alpha1.WorkerInstance {
		instance := getInstance(ctx, namespace, name)
		Expect(instance.Status.ProvisioningState).To(Equal(computev1alpha1.WorkerProvisioningFailed))
		Expect(instance.Status.JobName).To(BeEmpty())

		condition := meta.FindStatusCondition(instance.Status.Conditions, string(computev1alpha1.WorkerProvisioningFailed))
		Expect(condition).ToNot(BeNil())
		Expect(condition.Reason).To(Equal(reason))
		for _, part := range messageParts {
			Expect(condition.Message).To(ContainSubstring(part))
		}

		return instance
	}

	expectNothingCreated := func() {
		Expect(listSecrets(ctx, namespace)).To(BeEmpty())
		Expect(listJobs(ctx, namespace)).To(BeEmpty())
	}

	Context("request validation", func() {
		BeforeEach(func() {
			createTemplate(ctx, namespace, "template1", podSpecWithSecretVolumes("secret1"))
		})

		It("stores Failed when an embedded Secret has no name (AC-001)", func() {
			createInstance(ctx, namespace, "i1", "template1", "", embedRaw(`{"metadata":{},"data":{"k":"dmFsdWU="}}`))
			r, recorder := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "i1")
			Expect(err).ToNot(HaveOccurred())

			instance := expectFailed("i1", "SecretSpecInvalid", "secrets[0]")
			Expect(instance.Finalizers).To(ContainElement(finalizerName))
			expectNothingCreated()
			Expect(drainEvents(recorder)).To(ContainElement(And(HavePrefix("Warning SecretSpecInvalid"), ContainSubstring("secrets[0]"))))
		})

		It("fails without panicking on an undecodable Secret (AC-003)", func() {
			createInstance(ctx, namespace, "i1", "template1", "", embedRaw(`{"metadata":{"name":"secret1"},"data":{"k":"%%%not-base64"}}`))
			r, recorder := newTestReconciler(k8sClient)

			Expect(func() {
				_, err := reconcileInstance(ctx, r, namespace, "i1")
				Expect(err).ToNot(HaveOccurred())
			}).ToNot(Panic())

			expectFailed("i1", "SecretSpecInvalid", "secrets[0]")
			expectNothingCreated()

			events := drainEvents(recorder)
			Expect(events).To(ContainElement(And(HavePrefix("Warning SecretSpecInvalid"), ContainSubstring("secrets[0]"))))
			Expect(strings.Join(events, "\n")).ToNot(ContainSubstring("not-base64"))
		})

		It("lets the API server reject an embedded entry that is not an object (AC-003)", func() {
			instance := &computev1alpha1.WorkerInstance{
				ObjectMeta: metav1.ObjectMeta{Name: "i1", Namespace: namespace},
				Spec: computev1alpha1.WorkerInstanceSpec{
					TemplateName: "template1",
					Secrets:      embedRaw(`["a","b"]`),
				},
			}

			err := k8sClient.Create(ctx, instance)
			Expect(errors.IsInvalid(err)).To(BeTrue(), "unexpected error: %v", err)
		})

		DescribeTable("fails without panicking on a workerId with no valid character (AC-004)",
			func(workerId string) {
				createInstance(ctx, namespace, "i1", "template1", workerId, embed(opaqueSecret("secret1", map[string]string{"k": "v"})))
				r, _ := newTestReconciler(k8sClient)

				Expect(func() {
					_, err := reconcileInstance(ctx, r, namespace, "i1")
					Expect(err).ToNot(HaveOccurred())
				}).ToNot(Panic())

				expectFailed("i1", "WorkerIdInvalid", "workerId")
				expectNothingCreated()
			},
			Entry("symbols", "@@"),
			Entry("dashes", "---"),
		)

		It("validates every entry before creating any copy (AC-005)", func() {
			createInstance(ctx, namespace, "i1", "template1", "",
				embed(opaqueSecret("secret1", map[string]string{"k": "v"}), opaqueSecret("Bad_Name", map[string]string{"k": "v"})))
			r, _ := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "i1")
			Expect(err).ToNot(HaveOccurred())

			expectFailed("i1", "SecretSpecInvalid", "secrets[1]", "Bad_Name")
			expectNothingCreated()
		})

		DescribeTable("rejects entries that are not v1 Secrets (AC-006)",
			func(document string, messagePart string) {
				createInstance(ctx, namespace, "i1", "template1", "", embedRaw(document))
				r, _ := newTestReconciler(k8sClient)

				_, err := reconcileInstance(ctx, r, namespace, "i1")
				Expect(err).ToNot(HaveOccurred())

				expectFailed("i1", "SecretSpecInvalid", messagePart)
				expectNothingCreated()
			},
			Entry("ConfigMap kind", `{"kind":"ConfigMap","metadata":{"name":"secret1"},"data":{"k":"dg=="}}`, "kind must be Secret"),
			Entry("other apiVersion", `{"apiVersion":"v2","kind":"Secret","metadata":{"name":"secret1"}}`, "apiVersion must be v1"),
		)

		It("rejects duplicated names", func() {
			createInstance(ctx, namespace, "i1", "template1", "",
				embed(opaqueSecret("secret1", map[string]string{"k": "a"}), opaqueSecret("secret1", map[string]string{"k": "b"})))
			r, _ := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "i1")
			Expect(err).ToNot(HaveOccurred())

			expectFailed("i1", "SecretSpecInvalid", "secrets[1] (secret1): duplicated name")
			expectNothingCreated()
		})

		It("stays Creating and retries while the template is missing", func() {
			createInstance(ctx, namespace, "i1", "missing", "", nil)
			r, _ := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "i1")
			Expect(errors.IsNotFound(err)).To(BeTrue())

			instance := getInstance(ctx, namespace, "i1")
			Expect(instance.Status.ProvisioningState).To(Equal(computev1alpha1.WorkerProvisioningCreating))
			Expect(instance.Finalizers).To(ContainElement(finalizerName))
		})

		It("does nothing more once Failed", func() {
			createInstance(ctx, namespace, "i1", "template1", "@@", nil)
			r, recorder := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "i1")
			Expect(err).ToNot(HaveOccurred())
			before := getInstance(ctx, namespace, "i1")
			drainEvents(recorder)

			result, err := reconcileInstance(ctx, r, namespace, "i1")
			Expect(err).ToNot(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())
			Expect(drainEvents(recorder)).To(BeEmpty())
			Expect(getInstance(ctx, namespace, "i1").ResourceVersion).To(Equal(before.ResourceVersion))
		})
	})

	Context("template checks", func() {
		It("fails on a template without containers and creates nothing", func() {
			createTemplate(ctx, namespace, "template1", corev1.PodSpec{})
			createInstance(ctx, namespace, "i1", "template1", "", embed(opaqueSecret("secret1", map[string]string{"k": "v"})))
			r, recorder := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "i1")
			Expect(err).ToNot(HaveOccurred())

			expectFailed("i1", "JobSpecInvalid", "template1")
			expectNothingCreated()
			// One event on the instance and one on the template
			Expect(drainEvents(recorder)).To(HaveEach(HavePrefix("Warning JobSpecInvalid")))
		})

		It("keeps the copies when the API server rejects the Job", func() {
			spec := podSpecWithSecretVolumes("secret1")
			spec.Containers[0].Image = ""
			createTemplate(ctx, namespace, "template1", spec)
			createInstance(ctx, namespace, "i1", "template1", "w1", embed(opaqueSecret("secret1", map[string]string{"k": "v"})))
			r, _ := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "i1")
			Expect(err).ToNot(HaveOccurred())

			instance := expectFailed("i1", "JobSpecInvalid", "image")
			Expect(listJobs(ctx, namespace)).To(BeEmpty())
			Expect(instance.Status.SecretMappings).To(HaveLen(1))
			_, err = getSecret(ctx, namespace, expectedCopyName("w1", "secret1"))
			Expect(err).ToNot(HaveOccurred())

			deleteInstanceFully(ctx, r, namespace, "i1")
			Expect(listSecrets(ctx, namespace)).To(BeEmpty())
		})

		It("fails when an embedded Secret is used by imagePullSecrets (AC-010)", func() {
			spec := podSpecWithSecretVolumes()
			spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "secret1"}}
			createTemplate(ctx, namespace, "template1", spec)
			createInstance(ctx, namespace, "i1", "template1", "", embed(opaqueSecret("secret1", map[string]string{"k": "v"})))
			r, _ := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "i1")
			Expect(err).ToNot(HaveOccurred())

			expectFailed("i1", "UnsupportedSecretReference", "imagePullSecrets", "secret1")
			expectNothingCreated()
		})

		It("fails when an embedded Secret is used by a CSI volume (AC-010)", func() {
			spec := podSpecWithSecretVolumes()
			spec.Volumes = []corev1.Volume{{Name: "csi", VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{
				Driver:               "secrets-store.csi.k8s.io",
				NodePublishSecretRef: &corev1.LocalObjectReference{Name: "secret1"},
			}}}}
			createTemplate(ctx, namespace, "template1", spec)
			createInstance(ctx, namespace, "i1", "template1", "", embed(opaqueSecret("secret1", map[string]string{"k": "v"})))
			r, _ := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "i1")
			Expect(err).ToNot(HaveOccurred())

			expectFailed("i1", "UnsupportedSecretReference", "volumes[csi].csi.nodePublishSecretRef")
			expectNothingCreated()
		})

		It("leaves imagePullSecrets naming a namespace Secret alone", func() {
			spec := podSpecWithSecretVolumes("secret1")
			spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "regcred"}}
			createTemplate(ctx, namespace, "template1", spec)
			createInstance(ctx, namespace, "i1", "template1", "w1", embed(opaqueSecret("secret1", map[string]string{"k": "v"})))
			r, _ := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "i1")
			Expect(err).ToNot(HaveOccurred())

			job, err := getJob(ctx, namespace, expectedJobName("w1"))
			Expect(err).ToNot(HaveOccurred())
			Expect(job.Spec.Template.Spec.ImagePullSecrets).To(Equal([]corev1.LocalObjectReference{{Name: "regcred"}}))
		})
	})

	Context("reference rewriting and reporting", func() {
		It("rewrites every supported reference (AC-009)", func() {
			ref := corev1.LocalObjectReference{Name: "secret1"}
			spec := corev1.PodSpec{
				Volumes: []corev1.Volume{
					{Name: "plain", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "secret1"}}},
					{Name: "projected", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{
						{Secret: &corev1.SecretProjection{LocalObjectReference: ref}},
					}}}},
				},
				InitContainers: []corev1.Container{{
					Name:    "init",
					Image:   "busybox",
					EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: ref}}},
					Env:     []corev1.EnvVar{{Name: "A", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: ref, Key: "k"}}}},
				}},
				Containers: []corev1.Container{{
					Name:    "main",
					Image:   "busybox",
					EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: ref}}},
					Env:     []corev1.EnvVar{{Name: "A", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: ref, Key: "k"}}}},
				}},
			}
			createTemplate(ctx, namespace, "template1", spec)
			createInstance(ctx, namespace, "i1", "template1", "w1", embed(opaqueSecret("secret1", map[string]string{"k": "v"})))
			r, _ := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "i1")
			Expect(err).ToNot(HaveOccurred())

			job, err := getJob(ctx, namespace, expectedJobName("w1"))
			Expect(err).ToNot(HaveOccurred())

			raw, err := json.Marshal(job.Spec.Template.Spec)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(raw)).ToNot(ContainSubstring(`"secret1"`))
			Expect(strings.Count(string(raw), `"`+expectedCopyName("w1", "secret1")+`"`)).To(Equal(6))

			condition := meta.FindStatusCondition(getInstance(ctx, namespace, "i1").Status.Conditions, secretReferencesConditionType)
			Expect(condition).ToNot(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		})

		It("warns about an embedded Secret the template never references (AC-007)", func() {
			createTemplate(ctx, namespace, "template1", podSpecWithSecretVolumes("secret1"))
			createInstance(ctx, namespace, "i1", "template1", "w1",
				embed(opaqueSecret("secret1", map[string]string{"k": "v"}), opaqueSecret("secret2", map[string]string{"k": "v"})))
			r, recorder := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "i1")
			Expect(err).ToNot(HaveOccurred())

			instance := getInstance(ctx, namespace, "i1")
			Expect(instance.Status.ProvisioningState).To(Equal(computev1alpha1.WorkerProvisioningRunning))

			condition := meta.FindStatusCondition(instance.Status.Conditions, secretReferencesConditionType)
			Expect(condition).ToNot(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("UnreferencedSecrets"))
			Expect(condition.Message).To(ContainSubstring("secret2"))

			Expect(drainEvents(recorder)).To(ContainElement(And(HavePrefix("Warning UnreferencedSecrets"), ContainSubstring("secret2"))))
		})

		It("reports a template reference served by a namespace Secret (AC-008)", func() {
			spec := podSpecWithSecretVolumes("secret1")
			spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "shared-db"}}}}
			createTemplate(ctx, namespace, "template1", spec)
			createInstance(ctx, namespace, "i1", "template1", "w1", embed(opaqueSecret("secret1", map[string]string{"k": "v"})))
			r, _ := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "i1")
			Expect(err).ToNot(HaveOccurred())

			job, err := getJob(ctx, namespace, expectedJobName("w1"))
			Expect(err).ToNot(HaveOccurred())
			podSpec := job.Spec.Template.Spec
			Expect(podSpec.Containers[0].EnvFrom[0].SecretRef.Name).To(Equal("shared-db"))
			Expect(podSpec.Volumes[0].Secret.SecretName).To(Equal(expectedCopyName("w1", "secret1")))

			condition := meta.FindStatusCondition(getInstance(ctx, namespace, "i1").Status.Conditions, secretReferencesConditionType)
			Expect(condition).ToNot(BeNil())
			Expect(condition.Reason).To(Equal("NamespaceSecrets"))
			Expect(condition.Message).To(ContainSubstring("resolved from the namespace: shared-db"))
		})

		It("runs an instance without embedded Secrets", func() {
			createTemplate(ctx, namespace, "template1", podSpecWithSecretVolumes())
			createInstance(ctx, namespace, "i1", "template1", "", nil)
			r, _ := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "i1")
			Expect(err).ToNot(HaveOccurred())

			instance := getInstance(ctx, namespace, "i1")
			Expect(instance.Status.ProvisioningState).To(Equal(computev1alpha1.WorkerProvisioningRunning))
			Expect(instance.Status.JobName).To(Equal(expectedJobName("i1")))
			Expect(instance.Status.SecretMappings).To(BeEmpty())
			Expect(listSecrets(ctx, namespace)).To(BeEmpty())
		})
	})

	Context("Secret copies", func() {
		It("builds copies from scratch, discarding caller metadata", func() {
			createTemplate(ctx, namespace, "template1", podSpecWithSecretVolumes("secret1"))

			embedded := corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "secret1",
					Namespace:   "willneverexist",
					Labels:      map[string]string{"evil": "x", instanceLabelName: "spoofed"},
					Annotations: map[string]string{associatedToAnnotationName: "someone-else"},
					Finalizers:  []string{"example.com/block"},
				},
				Type:       corev1.SecretTypeOpaque,
				Data:       map[string][]byte{"k": []byte("v")},
				StringData: map[string]string{"s": "t"},
			}
			createInstance(ctx, namespace, "i1", "template1", "w1", embed(embedded))
			r, _ := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "i1")
			Expect(err).ToNot(HaveOccurred())

			secret, err := getSecret(ctx, namespace, expectedCopyName("w1", "secret1"))
			Expect(err).ToNot(HaveOccurred())
			Expect(secret.Labels).To(Equal(map[string]string{managedByLabelName: managedByLabelValue, instanceLabelName: "i1"}))
			Expect(secret.Annotations).To(Equal(map[string]string{associatedToAnnotationName: "i1"}))
			Expect(secret.Finalizers).To(BeEmpty())
			Expect(secret.Immutable).To(Equal(ptr.To(true)))
			Expect(secret.Type).To(Equal(corev1.SecretTypeOpaque))
			Expect(secret.Data).To(Equal(map[string][]byte{"k": []byte("v"), "s": []byte("t")}))
		})

		It("applies ttlSecondsAfterFinished from the instance only when set", func() {
			createTemplate(ctx, namespace, "template1", podSpecWithSecretVolumes(), func(t *batchv1.JobTemplateSpec) {
				t.Spec.TTLSecondsAfterFinished = ptr.To[int32](100)
			})
			createInstance(ctx, namespace, "override", "template1", "", nil, func(i *computev1alpha1.WorkerInstance) {
				i.Spec.TTLSecondsAfterFinished = ptr.To[int32](0)
			})
			createInstance(ctx, namespace, "inherit", "template1", "", nil)
			r, _ := newTestReconciler(k8sClient)

			for _, name := range []string{"override", "inherit"} {
				_, err := reconcileInstance(ctx, r, namespace, name)
				Expect(err).ToNot(HaveOccurred())
			}

			job, err := getJob(ctx, namespace, expectedJobName("override"))
			Expect(err).ToNot(HaveOccurred())
			Expect(job.Spec.TTLSecondsAfterFinished).To(Equal(ptr.To[int32](0)))

			job, err = getJob(ctx, namespace, expectedJobName("inherit"))
			Expect(err).ToNot(HaveOccurred())
			Expect(job.Spec.TTLSecondsAfterFinished).To(Equal(ptr.To[int32](100)))
		})
	})

	Context("ownership", func() {
		BeforeEach(func() {
			createTemplate(ctx, namespace, "template1", podSpecWithSecretVolumes("secret1"))
		})

		It("waits instead of borrowing another instance's copies (AC-011, AC-021)", func() {
			createInstance(ctx, namespace, "a", "template1", "w1", embed(opaqueSecret("secret1", map[string]string{"k": "a-value"})))
			r, recorder := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())

			createInstance(ctx, namespace, "b", "template1", "w1", embed(opaqueSecret("secret1", map[string]string{"k": "b-value"})))

			result, err := reconcileInstance(ctx, r, namespace, "b")
			Expect(err).ToNot(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(blockedRequeueInterval))

			b := getInstance(ctx, namespace, "b")
			Expect(b.Status.ProvisioningState).To(Equal(computev1alpha1.WorkerProvisioningCreating))
			Expect(b.Status.JobName).To(BeEmpty())
			blocked := meta.FindStatusCondition(b.Status.Conditions, blockedConditionType)
			Expect(blocked).ToNot(BeNil())
			Expect(blocked.Status).To(Equal(metav1.ConditionTrue))
			Expect(blocked.Message).To(ContainSubstring("instance a"))

			copyName := expectedCopyName("w1", "secret1")
			secret, err := getSecret(ctx, namespace, copyName)
			Expect(err).ToNot(HaveOccurred())
			Expect(secret.Annotations[associatedToAnnotationName]).To(Equal("a"))
			Expect(secret.Data["k"]).To(Equal([]byte("a-value")))

			job, err := getJob(ctx, namespace, expectedJobName("w1"))
			Expect(err).ToNot(HaveOccurred())
			Expect(job.Annotations[associatedToAnnotationName]).To(Equal("a"))

			// A second poll while blocked neither re-emits the event nor rewrites the status
			drainEvents(recorder)
			result, err = reconcileInstance(ctx, r, namespace, "b")
			Expect(err).ToNot(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(blockedRequeueInterval))
			Expect(drainEvents(recorder)).To(BeEmpty())
			Expect(getInstance(ctx, namespace, "b").ResourceVersion).To(Equal(b.ResourceVersion))

			deleteInstanceFully(ctx, r, namespace, "a")

			_, err = reconcileInstance(ctx, r, namespace, "b")
			Expect(err).ToNot(HaveOccurred())

			b = getInstance(ctx, namespace, "b")
			Expect(b.Status.ProvisioningState).To(Equal(computev1alpha1.WorkerProvisioningRunning))
			Expect(meta.IsStatusConditionFalse(b.Status.Conditions, blockedConditionType)).To(BeTrue())

			secret, err = getSecret(ctx, namespace, copyName)
			Expect(err).ToNot(HaveOccurred())
			Expect(secret.Annotations[associatedToAnnotationName]).To(Equal("b"))
			Expect(secret.Data["k"]).To(Equal([]byte("b-value")))

			job, err = getJob(ctx, namespace, expectedJobName("w1"))
			Expect(err).ToNot(HaveOccurred())
			Expect(job.Annotations[associatedToAnnotationName]).To(Equal("b"))
		})

		DescribeTable("waits on a copy name held by someone else",
			func(annotations map[string]string, messagePart string) {
				Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
					Name:        expectedCopyName("w1", "secret1"),
					Namespace:   namespace,
					Annotations: annotations,
				}})).To(Succeed())

				createInstance(ctx, namespace, "b", "template1", "w1", embed(opaqueSecret("secret1", map[string]string{"k": "v"})))
				r, _ := newTestReconciler(k8sClient)

				result, err := reconcileInstance(ctx, r, namespace, "b")
				Expect(err).ToNot(HaveOccurred())
				Expect(result.RequeueAfter).To(Equal(blockedRequeueInterval))

				blocked := meta.FindStatusCondition(getInstance(ctx, namespace, "b").Status.Conditions, blockedConditionType)
				Expect(blocked).ToNot(BeNil())
				Expect(blocked.Message).To(ContainSubstring(messagePart))
				Expect(listJobs(ctx, namespace)).To(BeEmpty())
			},
			Entry("another instance", map[string]string{associatedToAnnotationName: "other"}, "instance other (Secret"),
			Entry("an unmanaged Secret", nil, "an unmanaged Secret"),
		)

		It("deletes a blocked instance without touching the owner's resources", func() {
			createInstance(ctx, namespace, "a", "template1", "w1", embed(opaqueSecret("secret1", map[string]string{"k": "v"})))
			createInstance(ctx, namespace, "b", "template1", "w1", embed(opaqueSecret("secret1", map[string]string{"k": "v"})))
			r, _ := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())
			_, err = reconcileInstance(ctx, r, namespace, "b")
			Expect(err).ToNot(HaveOccurred())

			deleteInstanceFully(ctx, r, namespace, "b")

			job, err := getJob(ctx, namespace, expectedJobName("w1"))
			Expect(err).ToNot(HaveOccurred())
			Expect(job.DeletionTimestamp).To(BeNil())
			_, err = getSecret(ctx, namespace, expectedCopyName("w1", "secret1"))
			Expect(err).ToNot(HaveOccurred())
		})

		It("never deletes a Secret owned by another instance (AC-012)", func() {
			createInstance(ctx, namespace, "a", "template1", "w1", embed(opaqueSecret("secret1", map[string]string{"k": "v"})))
			r, _ := newTestReconciler(k8sClient)

			_, err := reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())

			// Labelled as a's, owned by someone else
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Name:        "foreign-labelled",
				Namespace:   namespace,
				Labels:      map[string]string{instanceLabelName: "a"},
				Annotations: map[string]string{associatedToAnnotationName: "other"},
			}})).To(Succeed())

			// Mapped in a's status, owned by someone else
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Name:        "foreign-mapped",
				Namespace:   namespace,
				Annotations: map[string]string{associatedToAnnotationName: "other"},
			}})).To(Succeed())
			a := getInstance(ctx, namespace, "a")
			a.Status.SecretMappings = append(a.Status.SecretMappings, computev1alpha1.SecretMapping{OriginalSecretName: "x", RemappedSecretName: "foreign-mapped"})
			Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())

			deleteInstanceFully(ctx, r, namespace, "a")

			for _, name := range []string{"foreign-labelled", "foreign-mapped"} {
				_, err := getSecret(ctx, namespace, name)
				Expect(err).ToNot(HaveOccurred(), name)
			}
			_, err = getSecret(ctx, namespace, expectedCopyName("w1", "secret1"))
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("deletion", func() {
		BeforeEach(func() {
			createTemplate(ctx, namespace, "template1", podSpecWithSecretVolumes("secret1"))
			createInstance(ctx, namespace, "a", "template1", "w1", embed(opaqueSecret("secret1", map[string]string{"k": "v"})))
		})

		It("deletes the Job in the foreground and the copies only after it is gone (FR-009)", func() {
			r, _ := newTestReconciler(k8sClient)
			_, err := reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())

			Expect(k8sClient.Delete(ctx, getInstance(ctx, namespace, "a"))).To(Succeed())

			result, err := reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(jobDeletionRequeueInterval))

			jobName := expectedJobName("w1")
			copyName := expectedCopyName("w1", "secret1")

			job, err := getJob(ctx, namespace, jobName)
			Expect(err).ToNot(HaveOccurred())
			Expect(job.DeletionTimestamp).ToNot(BeNil())
			Expect(job.Finalizers).To(ContainElement(metav1.FinalizerDeleteDependents))

			// Still waiting for the pods
			result, err = reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(jobDeletionRequeueInterval))
			_, err = getSecret(ctx, namespace, copyName)
			Expect(err).ToNot(HaveOccurred())
			Expect(getInstance(ctx, namespace, "a").Status.ProvisioningState).To(Equal(computev1alpha1.WorkerProvisioningDeleting))

			simulateJobGarbageCollection(ctx, namespace, jobName)

			_, err = reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())
			Expect(instanceExists(ctx, namespace, "a")).To(BeFalse())
			Expect(listSecrets(ctx, namespace)).To(BeEmpty())
		})

		It("stores Deleting and keeps the finalizer while a copy cannot be deleted (AC-002)", func() {
			r, _ := newTestReconciler(k8sClient)
			_, err := reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())

			faulty := &faultyClient{Client: k8sClient, failDeleteOnce: expectedCopyName("w1", "secret1")}
			fr, _ := newTestReconciler(faulty)

			Expect(k8sClient.Delete(ctx, getInstance(ctx, namespace, "a"))).To(Succeed())

			_, err = reconcileInstance(ctx, fr, namespace, "a")
			Expect(err).ToNot(HaveOccurred())
			simulateJobGarbageCollection(ctx, namespace, expectedJobName("w1"))

			_, err = reconcileInstance(ctx, fr, namespace, "a")
			Expect(err).To(MatchError(ContainSubstring("injected failure")))

			instance := getInstance(ctx, namespace, "a")
			Expect(instance.Status.ProvisioningState).To(Equal(computev1alpha1.WorkerProvisioningDeleting))
			Expect(instance.Finalizers).To(ContainElement(finalizerName))

			_, err = reconcileInstance(ctx, fr, namespace, "a")
			Expect(err).ToNot(HaveOccurred())
			Expect(instanceExists(ctx, namespace, "a")).To(BeFalse())
			Expect(listSecrets(ctx, namespace)).To(BeEmpty())
		})

		It("deletes a labelled copy missing from the mappings before removing the finalizer (AC-015)", func() {
			r, _ := newTestReconciler(k8sClient)
			_, err := reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())

			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Name:        "leaked",
				Namespace:   namespace,
				Labels:      map[string]string{instanceLabelName: "a"},
				Annotations: map[string]string{associatedToAnnotationName: "a"},
			}})).To(Succeed())

			faulty := &faultyClient{Client: k8sClient, failDeleteOnce: "leaked"}
			fr, _ := newTestReconciler(faulty)

			Expect(k8sClient.Delete(ctx, getInstance(ctx, namespace, "a"))).To(Succeed())
			_, err = reconcileInstance(ctx, fr, namespace, "a")
			Expect(err).ToNot(HaveOccurred())
			simulateJobGarbageCollection(ctx, namespace, expectedJobName("w1"))

			_, err = reconcileInstance(ctx, fr, namespace, "a")
			Expect(err).To(HaveOccurred())
			Expect(getInstance(ctx, namespace, "a").Finalizers).To(ContainElement(finalizerName))

			_, err = reconcileInstance(ctx, fr, namespace, "a")
			Expect(err).ToNot(HaveOccurred())
			Expect(instanceExists(ctx, namespace, "a")).To(BeFalse())
			Expect(listSecrets(ctx, namespace)).To(BeEmpty())
		})

		It("deletes an unrecorded Job left behind by a crash", func() {
			// Writes: finalizer, Creating, copy, mappings, Job. Crash right after the Job: the status never recorded it
			faulty := &faultyClient{Client: k8sClient, failAfterWrite: 5}
			fr, _ := newTestReconciler(faulty)
			_, err := reconcileInstance(ctx, fr, namespace, "a")
			Expect(err).To(HaveOccurred())
			Expect(getInstance(ctx, namespace, "a").Status.JobName).To(BeEmpty())

			r, _ := newTestReconciler(k8sClient)
			Expect(k8sClient.Delete(ctx, getInstance(ctx, namespace, "a"))).To(Succeed())

			result, err := reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(jobDeletionRequeueInterval))

			simulateJobGarbageCollection(ctx, namespace, expectedJobName("w1"))
			_, err = reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())

			Expect(instanceExists(ctx, namespace, "a")).To(BeFalse())
			Expect(listJobs(ctx, namespace)).To(BeEmpty())
			Expect(listSecrets(ctx, namespace)).To(BeEmpty())
		})

		It("deletes itself when its Job is gone", func() {
			r, _ := newTestReconciler(k8sClient)
			_, err := reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())

			job, err := getJob(ctx, namespace, expectedJobName("w1"))
			Expect(err).ToNot(HaveOccurred())
			Expect(k8sClient.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))).To(Succeed())

			_, err = reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())
			Expect(getInstance(ctx, namespace, "a").DeletionTimestamp).ToNot(BeNil())

			_, err = reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())
			Expect(instanceExists(ctx, namespace, "a")).To(BeFalse())
			Expect(listSecrets(ctx, namespace)).To(BeEmpty())
		})

		It("deletes itself when its Job name was taken over by another instance", func() {
			r, _ := newTestReconciler(k8sClient)
			_, err := reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())

			jobName := expectedJobName("w1")
			job, err := getJob(ctx, namespace, jobName)
			Expect(err).ToNot(HaveOccurred())
			Expect(k8sClient.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))).To(Succeed())

			Expect(k8sClient.Create(ctx, &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:        jobName,
					Namespace:   namespace,
					Annotations: map[string]string{associatedToAnnotationName: "other"},
				},
				Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers:    []corev1.Container{mainContainer()},
				}}},
			})).To(Succeed())

			_, err = reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())

			_, err = reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())
			Expect(instanceExists(ctx, namespace, "a")).To(BeFalse())

			job, err = getJob(ctx, namespace, jobName)
			Expect(err).ToNot(HaveOccurred())
			Expect(job.DeletionTimestamp).To(BeNil())
			Expect(job.Annotations[associatedToAnnotationName]).To(Equal("other"))
		})
	})

	Context("crash safety", func() {
		expectConverged := func(name string, workerId string, originals ...string) {
			instance := getInstance(ctx, namespace, name)
			Expect(instance.Status.ProvisioningState).To(Equal(computev1alpha1.WorkerProvisioningRunning))
			Expect(instance.Status.JobName).To(Equal(expectedJobName(workerId)))

			var mappings []computev1alpha1.SecretMapping
			var copyNames []string
			for _, original := range originals {
				copyName := expectedCopyName(workerId, original)
				mappings = append(mappings, computev1alpha1.SecretMapping{OriginalSecretName: original, RemappedSecretName: copyName})
				copyNames = append(copyNames, copyName)
			}
			Expect(instance.Status.SecretMappings).To(ConsistOf(mappings))

			var names []string
			for _, s := range listSecrets(ctx, namespace) {
				names = append(names, s.Name)
			}
			Expect(names).To(ConsistOf(copyNames))
			Expect(listJobs(ctx, namespace)).To(HaveLen(1))
		}

		// Writes on the creation path: finalizer, Creating, copy 1, copy 2, mappings, Job, Running
		for n := 1; n <= 7; n++ {
			It(fmt.Sprintf("converges after a failure following write %d (AC-020)", n), func() {
				createTemplate(ctx, namespace, "template1", podSpecWithSecretVolumes("secret1", "secret2"))
				createInstance(ctx, namespace, "a", "template1", "w1",
					embed(opaqueSecret("secret1", map[string]string{"k": "v1"}), opaqueSecret("secret2", map[string]string{"k": "v2"})))

				faulty := &faultyClient{Client: k8sClient, failAfterWrite: n}
				fr, _ := newTestReconciler(faulty)
				_, err := reconcileInstance(ctx, fr, namespace, "a")
				Expect(err).To(MatchError(ContainSubstring("injected failure")))

				r, _ := newTestReconciler(k8sClient)
				for range 3 {
					_, err = reconcileInstance(ctx, r, namespace, "a")
					Expect(err).ToNot(HaveOccurred())
				}

				expectConverged("a", "w1", "secret1", "secret2")
			})
		}

		It("converges when spec.secrets is reordered mid-creation", func() {
			createTemplate(ctx, namespace, "template1", podSpecWithSecretVolumes("secret1", "secret2"))
			createInstance(ctx, namespace, "a", "template1", "w1",
				embed(opaqueSecret("secret1", map[string]string{"k": "v1"}), opaqueSecret("secret2", map[string]string{"k": "v2"})))

			// Stops right after the first copy exists
			faulty := &faultyClient{Client: k8sClient, failAfterWrite: 3}
			fr, _ := newTestReconciler(faulty)
			_, err := reconcileInstance(ctx, fr, namespace, "a")
			Expect(err).To(HaveOccurred())

			instance := getInstance(ctx, namespace, "a")
			instance.Spec.Secrets = []runtime.RawExtension{instance.Spec.Secrets[1], instance.Spec.Secrets[0]}
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			r, _ := newTestReconciler(k8sClient)
			_, err = reconcileInstance(ctx, r, namespace, "a")
			Expect(err).ToNot(HaveOccurred())

			expectConverged("a", "w1", "secret1", "secret2")

			job, err := getJob(ctx, namespace, expectedJobName("w1"))
			Expect(err).ToNot(HaveOccurred())
			Expect(job.Spec.Template.Spec.Volumes[0].Secret.SecretName).To(Equal(expectedCopyName("w1", "secret1")))
			Expect(job.Spec.Template.Spec.Volumes[1].Secret.SecretName).To(Equal(expectedCopyName("w1", "secret2")))
		})
	})

	It("runs the published samples (AC-019)", func() {
		template := &computev1alpha1.WorkerTemplate{}
		instance := &computev1alpha1.WorkerInstance{}

		for file, obj := range map[string]any{
			"compute_v1alpha1_workertemplate.yaml": template,
			"compute_v1alpha1_workerinstance.yaml": instance,
		} {
			raw, err := os.ReadFile(filepath.Join("..", "..", "config", "samples", file))
			Expect(err).ToNot(HaveOccurred())
			Expect(yaml.UnmarshalStrict(raw, obj)).To(Succeed(), file)
		}

		template.Namespace = namespace
		instance.Namespace = namespace
		Expect(k8sClient.Create(ctx, template)).To(Succeed())
		Expect(k8sClient.Create(ctx, instance)).To(Succeed())

		r, _ := newTestReconciler(k8sClient)
		_, err := reconcileInstance(ctx, r, namespace, instance.Name)
		Expect(err).ToNot(HaveOccurred())

		updated := getInstance(ctx, namespace, instance.Name)
		Expect(updated.Status.ProvisioningState).To(Equal(computev1alpha1.WorkerProvisioningRunning))
		Expect(meta.IsStatusConditionTrue(updated.Status.Conditions, secretReferencesConditionType)).To(BeTrue())
	})

	It("never writes Secret values to logs, events or conditions (AC-017)", func() {
		values := []string{"plain-sentinel-4c1f", "failing-sentinel-9e2d", "unreferenced-sentinel-77ab"}

		var logs bytes.Buffer
		logCtx := logf.IntoContext(ctx, zap.New(zap.WriteTo(io.MultiWriter(&logs, GinkgoWriter)), zap.UseDevMode(true)))

		createTemplate(ctx, namespace, "template1", podSpecWithSecretVolumes("secret1"))
		createInstance(ctx, namespace, "ok", "template1", "",
			embed(opaqueSecret("secret1", map[string]string{"k": values[0]}), opaqueSecret("unused", map[string]string{"k": values[2]})))
		createInstance(ctx, namespace, "bad", "template1", "",
			embed(opaqueSecret("secret1", map[string]string{"k": values[1]}), opaqueSecret("Bad_Name", map[string]string{"k": values[1]})))
		createInstance(ctx, namespace, "undecodable", "template1", "",
			embedRaw(`{"metadata":{"name":"secret1"},"stringData":{"k":"`+values[1]+`"},"data":{"x":"%%%"}}`))

		r, recorder := newTestReconciler(k8sClient)
		for _, name := range []string{"ok", "bad", "undecodable"} {
			_, err := reconcileInstance(logCtx, r, namespace, name)
			Expect(err).ToNot(HaveOccurred())
		}

		var conditions []string
		for _, name := range []string{"ok", "bad", "undecodable"} {
			for _, c := range getInstance(ctx, namespace, name).Status.Conditions {
				conditions = append(conditions, c.Message)
			}
		}

		deleteInstanceFully(logCtx, r, namespace, "ok")

		output := logs.String() + "\n" + strings.Join(drainEvents(recorder), "\n") + "\n" + strings.Join(conditions, "\n")
		Expect(logs.Len()).To(BeNumerically(">", 0))
		for _, v := range values {
			Expect(output).ToNot(ContainSubstring(v))
			Expect(output).ToNot(ContainSubstring(base64.StdEncoding.EncodeToString([]byte(v))))
		}
	})
})
