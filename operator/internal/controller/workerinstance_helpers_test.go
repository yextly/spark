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
	"fmt"

	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	computev1alpha1 "spark/api/v1alpha1"
)

// Creates an isolated namespace, so each spec sees only its own objects
func newTestNamespace(ctx context.Context) string {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "wi-"}}
	Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

	return namespace.Name
}

func mainContainer() corev1.Container {
	return corev1.Container{Name: "main", Image: "busybox"}
}

// A pod spec mounting each named Secret as a volume
func podSpecWithSecretVolumes(names ...string) corev1.PodSpec {
	spec := corev1.PodSpec{Containers: []corev1.Container{mainContainer()}}

	for i, name := range names {
		volume := fmt.Sprintf("volume%d", i)

		spec.Volumes = append(spec.Volumes, corev1.Volume{
			Name:         volume,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name}},
		})
		spec.Containers[0].VolumeMounts = append(spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      volume,
			MountPath: "/var/secrets/" + name,
		})
	}

	return spec
}

func createTemplate(ctx context.Context, namespace string, name string, podSpec corev1.PodSpec, mutate ...func(*batchv1.JobTemplateSpec)) *computev1alpha1.WorkerTemplate {
	jobTemplate := batchv1.JobTemplateSpec{
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{Spec: podSpec},
		},
	}
	for _, m := range mutate {
		m(&jobTemplate)
	}

	raw, err := json.Marshal(jobTemplate)
	Expect(err).ToNot(HaveOccurred())

	template := &computev1alpha1.WorkerTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       computev1alpha1.WorkerTemplateSpec{JobTemplate: runtime.RawExtension{Raw: raw}},
	}
	Expect(k8sClient.Create(ctx, template)).To(Succeed())

	return template
}

func opaqueSecret(name string, values map[string]string) corev1.Secret {
	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Data:       map[string][]byte{},
	}
	for k, v := range values {
		secret.Data[k] = []byte(v)
	}

	return secret
}

func embed(secrets ...corev1.Secret) []runtime.RawExtension {
	result := make([]runtime.RawExtension, 0, len(secrets))

	for _, s := range secrets {
		raw, err := json.Marshal(s)
		Expect(err).ToNot(HaveOccurred())

		result = append(result, runtime.RawExtension{Raw: raw})
	}

	return result
}

func embedRaw(documents ...string) []runtime.RawExtension {
	result := make([]runtime.RawExtension, 0, len(documents))

	for _, d := range documents {
		result = append(result, runtime.RawExtension{Raw: []byte(d)})
	}

	return result
}

func createInstance(ctx context.Context, namespace string, name string, templateName string, workerId string, secrets []runtime.RawExtension, mutate ...func(*computev1alpha1.WorkerInstance)) *computev1alpha1.WorkerInstance {
	instance := &computev1alpha1.WorkerInstance{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: computev1alpha1.WorkerInstanceSpec{
			TemplateName: templateName,
			WorkerId:     workerId,
			Secrets:      secrets,
		},
	}
	for _, m := range mutate {
		m(instance)
	}

	Expect(k8sClient.Create(ctx, instance)).To(Succeed())

	return instance
}

func newTestReconciler(c client.Client) (*WorkerInstanceReconciler, *record.FakeRecorder) {
	recorder := record.NewFakeRecorder(1000)

	return &WorkerInstanceReconciler{
		Client:        c,
		Scheme:        c.Scheme(),
		EventRecorder: recorder,
	}, recorder
}

func reconcileInstance(ctx context.Context, r *WorkerInstanceReconciler, namespace string, name string) (ctrl.Result, error) {
	return r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}})
}

func getInstance(ctx context.Context, namespace string, name string) *computev1alpha1.WorkerInstance {
	instance := &computev1alpha1.WorkerInstance{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, instance)).To(Succeed())

	return instance
}

func instanceExists(ctx context.Context, namespace string, name string) bool {
	err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &computev1alpha1.WorkerInstance{})
	if errors.IsNotFound(err) {
		return false
	}
	Expect(err).ToNot(HaveOccurred())

	return true
}

func getSecret(ctx context.Context, namespace string, name string) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret)

	return secret, err
}

func getJob(ctx context.Context, namespace string, name string) (*batchv1.Job, error) {
	job := &batchv1.Job{}
	err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, job)

	return job, err
}

func listSecrets(ctx context.Context, namespace string) []corev1.Secret {
	list := &corev1.SecretList{}
	Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(Succeed())

	return list.Items
}

func listJobs(ctx context.Context, namespace string) []batchv1.Job {
	list := &batchv1.JobList{}
	Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(Succeed())

	return list.Items
}

// Copy names are deterministic: they are the names the reconciler derives for the given workerId
func expectedCopyName(workerId string, original string) string {
	_, full, err := sanitizeWorkerId(workerId)
	Expect(err).ToNot(HaveOccurred())

	name, _, err := sanitizeWorkerId(full + "-" + original)
	Expect(err).ToNot(HaveOccurred())

	return name
}

func expectedJobName(workerId string) string {
	name, _, err := sanitizeWorkerId(workerId)
	Expect(err).ToNot(HaveOccurred())

	return name
}

// envtest has no garbage collector: remove the deletion finalizers it would have processed
func simulateJobGarbageCollection(ctx context.Context, namespace string, name string) {
	job, err := getJob(ctx, namespace, name)
	if errors.IsNotFound(err) {
		return
	}
	Expect(err).ToNot(HaveOccurred())
	Expect(job.DeletionTimestamp).ToNot(BeNil())

	job.Finalizers = nil
	Expect(k8sClient.Update(ctx, job)).To(Succeed())
}

// Deletes the instance and drives the reconciler (and the simulated garbage collector) until it is gone
func deleteInstanceFully(ctx context.Context, r *WorkerInstanceReconciler, namespace string, name string) {
	Expect(k8sClient.Delete(ctx, getInstance(ctx, namespace, name))).To(Succeed())

	for range 10 {
		if !instanceExists(ctx, namespace, name) {
			return
		}

		result, err := reconcileInstance(ctx, r, namespace, name)
		Expect(err).ToNot(HaveOccurred())

		if result.RequeueAfter > 0 {
			for _, job := range listJobs(ctx, namespace) {
				if job.DeletionTimestamp != nil {
					simulateJobGarbageCollection(ctx, namespace, job.Name)
				}
			}
		}
	}

	Expect(instanceExists(ctx, namespace, name)).To(BeFalse(), "the instance was not deleted")
}

func drainEvents(recorder *record.FakeRecorder) []string {
	var events []string

	for {
		select {
		case e := <-recorder.Events:
			events = append(events, e)
		default:
			return events
		}
	}
}

// faultyClient fails the Nth write after performing it, which models a crash between the write and what follows.
// It can also fail the deletion of one named object once, before performing it.
type faultyClient struct {
	client.Client

	failAfterWrite int
	writes         int

	failDeleteOnce string
}

func (f *faultyClient) afterWrite() error {
	f.writes++
	if f.writes == f.failAfterWrite {
		return fmt.Errorf("injected failure after write %d", f.writes)
	}

	return nil
}

func (f *faultyClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := f.Client.Create(ctx, obj, opts...); err != nil {
		return err
	}
	return f.afterWrite()
}

func (f *faultyClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if err := f.Client.Update(ctx, obj, opts...); err != nil {
		return err
	}
	return f.afterWrite()
}

func (f *faultyClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if f.failDeleteOnce != "" && obj.GetName() == f.failDeleteOnce {
		f.failDeleteOnce = ""
		return fmt.Errorf("injected failure deleting %s", obj.GetName())
	}

	if err := f.Client.Delete(ctx, obj, opts...); err != nil {
		return err
	}
	return f.afterWrite()
}

func (f *faultyClient) Status() client.SubResourceWriter {
	return &faultyStatusWriter{SubResourceWriter: f.Client.Status(), parent: f}
}

type faultyStatusWriter struct {
	client.SubResourceWriter
	parent *faultyClient
}

func (w *faultyStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if err := w.SubResourceWriter.Update(ctx, obj, opts...); err != nil {
		return err
	}
	return w.parent.afterWrite()
}
