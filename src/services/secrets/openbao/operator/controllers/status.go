package controllers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	openbaov1alpha1 "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func specHash(spec any) string {
	data, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func setCondition(status *openbaov1alpha1.OpenBaoStatus, conditionType string, conditionStatus metav1.ConditionStatus, reason string, message string) {
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             conditionStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: status.ObservedGeneration,
	})
}
