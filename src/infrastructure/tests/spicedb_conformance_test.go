package tests

import (
	"regexp"
	"strings"
	"testing"
)

func TestSpiceDBOperatorIsNamespaceScoped(t *testing.T) {
	operatorPath := runfilePath("src/infrastructure/deployments/authorization/operator/operator.yaml")
	operator := readText(t, operatorPath)
	for _, want := range []string{
		"namespace: tenant-guardian-prod",
		"kind: Role",
		"kind: RoleBinding",
		"--crd=false",
		"--watch-namespaces=tenant-guardian-prod",
		"type: Recreate",
		"ghcr.io/authzed/spicedb-operator@sha256:b797b1a1f825c9ae5f49cb28401dee8b8abf74cb5902f9c40a01101b94a05754",
		"migration: populate-schema-tables",
		"digest: sha256:830527f85ac4cb40aaf3d3e8e3fe0b84cdace5ee81ceb43f905b3822ee580b82",
		"migration: add-index-for-transaction-gc",
		"digest: sha256:1d205e9e98e39d87f9640893a06787c854a77d1ffcc3f7ece5e3293896565aa9",
		"runAsNonRoot: true",
		"readOnlyRootFilesystem: true",
		"toEntities:",
		"- kube-apiserver",
	} {
		assertTextContains(t, operator, want, operatorPath)
	}
	for _, forbidden := range []string{
		"kind: ClusterRole\n",
		"kind: ClusterRoleBinding\n",
		"controller_runtime_reconcile_errors_total",
		"--watch-namespaces=" + "tenant-root",
		"tag: v1.52.0",
		"tag: v1.54.0",
		":latest",
	} {
		assertTextNotContains(t, operator, forbidden, operatorPath)
	}

	crdPath := runfilePath("src/infrastructure/deployments/authorization/operator/crd.yaml")
	crd := readText(t, crdPath)
	for _, want := range []string{
		"name: spicedbclusters.authzed.com",
		"scope: Namespaced",
		"name: v1alpha1",
	} {
		assertTextContains(t, crd, want, crdPath)
	}
}

func TestSpiceDBProductionTopologyAndSecurity(t *testing.T) {
	postgresPath := runfilePath("src/infrastructure/deployments/authorization/data/postgres.yaml")
	postgres := readText(t, postgresPath)
	for _, want := range []string{
		"kind: Postgres",
		"name: spicedb",
		"namespace: tenant-guardian-prod",
		"replicas: 3",
		"storageClass: replicated-encrypted",
		"version: v18",
		"max_connections: 100",
		"archive_timeout: 300s",
		"track_commit_timestamp: \"on\"",
		"minSyncReplicas: 1",
		"maxSyncReplicas: 2",
		"enabled: true",
		"useSystemBucket: true",
		"kind: Plan",
		"kind: BackupJob",
		"name: spicedb-postgres-archive-activation",
	} {
		assertTextContains(t, postgres, want, postgresPath)
	}

	spicedbPath := runfilePath("src/infrastructure/deployments/authorization/prod/spicedb.yaml")
	spicedb := readText(t, spicedbPath)
	for _, want := range []string{
		"kind: SpiceDBCluster",
		"channel: stable",
		"version: v1.52.0",
		"datastoreEngine: postgres",
		"datastoreTLSSecretName: postgres-spicedb-ca",
		"tlsSecretName: spicedb-server-tls",
		"dispatchUpstreamCASecretName: spicedb-server-tls",
		"replicas: 3",
		"minAvailable: \"2\"",
		"datastoreConnPoolReadMaxOpen: 12",
		"datastoreConnPoolWriteMaxOpen: 4",
		"topologyKey: kubernetes.io/hostname",
		"whenUnsatisfiable: DoNotSchedule",
		"app.kubernetes.io/instance: spicedb-spicedb",
		"postgres-spicedb-credentials",
		"spicedb-api-token-slot-a",
		"spicedb-api-token-slot-b",
		"reloader.stakater.com/auto: \"true\"",
		"sslmode=verify-full",
		"readOnlyRootFilesystem: true",
	} {
		assertTextContains(t, spicedb, want, spicedbPath)
	}
	for _, forbidden := range []string{
		"sslmode=disable",
		"sslmode=require",
		":latest",
	} {
		assertTextNotContains(t, spicedb, forbidden, spicedbPath)
	}

	credentialsPath := runfilePath("src/infrastructure/deployments/authorization/data/credentials.yaml")
	credentials := readText(t, credentialsPath)
	counts := map[string]int{}
	for _, doc := range yamlDocs(t, credentialsPath) {
		counts[stringValue(doc["kind"])]++
	}
	if got := counts["Password"]; got != 2 {
		t.Fatalf("%s has %d Password generators, want 2", credentialsPath, got)
	}
	if got := counts["ExternalSecret"]; got != 2 {
		t.Fatalf("%s has %d ExternalSecrets, want 2", credentialsPath, got)
	}
	for _, want := range []string{
		"refreshInterval: \"0\"",
		"length: 48",
		"guardian.dev/rotation: \"initial\"",
	} {
		assertTextContains(t, credentials, want, credentialsPath)
	}

	certPath := runfilePath("src/infrastructure/deployments/authorization/data/certificates.yaml")
	certs := readText(t, certPath)
	for _, want := range []string{
		"name: spicedb-ca",
		"name: spicedb-server",
		"secretName: spicedb-server-tls",
		"spicedb.tenant-guardian-prod.svc.cozy.local",
		"rotationPolicy: Always",
		"usages:",
		"- server auth",
	} {
		assertTextContains(t, certs, want, certPath)
	}
	assertTextNotContains(t, certs, "cluster.local", certPath)
}

func TestSpiceDBSchemaAndLiveAcceptanceGate(t *testing.T) {
	validationPath := runfilePath("src/infrastructure/deployments/authorization/prod/schema/validation.yaml")
	validation := readText(t, validationPath)
	for _, want := range []string{
		"assertTrue:",
		"assertFalse:",
		"organization:guardian#manage@guardian_account:alice",
		"organization:guardian#view@guardian_account:mallory",
		"postflight_repository:guardian#manage@guardian_account:alice",
		"postflight_repository:guardian#manage@guardian_account:mallory",
	} {
		assertTextContains(t, validation, want, validationPath)
	}
	if strings.Count(validation, "    - ") < 12 {
		t.Fatalf("%s does not carry a substantial positive/negative assertion set", validationPath)
	}

	jobPath := runfilePath("src/infrastructure/deployments/authorization/prod/schema-job.yaml")
	job := readText(t, jobPath)
	for _, want := range []string{
		"name: spicedb-schema-v1",
		"ghcr.io/authzed/zed@sha256:339db064131cfd75c9385938f16fa445bcfa4a82bd9eed73402fd10c00ea374c",
		"--hostname-override=spicedb.tenant-guardian-prod.svc.cozy.local",
		"--certificate-path=/tls/ca.crt",
		"--error-on-no-permission",
		"PERMISSIONSHIP_HAS_PERMISSION",
		"PERMISSIONSHIP_NO_PERMISSION",
		"deliberately-invalid-token",
		`test "${invalid_status}" = 401`,
		"SpiceDB accepted an unrelated CA",
		"SpiceDB accepted an invalid TLS hostname",
	} {
		assertTextContains(t, job, want, jobPath)
	}

	networkPath := runfilePath("src/infrastructure/deployments/authorization/prod/networkpolicy.yaml")
	network := readText(t, networkPath)
	for _, want := range []string{
		"guardian.dev/component: authorization-datastore",
		"app.kubernetes.io/name: spicedb-schema",
		"authzed.com/cluster-component: migration-job",
		`port: "50051"`,
		`port: "50053"`,
		`port: "8443"`,
		"cnpg.io/cluster: postgres-spicedb",
		"- kube-apiserver",
	} {
		assertTextContains(t, network, want, networkPath)
	}
}

func TestSpiceDBOperationalQualificationIsGitOpsOnly(t *testing.T) {
	thumperPath := runfilePath("src/infrastructure/deployments/authorization/prod/thumper.yaml")
	thumper := readText(t, thumperPath)
	for _, want := range []string{
		"quay.io/authzed/thumper:v0.1.0@sha256:65a4d2e5a5a2e532331f86812793c31a320f4c77991520f7c2c4f0ea5700089a",
		"--insecure=false",
		"--ca-path=/tls/ca.crt",
		"--qps=25",
		"SpiceDBThumperErrors",
		"SpiceDBThumperLatencyHigh",
	} {
		assertTextContains(t, thumper, want, thumperPath)
	}
	assertTextNotContains(t, thumper, "--no-verify-ca", thumperPath)

	loadPath := runfilePath("src/infrastructure/deployments/authorization/prod/thumper/qualification.yaml")
	load := readText(t, loadPath)
	for _, want := range []string{
		"weight: 1",
		"WriteRelationships",
		"DeleteRelationships",
		"randomObjectID",
		"permission: manage",
		"expectNoPermission: true",
		"consistency: AtLeastAsFresh",
	} {
		assertTextContains(t, load, want, loadPath)
	}
	assertTextNotContains(t, load, "WriteSchema", loadPath)
	if got := strings.Count(load, "expectNoPermission: true"); got != 2 {
		t.Fatalf("%s has %d negative decisions, want 2", loadPath, got)
	}

	observabilityPath := runfilePath("src/infrastructure/deployments/authorization/prod/observability.yaml")
	observability := readText(t, observabilityPath)
	for _, want := range []string{
		"SpiceDBReplicaCountDegraded",
		"SpiceDBReplicaPlacementDegraded",
		"SpiceDBPostgresPlacementDegraded",
		"SpiceDBRequestErrors",
		"SpiceDBP99LatencyHigh",
		"SpiceDBContainerRestarting",
		"SpiceDBMigrationFailed",
		"SpiceDBSchemaJobFailed",
		"SpiceDBCertificateExpiring",
		"SpiceDBAlertPathDrill",
		"expr: vector(0)",
	} {
		assertTextContains(t, observability, want, observabilityPath)
	}

	fluxPath := runfilePath("src/infrastructure/base/flux/sync.yaml")
	flux := readText(t, fluxPath)
	for _, want := range []string{
		"name: guardian-authorization-operator",
		"path: ./src/infrastructure/deployments/authorization/operator",
		"name: guardian-authorization-data",
		"path: ./src/infrastructure/deployments/authorization/data",
		"name: guardian-authorization-prod",
		"path: ./src/infrastructure/deployments/authorization/prod",
		"- name: guardian-authorization-operator",
		"- name: guardian-authorization-data",
		"name: postgres-spicedb-init-job",
		"name: spicedb-postgres-archive-activation",
		"name: spicedb-schema-v1",
		"name: spicedb-spicedb",
		"name: spicedb-server",
		"kind: SpiceDBCluster",
		"status.currentMigrationHash == status.targetMigrationHash",
		"status.conditions.exists(e,",
	} {
		assertTextContains(t, flux, want, fluxPath)
	}
	assertTextContains(t, flux, "name: spicedb-thumper", fluxPath)

	runbookPath := runfilePath("src/infrastructure/runbooks/spicedb.md")
	runbook := readText(t, runbookPath)
	for _, want := range []string{
		"Flux is the only writer",
		"Rolling SpiceDB restart",
		"PostgreSQL primary failover",
		"API credential rotation",
		"Certificate rotation",
		"Minor upgrade and rollback",
		"R2 copy-restore drill",
		"Alert delivery",
		"RPO at or below five minutes",
		"RTO below thirty minutes",
	} {
		assertTextContains(t, runbook, want, runbookPath)
	}

	qualifyPath := runfilePath("tools/ops/spicedb-qualify")
	qualify := readText(t, qualifyPath)
	assertTextContains(t, qualify, "This tool only reads Kubernetes objects and opens a port-forward", qualifyPath)

	manualMutation := regexp.MustCompile(`(?m)^\s*(?:sudo\s+)?kubectl\s+(?:apply|create|delete|edit|exec|patch|replace|rollout|scale|set|taint)\b`)
	for path, raw := range map[string]string{
		runbookPath: runbook,
		qualifyPath: qualify,
	} {
		if match := manualMutation.FindString(raw); match != "" {
			t.Fatalf("%s contains manual cluster mutation command %q", path, strings.TrimSpace(match))
		}
	}
}
