package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type OpenBaoMountSpec struct {
	Path           string            `json:"path"`
	Type           string            `json:"type"`
	Description    string            `json:"description,omitempty"`
	Options        map[string]string `json:"options,omitempty"`
	Tune           *OpenBaoTuneSpec  `json:"tune,omitempty"`
	DeletionPolicy DeletionPolicy    `json:"deletionPolicy,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type OpenBaoMount struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenBaoMountSpec `json:"spec,omitempty"`
	Status OpenBaoStatus    `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type OpenBaoMountList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenBaoMount `json:"items"`
}

func (in *OpenBaoMount) OpenBaoStatus() *OpenBaoStatus { return &in.Status }
func (in *OpenBaoMount) OpenBaoSpec() any              { return in.Spec }
