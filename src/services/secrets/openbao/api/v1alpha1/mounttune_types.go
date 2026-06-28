package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type OpenBaoTuneSpec struct {
	DefaultLeaseTTL           string   `json:"defaultLeaseTTL,omitempty"`
	MaxLeaseTTL               string   `json:"maxLeaseTTL,omitempty"`
	ListingVisibility         string   `json:"listingVisibility,omitempty"`
	PassthroughRequestHeaders []string `json:"passthroughRequestHeaders,omitempty"`
	AllowedResponseHeaders    []string `json:"allowedResponseHeaders,omitempty"`
	AuditNonHMACRequestKeys   []string `json:"auditNonHMACRequestKeys,omitempty"`
	AuditNonHMACResponseKeys  []string `json:"auditNonHMACResponseKeys,omitempty"`
}

type OpenBaoMountTuneSpec struct {
	MountPath string          `json:"mountPath"`
	Tune      OpenBaoTuneSpec `json:"tune"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type OpenBaoMountTune struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenBaoMountTuneSpec `json:"spec,omitempty"`
	Status OpenBaoStatus        `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type OpenBaoMountTuneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenBaoMountTune `json:"items"`
}

func (in *OpenBaoMountTune) OpenBaoStatus() *OpenBaoStatus { return &in.Status }
func (in *OpenBaoMountTune) OpenBaoSpec() any              { return in.Spec }
