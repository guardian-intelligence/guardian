package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type OpenBaoPKIRootIssuerSpec struct {
	MountPath  string `json:"mountPath"`
	IssuerName string `json:"issuerName"`
	CommonName string `json:"commonName"`
	TTL        string `json:"ttl"`
	KeyType    string `json:"keyType"`
	KeyBits    int    `json:"keyBits,omitempty"`
	SetDefault bool   `json:"setDefault"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type OpenBaoPKIRootIssuer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenBaoPKIRootIssuerSpec `json:"spec,omitempty"`
	Status OpenBaoStatus            `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type OpenBaoPKIRootIssuerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenBaoPKIRootIssuer `json:"items"`
}

func (in *OpenBaoPKIRootIssuer) OpenBaoStatus() *OpenBaoStatus { return &in.Status }
func (in *OpenBaoPKIRootIssuer) OpenBaoSpec() any              { return in.Spec }
