package controllers

import (
	"context"
	"fmt"

	openbaov1alpha1 "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/api/v1alpha1"
	bao "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/operator/openbao"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const policyFinalizer = "openbao.guardian.dev/policy"

type PolicyClient interface {
	GetPolicy(ctx context.Context, name string) (string, error)
	PutPolicy(ctx context.Context, name string, rules string) error
	DeletePolicy(ctx context.Context, name string) error
}

type PolicyReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	OpenBao func(context.Context) (PolicyClient, error)
	Mode    ReconcileMode
}

func (r *PolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var policy openbaov1alpha1.OpenBaoPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if r.OpenBao == nil {
		r.OpenBao = func(ctx context.Context) (PolicyClient, error) {
			return bao.NewAuthenticatedClientFromEnv(ctx)
		}
	}

	if !policy.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &policy)
	}

	if !r.Mode.AllowsWrites() && controllerutil.ContainsFinalizer(&policy, policyFinalizer) {
		controllerutil.RemoveFinalizer(&policy, policyFinalizer)
		if err := r.Update(ctx, &policy); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if r.Mode.AllowsWrites() && policy.Spec.DeletionPolicy == openbaov1alpha1.DeletionPolicyDelete && !controllerutil.ContainsFinalizer(&policy, policyFinalizer) {
		controllerutil.AddFinalizer(&policy, policyFinalizer)
		if err := r.Update(ctx, &policy); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if policy.Spec.DeletionPolicy != openbaov1alpha1.DeletionPolicyDelete && controllerutil.ContainsFinalizer(&policy, policyFinalizer) {
		controllerutil.RemoveFinalizer(&policy, policyFinalizer)
		if err := r.Update(ctx, &policy); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	desiredHash := specHash(policy.Spec)
	policyName := policyName(&policy)
	openbaoClient, err := r.OpenBao(ctx)
	if err != nil {
		return r.updatePolicyErrorStatus(ctx, &policy, policyStatusInput{
			authenticated: metav1.ConditionFalse,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        "AuthenticationFailed",
			message:       "OpenBao Kubernetes auth login failed.",
			lastError:     err.Error(),
		}, err)
	}

	currentRules, err := openbaoClient.GetPolicy(ctx, policyName)
	if err != nil {
		return r.updatePolicyErrorStatus(ctx, &policy, policyStatusInput{
			authenticated: metav1.ConditionTrue,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        "ReadFailed",
			message:       fmt.Sprintf("Failed to read OpenBao policy %q.", policyName),
			lastError:     err.Error(),
		}, err)
	}

	if currentRules != policy.Spec.Rules {
		if !r.Mode.AllowsWrites() {
			return r.updatePolicyStatus(ctx, &policy, policyStatusInput{
				authenticated:   metav1.ConditionTrue,
				applied:         metav1.ConditionFalse,
				drift:           metav1.ConditionTrue,
				ready:           metav1.ConditionFalse,
				reason:          "DriftDetected",
				message:         fmt.Sprintf("OpenBao policy %q differs from desired state; observe mode left it unchanged.", policyName),
				lastAppliedHash: desiredHash,
			})
		}
		if err := openbaoClient.PutPolicy(ctx, policyName, policy.Spec.Rules); err != nil {
			return r.updatePolicyErrorStatus(ctx, &policy, policyStatusInput{
				authenticated: metav1.ConditionTrue,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionTrue,
				ready:         metav1.ConditionFalse,
				reason:        "ApplyFailed",
				message:       fmt.Sprintf("Failed to apply OpenBao policy %q.", policyName),
				lastError:     err.Error(),
			}, err)
		}
	}

	return r.updatePolicyStatus(ctx, &policy, policyStatusInput{
		authenticated:   metav1.ConditionTrue,
		applied:         metav1.ConditionTrue,
		drift:           metav1.ConditionFalse,
		ready:           metav1.ConditionTrue,
		reason:          appliedReason(r.Mode),
		message:         fmt.Sprintf("OpenBao policy %q is applied.", policyName),
		lastAppliedHash: desiredHash,
	})
}

func (r *PolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&openbaov1alpha1.OpenBaoPolicy{}).
		Complete(r)
}

func (r *PolicyReconciler) reconcileDelete(ctx context.Context, policy *openbaov1alpha1.OpenBaoPolicy) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(policy, policyFinalizer) {
		return ctrl.Result{}, nil
	}
	if !r.Mode.AllowsWrites() {
		controllerutil.RemoveFinalizer(policy, policyFinalizer)
		return ctrl.Result{}, r.Update(ctx, policy)
	}
	if policy.Spec.DeletionPolicy == openbaov1alpha1.DeletionPolicyDelete {
		openbaoClient, err := r.OpenBao(ctx)
		if err != nil {
			return r.updatePolicyErrorStatus(ctx, policy, policyStatusInput{
				authenticated: metav1.ConditionFalse,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionUnknown,
				ready:         metav1.ConditionFalse,
				reason:        "AuthenticationFailed",
				message:       "OpenBao Kubernetes auth login failed while deleting policy.",
				lastError:     err.Error(),
			}, err)
		}
		if err := openbaoClient.DeletePolicy(ctx, policyName(policy)); err != nil {
			return r.updatePolicyErrorStatus(ctx, policy, policyStatusInput{
				authenticated: metav1.ConditionTrue,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionUnknown,
				ready:         metav1.ConditionFalse,
				reason:        "DeleteFailed",
				message:       fmt.Sprintf("Failed to delete OpenBao policy %q.", policyName(policy)),
				lastError:     err.Error(),
			}, err)
		}
	}
	controllerutil.RemoveFinalizer(policy, policyFinalizer)
	return ctrl.Result{}, r.Update(ctx, policy)
}

type policyStatusInput struct {
	authenticated   metav1.ConditionStatus
	applied         metav1.ConditionStatus
	drift           metav1.ConditionStatus
	ready           metav1.ConditionStatus
	reason          string
	message         string
	lastAppliedHash string
	lastError       string
}

func (r *PolicyReconciler) updatePolicyStatus(ctx context.Context, policy *openbaov1alpha1.OpenBaoPolicy, input policyStatusInput) (ctrl.Result, error) {
	status := &policy.Status
	status.ObservedGeneration = policy.Generation
	if input.lastAppliedHash != "" {
		status.LastAppliedHash = input.lastAppliedHash
	}
	status.LastError = input.lastError
	setCondition(status, openbaov1alpha1.ConditionAuthenticated, input.authenticated, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionApplied, input.applied, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionDriftDetected, input.drift, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionReady, input.ready, input.reason, input.message)
	err := r.Status().Update(ctx, policy)
	return ctrl.Result{}, err
}

func (r *PolicyReconciler) updatePolicyErrorStatus(ctx context.Context, policy *openbaov1alpha1.OpenBaoPolicy, input policyStatusInput, cause error) (ctrl.Result, error) {
	if _, err := r.updatePolicyStatus(ctx, policy, input); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, cause
}

func policyName(policy *openbaov1alpha1.OpenBaoPolicy) string {
	if policy.Spec.Name != "" {
		return policy.Spec.Name
	}
	return policy.Name
}
