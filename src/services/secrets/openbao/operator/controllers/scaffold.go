package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"

	openbaov1alpha1 "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type statusCarrier interface {
	client.Object
	OpenBaoStatus() *openbaov1alpha1.OpenBaoStatus
	OpenBaoSpec() any
}

func reconcileScaffold(ctx context.Context, kube client.Client, req ctrl.Request, obj statusCarrier) (ctrl.Result, error) {
	if err := kube.Get(ctx, req.NamespacedName, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	before := obj.DeepCopyObject()
	status := obj.OpenBaoStatus()
	status.ObservedGeneration = obj.GetGeneration()
	status.LastAppliedHash = specHash(obj.OpenBaoSpec())
	status.LastError = "OpenBao apply loop is not implemented in this scaffold"
	setCondition(status, openbaov1alpha1.ConditionReady, metav1.ConditionFalse, "ApplyLoopNotImplemented", "Controller scaffold is installed, but this resource is not applying OpenBao state yet.")
	setCondition(status, openbaov1alpha1.ConditionAuthenticated, metav1.ConditionUnknown, "ApplyLoopNotImplemented", "OpenBao Kubernetes auth login is not implemented yet.")
	setCondition(status, openbaov1alpha1.ConditionApplied, metav1.ConditionFalse, "ApplyLoopNotImplemented", "OpenBao API apply is not implemented yet.")
	setCondition(status, openbaov1alpha1.ConditionDriftDetected, metav1.ConditionUnknown, "ApplyLoopNotImplemented", "Drift detection is not implemented yet.")

	if reflect.DeepEqual(before, obj) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, kube.Status().Update(ctx, obj)
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
