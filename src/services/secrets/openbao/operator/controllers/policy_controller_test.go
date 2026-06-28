package controllers

import (
	"context"
	"errors"
	"strings"
	"testing"

	openbaov1alpha1 "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPolicyReconcilerAppliesDriftedPolicy(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "external-dns",
			Namespace: "tenant-guardian",
		},
		Spec: openbaov1alpha1.OpenBaoPolicySpec{
			Name:  "guardian-external-dns",
			Rules: `path "kv/data/example" { capabilities = ["read"] }`,
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoPolicy{}).
		WithObjects(obj).
		Build()
	openbao := &fakePolicyClient{policies: map[string]string{"guardian-external-dns": "old"}}
	reconciler := &PolicyReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (PolicyClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got := openbao.policies["guardian-external-dns"]; got != obj.Spec.Rules {
		t.Fatalf("policy rules = %q, want %q", got, obj.Spec.Rules)
	}
	if openbao.puts != 1 {
		t.Fatalf("puts = %d, want 1", openbao.puts)
	}

	var got openbaov1alpha1.OpenBaoPolicy
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled policy: %v", err)
	}
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionReady, metav1.ConditionTrue)
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionApplied, metav1.ConditionTrue)
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionDriftDetected, metav1.ConditionFalse)
	if got.Status.LastAppliedHash == "" {
		t.Fatalf("LastAppliedHash is empty")
	}
	if got.Status.LastError != "" {
		t.Fatalf("LastError = %q, want empty", got.Status.LastError)
	}
}

func TestPolicyReconcilerSkipsMatchingPolicy(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "metrics-reader", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoPolicySpec{
			Rules: `path "sys/metrics" { capabilities = ["read"] }`,
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoPolicy{}).
		WithObjects(obj).
		Build()
	openbao := &fakePolicyClient{policies: map[string]string{"metrics-reader": obj.Spec.Rules}}
	reconciler := &PolicyReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (PolicyClient, error) {
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

func TestPolicyReconcilerObserveModeReportsDriftWithoutApplying(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "external-dns", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoPolicySpec{
			Name:  "guardian-external-dns",
			Rules: `path "kv/data/example" { capabilities = ["read"] }`,
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoPolicy{}).
		WithObjects(obj).
		Build()
	openbao := &fakePolicyClient{policies: map[string]string{"guardian-external-dns": "old"}}
	reconciler := &PolicyReconciler{
		Client: kube,
		Scheme: scheme,
		Mode:   ReconcileModeObserve,
		OpenBao: func(context.Context) (PolicyClient, error) {
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
	if got := openbao.policies["guardian-external-dns"]; got != "old" {
		t.Fatalf("policy rules = %q, want unchanged old rules", got)
	}

	var got openbaov1alpha1.OpenBaoPolicy
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled policy: %v", err)
	}
	assertObservedDriftStatus(t, got.Status)
}

func TestPolicyReconcilerObserveModeReportsMatchingPolicyApplied(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "metrics-reader", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoPolicySpec{
			Rules: `path "sys/metrics" { capabilities = ["read"] }`,
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoPolicy{}).
		WithObjects(obj).
		Build()
	openbao := &fakePolicyClient{policies: map[string]string{"metrics-reader": obj.Spec.Rules}}
	reconciler := &PolicyReconciler{
		Client: kube,
		Scheme: scheme,
		Mode:   ReconcileModeObserve,
		OpenBao: func(context.Context) (PolicyClient, error) {
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

	var got openbaov1alpha1.OpenBaoPolicy
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled policy: %v", err)
	}
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionReady, metav1.ConditionTrue)
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionApplied, metav1.ConditionTrue)
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionDriftDetected, metav1.ConditionFalse)
	if got.Status.LastAppliedHash == "" {
		t.Fatalf("LastAppliedHash is empty")
	}
	if got.Status.LastError != "" {
		t.Fatalf("LastError = %q, want empty", got.Status.LastError)
	}
}

func TestPolicyReconcilerReportsBootstrapRequiredForMissingAuthRole(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "ops-controller", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoPolicySpec{
			Name:  "guardian-openbao-ops-controller",
			Rules: `path "sys/policies/acl/guardian-*" { capabilities = ["read"] }`,
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoPolicy{}).
		WithObjects(obj).
		Build()
	authErr := errors.New(`login to OpenBao with Kubernetes auth: invalid role name "guardian-openbao-ops-controller"`)
	reconciler := &PolicyReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (PolicyClient, error) {
			return nil, authErr
		},
	}

	result, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.RequeueAfter != bootstrapRequiredRequeueAfter {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, bootstrapRequiredRequeueAfter)
	}

	var got openbaov1alpha1.OpenBaoPolicy
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled policy: %v", err)
	}
	assertBootstrapRequiredStatus(t, got.Status)
	if !strings.Contains(got.Status.LastError, `invalid role name "guardian-openbao-ops-controller"`) {
		t.Fatalf("LastError = %q, want missing role detail", got.Status.LastError)
	}
}

func TestPolicyReconcilerAddsFinalizerForDeletePolicy(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := &openbaov1alpha1.OpenBaoPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "delete-me", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoPolicySpec{
			Rules:          `path "kv/data/delete-me" { capabilities = ["read"] }`,
			DeletionPolicy: openbaov1alpha1.DeletionPolicyDelete,
		},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoPolicy{}).
		WithObjects(obj).
		Build()
	reconciler := &PolicyReconciler{Client: kube, Scheme: scheme}

	result, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !result.Requeue {
		t.Fatalf("Reconcile() requeue = false, want true")
	}
	var got openbaov1alpha1.OpenBaoPolicy
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled policy: %v", err)
	}
	if len(got.Finalizers) != 1 || got.Finalizers[0] != policyFinalizer {
		t.Fatalf("finalizers = %#v, want %s", got.Finalizers, policyFinalizer)
	}
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	return scheme
}

func requestFor(obj client.Object) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}}
}

func assertCondition(t *testing.T, conditions []metav1.Condition, conditionType string, want metav1.ConditionStatus) {
	t.Helper()
	for _, condition := range conditions {
		if condition.Type == conditionType {
			if condition.Status != want {
				t.Fatalf("condition %s = %s, want %s", conditionType, condition.Status, want)
			}
			return
		}
	}
	t.Fatalf("missing condition %s in %#v", conditionType, conditions)
}

func assertConditionReason(t *testing.T, conditions []metav1.Condition, conditionType string, wantStatus metav1.ConditionStatus, wantReason string, wantMessageContains string) {
	t.Helper()
	for _, condition := range conditions {
		if condition.Type != conditionType {
			continue
		}
		if condition.Status != wantStatus {
			t.Fatalf("condition %s status = %s, want %s", conditionType, condition.Status, wantStatus)
		}
		if condition.Reason != wantReason {
			t.Fatalf("condition %s reason = %q, want %q", conditionType, condition.Reason, wantReason)
		}
		if !strings.Contains(condition.Message, wantMessageContains) {
			t.Fatalf("condition %s message = %q, want it to contain %q", conditionType, condition.Message, wantMessageContains)
		}
		return
	}
	t.Fatalf("missing condition %s in %#v", conditionType, conditions)
}

func assertObservedDriftStatus(t *testing.T, status openbaov1alpha1.OpenBaoStatus) {
	t.Helper()
	assertCondition(t, status.Conditions, openbaov1alpha1.ConditionReady, metav1.ConditionFalse)
	assertCondition(t, status.Conditions, openbaov1alpha1.ConditionAuthenticated, metav1.ConditionTrue)
	assertCondition(t, status.Conditions, openbaov1alpha1.ConditionApplied, metav1.ConditionFalse)
	assertCondition(t, status.Conditions, openbaov1alpha1.ConditionDriftDetected, metav1.ConditionTrue)
	if status.LastAppliedHash == "" {
		t.Fatalf("LastAppliedHash is empty")
	}
	if status.LastError != "" {
		t.Fatalf("LastError = %q, want empty", status.LastError)
	}
}

func assertBootstrapRequiredStatus(t *testing.T, status openbaov1alpha1.OpenBaoStatus) {
	t.Helper()
	assertConditionReason(t, status.Conditions, openbaov1alpha1.ConditionAuthenticated, metav1.ConditionFalse, reasonBootstrapRequired, "one-time OpenBao bootstrap")
	assertConditionReason(t, status.Conditions, openbaov1alpha1.ConditionApplied, metav1.ConditionFalse, reasonBootstrapRequired, "one-time OpenBao bootstrap")
	assertConditionReason(t, status.Conditions, openbaov1alpha1.ConditionDriftDetected, metav1.ConditionUnknown, reasonBootstrapRequired, "one-time OpenBao bootstrap")
	assertConditionReason(t, status.Conditions, openbaov1alpha1.ConditionReady, metav1.ConditionFalse, reasonBootstrapRequired, "one-time OpenBao bootstrap")
}

type fakePolicyClient struct {
	policies map[string]string
	puts     int
	deletes  int
}

func (f *fakePolicyClient) GetPolicy(_ context.Context, name string) (string, error) {
	return f.policies[name], nil
}

func (f *fakePolicyClient) PutPolicy(_ context.Context, name string, rules string) error {
	f.puts++
	f.policies[name] = rules
	return nil
}

func (f *fakePolicyClient) DeletePolicy(_ context.Context, name string) error {
	f.deletes++
	delete(f.policies, name)
	return nil
}
