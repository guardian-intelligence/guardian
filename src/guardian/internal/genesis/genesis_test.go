package genesis

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

func TestWriteEncryptedCreatesAgeBundle(t *testing.T) {
	root := t.TempDir()
	for path, body := range map[string]string{
		"talm/talm.key":                "key",
		"talm/secrets.yaml":            "secrets",
		"talm/nodes/controlplane.yaml": "machine",
		"talm/kubeconfig":              "kubeconfig",
		"operation.json":               "{}",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "genesis.bundle.tar.age")
	manifest, err := WriteEncrypted(Bundle{
		OutputPath:   output,
		Root:         root,
		ClusterName:  "guardian-dev",
		ConfigDigest: "digest",
		CreatedAt:    time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		Recipients:   []string{identity.Recipient().String()},
		Files: []string{
			"talm/talm.key",
			"talm/secrets.yaml",
			"talm/nodes/controlplane.yaml",
			"talm/kubeconfig",
			"operation.json",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 5 {
		t.Fatalf("manifest files = %d, want 5", len(manifest.Files))
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "age-encryption.org/v1") {
		t.Fatalf("bundle does not look like an age file: %q", raw[:min(len(raw), 32)])
	}
}

func TestValidateRecipientsRejectsEmpty(t *testing.T) {
	if err := ValidateRecipients(nil); err == nil {
		t.Fatal("ValidateRecipients(nil) = nil, want error")
	}
}
