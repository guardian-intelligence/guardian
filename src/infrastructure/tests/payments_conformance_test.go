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
		"127.0.0.1:13000,127.0.0.1:13001,127.0.0.1:13002",
		`name: CUSTOMER_CHECKOUT_ENABLED`,
		`value: "false"`,
		"guardian.dev/otel: producer",
		"guardian.dev/tigerbeetle-transport-api: mtls-host-ip-v1",
		"readOnlyRootFilesystem: true",
		"automountServiceAccountToken: false",
	} {
		assertTextContains(t, deployment, want, deploymentPath)
	}
	for _, forbidden := range []string{
		"sk_test_",
		"rk_test_",
		"whsec_",
		"host: 127.0.0.1",
		"TIGERBEETLE_CLUSTER_ID\n              value: \"1\"",
		"10.8.0.11:3000",
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

	railPath := runfilePath(root + "rail-canary.yaml")
	rail := readText(t, railPath)
	for _, want := range []string{
		"kustomize.toolkit.fluxcd.io/substitute: disabled",
		"- /bin/sh",
		`Authorization: Bearer ${PAYMENTS_CANARY_TOKEN}`,
	} {
		assertTextContains(t, rail, want, railPath)
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

	networkPath := runfilePath(root + "networkpolicy.yaml")
	network := readText(t, networkPath)
	for _, want := range []string{
		"name: guardian-analytics-otel-from-payments",
		"k8s:io.kubernetes.pod.namespace: guardian-analytics",
		"k8s:app.kubernetes.io/name: otel-collector",
		"k8s:io.kubernetes.pod.namespace: tenant-guardian-prod",
		"k8s:app.kubernetes.io/component: payments",
		`port: "3001"`,
	} {
		assertTextContains(t, network, want, networkPath)
	}

	transportPath := runfilePath(root + "transport.yaml")
	transport := readText(t, transportPath) + readText(t, runfilePath(root+"client.envoy"))
	for _, want := range []string{
		"name: payments-tigerbeetle-client",
		"name: guardian-tigerbeetle-ca",
		"guardian-payments.guardian.internal",
		"10.8.0.11",
		"10.8.0.12",
		"10.8.0.13",
		"port_value: 3001",
		"tigerbeetle.guardian.internal",
	} {
		assertTextContains(t, transport, want, transportPath)
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
