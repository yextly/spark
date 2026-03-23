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

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	computev1alpha1 "spark/api/v1alpha1"
)

// WorkerInstanceReconciler reconciles a WorkerInstance object
type WorkerInstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const finalizerName = "workerinstance.compute.yextly.io"

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
	// Retrieve the Ec2Instance resource from the Kubernetes API server using the provided request's NamespacedName.
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

			// Kubernetes will retry with backoff
			return ctrl.Result{}, err
		}
		// At this point, the instance state is terminated and the finalizer is removed
		return ctrl.Result{}, nil
	}

	if instance.Status.JobInstanceId == "" {
		logger.Info("Add finalizer")
		instance.Finalizers = append(instance.Finalizers, finalizerName)
		if err := r.Update(ctx, instance); err != nil {
			logger.Error(err, "Failed to add the finalizer")

			return ctrl.Result{}, err
		}

		jobInstance, err := scheduleInstance(instance)
		if err != nil {
			logger.Error(err, "Failed to schedule the instance")

			return ctrl.Result{}, err
		}

		instance.Status.JobInstanceId = jobInstance.Name

		err = r.Status().Update(ctx, instance)
		if err != nil {
			logger.Error(err, "Failed to update the status")

			return ctrl.Result{}, err
		}

	} else {
		logger.Info("The resource is already bound to a Job", "jobInstanceId", instance.Status.JobInstanceId)
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkerInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&computev1alpha1.WorkerInstance{}).
		Named("workerinstance").
		Complete(r)
}

func scheduleInstance(logger *logr.Logger, template *computev1alpha1.WorkerTemplate, instance *computev1alpha1.WorkerInstance) (jobInstance *batchv1.Job, err error) {

}
