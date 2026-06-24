package main

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultJobName(t *testing.T) {
	got := defaultJobName("root", "ClickHouse", time.Date(2026, 6, 22, 12, 34, 56, 0, time.UTC))
	want := "guardian-root-clickhouse-20260622t123456z"
	if got != want {
		t.Fatalf("defaultJobName() = %q, want %q", got, want)
	}
}

func TestActivationJobName(t *testing.T) {
	if got := activationJobName("guardian-root-clickhouse-test"); got != "guardian-root-clickhouse-test-prime" {
		t.Fatalf("activationJobName(short) = %q", got)
	}
	long := strings.Repeat("a", 63)
	got := activationJobName(long)
	if len(got) > 63 {
		t.Fatalf("activationJobName(long) length = %d, want <= 63", len(got))
	}
	if !strings.HasSuffix(got, "-prime") {
		t.Fatalf("activationJobName(long) = %q, want -prime suffix", got)
	}
}

func TestClickHouseEnvCheckShellDoesNotPrintValues(t *testing.T) {
	got := clickHouseEnvCheckShell()
	for _, want := range clickHouseSystemBucketEnv {
		if !strings.Contains(got, want) {
			t.Fatalf("clickHouseEnvCheckShell missing %s: %s", want, got)
		}
	}
	for _, forbidden := range []string{"printenv", "env |", "set"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("clickHouseEnvCheckShell may print values via %q: %s", forbidden, got)
		}
	}
}

func TestBackupJobManifest(t *testing.T) {
	cfg := drillConfig{
		Stage:           "root",
		Namespace:       "tenant-root",
		Component:       componentSpec{Kind: "ClickHouse", BackupClass: "cozy-default"},
		ApplicationName: "guardian",
		Name:            "guardian-root-clickhouse-test",
	}
	got := backupJobManifest(cfg)
	for _, want := range []string{
		"apiVersion: backups.cozystack.io/v1alpha1\nkind: BackupJob\n",
		"name: guardian-root-clickhouse-test\n  namespace: tenant-root\n",
		"guardian.dev/drill: cozystack-backup\n",
		"kind: ClickHouse\n    name: guardian\n",
		"backupClassName: cozy-default\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("backupJobManifest missing %q:\n%s", want, got)
		}
	}
}

func TestRestoreJobManifest(t *testing.T) {
	cfg := drillConfig{
		Stage:             "root",
		Namespace:         "tenant-root",
		Component:         componentSpec{Kind: "ClickHouse", BackupClass: "cozy-default"},
		RestoreTargetName: "guardian-restore",
	}
	got := restoreJobManifest(cfg, "guardian-root-clickhouse-test-restore", "guardian-root-clickhouse-test-20260622")
	for _, want := range []string{
		"apiVersion: backups.cozystack.io/v1alpha1\nkind: RestoreJob\n",
		"name: guardian-root-clickhouse-test-restore\n  namespace: tenant-root\n",
		"name: guardian-root-clickhouse-test-20260622\n",
		"kind: ClickHouse\n    name: guardian-restore\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("restoreJobManifest missing %q:\n%s", want, got)
		}
	}
}

func TestRestoreTargetGetArgs(t *testing.T) {
	got := restoreTargetGetArgs("clickhouses.apps.cozystack.io/guardian-restore")
	want := []string{"get", "clickhouses.apps.cozystack.io/guardian-restore"}
	if len(got) != len(want) {
		t.Fatalf("restoreTargetGetArgs() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("restoreTargetGetArgs() = %#v, want %#v", got, want)
		}
	}
}

func TestRestoreTargetManifestFromSource(t *testing.T) {
	cfg := drillConfig{
		Stage:             "root",
		Namespace:         "tenant-root",
		Component:         componentSpec{Kind: "ClickHouse"},
		RestoreTargetName: "guardian-restore",
	}
	source := []byte(`{
  "apiVersion": "apps.cozystack.io/v1alpha1",
  "kind": "ClickHouse",
  "metadata": {
    "name": "guardian",
    "namespace": "tenant-root",
    "resourceVersion": "123",
    "uid": "deadbeef"
  },
  "spec": {
    "replicas": 3,
    "storageClass": "replicated",
    "backup": {
      "cleanupStrategy": "--keep-last=3",
      "destinationPath": "s3://legacy",
      "enabled": true,
      "endpoint": "https://legacy.example",
      "endpointURL": "https://legacy.example",
      "resticPassword": "legacy",
      "s3AccessKey": "legacy",
      "s3Bucket": "legacy",
      "s3CredentialsSecret": {
        "name": "guardian-clickhouse-backup-creds"
      },
      "s3PathOverride": "legacy",
      "s3Region": "legacy",
      "s3SecretKey": "legacy",
      "schedule": "",
      "useSystemBucket": true
    }
  },
  "status": {
    "conditions": []
  }
}`)
	got, err := restoreTargetManifestFromSource(cfg, source)
	if err != nil {
		t.Fatalf("restoreTargetManifestFromSource() error = %v", err)
	}
	for _, want := range []string{
		`"apiVersion": "apps.cozystack.io/v1alpha1"`,
		`"kind": "ClickHouse"`,
		`"name": "guardian-restore"`,
		`"namespace": "tenant-root"`,
		`"guardian.dev/drill": "cozystack-restore-target"`,
		`"storageClass": "replicated"`,
		`"useSystemBucket": true`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("restore target manifest missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{
		"cleanupStrategy",
		"destinationPath",
		"endpointURL",
		"guardian-clickhouse-backup-creds",
		"resourceVersion",
		"resticPassword",
		"s3AccessKey",
		"s3Bucket",
		"s3CredentialsSecret",
		"s3PathOverride",
		"s3Region",
		"s3SecretKey",
		"status",
		"uid",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("restore target manifest retained %q:\n%s", forbidden, got)
		}
	}
}

func TestValidateConfig(t *testing.T) {
	base := drillConfig{
		Kubectl:         "/kubectl",
		Stage:           "root",
		Namespace:       "tenant-root",
		Component:       componentSpec{Kind: "Postgres", BackupClass: "cozy-default"},
		ApplicationName: "guardian",
		Name:            "guardian-root-postgres-test",
	}
	if err := validateConfig(base); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	restore := base
	restore.RestoreTargetName = "guardian-restore"
	if err := validateConfig(restore); err != nil {
		t.Fatalf("valid restore config rejected: %v", err)
	}

	createRestore := restore
	createRestore.CreateRestoreTarget = true
	if err := validateConfig(createRestore); err != nil {
		t.Fatalf("valid restore-target creation config rejected: %v", err)
	}

	missingRestoreTarget := base
	missingRestoreTarget.CreateRestoreTarget = true
	if err := validateConfig(missingRestoreTarget); err == nil {
		t.Fatalf("create-restore-target without restore-target was accepted")
	}

	inPlace := base
	inPlace.RestoreTargetName = "guardian"
	if err := validateConfig(inPlace); err == nil {
		t.Fatalf("in-place restore without explicit allow was accepted")
	}

	inPlace.AllowInPlaceRestore = true
	if err := validateConfig(inPlace); err != nil {
		t.Fatalf("explicitly allowed in-place restore rejected: %v", err)
	}

	createOverSource := inPlace
	createOverSource.CreateRestoreTarget = true
	if err := validateConfig(createOverSource); err == nil {
		t.Fatalf("create-restore-target over source app was accepted")
	}

	badName := base
	badName.Name = "Not_A_DNS_Label"
	if err := validateConfig(badName); err == nil {
		t.Fatalf("invalid DNS label was accepted")
	}

	longRestoreName := base
	longRestoreName.Name = strings.Repeat("a", 56)
	longRestoreName.RestoreTargetName = "guardian-restore"
	err := validateConfig(longRestoreName)
	if err == nil {
		t.Fatalf("restore config with too-long generated RestoreJob name was accepted")
	}
	if !strings.Contains(err.Error(), "--restore-job") {
		t.Fatalf("restore config rejected with %q, want restore-job error", err)
	}
}

func TestNamespaceAndComponentValidation(t *testing.T) {
	if got, err := namespaceForStage("root"); err != nil || got != "tenant-root" {
		t.Fatalf("namespaceForStage(root) = %q, %v", got, err)
	}
	if _, err := namespaceForStage("gamma"); err == nil {
		t.Fatalf("retired product stage was accepted")
	}
	if _, err := namespaceForStage("staging"); err == nil {
		t.Fatalf("invalid stage was accepted")
	}

	if got, err := componentForName("postgresql"); err != nil || got.Kind != "Postgres" || got.BackupClass != "cozy-default" {
		t.Fatalf("componentForName(postgresql) = %#v, %v", got, err)
	}
	if got, err := componentForName("clickhouse"); err != nil || got.Resource != "clickhouses.apps.cozystack.io" {
		t.Fatalf("componentForName(clickhouse) resource = %#v, %v", got, err)
	}
	if got, err := componentForName("postgres"); err != nil || got.Resource != "postgreses.apps.cozystack.io" {
		t.Fatalf("componentForName(postgres) resource = %#v, %v", got, err)
	}
	if _, err := componentForName("harbor"); err == nil {
		t.Fatalf("unsupported Harbor component was accepted")
	} else if !strings.Contains(err.Error(), "Harbor is not a Cozystack managed-database BackupJob target") {
		t.Fatalf("Harbor error = %q, want managed-database BackupJob guidance", err)
	}
}
