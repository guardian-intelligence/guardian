package controllers

import (
	"context"
	"fmt"
	"strings"

	openbaov1alpha1 "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/api/v1alpha1"
	bao "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/operator/openbao"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type PKIRootIssuerClient interface {
	GetPKIIssuer(ctx context.Context, mountPath string, issuerRef string) (bao.PKIIssuer, bool, error)
	GeneratePKIRootIssuer(ctx context.Context, issuer bao.PKIRootIssuer) (bao.PKIIssuer, error)
	GetPKIIssuerConfig(ctx context.Context, mountPath string) (bao.PKIIssuerConfig, bool, error)
	PutPKIIssuerConfig(ctx context.Context, mountPath string, config bao.PKIIssuerConfig) error
}

type PKIRootIssuerReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	OpenBao func(context.Context) (PKIRootIssuerClient, error)
	Mode    ReconcileMode
}

func (r *PKIRootIssuerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var rootIssuer openbaov1alpha1.OpenBaoPKIRootIssuer
	if err := r.Get(ctx, req.NamespacedName, &rootIssuer); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if r.OpenBao == nil {
		r.OpenBao = func(ctx context.Context) (PKIRootIssuerClient, error) {
			return bao.NewAuthenticatedClientFromEnv(ctx)
		}
	}

	if !rootIssuer.ObjectMeta.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	desired := desiredPKIRootIssuer(&rootIssuer)
	if err := validatePKIRootIssuer(desired); err != nil {
		return r.updatePKIRootIssuerStatus(ctx, &rootIssuer, pkiRootIssuerStatusInput{
			authenticated:   metav1.ConditionUnknown,
			applied:         metav1.ConditionFalse,
			drift:           metav1.ConditionUnknown,
			ready:           metav1.ConditionFalse,
			reason:          "InvalidSpec",
			message:         err.Error(),
			lastAppliedHash: specHash(rootIssuer.Spec),
			lastError:       err.Error(),
		})
	}

	openbaoClient, err := r.OpenBao(ctx)
	if err != nil {
		authStatus := openBaoAuthFailureStatus(err)
		return r.updatePKIRootIssuerErrorStatus(ctx, &rootIssuer, pkiRootIssuerStatusInput{
			authenticated: metav1.ConditionFalse,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        authStatus.reason,
			message:       authStatus.message,
			lastError:     err.Error(),
		}, err)
	}

	currentIssuer, issuerFound, err := openbaoClient.GetPKIIssuer(ctx, desired.MountPath, desired.IssuerName)
	if err != nil {
		return r.updatePKIRootIssuerErrorStatus(ctx, &rootIssuer, pkiRootIssuerStatusInput{
			authenticated: metav1.ConditionTrue,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        "ReadFailed",
			message:       fmt.Sprintf("Failed to read OpenBao PKI issuer %q.", desired.IssuerName),
			lastError:     err.Error(),
		}, err)
	}

	if !issuerFound {
		if !r.Mode.AllowsWrites() {
			return r.updatePKIRootIssuerStatus(ctx, &rootIssuer, pkiRootIssuerStatusInput{
				authenticated:   metav1.ConditionTrue,
				applied:         metav1.ConditionFalse,
				drift:           metav1.ConditionTrue,
				ready:           metav1.ConditionFalse,
				reason:          "DriftDetected",
				message:         fmt.Sprintf("OpenBao PKI issuer %q is missing; observe mode left it unchanged.", desired.IssuerName),
				lastAppliedHash: specHash(rootIssuer.Spec),
			})
		}
		currentIssuer, err = openbaoClient.GeneratePKIRootIssuer(ctx, desired)
		if err != nil {
			return r.updatePKIRootIssuerErrorStatus(ctx, &rootIssuer, pkiRootIssuerStatusInput{
				authenticated: metav1.ConditionTrue,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionTrue,
				ready:         metav1.ConditionFalse,
				reason:        "ApplyFailed",
				message:       fmt.Sprintf("Failed to generate OpenBao PKI root issuer %q.", desired.IssuerName),
				lastError:     err.Error(),
			}, err)
		}
	}

	config, configFound, err := openbaoClient.GetPKIIssuerConfig(ctx, desired.MountPath)
	if err != nil {
		return r.updatePKIRootIssuerErrorStatus(ctx, &rootIssuer, pkiRootIssuerStatusInput{
			authenticated: metav1.ConditionTrue,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        "ReadFailed",
			message:       fmt.Sprintf("Failed to read OpenBao PKI issuer config for %q.", desired.MountPath),
			lastError:     err.Error(),
		}, err)
	}

	defaultApplied := !rootIssuer.Spec.SetDefault || (configFound && pkiIssuerIsDefault(config, currentIssuer, desired.IssuerName))
	if !defaultApplied {
		if !r.Mode.AllowsWrites() {
			return r.updatePKIRootIssuerStatus(ctx, &rootIssuer, pkiRootIssuerStatusInput{
				authenticated:   metav1.ConditionTrue,
				applied:         metav1.ConditionFalse,
				drift:           metav1.ConditionTrue,
				ready:           metav1.ConditionFalse,
				reason:          "DriftDetected",
				message:         fmt.Sprintf("OpenBao PKI issuer %q is not the default issuer; observe mode left it unchanged.", desired.IssuerName),
				lastAppliedHash: specHash(rootIssuer.Spec),
			})
		}
		if err := openbaoClient.PutPKIIssuerConfig(ctx, desired.MountPath, bao.PKIIssuerConfig{
			Default:                    desired.IssuerName,
			DefaultFollowsLatestIssuer: false,
		}); err != nil {
			return r.updatePKIRootIssuerErrorStatus(ctx, &rootIssuer, pkiRootIssuerStatusInput{
				authenticated: metav1.ConditionTrue,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionTrue,
				ready:         metav1.ConditionFalse,
				reason:        "ApplyFailed",
				message:       fmt.Sprintf("Failed to set OpenBao PKI issuer %q as default.", desired.IssuerName),
				lastError:     err.Error(),
			}, err)
		}
	}

	return r.updatePKIRootIssuerStatus(ctx, &rootIssuer, pkiRootIssuerStatusInput{
		authenticated:   metav1.ConditionTrue,
		applied:         metav1.ConditionTrue,
		drift:           metav1.ConditionFalse,
		ready:           metav1.ConditionTrue,
		reason:          appliedReason(r.Mode),
		message:         fmt.Sprintf("OpenBao PKI root issuer %q is applied.", desired.IssuerName),
		lastAppliedHash: specHash(rootIssuer.Spec),
	})
}

func (r *PKIRootIssuerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&openbaov1alpha1.OpenBaoPKIRootIssuer{}).
		Complete(r)
}

type pkiRootIssuerStatusInput struct {
	authenticated   metav1.ConditionStatus
	applied         metav1.ConditionStatus
	drift           metav1.ConditionStatus
	ready           metav1.ConditionStatus
	reason          string
	message         string
	lastAppliedHash string
	lastError       string
}

func (r *PKIRootIssuerReconciler) updatePKIRootIssuerStatus(ctx context.Context, rootIssuer *openbaov1alpha1.OpenBaoPKIRootIssuer, input pkiRootIssuerStatusInput) (ctrl.Result, error) {
	status := &rootIssuer.Status
	status.ObservedGeneration = rootIssuer.Generation
	if input.lastAppliedHash != "" {
		status.LastAppliedHash = input.lastAppliedHash
	}
	status.LastError = input.lastError
	setCondition(status, openbaov1alpha1.ConditionAuthenticated, input.authenticated, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionApplied, input.applied, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionDriftDetected, input.drift, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionReady, input.ready, input.reason, input.message)
	return ctrl.Result{}, r.Status().Update(ctx, rootIssuer)
}

func (r *PKIRootIssuerReconciler) updatePKIRootIssuerErrorStatus(ctx context.Context, rootIssuer *openbaov1alpha1.OpenBaoPKIRootIssuer, input pkiRootIssuerStatusInput, cause error) (ctrl.Result, error) {
	if _, err := r.updatePKIRootIssuerStatus(ctx, rootIssuer, input); err != nil {
		return ctrl.Result{}, err
	}
	if input.reason == reasonSelfInitIncomplete {
		return ctrl.Result{RequeueAfter: selfInitIncompleteRequeueAfter}, nil
	}
	return ctrl.Result{}, cause
}

func desiredPKIRootIssuer(rootIssuer *openbaov1alpha1.OpenBaoPKIRootIssuer) bao.PKIRootIssuer {
	return bao.PKIRootIssuer{
		MountPath:  strings.Trim(rootIssuer.Spec.MountPath, "/"),
		IssuerName: rootIssuer.Spec.IssuerName,
		CommonName: rootIssuer.Spec.CommonName,
		TTL:        rootIssuer.Spec.TTL,
		KeyType:    rootIssuer.Spec.KeyType,
		KeyBits:    rootIssuer.Spec.KeyBits,
	}
}

func validatePKIRootIssuer(issuer bao.PKIRootIssuer) error {
	if issuer.MountPath == "" {
		return fmt.Errorf("OpenBao PKI root issuer mountPath is required")
	}
	if issuer.IssuerName == "" {
		return fmt.Errorf("OpenBao PKI root issuer issuerName is required")
	}
	if strings.Contains(issuer.IssuerName, "/") {
		return fmt.Errorf("OpenBao PKI root issuer issuerName must not contain '/'")
	}
	if issuer.CommonName == "" {
		return fmt.Errorf("OpenBao PKI root issuer commonName is required")
	}
	if issuer.TTL == "" {
		return fmt.Errorf("OpenBao PKI root issuer ttl is required")
	}
	if issuer.KeyType == "" {
		return fmt.Errorf("OpenBao PKI root issuer keyType is required")
	}
	return nil
}

func pkiIssuerIsDefault(config bao.PKIIssuerConfig, issuer bao.PKIIssuer, issuerName string) bool {
	if config.DefaultFollowsLatestIssuer {
		return false
	}
	defaultRef := strings.TrimSpace(config.Default)
	if defaultRef == "" {
		return false
	}
	for _, accepted := range []string{issuerName, issuer.IssuerRef, issuer.IssuerName, issuer.IssuerID} {
		if accepted != "" && defaultRef == accepted {
			return true
		}
	}
	return false
}
