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
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	computev1alpha1 "spark/api/v1alpha1"
)

// WorkerInstanceReconciler reconciles a WorkerInstance object
type WorkerInstanceReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	EventRecorder record.EventRecorder
}

const (
	finalizerName              = "compute.yextly.io/workerinstance"
	associatedToAnnotationName = "yextly.io/associated-to"
	instanceLabelName          = "compute.yextly.io/workerinstance"
	managedByLabelName         = "app.kubernetes.io/managed-by"
	managedByLabelValue        = "spark-operator"

	blockedConditionType          = "Blocked"
	secretReferencesConditionType = "SecretReferences"

	// A blocked instance is not woken up by the watches (Job events map to the owning instance), so it polls.
	blockedRequeueInterval     = 30 * time.Second
	jobDeletionRequeueInterval = 5 * time.Second
)

var invalidWorkerIdCharacters = regexp.MustCompile(`[^a-z0-9-]`)

// +kubebuilder:rbac:groups=compute.yextly.io,resources=workerinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.yextly.io,resources=workerinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.yextly.io,resources=workerinstances/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
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

	switch {
	case !instance.DeletionTimestamp.IsZero():
		return r.reconcileDelete(ctx, &logger, instance)
	case instance.Status.ProvisioningState == computev1alpha1.WorkerProvisioningFailed:
		logger.Info("The resource is in a failed state, nothing else can be done")
		return ctrl.Result{}, nil
	case instance.Status.JobName != "":
		return r.reconcileRunning(ctx, &logger, instance)
	default:
		return r.reconcileCreate(ctx, &logger, instance)
	}
}

func (r *WorkerInstanceReconciler) reconcileRunning(ctx context.Context, logger *logr.Logger, instance *computev1alpha1.WorkerInstance) (ctrl.Result, error) {
	job := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Namespace: instance.Namespace, Name: instance.Status.JobName}, job)

	// A Job with our name but another owner means ours expired and another instance with the same workerId took the name over
	if errors.IsNotFound(err) || (err == nil && job.Annotations[associatedToAnnotationName] != instance.Name) {
		logger.Info("The associated Job does no longer exist; deleting WorkerInstance", "job", instance.Status.JobName)

		return ctrl.Result{}, client.IgnoreNotFound(r.Delete(ctx, instance))
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("The resource is already bound to a Job", "jobInstanceId", instance.Status.JobName)
	return ctrl.Result{}, nil
}

func (r *WorkerInstanceReconciler) reconcileCreate(ctx context.Context, logger *logr.Logger, instance *computev1alpha1.WorkerInstance) (ctrl.Result, error) {
	// The finalizer goes first: an update of the main resource overwrites any in-memory status change with the stored one
	if !controllerutil.ContainsFinalizer(instance, finalizerName) {
		logger.Info("Add finalizer")

		controllerutil.AddFinalizer(instance, finalizerName)
		if err := r.Update(ctx, instance); err != nil {
			logger.Error(err, "Failed to add the finalizer")

			return ctrl.Result{}, err
		}
	}

	if instance.Status.ProvisioningState != computev1alpha1.WorkerProvisioningCreating {
		if err := r.setStatus(ctx, instance, computev1alpha1.WorkerProvisioningCreating, "ResourceCreation", "Creating the instance"); err != nil {
			logger.Error(err, "Failed to update the status")

			return ctrl.Result{}, err
		}
	}

	template, err := r.getTemplate(logger, ctx, instance.Spec.TemplateName, instance.Namespace)
	if err != nil {
		logger.Error(err, "Failed to get the template")

		return ctrl.Result{}, err
	}

	request, reqErr := validateRequest(template, instance)
	if reqErr != nil {
		return r.fail(ctx, logger, instance, template, reqErr)
	}

	r.reportReferences(instance, request)

	claim, err := r.claim(ctx, instance, request)
	if err != nil {
		return ctrl.Result{}, err
	}

	if claim.blockedBy != "" {
		return r.block(ctx, logger, instance, claim.blockedBy)
	}

	if meta.IsStatusConditionTrue(instance.Status.Conditions, blockedConditionType) {
		meta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:    blockedConditionType,
			Status:  metav1.ConditionFalse,
			Reason:  "OwnershipAvailable",
			Message: "No other instance holds the Job or the Secret copies",
		})
	}

	if claim.adoptedJob != nil {
		// A previous reconcile created the Job but did not record it
		logger.Info("Adopting the existing job", "name", claim.adoptedJob.Name)

		instance.Status.SecretMappings = mergeMappings(instance.Status.SecretMappings, request)
		return r.markRunning(ctx, logger, instance, claim.adoptedJob.Name)
	}

	if err := r.createSecrets(ctx, logger, instance, request, claim.ownedCopies); err != nil {
		if isPersistentError(err) {
			return r.fail(ctx, logger, instance, template, &requestError{
				reason:  "SecretCreationFailed",
				message: fmt.Sprintf("Cannot create the Secret copies: %s", errors.ReasonForError(err)),
			})
		}

		return ctrl.Result{}, err
	}

	job, err := r.createJob(ctx, logger, instance, request)
	if err != nil {
		if isPersistentError(err) {
			// The copies are kept for inspection; the deletion path removes them
			return r.fail(ctx, logger, instance, template, &requestError{
				reason:     "JobSpecInvalid",
				message:    fmt.Sprintf("The job specification of template %q is invalid: %v", template.Name, err),
				onTemplate: true,
			})
		}

		// AlreadyExists included: the next reconcile decides between adopting and waiting
		return ctrl.Result{}, err
	}

	return r.markRunning(ctx, logger, instance, job.Name)
}

func (r *WorkerInstanceReconciler) markRunning(ctx context.Context, logger *logr.Logger, instance *computev1alpha1.WorkerInstance, jobName string) (ctrl.Result, error) {
	instance.Status.JobName = jobName

	if err := r.setStatus(ctx, instance, computev1alpha1.WorkerProvisioningRunning, "JobCreation", "Schedule associated job"); err != nil {
		logger.Error(err, "Failed to update the status")

		return ctrl.Result{}, err
	}

	r.EventRecorder.Event(
		instance,
		corev1.EventTypeNormal,
		"WorkerReady",
		"The worker instance has been successfully created",
	)

	return ctrl.Result{}, nil
}

// Stores Failed with the reason; the request is never retried
func (r *WorkerInstanceReconciler) fail(ctx context.Context, logger *logr.Logger, instance *computev1alpha1.WorkerInstance, template *computev1alpha1.WorkerTemplate, reqErr *requestError) (ctrl.Result, error) {
	logger.Error(reqErr, "Persistent operation error. No retry will occur", "reason", reqErr.reason)

	r.EventRecorder.Event(instance, corev1.EventTypeWarning, reqErr.reason, reqErr.message)
	if reqErr.onTemplate && template != nil {
		r.EventRecorder.Event(template, corev1.EventTypeWarning, reqErr.reason, reqErr.message)
	}

	if err := r.setStatus(ctx, instance, computev1alpha1.WorkerProvisioningFailed, reqErr.reason, reqErr.message); err != nil {
		logger.Error(err, "Failed to update the status")

		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// Waits until the instance holding the Job or the Secret copies is gone
func (r *WorkerInstanceReconciler) block(ctx context.Context, logger *logr.Logger, instance *computev1alpha1.WorkerInstance, blockedBy string) (ctrl.Result, error) {
	message := fmt.Sprintf("Waiting for resources held by %s", blockedBy)

	logger.Info("The instance is blocked", "by", blockedBy)

	changed := meta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
		Type:    blockedConditionType,
		Status:  metav1.ConditionTrue,
		Reason:  "OwnershipConflict",
		Message: message,
	})

	if err := r.Status().Update(ctx, instance); err != nil {
		logger.Error(err, "Failed to update the status")

		return ctrl.Result{}, err
	}

	if changed {
		r.EventRecorder.Event(instance, corev1.EventTypeWarning, "OwnershipConflict", message)
	}

	return ctrl.Result{RequeueAfter: blockedRequeueInterval}, nil
}

func (r *WorkerInstanceReconciler) reconcileDelete(ctx context.Context, logger *logr.Logger, instance *computev1alpha1.WorkerInstance) (ctrl.Result, error) {
	logger.Info("The instance is being deleted")

	if !controllerutil.ContainsFinalizer(instance, finalizerName) {
		return ctrl.Result{}, nil
	}

	if instance.Status.ProvisioningState != computev1alpha1.WorkerProvisioningDeleting {
		if err := r.setStatus(ctx, instance, computev1alpha1.WorkerProvisioningDeleting, "ResourceDeletion", "Deleting the instance"); err != nil {
			logger.Error(err, "Failed to transition the state")

			return ctrl.Result{}, err
		}
	}

	// Without a recorded Job, a crash may still have left one behind under the deterministic name
	jobName := instance.Status.JobName
	if jobName == "" {
		jobName, _, _ = sanitizeWorkerId(effectiveWorkerId(instance))
	}

	if jobName != "" {
		job := &batchv1.Job{}
		err := r.Get(ctx, types.NamespacedName{Namespace: instance.Namespace, Name: jobName}, job)

		switch {
		case err == nil && job.Annotations[associatedToAnnotationName] == instance.Name:
			// Foreground keeps the Job until its pods are gone, so the Secret copies outlive every pod using them
			if job.DeletionTimestamp.IsZero() {
				logger.Info("Deleting the associated job", "name", jobName)

				if err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationForeground)); client.IgnoreNotFound(err) != nil {
					logger.Error(err, "Failed to delete the job", "name", jobName)

					return ctrl.Result{}, err
				}
			}

			return ctrl.Result{RequeueAfter: jobDeletionRequeueInterval}, nil
		case err == nil, errors.IsNotFound(err):
			// Gone, or held by another instance
		default:
			return ctrl.Result{}, err
		}
	}

	if err := r.deleteOwnedSecrets(ctx, logger, instance); err != nil {
		return ctrl.Result{}, err
	}

	if instance.Status.JobName != "" || len(instance.Status.SecretMappings) > 0 {
		instance.Status.JobName = ""
		instance.Status.SecretMappings = nil

		if err := r.Status().Update(ctx, instance); err != nil {
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

	return ctrl.Result{}, nil
}

// Deletes every copy owned by the instance: the labelled ones (covers copies never recorded in the status) and the
// recorded ones (covers copies created before labels existed). A Secret owned by another instance is never touched.
func (r *WorkerInstanceReconciler) deleteOwnedSecrets(ctx context.Context, logger *logr.Logger, instance *computev1alpha1.WorkerInstance) error {
	candidates := make(map[string]*corev1.Secret)

	list := &corev1.SecretList{}
	if err := r.List(ctx, list, client.InNamespace(instance.Namespace), client.MatchingLabels{instanceLabelName: instanceLabelValue(instance.Name)}); err != nil {
		logger.Error(err, "Failed to list the secrets")

		return err
	}
	for i := range list.Items {
		candidates[list.Items[i].Name] = &list.Items[i]
	}

	for _, mapping := range instance.Status.SecretMappings {
		if _, ok := candidates[mapping.RemappedSecretName]; ok {
			continue
		}

		secret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{Namespace: instance.Namespace, Name: mapping.RemappedSecretName}, secret)
		if errors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		candidates[secret.Name] = secret
	}

	for name, secret := range candidates {
		if secret.Annotations[associatedToAnnotationName] != instance.Name {
			logger.Info("Skipping a secret owned by another instance", "name", name, "owner", secret.Annotations[associatedToAnnotationName])
			continue
		}

		if err := r.Delete(ctx, secret); client.IgnoreNotFound(err) != nil {
			logger.Error(err, "Failed to delete the secret", "name", name)

			return err
		}
	}

	return nil
}

func (r *WorkerInstanceReconciler) setStatus(ctx context.Context, instance *computev1alpha1.WorkerInstance, state computev1alpha1.WorkerProvisioningState, reason string, message string) error {
	setCondition(instance, state, reason, message)

	return r.Status().Update(ctx, instance)
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

func (r *WorkerInstanceReconciler) getTemplate(logger *logr.Logger, ctx context.Context, name string, namespace string) (*computev1alpha1.WorkerTemplate, error) {
	template := &computev1alpha1.WorkerTemplate{}

	key := client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}

	if err := r.Get(ctx, key, template); err != nil {
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

func effectiveWorkerId(instance *computev1alpha1.WorkerInstance) string {
	if instance.Spec.WorkerId != "" {
		return instance.Spec.WorkerId
	}
	return instance.Name
}

// Generates a JobName in such a way that is resistant to collisions and does not violate naming rules. The total length is at worst 59 characters.
// Fails when the input contains no letter or digit.
func sanitizeWorkerId(input string) (string, string, error) {
	s := strings.ToLower(input)

	s = strings.NewReplacer(".", "-", "_", "-").Replace(s)

	// Keep only a-z, 0-9 and '-' and remove everything not allowed
	s = invalidWorkerIdCharacters.ReplaceAllString(s, "")

	if strings.Trim(s, "-") == "" {
		return "", "", fmt.Errorf("%q contains no character in [a-z0-9]", input)
	}

	temp := s

	hashBytes := sha256.Sum256([]byte(temp))
	hashHex := fmt.Sprintf("%x", hashBytes)

	const maxUserLength = 42

	if len(temp) > maxUserLength {
		temp = temp[:maxUserLength]
	}

	final := "spark-" + temp + "-" + hashHex[:10]

	return final, s, nil
}

// Label values are limited to 63 characters while instance names may be longer; the owner annotation stays authoritative
func instanceLabelValue(instanceName string) string {
	if len(validation.IsValidLabelValue(instanceName)) == 0 {
		return instanceName
	}

	hashBytes := sha256.Sum256([]byte(instanceName))
	prefix := strings.TrimRight(instanceName[:min(len(instanceName), 52)], "-.")

	return fmt.Sprintf("%s-%x", prefix, hashBytes[:5])
}

// requestError is a persistent failure: retrying the same request never succeeds
type requestError struct {
	reason     string
	message    string
	onTemplate bool
}

func (e *requestError) Error() string {
	return e.message
}

// workerRequest is a validated WorkerInstance, ready to be materialized
type workerRequest struct {
	job       *batchv1.Job
	jobName   string
	secrets   []corev1.Secret
	copyNames map[string]string

	unreferenced      []string
	namespaceResolved []string
}

// secretReference is a place in the pod spec naming a Secret
type secretReference struct {
	field string
	name  string
}

// Checks the whole request before any side effect
func validateRequest(template *computev1alpha1.WorkerTemplate, instance *computev1alpha1.WorkerInstance) (*workerRequest, *requestError) {
	var blueprint batchv1.JobTemplateSpec

	if err := json.Unmarshal(template.Spec.JobTemplate.Raw, &blueprint); err != nil {
		return nil, &requestError{
			reason:     "JobSpecInvalid",
			message:    fmt.Sprintf("Cannot decode the jobTemplate of template %q: %v", template.Name, err),
			onTemplate: true,
		}
	}

	if !naivelyValidateJob(&blueprint) {
		return nil, &requestError{
			reason:     "JobSpecInvalid",
			message:    fmt.Sprintf("The jobTemplate of template %q has no containers", template.Name),
			onTemplate: true,
		}
	}

	jobName, fullWorkerId, err := sanitizeWorkerId(effectiveWorkerId(instance))
	if err != nil {
		return nil, &requestError{
			reason:  "WorkerIdInvalid",
			message: fmt.Sprintf("workerId %v", err),
		}
	}

	request := &workerRequest{
		job: &batchv1.Job{
			ObjectMeta: blueprint.ObjectMeta,
			Spec:       blueprint.Spec,
		},
		jobName:   jobName,
		secrets:   make([]corev1.Secret, 0, len(instance.Spec.Secrets)),
		copyNames: make(map[string]string, len(instance.Spec.Secrets)),
	}

	for i, item := range instance.Spec.Secrets {
		secret, reqErr := decodeSecret(i, item)
		if reqErr != nil {
			return nil, reqErr
		}

		if _, ok := request.copyNames[secret.Name]; ok {
			return nil, &requestError{
				reason:  "SecretSpecInvalid",
				message: fmt.Sprintf("secrets[%d] (%s): duplicated name", i, secret.Name),
			}
		}

		// Cannot fail: the name is not empty
		copyName, _, _ := sanitizeWorkerId(fullWorkerId + "-" + secret.Name)

		request.copyNames[secret.Name] = copyName
		request.secrets = append(request.secrets, *secret)
	}

	supported, unsupported := collectSecretReferences(&request.job.Spec.Template.Spec)

	for _, ref := range unsupported {
		if _, ok := request.copyNames[ref.name]; ok {
			return nil, &requestError{
				reason:     "UnsupportedSecretReference",
				message:    fmt.Sprintf("Secret %s is referenced by %s, which the operator does not rewrite", ref.name, ref.field),
				onTemplate: true,
			}
		}
	}

	referenced := make(map[string]bool)
	for _, ref := range supported {
		referenced[ref.name] = true
	}

	for _, secret := range request.secrets {
		if !referenced[secret.Name] {
			request.unreferenced = append(request.unreferenced, secret.Name)
		}
	}

	for name := range referenced {
		if _, ok := request.copyNames[name]; !ok {
			request.namespaceResolved = append(request.namespaceResolved, name)
		}
	}
	sort.Strings(request.namespaceResolved)

	return request, nil
}

// Decodes one embedded Secret. Messages name the entry, never its content.
func decodeSecret(index int, data runtime.RawExtension) (*corev1.Secret, *requestError) {
	secret := &corev1.Secret{}

	if err := json.Unmarshal(data.Raw, secret); err != nil {
		return nil, &requestError{
			reason:  "SecretSpecInvalid",
			message: fmt.Sprintf("secrets[%d]: not a valid Secret object", index),
		}
	}

	if err := validateSecretEntry(index, secret); err != nil {
		return nil, err
	}

	return secret, nil
}

func validateSecretEntry(index int, secret *corev1.Secret) *requestError {
	invalid := func(format string, args ...any) *requestError {
		return &requestError{
			reason:  "SecretSpecInvalid",
			message: fmt.Sprintf(format, args...),
		}
	}

	if secret.APIVersion != "" && secret.APIVersion != "v1" {
		return invalid("secrets[%d]: apiVersion must be v1, not %q", index, secret.APIVersion)
	}

	if secret.Kind != "" && secret.Kind != "Secret" {
		return invalid("secrets[%d]: kind must be Secret, not %q", index, secret.Kind)
	}

	if secret.Name == "" {
		return invalid("secrets[%d]: metadata.name is required", index)
	}

	if errs := validation.IsDNS1123Subdomain(secret.Name); len(errs) > 0 {
		return invalid("secrets[%d] (%s): invalid name: %s", index, secret.Name, strings.Join(errs, "; "))
	}

	return nil
}

// Collects the Secret references the operator rewrites and the ones it cannot rewrite
func collectSecretReferences(spec *corev1.PodSpec) (supported []secretReference, unsupported []secretReference) {
	add := func(target *[]secretReference, field string, name string) {
		if name != "" {
			*target = append(*target, secretReference{field: field, name: name})
		}
	}

	for _, volume := range spec.Volumes {
		if s := volume.Secret; s != nil {
			add(&supported, fmt.Sprintf("volumes[%s].secret", volume.Name), s.SecretName)
		}

		if p := volume.Projected; p != nil {
			for i, source := range p.Sources {
				if s := source.Secret; s != nil {
					add(&supported, fmt.Sprintf("volumes[%s].projected.sources[%d].secret", volume.Name, i), s.Name)
				}
			}
		}

		if c := volume.CSI; c != nil && c.NodePublishSecretRef != nil {
			add(&unsupported, fmt.Sprintf("volumes[%s].csi.nodePublishSecretRef", volume.Name), c.NodePublishSecretRef.Name)
		}
	}

	collectContainers := func(kind string, containers []corev1.Container) {
		for _, container := range containers {
			for _, envFrom := range container.EnvFrom {
				if s := envFrom.SecretRef; s != nil {
					add(&supported, fmt.Sprintf("%s[%s].envFrom", kind, container.Name), s.Name)
				}
			}

			for _, env := range container.Env {
				if vf := env.ValueFrom; vf != nil && vf.SecretKeyRef != nil {
					add(&supported, fmt.Sprintf("%s[%s].env[%s].secretKeyRef", kind, container.Name, env.Name), vf.SecretKeyRef.Name)
				}
			}
		}
	}

	collectContainers("initContainers", spec.InitContainers)
	collectContainers("containers", spec.Containers)

	for _, ref := range spec.ImagePullSecrets {
		add(&unsupported, "imagePullSecrets", ref.Name)
	}

	return supported, unsupported
}

// Reports embedded Secrets the template never uses and template references resolved from the namespace
func (r *WorkerInstanceReconciler) reportReferences(instance *computev1alpha1.WorkerInstance, request *workerRequest) {
	condition := metav1.Condition{
		Type:    secretReferencesConditionType,
		Status:  metav1.ConditionTrue,
		Reason:  "AllReferencesRemapped",
		Message: "Every Secret reference of the template is served by an embedded Secret",
	}

	if len(request.unreferenced) > 0 || len(request.namespaceResolved) > 0 {
		var parts []string
		if len(request.unreferenced) > 0 {
			parts = append(parts, fmt.Sprintf("embedded but not referenced by the template: %s", strings.Join(request.unreferenced, ", ")))
		}
		if len(request.namespaceResolved) > 0 {
			parts = append(parts, fmt.Sprintf("referenced by the template and resolved from the namespace: %s", strings.Join(request.namespaceResolved, ", ")))
		}

		condition.Status = metav1.ConditionFalse
		condition.Reason = "NamespaceSecrets"
		if len(request.unreferenced) > 0 {
			condition.Reason = "UnreferencedSecrets"
		}
		condition.Message = strings.Join(parts, "; ")
	}

	// Emitted once per change, not on every retry
	if meta.SetStatusCondition(&instance.Status.Conditions, condition) && condition.Status == metav1.ConditionFalse {
		r.EventRecorder.Event(instance, corev1.EventTypeWarning, condition.Reason, condition.Message)
	}
}

type claimResult struct {
	blockedBy   string
	adoptedJob  *batchv1.Job
	ownedCopies map[string]bool
}

// Checks that neither the Job name nor any copy name is held by another instance
func (r *WorkerInstanceReconciler) claim(ctx context.Context, instance *computev1alpha1.WorkerInstance, request *workerRequest) (*claimResult, error) {
	result := &claimResult{ownedCopies: make(map[string]bool)}

	job := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Namespace: instance.Namespace, Name: request.jobName}, job)
	switch {
	case err == nil:
		if owner := job.Annotations[associatedToAnnotationName]; owner != instance.Name {
			result.blockedBy = describeOwner(owner, "Job", request.jobName)
			return result, nil
		}
		result.adoptedJob = job
	case !errors.IsNotFound(err):
		return nil, err
	}

	for _, secret := range request.secrets {
		copyName := request.copyNames[secret.Name]

		existing := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{Namespace: instance.Namespace, Name: copyName}, existing)
		switch {
		case errors.IsNotFound(err):
			continue
		case err != nil:
			return nil, err
		}

		if owner := existing.Annotations[associatedToAnnotationName]; owner != instance.Name {
			result.blockedBy = describeOwner(owner, "Secret", copyName)
			return result, nil
		}
		result.ownedCopies[copyName] = true
	}

	return result, nil
}

func describeOwner(owner string, kind string, name string) string {
	if owner == "" {
		return fmt.Sprintf("an unmanaged %s %s", kind, name)
	}
	return fmt.Sprintf("instance %s (%s %s)", owner, kind, name)
}

// Creates the missing Secret copies and records every mapping
func (r *WorkerInstanceReconciler) createSecrets(ctx context.Context, logger *logr.Logger, instance *computev1alpha1.WorkerInstance, request *workerRequest, ownedCopies map[string]bool) error {
	if len(request.secrets) == 0 {
		return nil
	}

	for i := range request.secrets {
		secret := &request.secrets[i]
		copyName := request.copyNames[secret.Name]

		if ownedCopies[copyName] {
			logger.Info("Reusing owned secret", "name", copyName, "namespace", instance.Namespace)
			continue
		}

		logger.Info("Ensure secret is created", "name", copyName, "namespace", instance.Namespace)

		if err := r.Create(ctx, newSecretCopy(instance, secret, copyName)); err != nil {
			logger.Error(err, "Cannot create the secret", "name", copyName)

			return err
		}

		logger.Info("Secret created", "name", copyName, "namespace", instance.Namespace)
	}

	instance.Status.SecretMappings = mergeMappings(instance.Status.SecretMappings, request)

	if err := r.Status().Update(ctx, instance); err != nil {
		logger.Error(err, "Failed to update the secret status")

		return err
	}

	return nil
}

// Builds a copy from scratch: the caller's metadata is discarded on purpose to avoid attacks
func newSecretCopy(instance *computev1alpha1.WorkerInstance, secret *corev1.Secret, copyName string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      copyName,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				managedByLabelName: managedByLabelValue,
				instanceLabelName:  instanceLabelValue(instance.Name),
			},
			Annotations: map[string]string{
				associatedToAnnotationName: instance.Name,
			},
		},
		// We force immutability to prevent unwanted upgrades
		Immutable:  ptr.To(true),
		Type:       secret.Type,
		Data:       secret.Data,
		StringData: secret.StringData,
	}
}

// Keys the mappings by original name, so reordering spec.secrets never breaks a resumed creation
func mergeMappings(existing []computev1alpha1.SecretMapping, request *workerRequest) []computev1alpha1.SecretMapping {
	result := make([]computev1alpha1.SecretMapping, 0, len(request.secrets)+len(existing))
	seen := make(map[string]bool)

	for _, secret := range request.secrets {
		result = append(result, computev1alpha1.SecretMapping{
			OriginalSecretName: secret.Name,
			RemappedSecretName: request.copyNames[secret.Name],
		})
		seen[secret.Name] = true
	}

	// Mappings of names removed from the spec stay, so the deletion path still finds their copies
	for _, mapping := range existing {
		if !seen[mapping.OriginalSecretName] {
			result = append(result, mapping)
		}
	}

	return result
}

// Creates the job
func (r *WorkerInstanceReconciler) createJob(ctx context.Context, logger *logr.Logger, instance *computev1alpha1.WorkerInstance, request *workerRequest) (*batchv1.Job, error) {
	job := request.job.DeepCopy()

	job.GenerateName = ""
	job.Name = request.jobName
	job.Namespace = instance.Namespace

	job.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyNever

	if job.Annotations == nil {
		job.Annotations = make(map[string]string)
	}

	if instance.Spec.TTLSecondsAfterFinished != nil {
		job.Spec.TTLSecondsAfterFinished = instance.Spec.TTLSecondsAfterFinished
	}

	job.Annotations[associatedToAnnotationName] = instance.Name

	patchSecrets(instance.Status.SecretMappings, &job.Spec.Template.Spec)

	logger.Info("About to schedule the job", "job", job)

	if err := r.Create(ctx, job); err != nil {
		logger.Error(err, "Cannot schedule the job")
		return nil, err
	}

	return job, nil
}

// Patches the secrets in order to be redirected to the remapped and per-instance ones
func patchSecrets(secrets []computev1alpha1.SecretMapping, spec *corev1.PodSpec) {
	if len(secrets) == 0 {
		return
	}

	for i := range spec.Volumes {
		volume := &spec.Volumes[i]

		if s := volume.Secret; s != nil {
			s.SecretName = remapSecret(secrets, s.SecretName)
		}

		if p := volume.Projected; p != nil {
			for j := range p.Sources {
				if s := p.Sources[j].Secret; s != nil {
					s.Name = remapSecret(secrets, s.Name)
				}
			}
		}
	}

	patchContainers := func(containers []corev1.Container) {
		for i := range containers {
			container := &containers[i]

			for j := range container.EnvFrom {
				if s := container.EnvFrom[j].SecretRef; s != nil {
					s.Name = remapSecret(secrets, s.Name)
				}
			}

			for j := range container.Env {
				if vf := container.Env[j].ValueFrom; vf != nil && vf.SecretKeyRef != nil {
					vf.SecretKeyRef.Name = remapSecret(secrets, vf.SecretKeyRef.Name)
				}
			}
		}
	}

	patchContainers(spec.InitContainers)
	patchContainers(spec.Containers)
}

func remapSecret(secrets []computev1alpha1.SecretMapping, name string) string {
	for _, secret := range secrets {
		if secret.OriginalSecretName == name {
			return secret.RemappedSecretName
		}
	}

	return name
}

func naivelyValidateJob(spec *batchv1.JobTemplateSpec) bool {
	return len(spec.Spec.Template.Spec.Containers) > 0
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
