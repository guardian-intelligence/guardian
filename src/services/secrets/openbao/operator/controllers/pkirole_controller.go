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

const pkiRoleFinalizer = "openbao.guardian.dev/pki-role"

type PKIRoleClient interface {
	GetPKIRole(ctx context.Context, mountPath string, roleName string) (bao.PKIRole, bool, error)
	PutPKIRole(ctx context.Context, role bao.PKIRole) error
	DeletePKIRole(ctx context.Context, mountPath string, roleName string) error
}

type PKIRoleReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	OpenBao func(context.Context) (PKIRoleClient, error)
	Mode    ReconcileMode
}

func (r *PKIRoleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var role openbaov1alpha1.OpenBaoPKIRole
	if err := r.Get(ctx, req.NamespacedName, &role); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if r.OpenBao == nil {
		r.OpenBao = func(ctx context.Context) (PKIRoleClient, error) {
			return bao.NewAuthenticatedClientFromEnv(ctx)
		}
	}

	if !role.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &role)
	}

	if !r.Mode.AllowsWrites() && controllerutil.ContainsFinalizer(&role, pkiRoleFinalizer) {
		controllerutil.RemoveFinalizer(&role, pkiRoleFinalizer)
		if err := r.Update(ctx, &role); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if r.Mode.AllowsWrites() && role.Spec.DeletionPolicy == openbaov1alpha1.DeletionPolicyDelete && !controllerutil.ContainsFinalizer(&role, pkiRoleFinalizer) {
		controllerutil.AddFinalizer(&role, pkiRoleFinalizer)
		if err := r.Update(ctx, &role); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if role.Spec.DeletionPolicy != openbaov1alpha1.DeletionPolicyDelete && controllerutil.ContainsFinalizer(&role, pkiRoleFinalizer) {
		controllerutil.RemoveFinalizer(&role, pkiRoleFinalizer)
		if err := r.Update(ctx, &role); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	desired := desiredPKIRole(&role)
	if err := validatePKIRole(desired); err != nil {
		return r.updatePKIRoleStatus(ctx, &role, pkiRoleStatusInput{
			authenticated:   metav1.ConditionUnknown,
			applied:         metav1.ConditionFalse,
			drift:           metav1.ConditionUnknown,
			ready:           metav1.ConditionFalse,
			reason:          "InvalidSpec",
			message:         err.Error(),
			lastAppliedHash: specHash(role.Spec),
			lastError:       err.Error(),
		})
	}

	openbaoClient, err := r.OpenBao(ctx)
	if err != nil {
		authStatus := openBaoAuthFailureStatus(err)
		return r.updatePKIRoleErrorStatus(ctx, &role, pkiRoleStatusInput{
			authenticated: metav1.ConditionFalse,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        authStatus.reason,
			message:       authStatus.message,
			lastError:     err.Error(),
		}, err)
	}

	current, found, err := openbaoClient.GetPKIRole(ctx, desired.MountPath, desired.RoleName)
	if err != nil {
		return r.updatePKIRoleErrorStatus(ctx, &role, pkiRoleStatusInput{
			authenticated: metav1.ConditionTrue,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        "ReadFailed",
			message:       fmt.Sprintf("Failed to read OpenBao PKI role %q.", desired.RoleName),
			lastError:     err.Error(),
		}, err)
	}

	if !found || !pkiRoleEqual(current, desired) {
		if !r.Mode.AllowsWrites() {
			return r.updatePKIRoleStatus(ctx, &role, pkiRoleStatusInput{
				authenticated:   metav1.ConditionTrue,
				applied:         metav1.ConditionFalse,
				drift:           metav1.ConditionTrue,
				ready:           metav1.ConditionFalse,
				reason:          "DriftDetected",
				message:         fmt.Sprintf("OpenBao PKI role %q differs from desired state; observe mode left it unchanged.", desired.RoleName),
				lastAppliedHash: specHash(role.Spec),
			})
		}
		if err := openbaoClient.PutPKIRole(ctx, desired); err != nil {
			return r.updatePKIRoleErrorStatus(ctx, &role, pkiRoleStatusInput{
				authenticated: metav1.ConditionTrue,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionTrue,
				ready:         metav1.ConditionFalse,
				reason:        "ApplyFailed",
				message:       fmt.Sprintf("Failed to apply OpenBao PKI role %q.", desired.RoleName),
				lastError:     err.Error(),
			}, err)
		}
	}

	return r.updatePKIRoleStatus(ctx, &role, pkiRoleStatusInput{
		authenticated:   metav1.ConditionTrue,
		applied:         metav1.ConditionTrue,
		drift:           metav1.ConditionFalse,
		ready:           metav1.ConditionTrue,
		reason:          appliedReason(r.Mode),
		message:         fmt.Sprintf("OpenBao PKI role %q is applied.", desired.RoleName),
		lastAppliedHash: specHash(role.Spec),
	})
}

func (r *PKIRoleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&openbaov1alpha1.OpenBaoPKIRole{}).
		Complete(r)
}

func (r *PKIRoleReconciler) reconcileDelete(ctx context.Context, role *openbaov1alpha1.OpenBaoPKIRole) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(role, pkiRoleFinalizer) {
		return ctrl.Result{}, nil
	}
	if !r.Mode.AllowsWrites() {
		controllerutil.RemoveFinalizer(role, pkiRoleFinalizer)
		return ctrl.Result{}, r.Update(ctx, role)
	}
	if role.Spec.DeletionPolicy == openbaov1alpha1.DeletionPolicyDelete {
		openbaoClient, err := r.OpenBao(ctx)
		if err != nil {
			authStatus := openBaoAuthFailureStatus(err)
			return r.updatePKIRoleErrorStatus(ctx, role, pkiRoleStatusInput{
				authenticated: metav1.ConditionFalse,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionUnknown,
				ready:         metav1.ConditionFalse,
				reason:        authStatus.reason,
				message:       authStatus.message,
				lastError:     err.Error(),
			}, err)
		}
		desired := desiredPKIRole(role)
		if err := openbaoClient.DeletePKIRole(ctx, desired.MountPath, desired.RoleName); err != nil {
			return r.updatePKIRoleErrorStatus(ctx, role, pkiRoleStatusInput{
				authenticated: metav1.ConditionTrue,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionUnknown,
				ready:         metav1.ConditionFalse,
				reason:        "DeleteFailed",
				message:       fmt.Sprintf("Failed to delete OpenBao PKI role %q.", desired.RoleName),
				lastError:     err.Error(),
			}, err)
		}
	}
	controllerutil.RemoveFinalizer(role, pkiRoleFinalizer)
	return ctrl.Result{}, r.Update(ctx, role)
}

type pkiRoleStatusInput struct {
	authenticated   metav1.ConditionStatus
	applied         metav1.ConditionStatus
	drift           metav1.ConditionStatus
	ready           metav1.ConditionStatus
	reason          string
	message         string
	lastAppliedHash string
	lastError       string
}

func (r *PKIRoleReconciler) updatePKIRoleStatus(ctx context.Context, role *openbaov1alpha1.OpenBaoPKIRole, input pkiRoleStatusInput) (ctrl.Result, error) {
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

func (r *PKIRoleReconciler) updatePKIRoleErrorStatus(ctx context.Context, role *openbaov1alpha1.OpenBaoPKIRole, input pkiRoleStatusInput, cause error) (ctrl.Result, error) {
	if _, err := r.updatePKIRoleStatus(ctx, role, input); err != nil {
		return ctrl.Result{}, err
	}
	if input.reason == reasonSelfInitIncomplete {
		return ctrl.Result{RequeueAfter: selfInitIncompleteRequeueAfter}, nil
	}
	return ctrl.Result{}, cause
}

func desiredPKIRole(role *openbaov1alpha1.OpenBaoPKIRole) bao.PKIRole {
	roleName := role.Spec.RoleName
	if roleName == "" {
		roleName = role.Name
	}
	return bao.PKIRole{
		MountPath:                 strings.Trim(role.Spec.MountPath, "/"),
		RoleName:                  roleName,
		IssuerRef:                 role.Spec.IssuerRef,
		TTL:                       role.Spec.TTL,
		MaxTTL:                    role.Spec.MaxTTL,
		AllowLocalhost:            role.Spec.AllowLocalhost,
		AllowedDomains:            append([]string(nil), role.Spec.AllowedDomains...),
		AllowBareDomains:          role.Spec.AllowBareDomains,
		AllowSubdomains:           role.Spec.AllowSubdomains,
		AllowGlobDomains:          role.Spec.AllowGlobDomains,
		AllowWildcardCertificates: role.Spec.AllowWildcardCertificates,
		AllowAnyName:              role.Spec.AllowAnyName,
		EnforceHostnames:          role.Spec.EnforceHostnames,
		AllowIPSANs:               role.Spec.AllowIPSANs,
		AllowedIPSANsCIDR:         append([]string(nil), role.Spec.AllowedIPSANsCIDR...),
		ServerFlag:                role.Spec.ServerFlag,
		ClientFlag:                role.Spec.ClientFlag,
		CodeSigningFlag:           role.Spec.CodeSigningFlag,
		EmailProtectionFlag:       role.Spec.EmailProtectionFlag,
		KeyType:                   role.Spec.KeyType,
		KeyBits:                   role.Spec.KeyBits,
		KeyUsage:                  append([]string(nil), role.Spec.KeyUsage...),
		ExtKeyUsage:               append([]string(nil), role.Spec.ExtKeyUsage...),
		CNValidations:             append([]string(nil), role.Spec.CNValidations...),
		UseCSRCommonName:          role.Spec.UseCSRCommonName,
		UseCSRSANs:                role.Spec.UseCSRSANs,
		GenerateLease:             role.Spec.GenerateLease,
		NoStore:                   role.Spec.NoStore,
		RequireCN:                 role.Spec.RequireCN,
		NotBeforeDuration:         role.Spec.NotBeforeDuration,
		NotBeforeBound:            role.Spec.NotBeforeBound,
		NotAfterBound:             role.Spec.NotAfterBound,
	}
}

func validatePKIRole(role bao.PKIRole) error {
	if role.MountPath == "" {
		return fmt.Errorf("OpenBao PKI role mountPath is required")
	}
	if role.RoleName == "" {
		return fmt.Errorf("OpenBao PKI role name is required")
	}
	if role.AllowIPSANs && len(role.AllowedIPSANsCIDR) == 0 {
		return fmt.Errorf("OpenBao PKI role %q allows IP SANs but has no allowedIPSANsCIDR entries", role.RoleName)
	}
	return nil
}

func pkiRoleEqual(current bao.PKIRole, desired bao.PKIRole) bool {
	return current.MountPath == desired.MountPath &&
		current.RoleName == desired.RoleName &&
		current.IssuerRef == desired.IssuerRef &&
		current.TTL == desired.TTL &&
		current.MaxTTL == desired.MaxTTL &&
		current.AllowLocalhost == desired.AllowLocalhost &&
		slices.Equal(current.AllowedDomains, desired.AllowedDomains) &&
		current.AllowBareDomains == desired.AllowBareDomains &&
		current.AllowSubdomains == desired.AllowSubdomains &&
		current.AllowGlobDomains == desired.AllowGlobDomains &&
		current.AllowWildcardCertificates == desired.AllowWildcardCertificates &&
		current.AllowAnyName == desired.AllowAnyName &&
		current.EnforceHostnames == desired.EnforceHostnames &&
		current.AllowIPSANs == desired.AllowIPSANs &&
		slices.Equal(current.AllowedIPSANsCIDR, desired.AllowedIPSANsCIDR) &&
		current.ServerFlag == desired.ServerFlag &&
		current.ClientFlag == desired.ClientFlag &&
		current.CodeSigningFlag == desired.CodeSigningFlag &&
		current.EmailProtectionFlag == desired.EmailProtectionFlag &&
		current.KeyType == desired.KeyType &&
		current.KeyBits == desired.KeyBits &&
		slices.Equal(current.KeyUsage, desired.KeyUsage) &&
		slices.Equal(current.ExtKeyUsage, desired.ExtKeyUsage) &&
		slices.Equal(current.CNValidations, desired.CNValidations) &&
		current.UseCSRCommonName == desired.UseCSRCommonName &&
		current.UseCSRSANs == desired.UseCSRSANs &&
		current.GenerateLease == desired.GenerateLease &&
		current.NoStore == desired.NoStore &&
		current.RequireCN == desired.RequireCN &&
		current.NotBeforeDuration == desired.NotBeforeDuration &&
		current.NotBeforeBound == desired.NotBeforeBound &&
		current.NotAfterBound == desired.NotAfterBound
}
