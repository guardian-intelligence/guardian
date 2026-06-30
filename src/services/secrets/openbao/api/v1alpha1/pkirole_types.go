package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type OpenBaoPKIRoleSpec struct {
	MountPath                 string         `json:"mountPath"`
	RoleName                  string         `json:"roleName,omitempty"`
	IssuerRef                 string         `json:"issuerRef,omitempty"`
	TTL                       string         `json:"ttl,omitempty"`
	MaxTTL                    string         `json:"maxTTL,omitempty"`
	AllowLocalhost            bool           `json:"allowLocalhost"`
	AllowedDomains            []string       `json:"allowedDomains"`
	AllowBareDomains          bool           `json:"allowBareDomains"`
	AllowSubdomains           bool           `json:"allowSubdomains"`
	AllowGlobDomains          bool           `json:"allowGlobDomains"`
	AllowWildcardCertificates bool           `json:"allowWildcardCertificates"`
	AllowAnyName              bool           `json:"allowAnyName"`
	EnforceHostnames          bool           `json:"enforceHostnames"`
	AllowIPSANs               bool           `json:"allowIPSANs"`
	AllowedIPSANsCIDR         []string       `json:"allowedIPSANsCIDR,omitempty"`
	ServerFlag                bool           `json:"serverFlag"`
	ClientFlag                bool           `json:"clientFlag"`
	CodeSigningFlag           bool           `json:"codeSigningFlag"`
	EmailProtectionFlag       bool           `json:"emailProtectionFlag"`
	KeyType                   string         `json:"keyType"`
	KeyBits                   int            `json:"keyBits,omitempty"`
	KeyUsage                  []string       `json:"keyUsage"`
	ExtKeyUsage               []string       `json:"extKeyUsage,omitempty"`
	CNValidations             []string       `json:"cnValidations,omitempty"`
	UseCSRCommonName          bool           `json:"useCSRCommonName"`
	UseCSRSANs                bool           `json:"useCSRSANs"`
	GenerateLease             bool           `json:"generateLease"`
	NoStore                   bool           `json:"noStore"`
	RequireCN                 bool           `json:"requireCN"`
	NotBeforeDuration         string         `json:"notBeforeDuration,omitempty"`
	NotBeforeBound            string         `json:"notBeforeBound,omitempty"`
	NotAfterBound             string         `json:"notAfterBound,omitempty"`
	DeletionPolicy            DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type OpenBaoPKIRole struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenBaoPKIRoleSpec `json:"spec,omitempty"`
	Status OpenBaoStatus      `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type OpenBaoPKIRoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenBaoPKIRole `json:"items"`
}

func (in *OpenBaoPKIRole) OpenBaoStatus() *OpenBaoStatus { return &in.Status }
func (in *OpenBaoPKIRole) OpenBaoSpec() any              { return in.Spec }
