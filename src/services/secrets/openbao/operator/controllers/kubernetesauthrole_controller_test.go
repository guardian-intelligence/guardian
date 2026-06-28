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

func TestKubernetesAuthRoleReconcilerAppliesMissingRole(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoKubernetesAuthRole{
		ObjectMeta: metav1.ObjectMeta{Name: "external-dns", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoKubernetesAuthRoleSpec{
			BackendPath:                   "kubernetes",
			RoleName:                      "guardian-external-dns",
			BoundServiceAccountNames:      []string{"external-dns-secrets"},
			BoundServiceAccountNamespaces: []string{"external-dns"},
			Audience:                      "openbao",
			TokenPolicies:                 []string{"guardian-external-dns"},
			TokenTTL:                      "3600",
			TokenMaxTTL:                   "3600",
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoKubernetesAuthRole{}).
		WithObjects(obj).
		Build()
	openbao := &fakeKubernetesAuthRoleClient{roles: map[string]bao.KubernetesAuthRole{}}
	reconciler := &KubernetesAuthRoleReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (KubernetesAuthRoleClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if openbao.puts != 1 {
		t.Fatalf("puts = %d, want 1", openbao.puts)
	}
	gotRole := openbao.roles["kubernetes/guardian-external-dns"]
	if gotRole.RoleName != "guardian-external-dns" {
		t.Fatalf("role name = %q, want guardian-external-dns", gotRole.RoleName)
	}
	if gotRole.BoundServiceAccountNames[0] != "external-dns-secrets" {
		t.Fatalf("bound service account names = %#v", gotRole.BoundServiceAccountNames)
	}

	var got openbaov1alpha1.OpenBaoKubernetesAuthRole
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled auth role: %v", err)
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

func TestKubernetesAuthRoleReconcilerSkipsMatchingRole(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoKubernetesAuthRole{
		ObjectMeta: metav1.ObjectMeta{Name: "ops-controller", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoKubernetesAuthRoleSpec{
			BackendPath:                   "kubernetes",
			RoleName:                      "guardian-openbao-ops-controller",
			BoundServiceAccountNames:      []string{"openbao-ops-controller"},
			BoundServiceAccountNamespaces: []string{"tenant-guardian"},
			Audience:                      "openbao",
			TokenPolicies:                 []string{"guardian-openbao-ops-controller"},
			TokenTTL:                      "900",
			TokenMaxTTL:                   "3600",
		},
	}
	desired := desiredKubernetesAuthRole(obj)
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoKubernetesAuthRole{}).
		WithObjects(obj).
		Build()
	openbao := &fakeKubernetesAuthRoleClient{
		roles: map[string]bao.KubernetesAuthRole{
			"kubernetes/guardian-openbao-ops-controller": desired,
		},
	}
	reconciler := &KubernetesAuthRoleReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (KubernetesAuthRoleClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if openbao.puts != 0 {
		t.Fatalf("puts = %d, want 0", openbao.puts)
	}
}

func TestKubernetesAuthRoleReconcilerObserveModeReportsMissingRoleWithoutApplying(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoKubernetesAuthRole{
		ObjectMeta: metav1.ObjectMeta{Name: "external-dns", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoKubernetesAuthRoleSpec{
			BackendPath:                   "kubernetes",
			RoleName:                      "guardian-external-dns",
			BoundServiceAccountNames:      []string{"external-dns-secrets"},
			BoundServiceAccountNamespaces: []string{"external-dns"},
			Audience:                      "openbao",
			TokenPolicies:                 []string{"guardian-external-dns"},
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoKubernetesAuthRole{}).
		WithObjects(obj).
		Build()
	openbao := &fakeKubernetesAuthRoleClient{roles: map[string]bao.KubernetesAuthRole{}}
	reconciler := &KubernetesAuthRoleReconciler{
		Client: kube,
		Scheme: scheme,
		Mode:   ReconcileModeObserve,
		OpenBao: func(context.Context) (KubernetesAuthRoleClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if openbao.puts != 0 {
		t.Fatalf("puts = %d, want 0", openbao.puts)
	}
	if _, ok := openbao.roles["kubernetes/guardian-external-dns"]; ok {
		t.Fatalf("role was created in observe mode")
	}

	var got openbaov1alpha1.OpenBaoKubernetesAuthRole
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled auth role: %v", err)
	}
	assertObservedDriftStatus(t, got.Status)
}

func TestKubernetesAuthRoleReconcilerAddsFinalizerForDeletePolicy(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoKubernetesAuthRole{
		ObjectMeta: metav1.ObjectMeta{Name: "delete-me", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoKubernetesAuthRoleSpec{
			BackendPath:                   "kubernetes",
			BoundServiceAccountNames:      []string{"delete-me"},
			BoundServiceAccountNamespaces: []string{"tenant-guardian"},
			DeletionPolicy:                openbaov1alpha1.DeletionPolicyDelete,
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoKubernetesAuthRole{}).
		WithObjects(obj).
		Build()
	reconciler := &KubernetesAuthRoleReconciler{Client: kube, Scheme: scheme}

	result, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !result.Requeue {
		t.Fatalf("Reconcile() requeue = false, want true")
	}
	var got openbaov1alpha1.OpenBaoKubernetesAuthRole
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled auth role: %v", err)
	}
	if len(got.Finalizers) != 1 || got.Finalizers[0] != kubernetesAuthRoleFinalizer {
		t.Fatalf("finalizers = %#v, want %s", got.Finalizers, kubernetesAuthRoleFinalizer)
	}
}

type fakeKubernetesAuthRoleClient struct {
	roles   map[string]bao.KubernetesAuthRole
	puts    int
	deletes int
}

func (f *fakeKubernetesAuthRoleClient) GetKubernetesAuthRole(_ context.Context, backendPath string, roleName string) (bao.KubernetesAuthRole, bool, error) {
	role, ok := f.roles[backendPath+"/"+roleName]
	return role, ok, nil
}

func (f *fakeKubernetesAuthRoleClient) PutKubernetesAuthRole(_ context.Context, role bao.KubernetesAuthRole) error {
	f.puts++
	f.roles[role.BackendPath+"/"+role.RoleName] = role
	return nil
}

func (f *fakeKubernetesAuthRoleClient) DeleteKubernetesAuthRole(_ context.Context, backendPath string, roleName string) error {
	f.deletes++
	delete(f.roles, backendPath+"/"+roleName)
	return nil
}
