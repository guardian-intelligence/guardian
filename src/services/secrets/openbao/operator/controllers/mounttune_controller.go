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

type MountTuneClient interface {
	GetMount(ctx context.Context, path string) (bao.Mount, bool, error)
	GetMountTune(ctx context.Context, mountPath string) (bao.TuneConfig, bool, error)
	PutMountTune(ctx context.Context, mountPath string, tune bao.TuneConfig) error
}

type MountTuneReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	OpenBao func(context.Context) (MountTuneClient, error)
	Mode    ReconcileMode
}

func (r *MountTuneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var tune openbaov1alpha1.OpenBaoMountTune
	if err := r.Get(ctx, req.NamespacedName, &tune); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if r.OpenBao == nil {
		r.OpenBao = func(ctx context.Context) (MountTuneClient, error) {
			return bao.NewAuthenticatedClientFromEnv(ctx)
		}
	}

	mountPath := strings.Trim(tune.Spec.MountPath, "/")
	openbaoClient, err := r.OpenBao(ctx)
	if err != nil {
		authStatus := openBaoAuthFailureStatus(err)
		return r.updateMountTuneErrorStatus(ctx, &tune, mountTuneStatusInput{
			authenticated: metav1.ConditionFalse,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        authStatus.reason,
			message:       authStatus.message,
			lastError:     err.Error(),
		}, err)
	}

	mount, found, err := openbaoClient.GetMount(ctx, mountPath)
	if err != nil {
		return r.updateMountTuneErrorStatus(ctx, &tune, mountTuneStatusInput{
			authenticated: metav1.ConditionTrue,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        "ReadFailed",
			message:       fmt.Sprintf("Failed to read OpenBao mount %q before tuning.", mountPath),
			lastError:     err.Error(),
		}, err)
	}
	if !found {
		err := fmt.Errorf("OpenBao mount %q does not exist", mountPath)
		if !r.Mode.AllowsWrites() {
			return r.updateMountTuneStatus(ctx, &tune, mountTuneStatusInput{
				authenticated:   metav1.ConditionTrue,
				applied:         metav1.ConditionFalse,
				drift:           metav1.ConditionTrue,
				ready:           metav1.ConditionFalse,
				reason:          "DriftDetected",
				message:         err.Error(),
				lastAppliedHash: specHash(tune.Spec),
			})
		}
		return r.updateMountTuneErrorStatus(ctx, &tune, mountTuneStatusInput{
			authenticated: metav1.ConditionTrue,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        "MountNotFound",
			message:       err.Error(),
			lastError:     err.Error(),
		}, err)
	}

	desired := desiredTuneConfig(mount.Description, &tune.Spec.Tune)
	current, tuneFound, err := openbaoClient.GetMountTune(ctx, mountPath)
	if err != nil {
		return r.updateMountTuneErrorStatus(ctx, &tune, mountTuneStatusInput{
			authenticated: metav1.ConditionTrue,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        "ReadFailed",
			message:       fmt.Sprintf("Failed to read OpenBao mount tune for %q.", mountPath),
			lastError:     err.Error(),
		}, err)
	}
	if !tuneFound {
		current = bao.TuneConfig{}
	}
	current.Description = mount.Description
	if !tuneConfigEqual(current, desired, &tune.Spec.Tune) {
		if !r.Mode.AllowsWrites() {
			return r.updateMountTuneStatus(ctx, &tune, mountTuneStatusInput{
				authenticated:   metav1.ConditionTrue,
				applied:         metav1.ConditionFalse,
				drift:           metav1.ConditionTrue,
				ready:           metav1.ConditionFalse,
				reason:          "DriftDetected",
				message:         fmt.Sprintf("OpenBao mount tune for %q differs from desired state; observe mode left it unchanged.", mountPath),
				lastAppliedHash: specHash(tune.Spec),
			})
		}
		if err := openbaoClient.PutMountTune(ctx, mountPath, desired); err != nil {
			return r.updateMountTuneErrorStatus(ctx, &tune, mountTuneStatusInput{
				authenticated: metav1.ConditionTrue,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionTrue,
				ready:         metav1.ConditionFalse,
				reason:        "ApplyFailed",
				message:       fmt.Sprintf("Failed to tune OpenBao mount %q.", mountPath),
				lastError:     err.Error(),
			}, err)
		}
	}

	return r.updateMountTuneStatus(ctx, &tune, mountTuneStatusInput{
		authenticated:   metav1.ConditionTrue,
		applied:         metav1.ConditionTrue,
		drift:           metav1.ConditionFalse,
		ready:           metav1.ConditionTrue,
		reason:          appliedReason(r.Mode),
		message:         fmt.Sprintf("OpenBao mount tune for %q is applied.", mountPath),
		lastAppliedHash: specHash(tune.Spec),
	})
}

func (r *MountTuneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&openbaov1alpha1.OpenBaoMountTune{}).
		Complete(r)
}

type mountTuneStatusInput struct {
	authenticated   metav1.ConditionStatus
	applied         metav1.ConditionStatus
	drift           metav1.ConditionStatus
	ready           metav1.ConditionStatus
	reason          string
	message         string
	lastAppliedHash string
	lastError       string
}

func (r *MountTuneReconciler) updateMountTuneStatus(ctx context.Context, tune *openbaov1alpha1.OpenBaoMountTune, input mountTuneStatusInput) (ctrl.Result, error) {
	status := &tune.Status
	status.ObservedGeneration = tune.Generation
	if input.lastAppliedHash != "" {
		status.LastAppliedHash = input.lastAppliedHash
	}
	status.LastError = input.lastError
	setCondition(status, openbaov1alpha1.ConditionAuthenticated, input.authenticated, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionApplied, input.applied, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionDriftDetected, input.drift, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionReady, input.ready, input.reason, input.message)
	return ctrl.Result{}, r.Status().Update(ctx, tune)
}

func (r *MountTuneReconciler) updateMountTuneErrorStatus(ctx context.Context, tune *openbaov1alpha1.OpenBaoMountTune, input mountTuneStatusInput, cause error) (ctrl.Result, error) {
	if _, err := r.updateMountTuneStatus(ctx, tune, input); err != nil {
		return ctrl.Result{}, err
	}
	if input.reason == reasonSelfInitIncomplete {
		return ctrl.Result{RequeueAfter: selfInitIncompleteRequeueAfter}, nil
	}
	return ctrl.Result{}, cause
}
