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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// WorkerProvisioningState represents the current provisioning state of a worker.
type WorkerProvisioningState string

const (
	WorkerProvisioningPending   WorkerProvisioningState = "Pending"
	WorkerProvisioningCreating  WorkerProvisioningState = "Creating"
	WorkerProvisioningRunning   WorkerProvisioningState = "Running"
	WorkerProvisioningDeleting  WorkerProvisioningState = "Deleting"
	WorkerProvisioningFailed    WorkerProvisioningState = "Failed"
	WorkerProvisioningSucceeded WorkerProvisioningState = "Succeeded"
)

// WorkerInstanceSpec defines the desired state of WorkerInstance.
type WorkerInstanceSpec struct {
	// Important: Run "make" to regenerate code after modifying this file

	// Specifies the name of the template to use
	// +kubebuilder:validation:Required
	TemplateName string `json:"templateName,omitempty"`

	// Specifies the unique identifier of the worker that will be used for Job scheduling
	// +kubebuilder:validation:Optional
	WorkerId string `json:"workerId,omitempty"`

	// ttlSecondsAfterFinished limits the lifetime of a Worker that has finished
	// execution (either Complete or Failed). If this field is set,
	// ttlSecondsAfterFinished after the Worker finishes, it is eligible to be
	// automatically deleted. When the Worker is being deleted, its lifecycle
	// guarantees (e.g. finalizers) will be honored. If this field is unset,
	// the Worker won't be automatically deleted. If this field is set to zero,
	// the Worker becomes eligible to be deleted immediately after it finishes.
	// Note that when using the same WorkerId value, no Job is created until the
	// previous one is deleted; therefore, you specify a value of 0 for the ususal case
	// and a value greater than 0 to force lingering the Job and allow inspection of the POD
	// +kubebuilder:validation:Optional
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`

	// List of secrets belonging to the pod.
	// More info: https://kubernetes.io/docs/concepts/storage/secrets
	// +optional
	// +kubebuilder:validation:Optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Secrets []runtime.RawExtension `json:"secrets,omitempty"`
}

// WorkerInstanceStatus defines the observed state of WorkerInstance.
type WorkerInstanceStatus struct {
	// Important: Run "make" to regenerate code after modifying this file

	// JobName traces the provisioning of the worker
	// +kubebuilder:validation:Optional
	JobName string `json:"jobName,omitempty"`

	// ProvisioningState indicates the current lifecycle phase of the worker.
	// +kubebuilder:validation:Enum=Pending;Creating;Running;Deleting;Failed;Succeeded
	ProvisioningState WorkerProvisioningState `json:"provisioningState,omitempty"`

	// Conditions traces the conditions of the current instance
	// +optional
	// +kubebuilder:validation:Optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// SecretMappings traces the associations between original secrets and remapped ones
	// +kubebuilder:validation:Optional
	SecretMappings []SecretMapping `json:"secretMappings,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// WorkerInstance is the Schema for the workerinstances API.
type WorkerInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkerInstanceSpec   `json:"spec,omitempty"`
	Status WorkerInstanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkerInstanceList contains a list of WorkerInstance.
type WorkerInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkerInstance `json:"items"`
}

// SecretMapping traces the association and remapping of secrets
type SecretMapping struct {
	// OriginalSecretName contains the secret name before being remapped
	// +kubebuilder:validation:Required
	OriginalSecretName string `json:"originalSecretName,omitempty"`

	// RemappedSecretName contains the secret name after being remapped
	// +kubebuilder:validation:Optional
	RemappedSecretName string `json:"remappedSecretName,omitempty"`
}

func init() {
	SchemeBuilder.Register(&WorkerInstance{}, &WorkerInstanceList{})
}
