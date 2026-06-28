package controllers

import "fmt"

type ReconcileMode string

const (
	ReconcileModeApply   ReconcileMode = "apply"
	ReconcileModeObserve ReconcileMode = "observe"
)

func ParseReconcileMode(raw string) (ReconcileMode, error) {
	switch ReconcileMode(raw) {
	case ReconcileModeApply:
		return ReconcileModeApply, nil
	case ReconcileModeObserve:
		return ReconcileModeObserve, nil
	default:
		return "", fmt.Errorf("unsupported reconcile mode %q", raw)
	}
}

func (m ReconcileMode) AllowsWrites() bool {
	return m == "" || m == ReconcileModeApply
}

func (m ReconcileMode) String() string {
	if m == "" {
		return string(ReconcileModeApply)
	}
	return string(m)
}

func appliedReason(mode ReconcileMode) string {
	if mode == ReconcileModeObserve {
		return "Observed"
	}
	return "Applied"
}
