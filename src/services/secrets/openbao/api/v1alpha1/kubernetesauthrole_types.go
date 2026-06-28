package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type OpenBaoKubernetesAuthRoleSpec struct {
	BackendPath                   string         `json:"backendPath"`
	RoleName                      string         `json:"roleName,omitempty"`
	BoundServiceAccountNames      []string       `json:"boundServiceAccountNames"`
	BoundServiceAccountNamespaces []string       `json:"boundServiceAccountNamespaces"`
	Audience                      string         `json:"audience,omitempty"`
	TokenPolicies                 []string       `json:"tokenPolicies,omitempty"`
	TokenTTL                      string         `json:"tokenTTL,omitempty"`
	TokenMaxTTL                   string         `json:"tokenMaxTTL,omitempty"`
	DeletionPolicy                DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type OpenBaoKubernetesAuthRole struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenBaoKubernetesAuthRoleSpec `json:"spec,omitempty"`
	Status OpenBaoStatus                 `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type OpenBaoKubernetesAuthRoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenBaoKubernetesAuthRole `json:"items"`
}

func (in *OpenBaoKubernetesAuthRole) OpenBaoStatus() *OpenBaoStatus { return &in.Status }
func (in *OpenBaoKubernetesAuthRole) OpenBaoSpec() any              { return in.Spec }
