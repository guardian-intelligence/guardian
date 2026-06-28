package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type OpenBaoAuditDeviceSpec struct {
	Path        string            `json:"path"`
	Type        string            `json:"type"`
	Description string            `json:"description,omitempty"`
	Options     map[string]string `json:"options,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type OpenBaoAuditDevice struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenBaoAuditDeviceSpec `json:"spec,omitempty"`
	Status OpenBaoStatus          `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type OpenBaoAuditDeviceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenBaoAuditDevice `json:"items"`
}

func (in *OpenBaoAuditDevice) OpenBaoStatus() *OpenBaoStatus { return &in.Status }
func (in *OpenBaoAuditDevice) OpenBaoSpec() any              { return in.Spec }
