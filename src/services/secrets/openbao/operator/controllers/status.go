package controllers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	openbaov1alpha1 "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	reasonAuthenticationFailed = "AuthenticationFailed"
	reasonSelfInitIncomplete   = "SelfInitIncomplete"

	messageAuthenticationFailed = "OpenBao Kubernetes auth login failed."
	messageSelfInitIncomplete   = "OpenBao self-init did not create the Kubernetes auth role required by the ops controller; inspect OpenBao startup logs and recreate the cluster with the declared self-init config."

	selfInitIncompleteRequeueAfter = time.Minute
)

type conditionReasonMessage struct {
	reason  string
	message string
}

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

func openBaoAuthFailureStatus(err error) conditionReasonMessage {
	if isOpenBaoKubernetesAuthRoleMissing(err) {
		return conditionReasonMessage{
			reason:  reasonSelfInitIncomplete,
			message: messageSelfInitIncomplete,
		}
	}
	return conditionReasonMessage{
		reason:  reasonAuthenticationFailed,
		message: messageAuthenticationFailed,
	}
}

func isOpenBaoKubernetesAuthRoleMissing(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "invalid role name")
}
