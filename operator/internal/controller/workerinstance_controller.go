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
	"k8s.io/client-go/tools/record"

	corev1 "k8s.io/api/core/v1"

	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

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
const jobAnnotationName = "yextly.io/associated-to"

// +kubebuilder:rbac:groups=compute.yextly.io,resources=workerinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.yextly.io,resources=workerinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.yextly.io,resources=workerinstances/finalizers,verbs=update

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

		// _, err := deleteInstance(ctx, instance)
		// if err != nil {
		// 	l.Error(err, "Failed to delete instance")
		// 	// Kubernetes will retry with backoff
		// 	return ctrl.Result{Requeue: true}, err
		// }

		// Remove the finalizer
		controllerutil.RemoveFinalizer(instance, finalizerName)
		if err := r.Update(ctx, instance); err != nil {
			logger.Error(err, "Failed to remove finalizer")

			return ctrl.Result{}, err
		}
		// At this point, the instance state is terminated and the finalizer is removed
		return ctrl.Result{}, nil
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

			r.EventRecorder.Event(
				template,
				corev1.EventTypeWarning,
				"JobSpecInvalid",
				"The job specification is not correct",
			)

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

	sanitizedWorkerId, _ := sanitizeWorkerId(workerId)

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

	if blueprint.Spec.TTLSecondsAfterFinished != nil {
		job.Spec.TTLSecondsAfterFinished = blueprint.Spec.TTLSecondsAfterFinished
	}

	job.ObjectMeta.Annotations[jobAnnotationName] = instance.Name

	logger.Info("About to schedule the job", "job", job)

	err = r.Client.Create(ctx, job)
	if err != nil {
		logger.Error(err, "Cannot schedule the job")
		return nil, err, false
	}

	return job, nil, false
}

func naivelyValidateJob(spec *batchv1.JobTemplateSpec) bool {
	if len(spec.Spec.Template.Spec.Containers) == 0 {
		return false
	}

	return true
}
