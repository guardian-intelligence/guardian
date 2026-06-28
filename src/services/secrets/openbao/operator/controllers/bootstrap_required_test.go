package controllers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	openbaov1alpha1 "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var errMissingOpsControllerRole = errors.New(`login to OpenBao with Kubernetes auth: invalid role name "guardian-openbao-ops-controller"`)

func TestKubernetesAuthRoleReconcilerReportsBootstrapRequiredWithoutError(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoKubernetesAuthRole{
		ObjectMeta: metav1.ObjectMeta{Name: "ops-controller", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoKubernetesAuthRoleSpec{
			BackendPath:                   "kubernetes",
			RoleName:                      "guardian-openbao-ops-controller",
			BoundServiceAccountNames:      []string{"openbao-ops-controller"},
			BoundServiceAccountNamespaces: []string{"tenant-guardian"},
			TokenPolicies:                 []string{"guardian-openbao-ops-controller"},
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoKubernetesAuthRole{}).
		WithObjects(obj).
		Build()
	reconciler := &KubernetesAuthRoleReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (KubernetesAuthRoleClient, error) {
			return nil, errMissingOpsControllerRole
		},
	}

	result, err := reconciler.Reconcile(ctx, requestFor(obj))
	assertBootstrapRequiredResult(t, result.RequeueAfter, err)

	var got openbaov1alpha1.OpenBaoKubernetesAuthRole
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled auth role: %v", err)
	}
	assertBootstrapRequiredStatus(t, got.Status)
	assertBootstrapRequiredLastError(t, got.Status.LastError)
}

func TestAuthBackendReconcilerReportsBootstrapRequiredWithoutError(t *testing.T) {
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
	reconciler := &AuthBackendReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (AuthBackendClient, error) {
			return nil, errMissingOpsControllerRole
		},
	}

	result, err := reconciler.Reconcile(ctx, requestFor(obj))
	assertBootstrapRequiredResult(t, result.RequeueAfter, err)

	var got openbaov1alpha1.OpenBaoAuthBackend
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled auth backend: %v", err)
	}
	assertBootstrapRequiredStatus(t, got.Status)
	assertBootstrapRequiredLastError(t, got.Status.LastError)
}

func TestMountReconcilerReportsBootstrapRequiredWithoutError(t *testing.T) {
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
	reconciler := &MountReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (MountClient, error) {
			return nil, errMissingOpsControllerRole
		},
	}

	result, err := reconciler.Reconcile(ctx, requestFor(obj))
	assertBootstrapRequiredResult(t, result.RequeueAfter, err)

	var got openbaov1alpha1.OpenBaoMount
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled mount: %v", err)
	}
	assertBootstrapRequiredStatus(t, got.Status)
	assertBootstrapRequiredLastError(t, got.Status.LastError)
}

func TestMountTuneReconcilerReportsBootstrapRequiredWithoutError(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoMountTune{
		ObjectMeta: metav1.ObjectMeta{Name: "kv", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoMountTuneSpec{
			MountPath: "kv",
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
	reconciler := &MountTuneReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (MountTuneClient, error) {
			return nil, errMissingOpsControllerRole
		},
	}

	result, err := reconciler.Reconcile(ctx, requestFor(obj))
	assertBootstrapRequiredResult(t, result.RequeueAfter, err)

	var got openbaov1alpha1.OpenBaoMountTune
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled mount tune: %v", err)
	}
	assertBootstrapRequiredStatus(t, got.Status)
	assertBootstrapRequiredLastError(t, got.Status.LastError)
}

func assertBootstrapRequiredResult(t *testing.T, requeueAfter time.Duration, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if requeueAfter != bootstrapRequiredRequeueAfter {
		t.Fatalf("RequeueAfter = %s, want %s", requeueAfter, bootstrapRequiredRequeueAfter)
	}
}

func assertBootstrapRequiredLastError(t *testing.T, lastError string) {
	t.Helper()
	if !strings.Contains(lastError, `invalid role name "guardian-openbao-ops-controller"`) {
		t.Fatalf("LastError = %q, want missing role detail", lastError)
	}
}
