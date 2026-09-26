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
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	computev1alpha1 "spark/api/v1alpha1"
)

var _ = Describe("WorkerInstance helpers", func() {

	Describe("sanitizeWorkerId", func() {
		It("keeps the names produced by previous releases", func() {
			jobName, full, err := sanitizeWorkerId("abc-def!123")
			Expect(err).ToNot(HaveOccurred())
			Expect(jobName).To(Equal("spark-abc-def123-c0a7f49bbc"))
			Expect(full).To(Equal("abc-def123"))

			copyName, _, err := sanitizeWorkerId(full + "-secret1")
			Expect(err).ToNot(HaveOccurred())
			Expect(copyName).To(Equal("spark-abc-def123-secret1-56e21f765a"))
		})

		It("maps dots and underscores to dashes and lowercases", func() {
			_, full, err := sanitizeWorkerId("Order.4711_A")
			Expect(err).ToNot(HaveOccurred())
			Expect(full).To(Equal("order-4711-a"))
		})

		DescribeTable("fails without panicking when nothing valid remains",
			func(input string) {
				Expect(func() {
					_, _, err := sanitizeWorkerId(input)
					Expect(err).To(HaveOccurred())
				}).ToNot(Panic())
			},
			Entry("empty", ""),
			Entry("symbols only", "@@"),
			Entry("dashes only", "---"),
			Entry("dots and underscores only", "._"),
		)

		It("truncates long identifiers to a valid DNS label", func() {
			input := strings.Repeat("a", 42) + strings.Repeat("b", 30)

			jobName, full, err := sanitizeWorkerId(input)
			Expect(err).ToNot(HaveOccurred())
			Expect(full).To(Equal(input))
			Expect(jobName).To(HavePrefix("spark-" + strings.Repeat("a", 42) + "-"))
			Expect(jobName).To(HaveLen(59))
			Expect(validation.IsDNS1123Label(jobName)).To(BeEmpty())
		})

		It("keeps identifiers sharing a 42-character prefix distinct", func() {
			prefix := strings.Repeat("x", 42)

			first, _, err := sanitizeWorkerId(prefix + "1")
			Expect(err).ToNot(HaveOccurred())
			second, _, err := sanitizeWorkerId(prefix + "2")
			Expect(err).ToNot(HaveOccurred())

			Expect(first).ToNot(Equal(second))
		})
	})

	Describe("instanceLabelValue", func() {
		It("uses short names as they are", func() {
			Expect(instanceLabelValue("instance1")).To(Equal("instance1"))
		})

		It("shortens long names into a valid, stable label value", func() {
			name := strings.Repeat("a", 60) + "." + strings.Repeat("b", 60)

			value := instanceLabelValue(name)
			Expect(validation.IsValidLabelValue(value)).To(BeEmpty())
			Expect(value).To(Equal(instanceLabelValue(name)))
			Expect(value).ToNot(Equal(instanceLabelValue(name + "c")))
		})
	})

	Describe("validateSecretEntry", func() {
		DescribeTable("checks an embedded Secret",
			func(secret corev1.Secret, expected string) {
				err := validateSecretEntry(3, &secret)
				if expected == "" {
					Expect(err).To(BeNil())
					return
				}

				Expect(err).ToNot(BeNil())
				Expect(err.reason).To(Equal("SecretSpecInvalid"))
				Expect(err.message).To(HavePrefix("secrets[3]"))
				Expect(err.message).To(ContainSubstring(expected))
			},
			Entry("valid, no type meta", corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "secret1"}}, ""),
			Entry("valid, explicit type meta", corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Name: "a.b-c"}}, ""),
			Entry("wrong kind", corev1.Secret{TypeMeta: metav1.TypeMeta{Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Name: "s"}}, "kind must be Secret"),
			Entry("wrong apiVersion", corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v2"}, ObjectMeta: metav1.ObjectMeta{Name: "s"}}, "apiVersion must be v1"),
			Entry("missing name", corev1.Secret{}, "metadata.name is required"),
			Entry("uppercase name", corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "Secret1"}}, "invalid name"),
			Entry("underscore in name", corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "bad_name"}}, "invalid name"),
			Entry("name too long", corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("a", 254)}}, "invalid name"),
		)
	})

	Describe("decodeSecret", func() {
		It("rejects a document that is not a Secret object without echoing it", func() {
			for _, raw := range []string{`["a","b"]`, `"text"`, `{"metadata":"x"}`, `{"metadata":{"name":"s"},"data":{"k":"%%%not-base64"}}`} {
				_, err := decodeSecret(0, runtime.RawExtension{Raw: []byte(raw)})

				Expect(err).ToNot(BeNil(), raw)
				Expect(err.message).To(Equal("secrets[0]: not a valid Secret object"))
			}
		})

		It("treats null as a Secret without a name", func() {
			_, err := decodeSecret(1, runtime.RawExtension{Raw: []byte(`null`)})

			Expect(err).ToNot(BeNil())
			Expect(err.message).To(ContainSubstring("metadata.name is required"))
		})
	})

	Describe("collectSecretReferences", func() {
		It("finds every supported and unsupported reference", func() {
			spec := corev1.PodSpec{
				ImagePullSecrets: []corev1.LocalObjectReference{{Name: "pull"}},
				Volumes: []corev1.Volume{
					{Name: "v1", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "vol"}}},
					{Name: "v2", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{
						{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "cm"}}},
						{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "proj"}}},
					}}}},
					{Name: "v3", VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{Driver: "d", NodePublishSecretRef: &corev1.LocalObjectReference{Name: "csi"}}}},
					{Name: "v4", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				},
				InitContainers: []corev1.Container{{
					Name:    "init",
					EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "init-from"}}}},
					Env: []corev1.EnvVar{{Name: "E", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "init-key"}, Key: "k"}}}},
				}},
				Containers: []corev1.Container{{
					Name: "main",
					EnvFrom: []corev1.EnvFromSource{
						{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cm"}}},
						{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "main-from"}}},
					},
					Env: []corev1.EnvVar{
						{Name: "PLAIN", Value: "x"},
						{Name: "E", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "main-key"}, Key: "k"}}},
					},
				}},
			}

			supported, unsupported := collectSecretReferences(&spec)

			Expect(supported).To(ConsistOf(
				secretReference{field: "volumes[v1].secret", name: "vol"},
				secretReference{field: "volumes[v2].projected.sources[1].secret", name: "proj"},
				secretReference{field: "initContainers[init].envFrom", name: "init-from"},
				secretReference{field: "initContainers[init].env[E].secretKeyRef", name: "init-key"},
				secretReference{field: "containers[main].envFrom", name: "main-from"},
				secretReference{field: "containers[main].env[E].secretKeyRef", name: "main-key"},
			))
			Expect(unsupported).To(ConsistOf(
				secretReference{field: "volumes[v3].csi.nodePublishSecretRef", name: "csi"},
				secretReference{field: "imagePullSecrets", name: "pull"},
			))
		})
	})

	Describe("patchSecrets", func() {
		It("rewrites mapped names and leaves the others alone", func() {
			spec := corev1.PodSpec{
				Volumes: []corev1.Volume{
					{Name: "a", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "mapped"}}},
					{Name: "b", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "shared"}}},
				},
				ImagePullSecrets: []corev1.LocalObjectReference{{Name: "mapped"}},
			}

			patchSecrets([]computev1alpha1.SecretMapping{{OriginalSecretName: "mapped", RemappedSecretName: "copy"}}, &spec)

			Expect(spec.Volumes[0].Secret.SecretName).To(Equal("copy"))
			Expect(spec.Volumes[1].Secret.SecretName).To(Equal("shared"))
			Expect(spec.ImagePullSecrets[0].Name).To(Equal("mapped"))
		})
	})

	Describe("mergeMappings", func() {
		It("orders by the spec and keeps mappings of removed names", func() {
			request := &workerRequest{
				secrets:   []corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "b"}}, {ObjectMeta: metav1.ObjectMeta{Name: "a"}}},
				copyNames: map[string]string{"a": "copy-a", "b": "copy-b"},
			}
			existing := []computev1alpha1.SecretMapping{
				{OriginalSecretName: "a", RemappedSecretName: "copy-a"},
				{OriginalSecretName: "gone", RemappedSecretName: "copy-gone"},
			}

			Expect(mergeMappings(existing, request)).To(Equal([]computev1alpha1.SecretMapping{
				{OriginalSecretName: "b", RemappedSecretName: "copy-b"},
				{OriginalSecretName: "a", RemappedSecretName: "copy-a"},
				{OriginalSecretName: "gone", RemappedSecretName: "copy-gone"},
			}))
		})
	})

	Describe("validateRequest", func() {
		template := func(podSpec corev1.PodSpec) *computev1alpha1.WorkerTemplate {
			raw, err := json.Marshal(batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: podSpec}}})
			Expect(err).ToNot(HaveOccurred())

			return &computev1alpha1.WorkerTemplate{
				ObjectMeta: metav1.ObjectMeta{Name: "t"},
				Spec:       computev1alpha1.WorkerTemplateSpec{JobTemplate: runtime.RawExtension{Raw: raw}},
			}
		}
		instance := func(secrets ...corev1.Secret) *computev1alpha1.WorkerInstance {
			return &computev1alpha1.WorkerInstance{
				ObjectMeta: metav1.ObjectMeta{Name: "i"},
				Spec:       computev1alpha1.WorkerInstanceSpec{TemplateName: "t", Secrets: embed(secrets...)},
			}
		}

		It("uses the instance name when workerId is empty", func() {
			request, err := validateRequest(template(podSpecWithSecretVolumes()), instance())
			Expect(err).To(BeNil())
			Expect(request.jobName).To(Equal(expectedJobName("i")))
		})

		It("rejects an undecodable job template", func() {
			t := template(podSpecWithSecretVolumes())
			t.Spec.JobTemplate.Raw = []byte(`{"spec":"x"}`)

			_, err := validateRequest(t, instance())
			Expect(err).ToNot(BeNil())
			Expect(err.reason).To(Equal("JobSpecInvalid"))
			Expect(err.onTemplate).To(BeTrue())
		})

		It("reports unreferenced and namespace-resolved names without failing", func() {
			request, err := validateRequest(
				template(podSpecWithSecretVolumes("secret1", "shared-b", "shared-a")),
				instance(opaqueSecret("secret1", nil), opaqueSecret("extra", nil)),
			)
			Expect(err).To(BeNil())
			Expect(request.unreferenced).To(Equal([]string{"extra"}))
			Expect(request.namespaceResolved).To(Equal([]string{"shared-a", "shared-b"}))
		})

		It("accepts an unsupported reference to a Secret that is not embedded", func() {
			spec := podSpecWithSecretVolumes("secret1")
			spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "regcred"}}

			_, err := validateRequest(template(spec), instance(opaqueSecret("secret1", nil)))
			Expect(err).To(BeNil())
		})
	})

	Describe("isPersistentError", func() {
		resource := schema.GroupResource{Resource: "jobs"}
		kind := schema.GroupKind{Kind: "Job"}

		DescribeTable("classifies API errors",
			func(err error, persistent bool) {
				Expect(isPersistentError(err)).To(Equal(persistent))
			},
			Entry("nil", nil, false),
			Entry("invalid", errors.NewInvalid(kind, "j", field.ErrorList{field.Required(field.NewPath("spec"), "")}), true),
			Entry("bad request", errors.NewBadRequest("bad"), true),
			Entry("forbidden", errors.NewForbidden(resource, "j", fmt.Errorf("rbac")), true),
			Entry("method not supported", errors.NewMethodNotSupported(resource, "patch"), true),
			Entry("conflict", errors.NewConflict(resource, "j", fmt.Errorf("stale")), false),
			Entry("already exists", errors.NewAlreadyExists(resource, "j"), false),
			Entry("not found", errors.NewNotFound(resource, "j"), false),
			Entry("server timeout", errors.NewServerTimeout(resource, "create", 1), false),
			Entry("plain error", fmt.Errorf("connection reset"), false),
		)
	})
})
