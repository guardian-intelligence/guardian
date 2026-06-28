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

func TestAuthBackendReconcilerAppliesMissingKubernetesBackend(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoAuthBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoAuthBackendSpec{
			Path:        "kubernetes",
			Type:        "kubernetes",
			Description: "Kubernetes service account auth for guardian-mgmt workloads.",
			Kubernetes: &openbaov1alpha1.OpenBaoKubernetesAuthConfig{
				KubernetesHost:       "https://kubernetes.default.svc:443",
				DisableISSValidation: true,
			},
			Tune: &openbaov1alpha1.OpenBaoTuneSpec{
				DefaultLeaseTTL:   "1h",
				MaxLeaseTTL:       "2h",
				ListingVisibility: "hidden",
			},
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoAuthBackend{}).
		WithObjects(obj).
		Build()
	openbao := &fakeAuthBackendClient{
		backends: map[string]bao.AuthBackend{},
		configs:  map[string]bao.KubernetesAuthConfig{},
		tunes:    map[string]bao.TuneConfig{},
	}
	reconciler := &AuthBackendReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (AuthBackendClient, error) {
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
	if openbao.configPuts != 1 {
		t.Fatalf("configPuts = %d, want 1", openbao.configPuts)
	}
	if openbao.tunePuts != 1 {
		t.Fatalf("tunePuts = %d, want 1", openbao.tunePuts)
	}
	if got := openbao.backends["kubernetes"].Description; got != obj.Spec.Description {
		t.Fatalf("backend description = %q, want %q", got, obj.Spec.Description)
	}
	if got := openbao.configs["kubernetes"].KubernetesHost; got != "https://kubernetes.default.svc:443" {
		t.Fatalf("kubernetes host = %q", got)
	}
	if got := openbao.tunes["kubernetes"].DefaultLeaseTTL; got != "1h" {
		t.Fatalf("default lease ttl = %q, want 1h", got)
	}

	var got openbaov1alpha1.OpenBaoAuthBackend
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled auth backend: %v", err)
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

func TestAuthBackendReconcilerSkipsMatchingBackend(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoAuthBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoAuthBackendSpec{
			Path:        "kubernetes",
			Type:        "kubernetes",
			Description: "Kubernetes service account auth.",
			Kubernetes: &openbaov1alpha1.OpenBaoKubernetesAuthConfig{
				KubernetesHost:       "https://kubernetes.default.svc:443",
				DisableISSValidation: true,
			},
			Tune: &openbaov1alpha1.OpenBaoTuneSpec{
				DefaultLeaseTTL: "1h",
				MaxLeaseTTL:     "2h",
			},
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoAuthBackend{}).
		WithObjects(obj).
		Build()
	openbao := &fakeAuthBackendClient{
		backends: map[string]bao.AuthBackend{
			"kubernetes": {Path: "kubernetes", Type: "kubernetes", Description: "Kubernetes service account auth."},
		},
		configs: map[string]bao.KubernetesAuthConfig{
			"kubernetes": desiredKubernetesAuthConfig(obj.Spec.Kubernetes),
		},
		tunes: map[string]bao.TuneConfig{
			"kubernetes": {Description: "Kubernetes service account auth.", DefaultLeaseTTL: "3600", MaxLeaseTTL: "7200"},
		},
	}
	reconciler := &AuthBackendReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (AuthBackendClient, error) {
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
	if openbao.configPuts != 0 {
		t.Fatalf("configPuts = %d, want 0", openbao.configPuts)
	}
	if openbao.tunePuts != 0 {
		t.Fatalf("tunePuts = %d, want 0", openbao.tunePuts)
	}
}

func TestAuthBackendReconcilerReportsTypeMismatch(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoAuthBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoAuthBackendSpec{
			Path: "kubernetes",
			Type: "kubernetes",
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoAuthBackend{}).
		WithObjects(obj).
		Build()
	openbao := &fakeAuthBackendClient{
		backends: map[string]bao.AuthBackend{
			"kubernetes": {Path: "kubernetes", Type: "userpass"},
		},
		configs: map[string]bao.KubernetesAuthConfig{},
		tunes:   map[string]bao.TuneConfig{},
	}
	reconciler := &AuthBackendReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (AuthBackendClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err == nil {
		t.Fatalf("Reconcile() error = nil, want type mismatch")
	}

	var got openbaov1alpha1.OpenBaoAuthBackend
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled auth backend: %v", err)
	}
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionReady, metav1.ConditionFalse)
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionApplied, metav1.ConditionFalse)
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionDriftDetected, metav1.ConditionTrue)
	if got.Status.LastError == "" {
		t.Fatalf("LastError is empty")
	}
}

func TestAuthBackendReconcilerAddsFinalizerForDeletePolicy(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoAuthBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "delete-me", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoAuthBackendSpec{
			Path:           "delete-me",
			Type:           "kubernetes",
			DeletionPolicy: openbaov1alpha1.DeletionPolicyDelete,
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoAuthBackend{}).
		WithObjects(obj).
		Build()
	reconciler := &AuthBackendReconciler{Client: kube, Scheme: scheme}

	result, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !result.Requeue {
		t.Fatalf("Reconcile() requeue = false, want true")
	}
	var got openbaov1alpha1.OpenBaoAuthBackend
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled auth backend: %v", err)
	}
	if len(got.Finalizers) != 1 || got.Finalizers[0] != authBackendFinalizer {
		t.Fatalf("finalizers = %#v, want %s", got.Finalizers, authBackendFinalizer)
	}
}

func TestAuthBackendReconcilerDeletePolicyDisablesBackend(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoAuthBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "delete-me", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoAuthBackendSpec{
			Path:           "delete-me",
			Type:           "kubernetes",
			DeletionPolicy: openbaov1alpha1.DeletionPolicyDelete,
		},
	}
	controllerutil.AddFinalizer(obj, authBackendFinalizer)
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoAuthBackend{}).
		WithObjects(obj).
		Build()
	openbao := &fakeAuthBackendClient{
		backends: map[string]bao.AuthBackend{
			"delete-me": {Path: "delete-me", Type: "kubernetes"},
		},
		configs: map[string]bao.KubernetesAuthConfig{},
		tunes:   map[string]bao.TuneConfig{},
	}
	reconciler := &AuthBackendReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (AuthBackendClient, error) {
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
	if _, ok := openbao.backends["delete-me"]; ok {
		t.Fatalf("delete-me backend still exists")
	}
}

type fakeAuthBackendClient struct {
	backends   map[string]bao.AuthBackend
	configs    map[string]bao.KubernetesAuthConfig
	tunes      map[string]bao.TuneConfig
	enables    int
	disables   int
	configPuts int
	tunePuts   int
}

func (f *fakeAuthBackendClient) GetAuthBackend(_ context.Context, path string) (bao.AuthBackend, bool, error) {
	backend, ok := f.backends[path]
	return backend, ok, nil
}

func (f *fakeAuthBackendClient) EnableAuthBackend(_ context.Context, backend bao.AuthBackend) error {
	f.enables++
	f.backends[backend.Path] = backend
	return nil
}

func (f *fakeAuthBackendClient) DisableAuthBackend(_ context.Context, path string) error {
	f.disables++
	delete(f.backends, path)
	return nil
}

func (f *fakeAuthBackendClient) GetKubernetesAuthConfig(_ context.Context, backendPath string) (bao.KubernetesAuthConfig, bool, error) {
	config, ok := f.configs[backendPath]
	return config, ok, nil
}

func (f *fakeAuthBackendClient) PutKubernetesAuthConfig(_ context.Context, backendPath string, config bao.KubernetesAuthConfig) error {
	f.configPuts++
	f.configs[backendPath] = config
	return nil
}

func (f *fakeAuthBackendClient) GetAuthTune(_ context.Context, backendPath string) (bao.TuneConfig, bool, error) {
	tune, ok := f.tunes[backendPath]
	return tune, ok, nil
}

func (f *fakeAuthBackendClient) PutAuthTune(_ context.Context, backendPath string, tune bao.TuneConfig) error {
	f.tunePuts++
	f.tunes[backendPath] = tune
	return nil
}
