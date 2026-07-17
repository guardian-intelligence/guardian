package tests

import (
	"strings"
	"testing"
)

func TestPaymentsRuntimeConformance(t *testing.T) {
	const root = "src/infrastructure/deployments/payments/prod/"

	deploymentPath := runfilePath(root + "deployment.yaml")
	deployment := readText(t, deploymentPath)
	for _, want := range []string{
		"replicas: 2",
		"postgres-products-rw.tenant-guardian-prod.svc:5432/payments?sslmode=require",
		"10.8.0.11:3000,10.8.0.12:3000,10.8.0.13:3000",
		`name: CUSTOMER_CHECKOUT_ENABLED`,
		`value: "false"`,
		"guardian.dev/otel: producer",
		"readOnlyRootFilesystem: true",
		"automountServiceAccountToken: false",
	} {
		assertTextContains(t, deployment, want, deploymentPath)
	}
	for _, forbidden := range []string{
		"sk_test_",
		"rk_test_",
		"whsec_",
		"TIGERBEETLE_CLUSTER_ID\n              value: \"1\"",
	} {
		assertTextNotContains(t, deployment, forbidden, deploymentPath)
	}

	postgresPath := runfilePath("src/infrastructure/deployments/products/prod/postgres.yaml")
	postgres := readText(t, postgresPath)
	for _, want := range []string{
		"storageClass: replicated-encrypted",
		"replicas: 3",
		"payments: {}",
		"- payments",
	} {
		assertTextContains(t, postgres, want, postgresPath)
	}

	secretsPath := runfilePath(root + "secrets.yaml")
	secrets := readText(t, secretsPath)
	for _, want := range []string{
		"name: payments-stripe",
		"name: payments-journal-r2",
		"name: payments-canary",
		"guardian/guardian-mgmt/tenant-guardian-prod/payments/stripe",
		"guardian/guardian-mgmt/tenant-guardian-prod/payments/journal-r2",
	} {
		assertTextContains(t, secrets, want, secretsPath)
	}
}

func TestPaymentsRolloutAndCanaryConformance(t *testing.T) {
	const root = "src/infrastructure/deployments/payments/prod/"

	canaryPath := runfilePath(root + "canary.yaml")
	canary := readText(t, canaryPath)
	for _, want := range []string{
		"provider: kubernetes",
		"name: dependency-readiness",
		"name: stripe-ledger-gate",
		"name: ready-under-load",
		"payments-canary.tenant-guardian-prod",
	} {
		assertTextContains(t, canary, want, canaryPath)
	}

	browserPath := runfilePath(root + "browser-canary.yaml")
	browser := readText(t, browserPath)
	for _, want := range []string{
		"schedule: \"*/5 * * * *\"",
		"namespace: guardian-analytics",
		"CLICKHOUSE_USER",
		"value: payments_canary",
		"PAYMENTS_PUBLIC_CANARY_URL",
		"readOnlyRootFilesystem: true",
	} {
		assertTextContains(t, browser, want, browserPath)
	}

	observabilityPath := runfilePath(root + "observability.yaml")
	observability := readText(t, observabilityPath)
	for _, want := range []string{
		"PaymentsStripeAccountBindingLost",
		"PaymentsProviderBacklog",
		"PaymentsJournalIncomplete",
		"PaymentsUnmatchedBalanceTransaction",
		"PaymentsReconciliationStale",
		"PaymentsRailCanaryStale",
		"PaymentsCheckoutCanaryStale",
		"PaymentsCanaryFailed",
		`outcome="invalid_signature"}[5m]) > 5`,
		"PaymentsLiveModeEventRejected",
	} {
		assertTextContains(t, observability, want, observabilityPath)
	}
	if strings.Contains(observability, `outcome="invalid_signature"}[5m]) > 0`) {
		t.Fatal("one invalid webhook signature must remain below the alert threshold")
	}
}

func TestPaymentsTraceAndFluxConformance(t *testing.T) {
	fluxPath := runfilePath("src/infrastructure/base/flux/sync.yaml")
	flux := readText(t, fluxPath)
	start := strings.Index(flux, "name: guardian-payments-prod")
	if start < 0 {
		t.Fatalf("%s has no guardian-payments-prod Kustomization", fluxPath)
	}
	paymentsFlux := flux[start:]
	for _, dependency := range []string{
		"name: guardian-products-prod",
		"name: guardian-iam-prod",
		"name: guardian-analytics",
		"name: guardian-tigerbeetle",
		"name: guardian-flagger",
	} {
		assertTextContains(t, paymentsFlux, dependency, "guardian-payments-prod Kustomization")
	}
}
