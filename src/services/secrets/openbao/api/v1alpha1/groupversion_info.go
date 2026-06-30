package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const GroupName = "openbao.guardian.dev"

var SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: "v1alpha1"}

var (
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(
		SchemeGroupVersion,
		&OpenBaoAuditDevice{},
		&OpenBaoAuditDeviceList{},
		&OpenBaoAuthBackend{},
		&OpenBaoAuthBackendList{},
		&OpenBaoKubernetesAuthRole{},
		&OpenBaoKubernetesAuthRoleList{},
		&OpenBaoMount{},
		&OpenBaoMountList{},
		&OpenBaoMountTune{},
		&OpenBaoMountTuneList{},
		&OpenBaoPKIRole{},
		&OpenBaoPKIRoleList{},
		&OpenBaoPKIRootIssuer{},
		&OpenBaoPKIRootIssuerList{},
		&OpenBaoPolicy{},
		&OpenBaoPolicyList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
