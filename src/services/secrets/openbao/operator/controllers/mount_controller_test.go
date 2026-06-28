package controllers

import (
	"context"
	"testing"

	openbaov1alpha1 "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/api/v1alpha1"
	bao "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/operator/openbao"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestMountReconcilerAppliesMissingMount(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoMount{
		ObjectMeta: metav1.ObjectMeta{Name: "kv", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoMountSpec{
			Path:        "kv",
			Type:        "kv-v2",
			Description: "Guardian management cluster secret material.",
			Tune: &openbaov1alpha1.OpenBaoTuneSpec{
				DefaultLeaseTTL: "1h",
				MaxLeaseTTL:     "2h",
			},
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoMount{}).
		WithObjects(obj).
		Build()
	openbao := &fakeMountClient{
		mounts: map[string]bao.Mount{},
		tunes:  map[string]bao.TuneConfig{},
	}
	reconciler := &MountReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (MountClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if openbao.enables != 1 {
		t.Fatalf("enables = %d, want 1", openbao.enables)
	}
	if openbao.tunePuts != 1 {
		t.Fatalf("tunePuts = %d, want 1", openbao.tunePuts)
	}
	if got := openbao.mounts["kv"].Type; got != "kv" {
		t.Fatalf("mount type = %q, want kv", got)
	}
	if got := openbao.mounts["kv"].Options["version"]; got != "2" {
		t.Fatalf("mount option version = %q, want 2", got)
	}
	if got := openbao.tunes["kv"].DefaultLeaseTTL; got != "1h" {
		t.Fatalf("default lease ttl = %q, want 1h", got)
	}

	var got openbaov1alpha1.OpenBaoMount
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled mount: %v", err)
	}
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionReady, metav1.ConditionTrue)
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionAuthenticated, metav1.ConditionTrue)
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionApplied, metav1.ConditionTrue)
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionDriftDetected, metav1.ConditionFalse)
	if got.Status.LastAppliedHash == "" {
		t.Fatalf("LastAppliedHash is empty")
	}
	if got.Status.LastError != "" {
		t.Fatalf("LastError = %q, want empty", got.Status.LastError)
	}
}

func TestMountReconcilerSkipsMatchingMount(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoMount{
		ObjectMeta: metav1.ObjectMeta{Name: "kv", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoMountSpec{
			Path:        "kv",
			Type:        "kv-v2",
			Description: "Guardian management cluster secret material.",
			Tune: &openbaov1alpha1.OpenBaoTuneSpec{
				DefaultLeaseTTL: "1h",
				MaxLeaseTTL:     "2h",
			},
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoMount{}).
		WithObjects(obj).
		Build()
	openbao := &fakeMountClient{
		mounts: map[string]bao.Mount{
			"kv": desiredMount(obj),
		},
		tunes: map[string]bao.TuneConfig{
			"kv": {Description: obj.Spec.Description, DefaultLeaseTTL: "3600", MaxLeaseTTL: "7200"},
		},
	}
	reconciler := &MountReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (MountClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if openbao.enables != 0 {
		t.Fatalf("enables = %d, want 0", openbao.enables)
	}
	if openbao.tunePuts != 0 {
		t.Fatalf("tunePuts = %d, want 0", openbao.tunePuts)
	}
}

func TestMountReconcilerReportsOptionsMismatch(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoMount{
		ObjectMeta: metav1.ObjectMeta{Name: "kv", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoMountSpec{
			Path: "kv",
			Type: "kv-v2",
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoMount{}).
		WithObjects(obj).
		Build()
	openbao := &fakeMountClient{
		mounts: map[string]bao.Mount{
			"kv": {Path: "kv", Type: "kv", Options: map[string]string{"version": "1"}},
		},
		tunes: map[string]bao.TuneConfig{},
	}
	reconciler := &MountReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (MountClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err == nil {
		t.Fatalf("Reconcile() error = nil, want options mismatch")
	}
	if openbao.enables != 0 {
		t.Fatalf("enables = %d, want 0", openbao.enables)
	}

	var got openbaov1alpha1.OpenBaoMount
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled mount: %v", err)
	}
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionReady, metav1.ConditionFalse)
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionApplied, metav1.ConditionFalse)
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionDriftDetected, metav1.ConditionTrue)
	if got.Status.LastError == "" {
		t.Fatalf("LastError is empty")
	}
}

func TestMountReconcilerAddsFinalizerForDeletePolicy(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoMount{
		ObjectMeta: metav1.ObjectMeta{Name: "delete-me", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoMountSpec{
			Path:           "delete-me",
			Type:           "kv-v2",
			DeletionPolicy: openbaov1alpha1.DeletionPolicyDelete,
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoMount{}).
		WithObjects(obj).
		Build()
	reconciler := &MountReconciler{Client: kube, Scheme: scheme}

	result, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !result.Requeue {
		t.Fatalf("Reconcile() requeue = false, want true")
	}
	var got openbaov1alpha1.OpenBaoMount
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled mount: %v", err)
	}
	if len(got.Finalizers) != 1 || got.Finalizers[0] != mountFinalizer {
		t.Fatalf("finalizers = %#v, want %s", got.Finalizers, mountFinalizer)
	}
}

func TestMountReconcilerDeletePolicyDisablesMount(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoMount{
		ObjectMeta: metav1.ObjectMeta{Name: "delete-me", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoMountSpec{
			Path:           "delete-me",
			Type:           "kv-v2",
			DeletionPolicy: openbaov1alpha1.DeletionPolicyDelete,
		},
	}
	controllerutil.AddFinalizer(obj, mountFinalizer)
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoMount{}).
		WithObjects(obj).
		Build()
	openbao := &fakeMountClient{
		mounts: map[string]bao.Mount{
			"delete-me": {Path: "delete-me", Type: "kv"},
		},
		tunes: map[string]bao.TuneConfig{},
	}
	reconciler := &MountReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (MountClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.reconcileDelete(ctx, obj)
	if err != nil {
		t.Fatalf("reconcileDelete() error = %v", err)
	}
	if openbao.disables != 1 {
		t.Fatalf("disables = %d, want 1", openbao.disables)
	}
	if _, ok := openbao.mounts["delete-me"]; ok {
		t.Fatalf("delete-me mount still exists")
	}
}

type fakeMountClient struct {
	mounts   map[string]bao.Mount
	tunes    map[string]bao.TuneConfig
	enables  int
	disables int
	tunePuts int
}

func (f *fakeMountClient) GetMount(_ context.Context, path string) (bao.Mount, bool, error) {
	mount, ok := f.mounts[path]
	return mount, ok, nil
}

func (f *fakeMountClient) EnableMount(_ context.Context, mount bao.Mount) error {
	f.enables++
	f.mounts[mount.Path] = mount
	return nil
}

func (f *fakeMountClient) DisableMount(_ context.Context, path string) error {
	f.disables++
	delete(f.mounts, path)
	return nil
}

func (f *fakeMountClient) GetMountTune(_ context.Context, mountPath string) (bao.TuneConfig, bool, error) {
	tune, ok := f.tunes[mountPath]
	return tune, ok, nil
}

func (f *fakeMountClient) PutMountTune(_ context.Context, mountPath string, tune bao.TuneConfig) error {
	f.tunePuts++
	f.tunes[mountPath] = tune
	return nil
}
