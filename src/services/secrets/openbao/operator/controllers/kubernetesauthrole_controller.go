package controllers

import (
	"context"
	"fmt"
	"slices"
	"strings"

	openbaov1alpha1 "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/api/v1alpha1"
	bao "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/operator/openbao"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const kubernetesAuthRoleFinalizer = "openbao.guardian.dev/kubernetes-auth-role"

type KubernetesAuthRoleClient interface {
	GetKubernetesAuthRole(ctx context.Context, backendPath string, roleName string) (bao.KubernetesAuthRole, bool, error)
	PutKubernetesAuthRole(ctx context.Context, role bao.KubernetesAuthRole) error
	DeleteKubernetesAuthRole(ctx context.Context, backendPath string, roleName string) error
}

type KubernetesAuthRoleReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	OpenBao func(context.Context) (KubernetesAuthRoleClient, error)
	Mode    ReconcileMode
}

func (r *KubernetesAuthRoleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var role openbaov1alpha1.OpenBaoKubernetesAuthRole
	if err := r.Get(ctx, req.NamespacedName, &role); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if r.OpenBao == nil {
		r.OpenBao = func(ctx context.Context) (KubernetesAuthRoleClient, error) {
			return bao.NewAuthenticatedClientFromEnv(ctx)
		}
	}

	if !role.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &role)
	}

	if !r.Mode.AllowsWrites() && controllerutil.ContainsFinalizer(&role, kubernetesAuthRoleFinalizer) {
		controllerutil.RemoveFinalizer(&role, kubernetesAuthRoleFinalizer)
		if err := r.Update(ctx, &role); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if r.Mode.AllowsWrites() && role.Spec.DeletionPolicy == openbaov1alpha1.DeletionPolicyDelete && !controllerutil.ContainsFinalizer(&role, kubernetesAuthRoleFinalizer) {
		controllerutil.AddFinalizer(&role, kubernetesAuthRoleFinalizer)
		if err := r.Update(ctx, &role); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if role.Spec.DeletionPolicy != openbaov1alpha1.DeletionPolicyDelete && controllerutil.ContainsFinalizer(&role, kubernetesAuthRoleFinalizer) {
		controllerutil.RemoveFinalizer(&role, kubernetesAuthRoleFinalizer)
		if err := r.Update(ctx, &role); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	desired := desiredKubernetesAuthRole(&role)
	openbaoClient, err := r.OpenBao(ctx)
	if err != nil {
		authStatus := openBaoAuthFailureStatus(err)
		return r.updateKubernetesAuthRoleErrorStatus(ctx, &role, kubernetesAuthRoleStatusInput{
			authenticated: metav1.ConditionFalse,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        authStatus.reason,
			message:       authStatus.message,
			lastError:     err.Error(),
		}, err)
	}

	current, found, err := openbaoClient.GetKubernetesAuthRole(ctx, desired.BackendPath, desired.RoleName)
	if err != nil {
		return r.updateKubernetesAuthRoleErrorStatus(ctx, &role, kubernetesAuthRoleStatusInput{
			authenticated: metav1.ConditionTrue,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        "ReadFailed",
			message:       fmt.Sprintf("Failed to read OpenBao Kubernetes auth role %q.", desired.RoleName),
			lastError:     err.Error(),
		}, err)
	}

	if !found || !kubernetesAuthRoleEqual(current, desired) {
		if !r.Mode.AllowsWrites() {
			return r.updateKubernetesAuthRoleStatus(ctx, &role, kubernetesAuthRoleStatusInput{
				authenticated:   metav1.ConditionTrue,
				applied:         metav1.ConditionFalse,
				drift:           metav1.ConditionTrue,
				ready:           metav1.ConditionFalse,
				reason:          "DriftDetected",
				message:         fmt.Sprintf("OpenBao Kubernetes auth role %q differs from desired state; observe mode left it unchanged.", desired.RoleName),
				lastAppliedHash: specHash(role.Spec),
			})
		}
		if err := openbaoClient.PutKubernetesAuthRole(ctx, desired); err != nil {
			return r.updateKubernetesAuthRoleErrorStatus(ctx, &role, kubernetesAuthRoleStatusInput{
				authenticated: metav1.ConditionTrue,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionTrue,
				ready:         metav1.ConditionFalse,
				reason:        "ApplyFailed",
				message:       fmt.Sprintf("Failed to apply OpenBao Kubernetes auth role %q.", desired.RoleName),
				lastError:     err.Error(),
			}, err)
		}
	}

	return r.updateKubernetesAuthRoleStatus(ctx, &role, kubernetesAuthRoleStatusInput{
		authenticated:   metav1.ConditionTrue,
		applied:         metav1.ConditionTrue,
		drift:           metav1.ConditionFalse,
		ready:           metav1.ConditionTrue,
		reason:          appliedReason(r.Mode),
		message:         fmt.Sprintf("OpenBao Kubernetes auth role %q is applied.", desired.RoleName),
		lastAppliedHash: specHash(role.Spec),
	})
}

func (r *KubernetesAuthRoleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&openbaov1alpha1.OpenBaoKubernetesAuthRole{}).
		Complete(r)
}

func (r *KubernetesAuthRoleReconciler) reconcileDelete(ctx context.Context, role *openbaov1alpha1.OpenBaoKubernetesAuthRole) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(role, kubernetesAuthRoleFinalizer) {
		return ctrl.Result{}, nil
	}
	if !r.Mode.AllowsWrites() {
		controllerutil.RemoveFinalizer(role, kubernetesAuthRoleFinalizer)
		return ctrl.Result{}, r.Update(ctx, role)
	}
	if role.Spec.DeletionPolicy == openbaov1alpha1.DeletionPolicyDelete {
		openbaoClient, err := r.OpenBao(ctx)
		if err != nil {
			authStatus := openBaoAuthFailureStatus(err)
			return r.updateKubernetesAuthRoleErrorStatus(ctx, role, kubernetesAuthRoleStatusInput{
				authenticated: metav1.ConditionFalse,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionUnknown,
				ready:         metav1.ConditionFalse,
				reason:        authStatus.reason,
				message:       authStatus.message,
				lastError:     err.Error(),
			}, err)
		}
		desired := desiredKubernetesAuthRole(role)
		if err := openbaoClient.DeleteKubernetesAuthRole(ctx, desired.BackendPath, desired.RoleName); err != nil {
			return r.updateKubernetesAuthRoleErrorStatus(ctx, role, kubernetesAuthRoleStatusInput{
				authenticated: metav1.ConditionTrue,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionUnknown,
				ready:         metav1.ConditionFalse,
				reason:        "DeleteFailed",
				message:       fmt.Sprintf("Failed to delete OpenBao Kubernetes auth role %q.", desired.RoleName),
				lastError:     err.Error(),
			}, err)
		}
	}
	controllerutil.RemoveFinalizer(role, kubernetesAuthRoleFinalizer)
	return ctrl.Result{}, r.Update(ctx, role)
}

type kubernetesAuthRoleStatusInput struct {
	authenticated   metav1.ConditionStatus
	applied         metav1.ConditionStatus
	drift           metav1.ConditionStatus
	ready           metav1.ConditionStatus
	reason          string
	message         string
	lastAppliedHash string
	lastError       string
}

func (r *KubernetesAuthRoleReconciler) updateKubernetesAuthRoleStatus(ctx context.Context, role *openbaov1alpha1.OpenBaoKubernetesAuthRole, input kubernetesAuthRoleStatusInput) (ctrl.Result, error) {
	status := &role.Status
	status.ObservedGeneration = role.Generation
	if input.lastAppliedHash != "" {
		status.LastAppliedHash = input.lastAppliedHash
	}
	status.LastError = input.lastError
	setCondition(status, openbaov1alpha1.ConditionAuthenticated, input.authenticated, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionApplied, input.applied, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionDriftDetected, input.drift, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionReady, input.ready, input.reason, input.message)
	return ctrl.Result{}, r.Status().Update(ctx, role)
}

func (r *KubernetesAuthRoleReconciler) updateKubernetesAuthRoleErrorStatus(ctx context.Context, role *openbaov1alpha1.OpenBaoKubernetesAuthRole, input kubernetesAuthRoleStatusInput, cause error) (ctrl.Result, error) {
	if _, err := r.updateKubernetesAuthRoleStatus(ctx, role, input); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, cause
}

func desiredKubernetesAuthRole(role *openbaov1alpha1.OpenBaoKubernetesAuthRole) bao.KubernetesAuthRole {
	roleName := role.Spec.RoleName
	if roleName == "" {
		roleName = role.Name
	}
	return bao.KubernetesAuthRole{
		BackendPath:                   strings.Trim(role.Spec.BackendPath, "/"),
		RoleName:                      roleName,
		BoundServiceAccountNames:      append([]string(nil), role.Spec.BoundServiceAccountNames...),
		BoundServiceAccountNamespaces: append([]string(nil), role.Spec.BoundServiceAccountNamespaces...),
		Audience:                      role.Spec.Audience,
		TokenPolicies:                 append([]string(nil), role.Spec.TokenPolicies...),
		TokenTTL:                      role.Spec.TokenTTL,
		TokenMaxTTL:                   role.Spec.TokenMaxTTL,
	}
}

func kubernetesAuthRoleEqual(current bao.KubernetesAuthRole, desired bao.KubernetesAuthRole) bool {
	return current.BackendPath == desired.BackendPath &&
		current.RoleName == desired.RoleName &&
		slices.Equal(current.BoundServiceAccountNames, desired.BoundServiceAccountNames) &&
		slices.Equal(current.BoundServiceAccountNamespaces, desired.BoundServiceAccountNamespaces) &&
		current.Audience == desired.Audience &&
		slices.Equal(current.TokenPolicies, desired.TokenPolicies) &&
		current.TokenTTL == desired.TokenTTL &&
		current.TokenMaxTTL == desired.TokenMaxTTL
}
