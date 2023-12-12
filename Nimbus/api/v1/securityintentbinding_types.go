/*
Copyright 2023.

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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// SecurityIntentBindingSpec defines the desired state of SecurityIntentBinding
type SecurityIntentBindingSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Foo is an example field of SecurityIntentBinding. Edit securityintentbinding_types.go to remove/update
	Selector       Selector        `json:"selector"`
	IntentRequests []IntentRequest `json:"intentRequests"`
}

// Selector defines the selection criteria for resources
type Selector struct {
	Any []ResourceFilter `json:"any,omitempty"`
	All []ResourceFilter `json:"all,omitempty"`
	CEL []string         `json:"cel,omitempty"`
}

// ResourceFilter is used for filtering resources
type ResourceFilter struct {
	Resources Resources `json:"resources,omitempty"`
}

// Resources defines the properties for selecting Kubernetes resources
type Resources struct {
	Kind        string            `json:"kind,omitempty"`
	Namespace   string            `json:"namespace,omitempty"`
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

// IntentRequest defines the request for a specific SecurityIntent
type IntentRequest struct {
	Type        string `json:"type"`
	IntentName  string `json:"intentName"`
	Description string `json:"description"`
	Mode        string `json:"mode"`
}

// SecurityIntentBindingStatus defines the observed state of SecurityIntentBinding
type SecurityIntentBindingStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// SecurityIntentBinding is the Schema for the securityintentbindings API
type SecurityIntentBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SecurityIntentBindingSpec   `json:"spec,omitempty"`
	Status SecurityIntentBindingStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// SecurityIntentBindingList contains a list of SecurityIntentBinding
type SecurityIntentBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SecurityIntentBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SecurityIntentBinding{}, &SecurityIntentBindingList{})
}
