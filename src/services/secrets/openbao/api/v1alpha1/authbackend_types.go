package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type OpenBaoAuthBackendSpec struct {
	Path           string                       `json:"path"`
	Type           string                       `json:"type"`
	Description    string                       `json:"description,omitempty"`
	Kubernetes     *OpenBaoKubernetesAuthConfig `json:"kubernetes,omitempty"`
	Tune           *OpenBaoTuneSpec             `json:"tune,omitempty"`
	DeletionPolicy DeletionPolicy               `json:"deletionPolicy,omitempty"`
}

type OpenBaoKubernetesAuthConfig struct {
	KubernetesHost       string   `json:"kubernetesHost,omitempty"`
	KubernetesCACert     string   `json:"kubernetesCACert,omitempty"`
	Issuer               string   `json:"issuer,omitempty"`
	PEMKeys              []string `json:"pemKeys,omitempty"`
	DisableISSValidation bool     `json:"disableISSValidation,omitempty"`
	DisableLocalCAJWT    bool     `json:"disableLocalCAJWT,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type OpenBaoAuthBackend struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenBaoAuthBackendSpec `json:"spec,omitempty"`
	Status OpenBaoStatus          `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type OpenBaoAuthBackendList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenBaoAuthBackend `json:"items"`
}

func (in *OpenBaoAuthBackend) OpenBaoStatus() *OpenBaoStatus { return &in.Status }
func (in *OpenBaoAuthBackend) OpenBaoSpec() any              { return in.Spec }
