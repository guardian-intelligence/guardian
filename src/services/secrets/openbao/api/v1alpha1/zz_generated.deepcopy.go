package v1alpha1

import "k8s.io/apimachinery/pkg/runtime"

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func copyStringSlice(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func copyTune(in *OpenBaoTuneSpec) *OpenBaoTuneSpec {
	if in == nil {
		return nil
	}
	out := *in
	out.PassthroughRequestHeaders = copyStringSlice(in.PassthroughRequestHeaders)
	out.AllowedResponseHeaders = copyStringSlice(in.AllowedResponseHeaders)
	out.AuditNonHMACRequestKeys = copyStringSlice(in.AuditNonHMACRequestKeys)
	out.AuditNonHMACResponseKeys = copyStringSlice(in.AuditNonHMACResponseKeys)
	return &out
}

func (in *OpenBaoAuditDevice) DeepCopyObject() runtime.Object {
	if out := in.DeepCopy(); out != nil {
		return out
	}
	return nil
}

func (in *OpenBaoAuditDevice) DeepCopy() *OpenBaoAuditDevice {
	if in == nil {
		return nil
	}
	out := new(OpenBaoAuditDevice)
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	out.Spec.Options = copyStringMap(in.Spec.Options)
	out.Status = in.Status.DeepCopy()
	return out
}

func (in *OpenBaoAuditDeviceList) DeepCopyObject() runtime.Object {
	if out := in.DeepCopy(); out != nil {
		return out
	}
	return nil
}

func (in *OpenBaoAuditDeviceList) DeepCopy() *OpenBaoAuditDeviceList {
	if in == nil {
		return nil
	}
	out := new(OpenBaoAuditDeviceList)
	*out = *in
	out.ListMeta = in.ListMeta
	if in.Items != nil {
		out.Items = make([]OpenBaoAuditDevice, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *in.Items[i].DeepCopy()
		}
	}
	return out
}

func (in *OpenBaoAuthBackend) DeepCopyObject() runtime.Object {
	if out := in.DeepCopy(); out != nil {
		return out
	}
	return nil
}

func (in *OpenBaoAuthBackend) DeepCopy() *OpenBaoAuthBackend {
	if in == nil {
		return nil
	}
	out := new(OpenBaoAuthBackend)
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	if in.Spec.Kubernetes != nil {
		cfg := *in.Spec.Kubernetes
		cfg.PEMKeys = copyStringSlice(in.Spec.Kubernetes.PEMKeys)
		out.Spec.Kubernetes = &cfg
	}
	out.Spec.Tune = copyTune(in.Spec.Tune)
	out.Status = in.Status.DeepCopy()
	return out
}

func (in *OpenBaoAuthBackendList) DeepCopyObject() runtime.Object {
	if out := in.DeepCopy(); out != nil {
		return out
	}
	return nil
}

func (in *OpenBaoAuthBackendList) DeepCopy() *OpenBaoAuthBackendList {
	if in == nil {
		return nil
	}
	out := new(OpenBaoAuthBackendList)
	*out = *in
	out.ListMeta = in.ListMeta
	if in.Items != nil {
		out.Items = make([]OpenBaoAuthBackend, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *in.Items[i].DeepCopy()
		}
	}
	return out
}

func (in *OpenBaoKubernetesAuthRole) DeepCopyObject() runtime.Object {
	if out := in.DeepCopy(); out != nil {
		return out
	}
	return nil
}

func (in *OpenBaoKubernetesAuthRole) DeepCopy() *OpenBaoKubernetesAuthRole {
	if in == nil {
		return nil
	}
	out := new(OpenBaoKubernetesAuthRole)
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	out.Spec.BoundServiceAccountNames = copyStringSlice(in.Spec.BoundServiceAccountNames)
	out.Spec.BoundServiceAccountNamespaces = copyStringSlice(in.Spec.BoundServiceAccountNamespaces)
	out.Spec.TokenPolicies = copyStringSlice(in.Spec.TokenPolicies)
	out.Status = in.Status.DeepCopy()
	return out
}

func (in *OpenBaoKubernetesAuthRoleList) DeepCopyObject() runtime.Object {
	if out := in.DeepCopy(); out != nil {
		return out
	}
	return nil
}

func (in *OpenBaoKubernetesAuthRoleList) DeepCopy() *OpenBaoKubernetesAuthRoleList {
	if in == nil {
		return nil
	}
	out := new(OpenBaoKubernetesAuthRoleList)
	*out = *in
	out.ListMeta = in.ListMeta
	if in.Items != nil {
		out.Items = make([]OpenBaoKubernetesAuthRole, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *in.Items[i].DeepCopy()
		}
	}
	return out
}

func (in *OpenBaoMount) DeepCopyObject() runtime.Object {
	if out := in.DeepCopy(); out != nil {
		return out
	}
	return nil
}

func (in *OpenBaoMount) DeepCopy() *OpenBaoMount {
	if in == nil {
		return nil
	}
	out := new(OpenBaoMount)
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	out.Spec.Options = copyStringMap(in.Spec.Options)
	out.Spec.Tune = copyTune(in.Spec.Tune)
	out.Status = in.Status.DeepCopy()
	return out
}

func (in *OpenBaoMountList) DeepCopyObject() runtime.Object {
	if out := in.DeepCopy(); out != nil {
		return out
	}
	return nil
}

func (in *OpenBaoMountList) DeepCopy() *OpenBaoMountList {
	if in == nil {
		return nil
	}
	out := new(OpenBaoMountList)
	*out = *in
	out.ListMeta = in.ListMeta
	if in.Items != nil {
		out.Items = make([]OpenBaoMount, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *in.Items[i].DeepCopy()
		}
	}
	return out
}

func (in *OpenBaoMountTune) DeepCopyObject() runtime.Object {
	if out := in.DeepCopy(); out != nil {
		return out
	}
	return nil
}

func (in *OpenBaoMountTune) DeepCopy() *OpenBaoMountTune {
	if in == nil {
		return nil
	}
	out := new(OpenBaoMountTune)
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	out.Spec.Tune = *copyTune(&in.Spec.Tune)
	out.Status = in.Status.DeepCopy()
	return out
}

func (in *OpenBaoMountTuneList) DeepCopyObject() runtime.Object {
	if out := in.DeepCopy(); out != nil {
		return out
	}
	return nil
}

func (in *OpenBaoMountTuneList) DeepCopy() *OpenBaoMountTuneList {
	if in == nil {
		return nil
	}
	out := new(OpenBaoMountTuneList)
	*out = *in
	out.ListMeta = in.ListMeta
	if in.Items != nil {
		out.Items = make([]OpenBaoMountTune, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *in.Items[i].DeepCopy()
		}
	}
	return out
}

func (in *OpenBaoPolicy) DeepCopyObject() runtime.Object {
	if out := in.DeepCopy(); out != nil {
		return out
	}
	return nil
}

func (in *OpenBaoPolicy) DeepCopy() *OpenBaoPolicy {
	if in == nil {
		return nil
	}
	out := new(OpenBaoPolicy)
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	out.Status = in.Status.DeepCopy()
	return out
}

func (in *OpenBaoPolicyList) DeepCopyObject() runtime.Object {
	if out := in.DeepCopy(); out != nil {
		return out
	}
	return nil
}

func (in *OpenBaoPolicyList) DeepCopy() *OpenBaoPolicyList {
	if in == nil {
		return nil
	}
	out := new(OpenBaoPolicyList)
	*out = *in
	out.ListMeta = in.ListMeta
	if in.Items != nil {
		out.Items = make([]OpenBaoPolicy, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *in.Items[i].DeepCopy()
		}
	}
	return out
}
