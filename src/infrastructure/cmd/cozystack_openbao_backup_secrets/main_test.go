package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValidateConfig(t *testing.T) {
	cfg := backupSecretsConfig{
		Kubectl:              "/kubectl",
		Namespace:            "tenant-root",
		StatefulSet:          "openbao-guardian",
		Service:              "openbao-guardian",
		BootstrapSecret:      "openbao-guardian-bootstrap",
		Endpoint:             "https://account.r2.cloudflarestorage.com",
		Bucket:               "guardian-vault",
		Region:               "auto",
		Stages:               "root,dev,gamma,prod",
		PortForwardReadyWait: time.Second,
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	badStage := cfg
	badStage.Stages = "root,nope"
	if err := validateConfig(badStage); err == nil {
		t.Fatalf("invalid stage accepted")
	}

	badService := cfg
	badService.Service = "OpenBao"
	if err := validateConfig(badService); err == nil {
		t.Fatalf("invalid service accepted")
	}

	missingEndpoint := cfg
	missingEndpoint.Endpoint = ""
	if err := validateConfig(missingEndpoint); err == nil {
		t.Fatalf("missing endpoint accepted")
	}
}

func TestParseStages(t *testing.T) {
	got, err := parseStages("root,dev,root,prod")
	if err != nil {
		t.Fatalf("parseStages() error = %v", err)
	}
	want := []stageConfig{
		{Name: "root", Namespace: "tenant-root"},
		{Name: "dev", Namespace: "tenant-dev"},
		{Name: "prod", Namespace: "tenant-prod"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseStages() = %#v, want %#v", got, want)
	}
	if _, err := parseStages(""); err == nil {
		t.Fatalf("empty stages accepted")
	}
}

func TestCredentialsFromEnv(t *testing.T) {
	got, err := credentialsFromEnv([]string{
		"AWS_ACCESS_KEY_ID=state-access",
		"AWS_SECRET_ACCESS_KEY=state-secret",
		"GUARDIAN_BACKUP_AWS_ACCESS_KEY_ID=backup-access",
		"GUARDIAN_BACKUP_AWS_SECRET_ACCESS_KEY=backup-secret",
	})
	if err != nil {
		t.Fatalf("credentialsFromEnv() error = %v", err)
	}
	if got.AccessKeyID != "backup-access" || got.SecretKey != "backup-secret" {
		t.Fatalf("credentialsFromEnv() = %#v", got)
	}
	if got.Source != "GUARDIAN_BACKUP_AWS_ACCESS_KEY_ID/GUARDIAN_BACKUP_AWS_SECRET_ACCESS_KEY" {
		t.Fatalf("credential source = %q", got.Source)
	}

	got, err = credentialsFromEnv([]string{
		"AWS_ACCESS_KEY_ID=state-access",
		"AWS_SECRET_ACCESS_KEY=state-secret",
	})
	if err != nil {
		t.Fatalf("fallback credentialsFromEnv() error = %v", err)
	}
	if got.AccessKeyID != "state-access" || got.SecretKey != "state-secret" {
		t.Fatalf("fallback credentialsFromEnv() = %#v", got)
	}
	if _, err := credentialsFromEnv([]string{"AWS_ACCESS_KEY_ID=only-access"}); err == nil {
		t.Fatalf("missing secret key accepted")
	}
}

func TestDryRunDoesNotRequireCredentials(t *testing.T) {
	cfg := backupSecretsConfig{
		Kubectl:              "/kubectl",
		Namespace:            "tenant-root",
		StatefulSet:          "openbao-guardian",
		Service:              "openbao-guardian",
		BootstrapSecret:      "openbao-guardian-bootstrap",
		Endpoint:             "https://account.r2.cloudflarestorage.com",
		Bucket:               "guardian-vault",
		Region:               "auto",
		Stages:               "root",
		DryRun:               true,
		PortForwardReadyWait: time.Second,
	}
	if err := runBackupSecrets(context.Background(), cfg, nil); err != nil {
		t.Fatalf("dry-run without env credentials failed: %v", err)
	}
}

func TestBackupSecretWrites(t *testing.T) {
	stages := []stageConfig{{Name: "root", Namespace: "tenant-root"}, {Name: "dev", Namespace: "tenant-dev"}}
	creds := backupSecretCredential{AccessKeyID: "access", SecretKey: "secret"}
	writes := backupSecretWrites(stages, creds, "https://r2.example", "guardian-vault", "auto")
	if len(writes) != 4 {
		t.Fatalf("backupSecretWrites() produced %d writes, want 4", len(writes))
	}
	assertWrite(t, writes[0], "guardian/guardian-mgmt/tenant-root/postgres/guardian/cnpg-backup", map[string]string{
		"AWS_ACCESS_KEY_ID":     "access",
		"AWS_SECRET_ACCESS_KEY": "secret",
	})
	assertWrite(t, writes[1], "guardian/guardian-mgmt/tenant-root/clickhouse/guardian/backup", map[string]string{
		"bucketName": "guardian-vault",
		"endpoint":   "https://r2.example",
		"region":     "auto",
		"accessKey":  "access",
		"secretKey":  "secret",
	})
	assertWrite(t, writes[2], "guardian/guardian-mgmt/tenant-dev/postgres/guardian/cnpg-backup", map[string]string{
		"AWS_ACCESS_KEY_ID":     "access",
		"AWS_SECRET_ACCESS_KEY": "secret",
	})
}

func TestWriteKVV2(t *testing.T) {
	var gotPath string
	var gotToken string
	var gotData map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Vault-Token")
		var body struct {
			Data map[string]string `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotData = body.Data
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	write := secretWrite{
		Path: "guardian/guardian-mgmt/tenant-root/postgres/guardian/cnpg-backup",
		Data: map[string]string{"AWS_ACCESS_KEY_ID": "access"},
	}
	if err := writeKVV2(context.Background(), server.URL, "root-token", write); err != nil {
		t.Fatalf("writeKVV2() error = %v", err)
	}
	if gotPath != "/v1/kv/data/"+write.Path {
		t.Fatalf("request path = %q, want %q", gotPath, "/v1/kv/data/"+write.Path)
	}
	if gotToken != "root-token" {
		t.Fatalf("token header = %q", gotToken)
	}
	if !reflect.DeepEqual(gotData, write.Data) {
		t.Fatalf("request data = %#v, want %#v", gotData, write.Data)
	}
}

func TestDecodeRootToken(t *testing.T) {
	want := "root-token"
	got, err := decodeRootToken(base64.StdEncoding.EncodeToString([]byte(want)))
	if err != nil {
		t.Fatalf("decodeRootToken() error = %v", err)
	}
	if got != want {
		t.Fatalf("decodeRootToken() = %q, want %q", got, want)
	}
	if _, err := decodeRootToken(""); err == nil {
		t.Fatalf("empty token accepted")
	}
	if _, err := decodeRootToken("not base64"); err == nil {
		t.Fatalf("invalid base64 accepted")
	}
}

func TestOpenBaoPortForwardArgs(t *testing.T) {
	runner := kubectlRunner{
		kubeconfig:     "/kubeconfig",
		requestTimeout: "15s",
		namespace:      "tenant-root",
	}
	want := []string{
		"--kubeconfig", "/kubeconfig",
		"--request-timeout=15s",
		"-n", "tenant-root",
		"port-forward",
		"--address", "127.0.0.1",
		"svc/openbao-guardian",
		"18200:8200",
	}
	if got := openBaoPortForwardArgs(runner, "openbao-guardian", 18200); !reflect.DeepEqual(got, want) {
		t.Fatalf("openBaoPortForwardArgs() = %#v, want %#v", got, want)
	}
}

func TestPropertyNames(t *testing.T) {
	got := propertyNames(map[string]string{
		"secretKey":  "secret",
		"accessKey":  "access",
		"bucketName": "bucket",
	})
	if got != "bucketName,accessKey,secretKey" {
		t.Fatalf("propertyNames() = %q", got)
	}
}

func assertWrite(t *testing.T, got secretWrite, wantPath string, wantData map[string]string) {
	t.Helper()
	if got.Path != wantPath {
		t.Fatalf("write path = %q, want %q", got.Path, wantPath)
	}
	if !reflect.DeepEqual(got.Data, wantData) {
		t.Fatalf("write data for %s = %#v, want %#v", wantPath, got.Data, wantData)
	}
	for _, value := range got.Data {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("write data for %s has empty value", wantPath)
		}
	}
}
