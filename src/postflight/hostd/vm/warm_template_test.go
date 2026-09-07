package vm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func warmTemplateArtifactFixture(t *testing.T) (WarmTemplate, string) {
	t.Helper()
	directory := t.TempDir()
	guidPath := filepath.Join(directory, "snapshot-guid")
	if err := os.WriteFile(guidPath, []byte("12345678901234567890\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Match the real CLI's snapshot identity query, while allowing the test to
	// replace the snapshot under an unchanged name without a live ZFS pool.
	script := `#!/bin/sh
set -eu
[ "$#" -eq 7 ] && [ "$1" = get ] && [ "$2" = -H ] && [ "$3" = -p ] &&
  [ "$4" = -o ] && [ "$5" = value ] && [ "$6" = guid ] &&
  [ "$7" = tank/postflight/templates/t-template@warm ] || exit 90
exec /bin/cat "$POSTFLIGHT_TEST_SNAPSHOT_GUID_FILE"
`
	if err := os.WriteFile(filepath.Join(directory, "zfs"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("POSTFLIGHT_TEST_SNAPSHOT_GUID_FILE", guidPath)
	memoryPath := filepath.Join(directory, "memory")
	if err := os.WriteFile(memoryPath, []byte("generic unregistered VM memory"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := fileDigest(memoryPath)
	if err != nil {
		t.Fatal(err)
	}
	guid, err := templateSnapshotGUID(context.Background(), "tank/postflight/templates/t-template@warm")
	if err != nil {
		t.Fatal(err)
	}
	template := WarmTemplate{
		Key: "template", RootSnapshot: "tank/postflight/templates/t-template@warm",
		RootSnapshotGUID: guid, MemoryPath: memoryPath, MemorySHA256: digest,
	}
	// Exercise the persisted contract, rather than only an in-memory field.
	raw, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	var persisted WarmTemplate
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if err := validateTemplateArtifacts(context.Background(), persisted); err != nil {
		t.Fatalf("valid paired artifacts rejected: %v", err)
	}
	return persisted, guidPath
}

func TestWarmTemplateRejectsReplacedRootSnapshot(t *testing.T) {
	template, guidPath := warmTemplateArtifactFixture(t)
	// Startup succeeded, then the root snapshot was replaced under the same
	// name. Its matching RAM checksum must not authorize the new disk bytes.
	if err := os.WriteFile(guidPath, []byte("12345678901234567891\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateTemplateArtifacts(context.Background(), template); err == nil || !strings.Contains(err.Error(), "root snapshot GUID mismatch") {
		t.Fatalf("replaced root snapshot accepted: %v", err)
	}
}

func TestWarmTemplateRejectsReplacedMemory(t *testing.T) {
	template, _ := warmTemplateArtifactFixture(t)
	if err := os.WriteFile(template.MemoryPath, []byte("different VM memory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateTemplateArtifacts(context.Background(), template); err == nil || !strings.Contains(err.Error(), "memory digest mismatch") {
		t.Fatalf("replaced memory accepted with matching root snapshot: %v", err)
	}
}

func TestWarmTemplateRejectsMissingOrInvalidSnapshotIdentity(t *testing.T) {
	for _, guid := range []string{"", "0", "-", "18446744073709551616"} {
		t.Run("manifest-"+guid, func(t *testing.T) {
			template, _ := warmTemplateArtifactFixture(t)
			template.RootSnapshotGUID = guid
			if err := validateTemplateArtifacts(context.Background(), template); err == nil || !strings.Contains(err.Error(), "manifest root snapshot GUID") {
				t.Fatalf("invalid manifest GUID accepted: %v", err)
			}
		})
		t.Run("zfs-"+guid, func(t *testing.T) {
			template, guidPath := warmTemplateArtifactFixture(t)
			if err := os.WriteFile(guidPath, []byte(guid+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := validateTemplateArtifacts(context.Background(), template); err == nil || !strings.Contains(err.Error(), "root snapshot GUID is invalid") {
				t.Fatalf("invalid ZFS snapshot GUID accepted: %v", err)
			}
		})
	}
	t.Run("snapshot-absent", func(t *testing.T) {
		template, guidPath := warmTemplateArtifactFixture(t)
		if err := os.Remove(guidPath); err != nil {
			t.Fatal(err)
		}
		if err := validateTemplateArtifacts(context.Background(), template); err == nil || !strings.Contains(err.Error(), "read warm template root snapshot GUID") {
			t.Fatalf("missing snapshot accepted: %v", err)
		}
	})
}

func TestWarmTemplateRejectsEveryCredentialBearingDonor(t *testing.T) {
	warm := Status{Phase: PhaseWarm}
	if !templateDonorEligible(meta{}, warm) {
		t.Fatal("fresh warm guest rejected")
	}
	for name, record := range map[string]meta{
		"registered":        {MemberID: "member"},
		"assignment":        {Assignment: &Assignment{RequestID: "job"}},
		"assignment ID":     {AssignmentID: "assignment"},
		"workspace":         {WorkspaceMountpoint: "/work"},
		"tools":             {ToolMountpoint: "/home/runner"},
		"process":           {ProcessMountpoint: "/process"},
		"restored template": {Restored: true},
	} {
		t.Run(name, func(t *testing.T) {
			if templateDonorEligible(record, warm) {
				t.Fatal("unsafe donor accepted")
			}
		})
	}
	for _, phase := range []Phase{PhaseBooting, PhaseAssigned, PhaseListening, PhaseJobAssigned, PhaseReady, PhaseExited} {
		if templateDonorEligible(meta{}, Status{Phase: phase}) {
			t.Fatalf("accepted %s donor", phase)
		}
	}
}

func TestTurboVMGenerationIDsDivergeAndIncomingIsExplicit(t *testing.T) {
	left := LaunchSpec{Flavor: FlavorTurbo, ID: "left"}
	right := LaunchSpec{Flavor: FlavorTurbo, ID: "right", Incoming: true}
	if argvDigest(left.Argv()) == argvDigest(right.Argv()) {
		t.Fatal("restored VM identity reused")
	}
	args := right.Argv()
	if args[len(args)-2] != "-incoming" || args[len(args)-1] != "defer" {
		t.Fatal("incoming migration must stay paused")
	}
}
