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

// WorkerTemplateSpec defines the desired state of WorkerTemplate.
type WorkerTemplateSpec struct {
	// Specification of the desired behavior of the pod in the form of a PodSpec.
	// +kubebuilder:validation:Required
	// +kubebuilder:pruning:PreserveUnknownFields
	Template runtime.RawExtension `json:"template,omitempty"`
}

// WorkerTemplateStatus defines the observed state of WorkerTemplate.
type WorkerTemplateStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	JobInstanceId string `json:"jobInstanceId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// WorkerTemplate is the Schema for the workertemplates API.
type WorkerTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkerTemplateSpec   `json:"spec,omitempty"`
	Status WorkerTemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkerTemplateList contains a list of WorkerTemplate.
type WorkerTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkerTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkerTemplate{}, &WorkerTemplateList{})
}
