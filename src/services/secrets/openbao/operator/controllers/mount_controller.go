package controllers

import (
	"context"
	"fmt"
	"maps"
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

const mountFinalizer = "openbao.guardian.dev/mount"

type MountClient interface {
	GetMount(ctx context.Context, path string) (bao.Mount, bool, error)
	EnableMount(ctx context.Context, mount bao.Mount) error
	DisableMount(ctx context.Context, path string) error
	GetMountTune(ctx context.Context, mountPath string) (bao.TuneConfig, bool, error)
	PutMountTune(ctx context.Context, mountPath string, tune bao.TuneConfig) error
}

type MountReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	OpenBao func(context.Context) (MountClient, error)
}

func (r *MountReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var mount openbaov1alpha1.OpenBaoMount
	if err := r.Get(ctx, req.NamespacedName, &mount); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if r.OpenBao == nil {
		r.OpenBao = func(ctx context.Context) (MountClient, error) {
			return bao.NewAuthenticatedClientFromEnv(ctx)
		}
	}

	if !mount.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &mount)
	}

	if mount.Spec.DeletionPolicy == openbaov1alpha1.DeletionPolicyDelete && !controllerutil.ContainsFinalizer(&mount, mountFinalizer) {
		controllerutil.AddFinalizer(&mount, mountFinalizer)
		if err := r.Update(ctx, &mount); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if mount.Spec.DeletionPolicy != openbaov1alpha1.DeletionPolicyDelete && controllerutil.ContainsFinalizer(&mount, mountFinalizer) {
		controllerutil.RemoveFinalizer(&mount, mountFinalizer)
		if err := r.Update(ctx, &mount); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	desired := desiredMount(&mount)
	openbaoClient, err := r.OpenBao(ctx)
	if err != nil {
		return r.updateMountErrorStatus(ctx, &mount, mountStatusInput{
			authenticated: metav1.ConditionFalse,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        "AuthenticationFailed",
			message:       "OpenBao Kubernetes auth login failed.",
			lastError:     err.Error(),
		}, err)
	}

	current, found, err := openbaoClient.GetMount(ctx, desired.Path)
	if err != nil {
		return r.updateMountErrorStatus(ctx, &mount, mountStatusInput{
			authenticated: metav1.ConditionTrue,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionUnknown,
			ready:         metav1.ConditionFalse,
			reason:        "ReadFailed",
			message:       fmt.Sprintf("Failed to read OpenBao mount %q.", desired.Path),
			lastError:     err.Error(),
		}, err)
	}

	if found && current.Type != desired.Type {
		err := fmt.Errorf("OpenBao mount %q has type %q, want %q", desired.Path, current.Type, desired.Type)
		return r.updateMountErrorStatus(ctx, &mount, mountStatusInput{
			authenticated: metav1.ConditionTrue,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionTrue,
			ready:         metav1.ConditionFalse,
			reason:        "TypeMismatch",
			message:       err.Error(),
			lastError:     err.Error(),
		}, err)
	}
	if found && !maps.Equal(current.Options, desired.Options) {
		err := fmt.Errorf("OpenBao mount %q has options %#v, want %#v", desired.Path, current.Options, desired.Options)
		return r.updateMountErrorStatus(ctx, &mount, mountStatusInput{
			authenticated: metav1.ConditionTrue,
			applied:       metav1.ConditionFalse,
			drift:         metav1.ConditionTrue,
			ready:         metav1.ConditionFalse,
			reason:        "OptionsMismatch",
			message:       err.Error(),
			lastError:     err.Error(),
		}, err)
	}

	if !found {
		if err := openbaoClient.EnableMount(ctx, desired); err != nil {
			return r.updateMountErrorStatus(ctx, &mount, mountStatusInput{
				authenticated: metav1.ConditionTrue,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionTrue,
				ready:         metav1.ConditionFalse,
				reason:        "ApplyFailed",
				message:       fmt.Sprintf("Failed to enable OpenBao mount %q.", desired.Path),
				lastError:     err.Error(),
			}, err)
		}
		current = desired
	}

	desiredTune := desiredTuneConfig(desired.Description, mount.Spec.Tune)
	if mount.Spec.Tune != nil || current.Description != desired.Description {
		currentTune, tuneFound, err := openbaoClient.GetMountTune(ctx, desired.Path)
		if err != nil {
			return r.updateMountErrorStatus(ctx, &mount, mountStatusInput{
				authenticated: metav1.ConditionTrue,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionUnknown,
				ready:         metav1.ConditionFalse,
				reason:        "ReadFailed",
				message:       fmt.Sprintf("Failed to read OpenBao mount tune for %q.", desired.Path),
				lastError:     err.Error(),
			}, err)
		}
		if !tuneFound {
			currentTune = bao.TuneConfig{}
		}
		currentTune.Description = current.Description
		if !tuneConfigEqual(currentTune, desiredTune, mount.Spec.Tune) {
			if err := openbaoClient.PutMountTune(ctx, desired.Path, desiredTune); err != nil {
				return r.updateMountErrorStatus(ctx, &mount, mountStatusInput{
					authenticated: metav1.ConditionTrue,
					applied:       metav1.ConditionFalse,
					drift:         metav1.ConditionTrue,
					ready:         metav1.ConditionFalse,
					reason:        "ApplyFailed",
					message:       fmt.Sprintf("Failed to tune OpenBao mount %q.", desired.Path),
					lastError:     err.Error(),
				}, err)
			}
		}
	}

	return r.updateMountStatus(ctx, &mount, mountStatusInput{
		authenticated:   metav1.ConditionTrue,
		applied:         metav1.ConditionTrue,
		drift:           metav1.ConditionFalse,
		ready:           metav1.ConditionTrue,
		reason:          "Applied",
		message:         fmt.Sprintf("OpenBao mount %q is applied.", desired.Path),
		lastAppliedHash: specHash(mount.Spec),
	})
}

func (r *MountReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&openbaov1alpha1.OpenBaoMount{}).
		Complete(r)
}

func (r *MountReconciler) reconcileDelete(ctx context.Context, mount *openbaov1alpha1.OpenBaoMount) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(mount, mountFinalizer) {
		return ctrl.Result{}, nil
	}
	if mount.Spec.DeletionPolicy == openbaov1alpha1.DeletionPolicyDelete {
		openbaoClient, err := r.OpenBao(ctx)
		if err != nil {
			return r.updateMountErrorStatus(ctx, mount, mountStatusInput{
				authenticated: metav1.ConditionFalse,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionUnknown,
				ready:         metav1.ConditionFalse,
				reason:        "AuthenticationFailed",
				message:       "OpenBao Kubernetes auth login failed while deleting mount.",
				lastError:     err.Error(),
			}, err)
		}
		if err := openbaoClient.DisableMount(ctx, strings.Trim(mount.Spec.Path, "/")); err != nil {
			return r.updateMountErrorStatus(ctx, mount, mountStatusInput{
				authenticated: metav1.ConditionTrue,
				applied:       metav1.ConditionFalse,
				drift:         metav1.ConditionUnknown,
				ready:         metav1.ConditionFalse,
				reason:        "DeleteFailed",
				message:       fmt.Sprintf("Failed to disable OpenBao mount %q.", mount.Spec.Path),
				lastError:     err.Error(),
			}, err)
		}
	}
	controllerutil.RemoveFinalizer(mount, mountFinalizer)
	return ctrl.Result{}, r.Update(ctx, mount)
}

type mountStatusInput struct {
	authenticated   metav1.ConditionStatus
	applied         metav1.ConditionStatus
	drift           metav1.ConditionStatus
	ready           metav1.ConditionStatus
	reason          string
	message         string
	lastAppliedHash string
	lastError       string
}

func (r *MountReconciler) updateMountStatus(ctx context.Context, mount *openbaov1alpha1.OpenBaoMount, input mountStatusInput) (ctrl.Result, error) {
	status := &mount.Status
	status.ObservedGeneration = mount.Generation
	if input.lastAppliedHash != "" {
		status.LastAppliedHash = input.lastAppliedHash
	}
	status.LastError = input.lastError
	setCondition(status, openbaov1alpha1.ConditionAuthenticated, input.authenticated, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionApplied, input.applied, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionDriftDetected, input.drift, input.reason, input.message)
	setCondition(status, openbaov1alpha1.ConditionReady, input.ready, input.reason, input.message)
	return ctrl.Result{}, r.Status().Update(ctx, mount)
}

func (r *MountReconciler) updateMountErrorStatus(ctx context.Context, mount *openbaov1alpha1.OpenBaoMount, input mountStatusInput, cause error) (ctrl.Result, error) {
	if _, err := r.updateMountStatus(ctx, mount, input); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, cause
}

func desiredMount(mount *openbaov1alpha1.OpenBaoMount) bao.Mount {
	mountType := mount.Spec.Type
	options := maps.Clone(mount.Spec.Options)
	if mountType == "kv-v2" {
		mountType = "kv"
		if options == nil {
			options = map[string]string{}
		}
		options["version"] = "2"
	}
	return bao.Mount{
		Path:        strings.Trim(mount.Spec.Path, "/"),
		Type:        mountType,
		Description: mount.Spec.Description,
		Options:     options,
	}
}
