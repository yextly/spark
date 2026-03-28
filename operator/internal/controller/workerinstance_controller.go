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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"

	corev1 "k8s.io/api/core/v1"

	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"spark/api/v1alpha1"
	computev1alpha1 "spark/api/v1alpha1"
)

// WorkerInstanceReconciler reconciles a WorkerInstance object
type WorkerInstanceReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	EventRecorder record.EventRecorder
}

const finalizerName = "compute.yextly.io/workerinstance"
const associatedToAnnotationName = "yextly.io/associated-to"

// +kubebuilder:rbac:groups=compute.yextly.io,resources=workerinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.yextly.io,resources=workerinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.yextly.io,resources=workerinstances/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the WorkerInstance object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *WorkerInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	// Note that we are not using OwnedReferences since they use a UID and since we will be hosted by different PODs
	// and by design we want to upgrade the operator while the cluster is running, we should go on haunt to fix the UID everytime.
	// The current approach seems good enough for our purposes (at least for now).

	logger.Info(">>> Reconciliation", "namespace", req.Namespace, "name", req.Name)

	instance := &computev1alpha1.WorkerInstance{}

	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("The instance has been deleted")

			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Check if we are deleting
	if !instance.DeletionTimestamp.IsZero() {
		logger.Info("The instance is being deleted")

		// This will be updated the first time the resource is updated
		setCondition(instance, v1alpha1.WorkerProvisioningDeleting, "ResourceDeletion", "Deleting the instance")

		if err := r.Update(ctx, instance); err != nil {
			logger.Error(err, "Failed to transition the state")

			return ctrl.Result{}, err
		}

		if instance.Status.JobName != "" {
			// If we have an assigned job, we must delete it
			logger.Info("Deleting the associated job", "name", instance.Status.JobName)

			job := &batchv1.Job{}
			err := r.Get(ctx, types.NamespacedName{
				Namespace: instance.Namespace,
				Name:      instance.Status.JobName,
			}, job)

			if err == nil {
				// We have a job do delete
				instance.Status.JobName = ""

				if err := r.Update(ctx, instance); err != nil {
					logger.Error(err, "Failed to update the resource")

					return ctrl.Result{}, err
				}

				return ctrl.Result{}, r.Delete(ctx, job)
			} else if errors.IsNotFound(err) {
				instance.Status.JobName = ""
				if err := r.Update(ctx, instance); err != nil {
					logger.Error(err, "Failed to update the resource")

					return ctrl.Result{}, err
				}
			} else {
				logger.Error(err, "Ignored error")
			}
		}

		if len(instance.Status.SecretMappings) > 0 {
			// Delete the secrets
			for _, s := range instance.Status.SecretMappings {
				if existingSecret := r.getExistingSecret(ctx, s.RemappedSecretName, instance.Namespace); existingSecret != nil {
					if err := r.Delete(ctx, existingSecret); err != nil {
						logger.Error(err, "Failed to delete the secret", "name", existingSecret.ObjectMeta.Name)

						return ctrl.Result{}, err
					}
				}
			}

			instance.Status.SecretMappings = nil
			if err := r.Update(ctx, instance); err != nil {
				logger.Error(err, "Failed to update the status")

				return ctrl.Result{}, err
			}
		}

		// Remove the finalizer and allow deletion
		controllerutil.RemoveFinalizer(instance, finalizerName)
		if err := r.Update(ctx, instance); err != nil {
			logger.Error(err, "Failed to remove finalizer")

			return ctrl.Result{}, err
		}

		// At this point, the instance state is terminated and the finalizer is removed
		return ctrl.Result{}, nil
	}

	if instance.Status.JobName != "" {
		// If the job is no longer available, then self delete ourselves

		job := &batchv1.Job{}
		err := r.Get(ctx, types.NamespacedName{
			Namespace: instance.Namespace,
			Name:      instance.Status.JobName,
		}, job)

		if errors.IsNotFound(err) {
			logger.Info("The associated Job does no longer exist; deleting WorkerInstance", "job", instance.Status.JobName)

			return ctrl.Result{}, r.Delete(ctx, instance)
		}
	}

	if instance.Status.JobName == "" && instance.Status.ProvisioningState != v1alpha1.WorkerProvisioningFailed {
		// This is the creation path

		logger.Info("Add finalizer")

		setCondition(instance, v1alpha1.WorkerProvisioningCreating, "ResourceCreation", "Creating the instance")

		if !controllerutil.ContainsFinalizer(instance, finalizerName) {
			instance.Finalizers = append(instance.Finalizers, finalizerName)
			if err := r.Update(ctx, instance); err != nil {
				logger.Error(err, "Failed to add the finalizer")

				return ctrl.Result{}, err
			}
		}

		template, err := r.getTemplate(&logger, ctx, instance.Spec.TemplateName, instance.Namespace)
		if err != nil {
			logger.Error(err, "Failed to get the template")

			return ctrl.Result{}, err
		}

		jobInstance, err, fail := r.scheduleInstance(&logger, ctx, template, instance)
		if fail {
			if err != nil {
				logger.Error(err, "Persistent operation error. No retry will occur")
			} else {
				logger.Error(nil, "The operation will not be retried since the error is persistent")
			}

			setCondition(instance, v1alpha1.WorkerProvisioningFailed, "Failed", "Failed to create the instance")

			if err := r.Update(ctx, instance); err != nil {
				logger.Error(err, "Failed to update the resource")

				return ctrl.Result{}, err
			}

			return ctrl.Result{}, nil
		}
		if err != nil {
			logger.Error(err, "Failed to schedule the instance")

			return ctrl.Result{}, err
		}

		instance.Status.JobName = jobInstance.Name

		setCondition(instance, v1alpha1.WorkerProvisioningRunning, "JobCreation", "Schedule associated job")

		err = r.Status().Update(ctx, instance)
		if err != nil {
			logger.Error(err, "Failed to update the status")

			return ctrl.Result{}, err
		}

		r.EventRecorder.Event(
			instance,
			corev1.EventTypeNormal,
			"WorkerReady",
			"The worker instance has been successfully created",
		)

	} else if instance.Status.ProvisioningState == v1alpha1.WorkerProvisioningFailed {
		logger.Info("The resource is in a failed state, nothing else can be done")
		return ctrl.Result{}, nil
	} else {
		logger.Info("The resource is already bound to a Job", "jobInstanceId", instance.Status.JobName)
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func setCondition(instance *computev1alpha1.WorkerInstance, status computev1alpha1.WorkerProvisioningState, reason string, message string) {
	meta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
		Type:    string(status),
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: message,
	})
	instance.Status.ProvisioningState = status
}

func (r *WorkerInstanceReconciler) getTemplate(logger *logr.Logger, ctx context.Context, name string, namespace string) (*v1alpha1.WorkerTemplate, error) {
	template := &computev1alpha1.WorkerTemplate{}

	key := client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}

	if err := r.Client.Get(ctx, key, template); err != nil {
		logger.Error(err, "The template is not available", "name", name, "namespace", namespace)
		return nil, err
	}
	return template, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkerInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.EventRecorder = mgr.GetEventRecorderFor("spark operator")

	return ctrl.NewControllerManagedBy(mgr).
		For(&computev1alpha1.WorkerInstance{}).
		Named("workerinstance").
		Watches(
			&batchv1.Job{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
				job := o.(*batchv1.Job)
				instanceName, ok := job.Annotations[associatedToAnnotationName]
				if !ok {
					return nil
				}
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Namespace: job.Namespace,
						Name:      instanceName,
					},
				}}
			}),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Complete(r)
}

// Generates a JobName in such a way that is resistant to collisions and does not violate naming rules. The total length is at worst 60 characters
func sanitizeWorkerId(input string) (string, string) {
	s := strings.ToLower(input)

	s = strings.NewReplacer(".", "-", "_", "-").Replace(s)

	// Keep only a-z and '-' and remove everything not allowed
	reg := regexp.MustCompile(`[^a-z-]`)
	s = reg.ReplaceAllString(s, "")

	if s == "" {
		panic("Invalid input string")
	}

	temp := s

	hashBytes := sha256.Sum256([]byte(temp))
	hashHex := fmt.Sprintf("%x", hashBytes)

	const maxUserLength = 42

	if len(temp) > maxUserLength {
		temp = temp[:maxUserLength]
	}

	final := "worker-" + temp + "-" + hashHex[:10]

	return final, s
}

// Creates and schedules a Job. In case of known failures, "fail" is set. When "fail" is set, **no job is created** by design
func (r *WorkerInstanceReconciler) scheduleInstance(logger *logr.Logger, ctx context.Context, template *computev1alpha1.WorkerTemplate, instance *computev1alpha1.WorkerInstance) (jobInstance *batchv1.Job, err error, fail bool) {

	workerId := instance.Spec.WorkerId

	if workerId == "" {
		workerId = instance.Name
	}

	sanitizedWorkerId, fullInternalWorkerId := sanitizeWorkerId(workerId)

	err, fail, faultSecret := r.createSecrets(logger, ctx, instance, fullInternalWorkerId)
	if err != nil {
		if fail {
			r.EventRecorder.Event(
				faultSecret,
				corev1.EventTypeWarning,
				"ScretSpecInvalid",
				"The secret specification is invalid",
			)
		}

		return nil, err, fail
	}

	job, err, fail := r.createJob(logger, ctx, template, instance, sanitizedWorkerId)
	if err != nil {

		if fail {
			r.EventRecorder.Event(
				template,
				corev1.EventTypeWarning,
				"JobSpecInvalid",
				"The job specification is invalid",
			)
		}

		return nil, err, fail
	}

	return job, nil, false
}

// Creates the job
func (r *WorkerInstanceReconciler) createJob(logger *logr.Logger, ctx context.Context, template *computev1alpha1.WorkerTemplate, instance *computev1alpha1.WorkerInstance, sanitizedWorkerId string) (jobInstance *batchv1.Job, err error, fail bool) {
	var blueprint batchv1.JobTemplateSpec

	if err := json.Unmarshal(template.Spec.JobTemplate.Raw, &blueprint); err != nil {
		logger.Error(err, "Invalid JobTemplateSpec in resource", "name", template.Name)

		return nil, fmt.Errorf("Cannot decode JobTemplateSpec: %w", err), true
	}

	isValid := naivelyValidateJob(&blueprint)

	if !isValid {
		return nil, fmt.Errorf("The job specification is not correct"), true
	}

	job := &batchv1.Job{
		ObjectMeta: blueprint.ObjectMeta,
		Spec:       blueprint.Spec,
	}

	job.ObjectMeta.GenerateName = ""
	job.ObjectMeta.Name = sanitizedWorkerId
	job.ObjectMeta.Namespace = instance.Namespace

	job.Spec.Template.Spec.RestartPolicy = v1.RestartPolicyNever

	if job.ObjectMeta.Annotations == nil {
		job.ObjectMeta.Annotations = make(map[string]string)
	}

	if instance.Spec.TTLSecondsAfterFinished != nil {
		job.Spec.TTLSecondsAfterFinished = instance.Spec.TTLSecondsAfterFinished
	}

	job.ObjectMeta.Annotations[associatedToAnnotationName] = instance.Name

	patchSecrets(instance, job)

	logger.Info("About to schedule the job", "job", job)

	err = r.Client.Create(ctx, job)
	if err != nil {
		logger.Error(err, "Cannot schedule the job")
		return nil, err, isPersistentError(err)
	}

	return job, nil, false
}

// Patches the secrets in order to be redirected to the remapped and per-instance ones
func patchSecrets(instance *computev1alpha1.WorkerInstance, job *batchv1.Job) {

	var secrets = instance.Status.SecretMappings
	if len(secrets) == 0 {
		return
	}

	// Volumes could use secrets
	if volumes := job.Spec.Template.Spec.Volumes; len(volumes) > 0 {
		for _, volume := range volumes {
			if s := volume.Secret; s != nil {
				s.SecretName = remapSecret(secrets, s.SecretName)
			}
		}
	}

	// Env vars could use secrets
	if containers := job.Spec.Template.Spec.Containers; len(containers) > 0 {
		for _, container := range containers {

			// EnvFrom in container
			for _, envFrom := range container.EnvFrom {
				if s := envFrom.SecretRef; s != nil {
					s.Name = remapSecret(secrets, s.Name)
				}
			}

			// ValueFrom in container environment
			for _, env := range container.Env {
				if vf := env.ValueFrom; vf != nil {
					if skf := vf.SecretKeyRef; skf != nil {
						skf.Name = remapSecret(secrets, skf.Name)
					}
				}
			}
		}
	}
}

func remapSecret(secrets []computev1alpha1.SecretMapping, name string) string {
	for _, secret := range secrets {
		if secret.OriginalSecretName == name {
			return secret.RemappedSecretName
		}
	}

	return name
}

// Creates the secrets
func (r *WorkerInstanceReconciler) createSecrets(logger *logr.Logger, ctx context.Context, instance *computev1alpha1.WorkerInstance, fullWorkerId string) (err error, fail bool, faultSecret *v1.Secret) {
	expectedLength := len(instance.Spec.Secrets)
	actualLength := len(instance.Status.SecretMappings)

	if expectedLength == actualLength {
		return nil, false, nil
	}

	if instance.Status.SecretMappings == nil {
		instance.Status.SecretMappings = make([]computev1alpha1.SecretMapping, 0)
	}

	for i := actualLength; i < expectedLength; i++ {
		secret := instance.Spec.Secrets[i]
		remappedSecretName, _ := sanitizeWorkerId(fullWorkerId + "-" + secret.Name)

		newSecret := secret.DeepCopy()

		// On purpose create a new meta to avoid attacks
		newMeta := metav1.ObjectMeta{
			Name:        remappedSecretName,
			Namespace:   instance.Namespace,
			Annotations: make(map[string]string),
		}

		newMeta.Annotations[associatedToAnnotationName] = instance.Name
		newSecret.ObjectMeta = newMeta

		// We force immutability to prevent unwanted upgrades
		newSecret.Immutable = pointer.Bool(true)

		if existingSecret := r.getExistingSecret(ctx, remappedSecretName, instance.Namespace); existingSecret == nil {
			err = r.Client.Create(ctx, newSecret)
			if err != nil {
				logger.Error(err, "Cannot create the secret", "name", remappedSecretName)
				return err, isPersistentError(err), &secret
			}
		}

		mapping := v1alpha1.SecretMapping{
			OriginalSecretName: secret.Name,
			RemappedSecretName: remappedSecretName,
		}

		instance.Status.SecretMappings = append(instance.Status.SecretMappings, mapping)

		err = r.Status().Update(ctx, instance)
		if err != nil {
			logger.Error(err, "Failed to update the secret status")

			return err, isPersistentError(err), nil
		}
	}

	return nil, false, nil
}

func (r *WorkerInstanceReconciler) getExistingSecret(ctx context.Context, name string, namespace string) (secret *v1.Secret) {
	s := &v1.Secret{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}, s)

	if err == nil {
		return nil
	} else {
		return s
	}
}

func naivelyValidateJob(spec *batchv1.JobTemplateSpec) bool {
	if len(spec.Spec.Template.Spec.Containers) == 0 {
		return false
	}

	return true
}

// isPersistentError returns true if the error indicates the request
// is invalid and retrying will *never* succeed. These are semantic
// or structural errors that require user action.
//
// Returns false for errors that are transient and may succeed if retried.
func isPersistentError(err error) bool {
	if err == nil {
		return false
	}

	switch {
	// The object is invalid (field validation, schema violation, etc.)
	case errors.IsInvalid(err):
		return true

	// The request itself is malformed (bad syntax, wrong types, etc.)
	case errors.IsBadRequest(err):
		return true

	// Forbidden usually means RBAC denial. This will not change by retrying.
	case errors.IsForbidden(err):
		return true

	// Method not allowed, not supported — retrying won't fix it.
	case errors.IsMethodNotSupported(err):
		return true

	default:
		return false
	}
}
