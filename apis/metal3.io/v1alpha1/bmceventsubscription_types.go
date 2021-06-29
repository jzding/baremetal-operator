/*


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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (

	// BMCEventSubscriptionFinalizer is the name of the finalizer added to
	// subscriptions to block delete operations until the subscription is removed
	// from the BMC.
	BMCEventSubscriptionFinalizer string = "bmceventsubscription.metal3.io"
)

type BMCEventSubscriptionSpec struct {
	Taints []corev1.Taint `json:"taints,omitempty"`

	// A reference to a BareMetalHost
	HostRef string `json:"hostRef,omitempty"`

	// A webhook URL to send events to
	Destination string `json:"destination,omitempty"`

	// Arbitrary user-provided context for the event
	Context string `json:"context,omitempty"`

	// Messages of which type we should pass along; defaults to ["Alert"]
	EventTypes []string `json:"eventTypes,omitempty"`

	// The BMC protocol to use; defaults to "Redfish"
	Protocol string `json:"protocol,omitempty"`

	// A secret containing HTTP headers which should be passed along to the Destination
	// when making a request
	HTTPHeadersRef *corev1.SecretReference `json:"httpHeadersRef,omitempty"`
}

type BMCEventSubscriptionStatus struct {
	SubscriptionID string `json:"subscriptionID,omitempty"`
	ErrorMessage   string `json:"errorMessage"`
	// ErrorCount records how many times the host has encoutered an error since the last successful operation
	// +kubebuilder:default:=0
	ErrorCount int `json:"errorCount"`
	// LastUpdated identifies when this status was last observed.
	// +optional
	LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
//
// BMCEventSubscription is the Schema for the ...
// +k8s:openapi-gen=true
// +kubebuilder:resource:shortName=bes;bmcevent
// +kubebuilder:subresource:status
// +kubebuilder:object:root=true
type BMCEventSubscription struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BMCEventSubscriptionSpec   `json:"spec,omitempty"`
	Status BMCEventSubscriptionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BareMetalHostList contains a list of BareMetalHost
type BMCEventSubscriptionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BMCEventSubscription `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BMCEventSubscription{}, &BMCEventSubscriptionList{})
}
