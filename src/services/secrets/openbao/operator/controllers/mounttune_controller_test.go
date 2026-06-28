package controllers

import (
	"context"
	"testing"

	openbaov1alpha1 "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/api/v1alpha1"
	bao "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/operator/openbao"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMountTuneReconcilerAppliesDriftedTune(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoMountTune{
		ObjectMeta: metav1.ObjectMeta{Name: "kv", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoMountTuneSpec{
			MountPath: "kv/",
			Tune: openbaov1alpha1.OpenBaoTuneSpec{
				DefaultLeaseTTL:           "1h",
				MaxLeaseTTL:               "2h",
				ListingVisibility:         "hidden",
				PassthroughRequestHeaders: []string{"X-Guardian-Request"},
			},
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoMountTune{}).
		WithObjects(obj).
		Build()
	openbao := &fakeMountClient{
		mounts: map[string]bao.Mount{
			"kv": {Path: "kv", Type: "kv", Description: "Guardian management cluster secret material."},
		},
		tunes: map[string]bao.TuneConfig{
			"kv": {Description: "Guardian management cluster secret material.", DefaultLeaseTTL: "1800"},
		},
	}
	reconciler := &MountTuneReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (MountTuneClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if openbao.tunePuts != 1 {
		t.Fatalf("tunePuts = %d, want 1", openbao.tunePuts)
	}
	if got := openbao.tunes["kv"].Description; got != "Guardian management cluster secret material." {
		t.Fatalf("description = %q, want mount description preserved", got)
	}
	if got := openbao.tunes["kv"].DefaultLeaseTTL; got != "1h" {
		t.Fatalf("default lease ttl = %q, want 1h", got)
	}

	var got openbaov1alpha1.OpenBaoMountTune
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled mount tune: %v", err)
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

func TestMountTuneReconcilerSkipsMatchingTune(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoMountTune{
		ObjectMeta: metav1.ObjectMeta{Name: "kv", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoMountTuneSpec{
			MountPath: "kv",
			Tune: openbaov1alpha1.OpenBaoTuneSpec{
				DefaultLeaseTTL: "1h",
				MaxLeaseTTL:     "2h",
			},
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoMountTune{}).
		WithObjects(obj).
		Build()
	openbao := &fakeMountClient{
		mounts: map[string]bao.Mount{
			"kv": {Path: "kv", Type: "kv", Description: "Guardian management cluster secret material."},
		},
		tunes: map[string]bao.TuneConfig{
			"kv": {Description: "Guardian management cluster secret material.", DefaultLeaseTTL: "3600", MaxLeaseTTL: "7200"},
		},
	}
	reconciler := &MountTuneReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (MountTuneClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if openbao.tunePuts != 0 {
		t.Fatalf("tunePuts = %d, want 0", openbao.tunePuts)
	}
}

func TestMountTuneReconcilerObserveModeReportsDriftWithoutApplying(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoMountTune{
		ObjectMeta: metav1.ObjectMeta{Name: "kv", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoMountTuneSpec{
			MountPath: "kv",
			Tune: openbaov1alpha1.OpenBaoTuneSpec{
				DefaultLeaseTTL: "1h",
				MaxLeaseTTL:     "2h",
			},
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoMountTune{}).
		WithObjects(obj).
		Build()
	openbao := &fakeMountClient{
		mounts: map[string]bao.Mount{
			"kv": {Path: "kv", Type: "kv", Description: "Guardian management cluster secret material."},
		},
		tunes: map[string]bao.TuneConfig{
			"kv": {Description: "Guardian management cluster secret material.", DefaultLeaseTTL: "1800"},
		},
	}
	reconciler := &MountTuneReconciler{
		Client: kube,
		Scheme: scheme,
		Mode:   ReconcileModeObserve,
		OpenBao: func(context.Context) (MountTuneClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if openbao.tunePuts != 0 {
		t.Fatalf("tunePuts = %d, want 0", openbao.tunePuts)
	}
	if got := openbao.tunes["kv"].DefaultLeaseTTL; got != "1800" {
		t.Fatalf("default lease ttl = %q, want unchanged old ttl", got)
	}

	var got openbaov1alpha1.OpenBaoMountTune
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled mount tune: %v", err)
	}
	assertObservedDriftStatus(t, got.Status)
}

func TestMountTuneReconcilerReportsMissingMount(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoMountTune{
		ObjectMeta: metav1.ObjectMeta{Name: "missing", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoMountTuneSpec{
			MountPath: "missing",
			Tune: openbaov1alpha1.OpenBaoTuneSpec{
				DefaultLeaseTTL: "1h",
			},
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoMountTune{}).
		WithObjects(obj).
		Build()
	openbao := &fakeMountClient{
		mounts: map[string]bao.Mount{},
		tunes:  map[string]bao.TuneConfig{},
	}
	reconciler := &MountTuneReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (MountTuneClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err == nil {
		t.Fatalf("Reconcile() error = nil, want missing mount")
	}

	var got openbaov1alpha1.OpenBaoMountTune
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled mount tune: %v", err)
	}
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionReady, metav1.ConditionFalse)
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionApplied, metav1.ConditionFalse)
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionDriftDetected, metav1.ConditionUnknown)
	if got.Status.LastError == "" {
		t.Fatalf("LastError is empty")
	}
}
