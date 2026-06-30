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

func TestPKIRootIssuerReconcilerGeneratesMissingIssuerAndSetsDefault(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := openBaoAPIPKIRootIssuer()
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoPKIRootIssuer{}).
		WithObjects(obj).
		Build()
	openbao := &fakePKIRootIssuerClient{
		issuers: map[string]bao.PKIIssuer{},
	}
	reconciler := &PKIRootIssuerReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (PKIRootIssuerClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if openbao.generates != 1 {
		t.Fatalf("generates = %d, want 1", openbao.generates)
	}
	if openbao.configPuts != 1 {
		t.Fatalf("configPuts = %d, want 1", openbao.configPuts)
	}
	gotIssuer := openbao.issuers["pki/openbao-api/openbao-api-root-2026"]
	if gotIssuer.IssuerName != "openbao-api-root-2026" {
		t.Fatalf("issuer name = %q", gotIssuer.IssuerName)
	}
	if openbao.config.Default != "openbao-api-root-2026" {
		t.Fatalf("default issuer = %q, want openbao-api-root-2026", openbao.config.Default)
	}
	if openbao.config.DefaultFollowsLatestIssuer {
		t.Fatalf("default_follows_latest_issuer = true, want false")
	}

	var got openbaov1alpha1.OpenBaoPKIRootIssuer
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled PKI root issuer: %v", err)
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

func TestPKIRootIssuerReconcilerSkipsExistingDefaultIssuerByID(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := openBaoAPIPKIRootIssuer()
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoPKIRootIssuer{}).
		WithObjects(obj).
		Build()
	openbao := &fakePKIRootIssuerClient{
		issuers: map[string]bao.PKIIssuer{
			"pki/openbao-api/openbao-api-root-2026": {
				MountPath:  "pki/openbao-api",
				IssuerRef:  "openbao-api-root-2026",
				IssuerID:   "issuer-id",
				IssuerName: "openbao-api-root-2026",
			},
		},
		configFound: true,
		config: bao.PKIIssuerConfig{
			Default: "issuer-id",
		},
	}
	reconciler := &PKIRootIssuerReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (PKIRootIssuerClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if openbao.generates != 0 {
		t.Fatalf("generates = %d, want 0", openbao.generates)
	}
	if openbao.configPuts != 0 {
		t.Fatalf("configPuts = %d, want 0", openbao.configPuts)
	}
}

func TestPKIRootIssuerReconcilerObserveModeReportsMissingIssuerWithoutApplying(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := openBaoAPIPKIRootIssuer()
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoPKIRootIssuer{}).
		WithObjects(obj).
		Build()
	openbao := &fakePKIRootIssuerClient{
		issuers: map[string]bao.PKIIssuer{},
	}
	reconciler := &PKIRootIssuerReconciler{
		Client: kube,
		Scheme: scheme,
		Mode:   ReconcileModeObserve,
		OpenBao: func(context.Context) (PKIRootIssuerClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if openbao.generates != 0 {
		t.Fatalf("generates = %d, want 0", openbao.generates)
	}
	if openbao.configPuts != 0 {
		t.Fatalf("configPuts = %d, want 0", openbao.configPuts)
	}

	var got openbaov1alpha1.OpenBaoPKIRootIssuer
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled PKI root issuer: %v", err)
	}
	assertObservedDriftStatus(t, got.Status)
}

func TestPKIRootIssuerReconcilerRejectsInvalidSpec(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	obj := openBaoAPIPKIRootIssuer()
	obj.Spec.IssuerName = "bad/name"
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoPKIRootIssuer{}).
		WithObjects(obj).
		Build()
	openbao := &fakePKIRootIssuerClient{
		issuers: map[string]bao.PKIIssuer{},
	}
	reconciler := &PKIRootIssuerReconciler{
		Client: kube,
		Scheme: scheme,
		OpenBao: func(context.Context) (PKIRootIssuerClient, error) {
			return openbao, nil
		},
	}

	_, err := reconciler.Reconcile(ctx, requestFor(obj))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if openbao.generates != 0 {
		t.Fatalf("generates = %d, want 0", openbao.generates)
	}
	var got openbaov1alpha1.OpenBaoPKIRootIssuer
	if err := kube.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
		t.Fatalf("get reconciled PKI root issuer: %v", err)
	}
	assertCondition(t, got.Status.Conditions, openbaov1alpha1.ConditionReady, metav1.ConditionFalse)
	if !strings.Contains(got.Status.LastError, "must not contain") {
		t.Fatalf("LastError = %q, want validation error", got.Status.LastError)
	}
}

func openBaoAPIPKIRootIssuer() *openbaov1alpha1.OpenBaoPKIRootIssuer {
	return &openbaov1alpha1.OpenBaoPKIRootIssuer{
		ObjectMeta: metav1.ObjectMeta{Name: "openbao-api-root-2026", Namespace: "tenant-guardian"},
		Spec: openbaov1alpha1.OpenBaoPKIRootIssuerSpec{
			MountPath:  "pki/openbao-api",
			IssuerName: "openbao-api-root-2026",
			CommonName: "Guardian OpenBao API Root 2026",
			TTL:        "87600h",
			KeyType:    "ec",
			KeyBits:    256,
			SetDefault: true,
		},
	}
}

type fakePKIRootIssuerClient struct {
	issuers     map[string]bao.PKIIssuer
	config      bao.PKIIssuerConfig
	configFound bool
	generates   int
	configPuts  int
}

func (f *fakePKIRootIssuerClient) GetPKIIssuer(_ context.Context, mountPath string, issuerRef string) (bao.PKIIssuer, bool, error) {
	issuer, ok := f.issuers[mountPath+"/"+issuerRef]
	return issuer, ok, nil
}

func (f *fakePKIRootIssuerClient) GeneratePKIRootIssuer(_ context.Context, issuer bao.PKIRootIssuer) (bao.PKIIssuer, error) {
	f.generates++
	generated := bao.PKIIssuer{
		MountPath:   issuer.MountPath,
		IssuerRef:   issuer.IssuerName,
		IssuerID:    "issuer-id",
		IssuerName:  issuer.IssuerName,
		KeyID:       "key-id",
		KeyName:     issuer.IssuerName,
		Certificate: "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----\n",
	}
	f.issuers[issuer.MountPath+"/"+issuer.IssuerName] = generated
	return generated, nil
}

func (f *fakePKIRootIssuerClient) GetPKIIssuerConfig(_ context.Context, _ string) (bao.PKIIssuerConfig, bool, error) {
	return f.config, f.configFound, nil
}

func (f *fakePKIRootIssuerClient) PutPKIIssuerConfig(_ context.Context, _ string, config bao.PKIIssuerConfig) error {
	f.configPuts++
	f.config = config
	f.configFound = true
	return nil
}
