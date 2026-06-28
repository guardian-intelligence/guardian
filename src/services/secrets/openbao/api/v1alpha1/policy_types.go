package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type OpenBaoPolicySpec struct {
	Name           string         `json:"name,omitempty"`
	Rules          string         `json:"rules"`
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type OpenBaoPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenBaoPolicySpec `json:"spec,omitempty"`
	Status OpenBaoStatus     `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type OpenBaoPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenBaoPolicy `json:"items"`
}

func (in *OpenBaoPolicy) OpenBaoStatus() *OpenBaoStatus { return &in.Status }
func (in *OpenBaoPolicy) OpenBaoSpec() any              { return in.Spec }
