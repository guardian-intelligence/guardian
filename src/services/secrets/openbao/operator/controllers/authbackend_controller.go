package controllers

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	openbaov1alpha1 "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/api/v1alpha1"
	bao "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/operator/openbao"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const authBackendFinalizer = "openbao.guardian.dev/auth-backend"

type AuthBackendClient interface {
	GetAuthBackend(ctx context.Context, path string) (bao.AuthBackend, bool, error)
	EnableAuthBackend(ctx context.Context, backend bao.AuthBackend) error
	DisableAuthBackend(ctx context.Context, path string) error
	GetKubernetesAuthConfig(ctx context.Context, backendPath string) (bao.KubernetesAuthConfig, bool, error)
	PutKubernetesAuthConfig(ctx context.Context, backendPath string, config bao.KubernetesAuthConfig) error
	GetAuthTune(ctx context.Context, backendPath string) (bao.TuneConfig, bool, error)
	PutAuthTune(ctx context.Context, backendPath string, tune bao.TuneConfig) error
}

type AuthBackendReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	OpenBao func(context.Context) (AuthBackendClient, error)
	Mode    ReconcileMode
}

func (r *AuthBackendReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var backend openbaov1alpha1.OpenBaoAuthBackend
	if err := r.Get(ctx, req.NamespacedName, &backend); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if r.OpenBao == nil {
		r.OpenBao = func(ctx context.Context) (AuthBackendClient, error) {
			return bao.NewAuthenticatedClientFromEnv(ctx)
		}
	}

	if !backend.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &backend)
	}

	if !r.Mode.AllowsWrites() && controllerutil.ContainsFinalizer(&backend, authBackendFinalizer) {
		controllerutil.RemoveFinalizer(&backend, authBackendFinalizer)
		if err := r.Update(ctx, &backend); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if r.Mode.AllowsWrites() && backend.Spec.DeletionPolicy == openbaov1alpha1.DeletionPolicyDelete && !controllerutil.ContainsFinalizer(&backend, authBackendFinalizer) {
		controllerutil.AddFinalizer(&backend, authBackendFinalizer)
		if err := r.Update(ctx, &backend); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if backend.Spec.DeletionPolicy != openbaov1alpha1.DeletionPolicyDelete && controllerutil.ContainsFinalizer(&backend, authBackendFinalizer) {
		controllerutil.RemoveFinalizer(&backend, authBackendFinalizer)
		if err := r.Update(ctx, &backend); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	desired, err := desiredAuthBackend(&backend)
	if err != nil {
		return r.updateAuthBackendErrorStatus(ctx, &backend, authBackendStatusInput{
			authenticated: metav1.ConditionUnknown,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        "InvalidSpec",
			message:       err.Error(),
			lastError:     err.Error(),
		}, err)
	}

	openbaoClient, err := r.OpenBao(ctx)
	if err != nil {
		authStatus := openBaoAuthFailureStatus(err)
		return r.updateAuthBackendErrorStatus(ctx, &backend, authBackendStatusInput{
			authenticated: metav1.ConditionFalse,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        authStatus.reason,
			message:       authStatus.message,
			lastError:     err.Error(),
		}, err)
	}

	current, found, err := openbaoClient.GetAuthBackend(ctx, desired.Path)
	if err != nil {
		return r.updateAuthBackendErrorStatus(ctx, &backend, authBackendStatusInput{
			authenticated: metav1.ConditionTrue,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        "ReadFailed",
			message:       fmt.Sprintf("Failed to read OpenBao auth backend %q.", desired.Path),
			lastError:     err.Error(),
		}, err)
	}

	if found && current.Type != desired.Type {
		err := fmt.Errorf("OpenBao auth backend %q has type %q, want %q", desired.Path, current.Type, desired.Type)
		if !r.Mode.AllowsWrites() {
			return r.updateAuthBackendStatus(ctx, &backend, authBackendStatusInput{
				authenticated:   metav1.ConditionTrue,
				applied:         metav1.ConditionFalse,
				drift:           metav1.ConditionTrue,
				ready:           metav1.ConditionFalse,
				reason:          "DriftDetected",
				message:         err.Error(),
				lastAppliedHash: specHash(backend.Spec),
			})
		}
		return r.updateAuthBackendErrorStatus(ctx, &backend, authBackendStatusInput{
			authenticated: metav1.ConditionTrue,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionTrue,
			ready:         metav1.ConditionFalse,
			reason:        "TypeMismatch",
			message:       err.Error(),
			lastError:     err.Error(),
		}, err)
	}

	if !found {
		if !r.Mode.AllowsWrites() {
			return r.updateAuthBackendStatus(ctx, &backend, authBackendStatusInput{
				authenticated:   metav1.ConditionTrue,
				applied:         metav1.ConditionFalse,
				drift:           metav1.ConditionTrue,
				ready:           metav1.ConditionFalse,
				reason:          "DriftDetected",
				message:         fmt.Sprintf("OpenBao auth backend %q is missing; observe mode left it unchanged.", desired.Path),
				lastAppliedHash: specHash(backend.Spec),
			})
		}
		if err := openbaoClient.EnableAuthBackend(ctx, desired); err != nil {
			return r.updateAuthBackendErrorStatus(ctx, &backend, authBackendStatusInput{
				authenticated: metav1.ConditionTrue,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionTrue,
				ready:         metav1.ConditionFalse,
				reason:        "ApplyFailed",
				message:       fmt.Sprintf("Failed to enable OpenBao auth backend %q.", desired.Path),
				lastError:     err.Error(),
			}, err)
		}
		current = desired
	}

	if backend.Spec.Type == "kubernetes" && backend.Spec.Kubernetes != nil {
		stop, err := r.reconcileKubernetesConfig(ctx, openbaoClient, &backend, desired.Path, desiredKubernetesAuthConfig(backend.Spec.Kubernetes))
		if err != nil || stop {
			return ctrl.Result{}, err
		}
	}

	desiredTune := desiredAuthTune(&backend)
	if backend.Spec.Tune != nil || current.Description != desired.Description {
		currentTune, tuneFound, err := openbaoClient.GetAuthTune(ctx, desired.Path)
		if err != nil {
			return r.updateAuthBackendErrorStatus(ctx, &backend, authBackendStatusInput{
				authenticated: metav1.ConditionTrue,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionUnknown,
				ready:         metav1.ConditionFalse,
				reason:        "ReadFailed",
				message:       fmt.Sprintf("Failed to read OpenBao auth backend tune for %q.", desired.Path),
				lastError:     err.Error(),
			}, err)
		}
		if !tuneFound {
			currentTune = bao.TuneConfig{}
		}
		currentTune.Description = current.Description
		if !tuneConfigEqual(currentTune, desiredTune, backend.Spec.Tune) {
			if !r.Mode.AllowsWrites() {
				return r.updateAuthBackendStatus(ctx, &backend, authBackendStatusInput{
					authenticated:   metav1.ConditionTrue,
					applied:         metav1.ConditionFalse,
					drift:           metav1.ConditionTrue,
					ready:           metav1.ConditionFalse,
					reason:          "DriftDetected",
					message:         fmt.Sprintf("OpenBao auth backend %q tune differs from desired state; observe mode left it unchanged.", desired.Path),
					lastAppliedHash: specHash(backend.Spec),
				})
			}
			if err := openbaoClient.PutAuthTune(ctx, desired.Path, desiredTune); err != nil {
				return r.updateAuthBackendErrorStatus(ctx, &backend, authBackendStatusInput{
					authenticated: metav1.ConditionTrue,
					applied:       metav1.ConditionFalse,
					drift:         metav1.ConditionTrue,
					ready:         metav1.ConditionFalse,
					reason:        "ApplyFailed",
					message:       fmt.Sprintf("Failed to tune OpenBao auth backend %q.", desired.Path),
					lastError:     err.Error(),
				}, err)
			}
		}
	}

	return r.updateAuthBackendStatus(ctx, &backend, authBackendStatusInput{
		authenticated:   metav1.ConditionTrue,
		applied:         metav1.ConditionTrue,
		drift:           metav1.ConditionFalse,
		ready:           metav1.ConditionTrue,
		reason:          appliedReason(r.Mode),
		message:         fmt.Sprintf("OpenBao auth backend %q is applied.", desired.Path),
		lastAppliedHash: specHash(backend.Spec),
	})
}

func (r *AuthBackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&openbaov1alpha1.OpenBaoAuthBackend{}).
		Complete(r)
}

func (r *AuthBackendReconciler) reconcileKubernetesConfig(ctx context.Context, openbaoClient AuthBackendClient, backend *openbaov1alpha1.OpenBaoAuthBackend, backendPath string, desired bao.KubernetesAuthConfig) (bool, error) {
	current, found, err := openbaoClient.GetKubernetesAuthConfig(ctx, backendPath)
	if err != nil {
		_, statusErr := r.updateAuthBackendErrorStatus(ctx, backend, authBackendStatusInput{
			authenticated: metav1.ConditionTrue,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        "ReadFailed",
			message:       fmt.Sprintf("Failed to read OpenBao Kubernetes auth config for %q.", backendPath),
			lastError:     err.Error(),
		}, err)
		return true, statusErr
	}
	if found && kubernetesAuthConfigEqual(current, desired) {
		return false, nil
	}
	if !r.Mode.AllowsWrites() {
		_, statusErr := r.updateAuthBackendStatus(ctx, backend, authBackendStatusInput{
			authenticated:   metav1.ConditionTrue,
			applied:         metav1.ConditionFalse,
			drift:           metav1.ConditionTrue,
			ready:           metav1.ConditionFalse,
			reason:          "DriftDetected",
			message:         fmt.Sprintf("OpenBao Kubernetes auth config for %q differs from desired state; observe mode left it unchanged.", backendPath),
			lastAppliedHash: specHash(backend.Spec),
		})
		return true, statusErr
	}
	if err := openbaoClient.PutKubernetesAuthConfig(ctx, backendPath, desired); err != nil {
		_, statusErr := r.updateAuthBackendErrorStatus(ctx, backend, authBackendStatusInput{
			authenticated: metav1.ConditionTrue,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionTrue,
			ready:         metav1.ConditionFalse,
			reason:        "ApplyFailed",
			message:       fmt.Sprintf("Failed to apply OpenBao Kubernetes auth config for %q.", backendPath),
			lastError:     err.Error(),
		}, err)
		return true, statusErr
	}
	return false, nil
}

func (r *AuthBackendReconciler) reconcileDelete(ctx context.Context, backend *openbaov1alpha1.OpenBaoAuthBackend) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(backend, authBackendFinalizer) {
		return ctrl.Result{}, nil
	}
	if !r.Mode.AllowsWrites() {
		controllerutil.RemoveFinalizer(backend, authBackendFinalizer)
		return ctrl.Result{}, r.Update(ctx, backend)
	}
	if backend.Spec.DeletionPolicy == openbaov1alpha1.DeletionPolicyDelete {
		openbaoClient, err := r.OpenBao(ctx)
		if err != nil {
			authStatus := openBaoAuthFailureStatus(err)
			return r.updateAuthBackendErrorStatus(ctx, backend, authBackendStatusInput{
				authenticated: metav1.ConditionFalse,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionUnknown,
				ready:         metav1.ConditionFalse,
				reason:        authStatus.reason,
				message:       authStatus.message,
				lastError:     err.Error(),
			}, err)
		}
		if err := openbaoClient.DisableAuthBackend(ctx, strings.Trim(backend.Spec.Path, "/")); err != nil {
			return r.updateAuthBackendErrorStatus(ctx, backend, authBackendStatusInput{
				authenticated: metav1.ConditionTrue,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionUnknown,
				ready:         metav1.ConditionFalse,
				reason:        "DeleteFailed",
				message:       fmt.Sprintf("Failed to disable OpenBao auth backend %q.", backend.Spec.Path),
				lastError:     err.Error(),
			}, err)
		}
	}
	controllerutil.RemoveFinalizer(backend, authBackendFinalizer)
	return ctrl.Result{}, r.Update(ctx, backend)
}

type authBackendStatusInput struct {
	authenticated   metav1.ConditionStatus
	applied         metav1.ConditionStatus
	drift           metav1.ConditionStatus
	ready           metav1.ConditionStatus
	reason          string
	message         string
	lastAppliedHash string
	lastError       string
}

func (r *AuthBackendReconciler) updateAuthBackendStatus(ctx context.Context, backend *openbaov1alpha1.OpenBaoAuthBackend, input authBackendStatusInput) (ctrl.Result, error) {
	status := &backend.Status
	status.ObservedGeneration = backend.Generation
	if input.lastAppliedHash != "" {
		status.LastAppliedHash = input.lastAppliedHash
	}
	status.LastError = input.lastError
	setCondition(status, openbaov1alpha1.ConditionAuthenticated, input.authenticated, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionApplied, input.applied, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionDriftDetected, input.drift, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionReady, input.ready, input.reason, input.message)
	return ctrl.Result{}, r.Status().Update(ctx, backend)
}

func (r *AuthBackendReconciler) updateAuthBackendErrorStatus(ctx context.Context, backend *openbaov1alpha1.OpenBaoAuthBackend, input authBackendStatusInput, cause error) (ctrl.Result, error) {
	if _, err := r.updateAuthBackendStatus(ctx, backend, input); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, cause
}

func desiredAuthBackend(backend *openbaov1alpha1.OpenBaoAuthBackend) (bao.AuthBackend, error) {
	if backend.Spec.Type != "kubernetes" {
		return bao.AuthBackend{}, fmt.Errorf("unsupported OpenBao auth backend type %q", backend.Spec.Type)
	}
	return bao.AuthBackend{
		Path:        strings.Trim(backend.Spec.Path, "/"),
		Type:        backend.Spec.Type,
		Description: backend.Spec.Description,
	}, nil
}

func desiredKubernetesAuthConfig(config *openbaov1alpha1.OpenBaoKubernetesAuthConfig) bao.KubernetesAuthConfig {
	return bao.KubernetesAuthConfig{
		KubernetesHost:       config.KubernetesHost,
		KubernetesCACert:     config.KubernetesCACert,
		Issuer:               config.Issuer,
		PEMKeys:              append([]string(nil), config.PEMKeys...),
		DisableISSValidation: config.DisableISSValidation,
		DisableLocalCAJWT:    config.DisableLocalCAJWT,
	}
}

func desiredAuthTune(backend *openbaov1alpha1.OpenBaoAuthBackend) bao.TuneConfig {
	return desiredTuneConfig(backend.Spec.Description, backend.Spec.Tune)
}

func desiredTuneConfig(description string, spec *openbaov1alpha1.OpenBaoTuneSpec) bao.TuneConfig {
	tune := bao.TuneConfig{Description: description}
	if spec == nil {
		return tune
	}
	tune.DefaultLeaseTTL = spec.DefaultLeaseTTL
	tune.MaxLeaseTTL = spec.MaxLeaseTTL
	tune.ListingVisibility = spec.ListingVisibility
	tune.PassthroughRequestHeaders = append([]string(nil), spec.PassthroughRequestHeaders...)
	tune.AllowedResponseHeaders = append([]string(nil), spec.AllowedResponseHeaders...)
	tune.AuditNonHMACRequestKeys = append([]string(nil), spec.AuditNonHMACRequestKeys...)
	tune.AuditNonHMACResponseKeys = append([]string(nil), spec.AuditNonHMACResponseKeys...)
	return tune
}

func kubernetesAuthConfigEqual(current bao.KubernetesAuthConfig, desired bao.KubernetesAuthConfig) bool {
	return current.KubernetesHost == desired.KubernetesHost &&
		current.KubernetesCACert == desired.KubernetesCACert &&
		current.Issuer == desired.Issuer &&
		slices.Equal(current.PEMKeys, desired.PEMKeys) &&
		current.DisableISSValidation == desired.DisableISSValidation &&
		current.DisableLocalCAJWT == desired.DisableLocalCAJWT
}

func tuneConfigEqual(current bao.TuneConfig, desired bao.TuneConfig, spec *openbaov1alpha1.OpenBaoTuneSpec) bool {
	if current.Description != desired.Description {
		return false
	}
	if spec == nil {
		return true
	}
	if desired.DefaultLeaseTTL != "" && !ttlEqual(current.DefaultLeaseTTL, desired.DefaultLeaseTTL) {
		return false
	}
	if desired.MaxLeaseTTL != "" && !ttlEqual(current.MaxLeaseTTL, desired.MaxLeaseTTL) {
		return false
	}
	if desired.ListingVisibility != "" && current.ListingVisibility != desired.ListingVisibility {
		return false
	}
	if desired.PassthroughRequestHeaders != nil && !slices.Equal(current.PassthroughRequestHeaders, desired.PassthroughRequestHeaders) {
		return false
	}
	if desired.AllowedResponseHeaders != nil && !slices.Equal(current.AllowedResponseHeaders, desired.AllowedResponseHeaders) {
		return false
	}
	if desired.AuditNonHMACRequestKeys != nil && !slices.Equal(current.AuditNonHMACRequestKeys, desired.AuditNonHMACRequestKeys) {
		return false
	}
	if desired.AuditNonHMACResponseKeys != nil && !slices.Equal(current.AuditNonHMACResponseKeys, desired.AuditNonHMACResponseKeys) {
		return false
	}
	return true
}

func ttlEqual(current string, desired string) bool {
	if desired == "" {
		return current == ""
	}
	return normalizeTTL(current) == normalizeTTL(desired)
}

func normalizeTTL(value string) string {
	if value == "" {
		return ""
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		return strconv.FormatInt(seconds, 10)
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return value
	}
	return strconv.FormatInt(int64(duration/time.Second), 10)
}
