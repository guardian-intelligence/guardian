package controllers

import (
	"context"
	"strings"
	"testing"

	openbaov1alpha1 "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/api/v1alpha1"
	bao "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/operator/openbao"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPKIRoleReconcilerAppliesMissingRole(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := openBaoAPIPKIRole()
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoPKIRole{}).
		WithObjects(obj).
		Build()
	openbao := &fakePKIRoleClient{roles: map[string]bao.PKIRole{}}
	reconciler := &PKIRoleReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (PKIRoleClient, error) {
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
	gotRole := openbao.roles["pki/openbao-api/openbao-api"]
	if gotRole.ClientFlag {
		t.Fatalf("clientFlag = true, want false")
	}
	if !gotRole.ServerFlag {
		t.Fatalf("serverFlag = false, want true")
	}
	if got := gotRole.AllowedIPSANsCIDR; len(got) != 1 || got[0] != "127.0.0.1/32" {
		t.Fatalf("allowed IP SAN CIDRs = %#v, want 127.0.0.1/32", got)
	}

	var got openbaov1alpha1.OpenBaoPKIRole
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled PKI role: %v", err)
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

func TestPKIRoleReconcilerObserveModeReportsMissingRoleWithoutApplying(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := openBaoAPIPKIRole()
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoPKIRole{}).
		WithObjects(obj).
		Build()
	openbao := &fakePKIRoleClient{roles: map[string]bao.PKIRole{}}
	reconciler := &PKIRoleReconciler{
		Client: kube,
		Scheme: scheme,
		Mode:   ReconcileModeObserve,
		OpenBao: func(context.Context) (PKIRoleClient, error) {
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
	if _, ok := openbao.roles["pki/openbao-api/openbao-api"]; ok {
		t.Fatalf("PKI role was created in observe mode")
	}

	var got openbaov1alpha1.OpenBaoPKIRole
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled PKI role: %v", err)
	}
	assertObservedDriftStatus(t, got.Status)
}

func TestPKIRoleReconcilerRejectsUnboundedIPSANs(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := openBaoAPIPKIRole()
	obj.Spec.AllowedIPSANsCIDR = nil
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoPKIRole{}).
		WithObjects(obj).
		Build()
	openbao := &fakePKIRoleClient{roles: map[string]bao.PKIRole{}}
	reconciler := &PKIRoleReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (PKIRoleClient, error) {
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
	var got openbaov1alpha1.OpenBaoPKIRole
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled PKI role: %v", err)
	}
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionReady, metav1.ConditionFalse)
	if !strings.Contains(got.Status.LastError, "allowedIPSANsCIDR") {
		t.Fatalf("LastError = %q, want allowedIPSANsCIDR validation error", got.Status.LastError)
	}
}

func openBaoAPIPKIRole() *openbaov1alpha1.OpenBaoPKIRole {
	return &openbaov1alpha1.OpenBaoPKIRole{
		ObjectMeta: metav1.ObjectMeta{Name: "openbao-api", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoPKIRoleSpec{
			MountPath:                 "pki/openbao-api",
			TTL:                       "2160h",
			MaxTTL:                    "2160h",
			AllowLocalhost:            true,
			AllowedDomains:            []string{"guardian-openbao.tenant-guardian.svc"},
			AllowBareDomains:          true,
			AllowSubdomains:           false,
			AllowGlobDomains:          false,
			AllowWildcardCertificates: false,
			AllowAnyName:              false,
			EnforceHostnames:          true,
			AllowIPSANs:               true,
			AllowedIPSANsCIDR:         []string{"127.0.0.1/32"},
			ServerFlag:                true,
			ClientFlag:                false,
			CodeSigningFlag:           false,
			EmailProtectionFlag:       false,
			KeyType:                   "ec",
			KeyBits:                   256,
			KeyUsage:                  []string{"DigitalSignature", "KeyEncipherment"},
			CNValidations:             []string{"hostname"},
			UseCSRCommonName:          true,
			UseCSRSANs:                true,
			GenerateLease:             false,
			NoStore:                   false,
			RequireCN:                 false,
			DeletionPolicy:            openbaov1alpha1.DeletionPolicyRetain,
		},
	}
}

type fakePKIRoleClient struct {
	roles   map[string]bao.PKIRole
	puts    int
	deletes int
}

func (f *fakePKIRoleClient) GetPKIRole(_ context.Context, mountPath string, roleName string) (bao.PKIRole, bool, error) {
	role, ok := f.roles[mountPath+"/"+roleName]
	return role, ok, nil
}

func (f *fakePKIRoleClient) PutPKIRole(_ context.Context, role bao.PKIRole) error {
	f.puts++
	f.roles[role.MountPath+"/"+role.RoleName] = role
	return nil
}

func (f *fakePKIRoleClient) DeletePKIRole(_ context.Context, mountPath string, roleName string) error {
	f.deletes++
	delete(f.roles, mountPath+"/"+roleName)
	return nil
}
