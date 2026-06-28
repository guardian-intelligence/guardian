package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	ConditionReady         = "Ready"
	ConditionAuthenticated = "Authenticated"
	ConditionApplied       = "Applied"
	ConditionDriftDetected = "DriftDetected"
)

type OpenBaoStatus struct {
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	LastAppliedHash    string             `json:"lastAppliedHash,omitempty"`
	LastError          string             `json:"lastError,omitempty"`
}

func (in *OpenBaoStatus) DeepCopy() OpenBaoStatus {
	if in == nil {
		return OpenBaoStatus{}
	}
	out := *in
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		copy(out.Conditions, in.Conditions)
	}
	return out
}

type DeletionPolicy string

const (
	DeletionPolicyRetain DeletionPolicy = "Retain"
	DeletionPolicyDelete DeletionPolicy = "Delete"
)
