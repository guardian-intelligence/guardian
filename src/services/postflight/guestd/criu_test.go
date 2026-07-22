package guestd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/guardian-intelligence/guardian/src/services/postflight/generation"
)

type fakeCapsules struct {
	journal     []string
	resetErr    error
	startErr    error
	startErrors []error
}

func (f *fakeCapsules) Start(context.Context) error {
	f.journal = append(f.journal, "start")
	if len(f.startErrors) > 0 {
		err := f.startErrors[0]
		f.startErrors = f.startErrors[1:]
		return err
	}
	return f.startErr
}
func (f *fakeCapsules) Reset(context.Context) error {
	f.journal = append(f.journal, "reset")
	return f.resetErr
}
func (f *fakeCapsules) UseRestored(_ context.Context, pid int) error {
	f.journal = append(f.journal, "use-restored:"+strconv.Itoa(pid))
	return nil
}
func (f *fakeCapsules) RootPID() (int, error)                      { return 123, nil }
func (f *fakeCapsules) PrepareForCheckpoint(context.Context) error { return nil }

func TestCRIUDumpAndRestoreContract(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CRIU_ARGS_DIR", root)
	bin := filepath.Join(root, "criu")
	script := `#!/bin/sh
set -eu
operation="$1"
printf '%s\n' "$@" >"$CRIU_ARGS_DIR/$operation.args"
case "$1" in
  --version) echo 'Version: 4.2' ;;
  check) echo 'Looks good.' ;;
  dump)
    while [ "$#" -gt 0 ]; do
      [ "$1" = --images-dir ] && { shift; images="$1"; }
      shift
    done
    printf image >"$images/inventory.img"
    ;;
  restore)
    while [ "$#" -gt 0 ]; do
      [ "$1" = --pidfile ] && { shift; pidfile="$1"; }
      shift
    done
    printf '4321\n' >"$pidfile"
    ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	imagesRoot := filepath.Join(root, "encrypted")
	images := filepath.Join(imagesRoot, "generation-1")
	engine := CRIU{Path: bin, ImagesRoot: imagesRoot, WorkRoot: filepath.Join(imagesRoot, "work")}
	capsule := Capsule{RootPID: 123, ImagesDir: images, ExternalMounts: []ExternalMount{
		{Key: "workspace", Path: "/workspace"},
		{Key: "tool", Path: "/opt/actions-runner/_work/_tool"},
	}}
	var stages []string
	artifact, err := engine.dumpObserved(context.Background(), capsule, func(event string) {
		stages = append(stages, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	wantStages := []string{
		"checkpoint_version_started", "checkpoint_version_completed",
		"checkpoint_criu_dump_started", "checkpoint_criu_dump_completed",
		"checkpoint_digest_started", "checkpoint_digest_completed",
	}
	if strings.Join(stages, ",") != strings.Join(wantStages, ",") {
		t.Fatalf("checkpoint stages = %v, want %v", stages, wantStages)
	}
	dumpArgs, err := os.ReadFile(filepath.Join(root, "dump.args"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dumpArgs), "--ext-mount-map\nauto\n") ||
		!strings.Contains(string(dumpArgs), "--enable-external-masters\n") ||
		!strings.Contains(string(dumpArgs), "mnt[/opt/actions-runner/_work/_tool]:tool\n") ||
		!strings.Contains(string(dumpArgs), "mnt[/workspace]:workspace\n") ||
		strings.Contains(string(dumpArgs), "mnt[]") {
		t.Fatalf("dump args do not carry the proven external-mount contract:\n%s", dumpArgs)
	}
	if artifact.Version != "Version: 4.2" || !strings.HasPrefix(artifact.Digest, "sha256:") {
		t.Fatalf("artifact = %+v", artifact)
	}
	var restoreStages []string
	pid, err := engine.restoreObserved(context.Background(), capsule, artifact.Digest, artifact.Version, func(event string) {
		restoreStages = append(restoreStages, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if pid != 4321 {
		t.Fatalf("pid = %d", pid)
	}
	wantRestoreStages := []string{
		"restore_version_started", "restore_version_completed",
		"restore_digest_started", "restore_digest_completed",
		"restore_criu_started", "restore_criu_completed",
	}
	if strings.Join(restoreStages, ",") != strings.Join(wantRestoreStages, ",") {
		t.Fatalf("restore stages = %v, want %v", restoreStages, wantRestoreStages)
	}
	restoreArgs, err := os.ReadFile(filepath.Join(root, "restore.args"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(restoreArgs), "--ext-mount-map\nauto\n") ||
		!strings.Contains(string(restoreArgs), "--enable-external-masters\n") ||
		!strings.Contains(string(restoreArgs), "mnt[tool]:/opt/actions-runner/_work/_tool\n") ||
		!strings.Contains(string(restoreArgs), "mnt[workspace]:/workspace\n") ||
		strings.Contains(string(restoreArgs), "mnt[]") {
		t.Fatalf("restore args do not carry the proven external-mount contract:\n%s", restoreArgs)
	}
	if _, err := engine.Restore(context.Background(), capsule, "sha256:"+strings.Repeat("0", 64), artifact.Version); err == nil {
		t.Fatal("tampered artifact restored")
	}
	if _, err := engine.Restore(context.Background(), capsule, artifact.Digest, "Version: 4.1"); err == nil || !strings.Contains(err.Error(), "does not match checkpoint version") {
		t.Fatalf("mismatched CRIU version error = %v", err)
	}
}

func TestProcessRestoreFallsBackColdOnlyAfterCleanup(t *testing.T) {
	root := t.TempDir()
	imagesRoot := filepath.Join(root, "encrypted")
	images := filepath.Join(imagesRoot, "images")
	if err := os.MkdirAll(images, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(images, "inventory.img"), []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	versionBin := filepath.Join(root, "criu")
	if err := os.WriteFile(versionBin, []byte("#!/bin/sh\necho 'Version: 4.2'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest, err := digestDirectory(images)
	if err != nil {
		t.Fatal(err)
	}
	capsules := &fakeCapsules{}
	checkpoints := ProcessCheckpoints{
		Capsules: capsules, ImagesRoot: imagesRoot,
		CRIU: CRIU{
			Path: versionBin, ImagesRoot: imagesRoot, WorkRoot: filepath.Join(imagesRoot, "work"),
			RestoreRun: func(context.Context, string, ...string) (string, error) {
				if strings.Join(capsules.journal, ",") != "reset" {
					t.Fatalf("restore began without an empty capsule boundary: %v", capsules.journal)
				}
				return "", errors.New("unsupported descriptor")
			},
		},
	}
	result, err := checkpoints.RestoreOrCold(context.Background(), images, digest, "Version: 4.2", []ExternalMount{{Key: "workspace", Path: "/work"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ColdFallback || !result.ProcessInvalidated || result.FailureCode != "criu-rejected" {
		t.Fatalf("fallback result = %+v", result)
	}
	if strings.Join(capsules.journal, ",") != "reset,reset,start" {
		t.Fatalf("capsule operations = %v", capsules.journal)
	}

	capsules.journal = nil
	_, err = checkpoints.RestoreOrCold(context.Background(), images, "sha256:"+strings.Repeat("0", 64), "Version: 4.2", []ExternalMount{{Key: "workspace", Path: "/work"}}, nil)
	class, code := generation.RestoreFailureDetails(err)
	if class != generation.RestoreIntegrity || code != "artifact-digest" {
		t.Fatalf("integrity result = %s/%s: %v", class, code, err)
	}
	if strings.Join(capsules.journal, ",") != "reset,reset" {
		t.Fatalf("integrity failure capsule operations = %v", capsules.journal)
	}
}

func TestProcessRestoreEstablishesBoundaryBeforeAdoption(t *testing.T) {
	root := t.TempDir()
	imagesRoot := filepath.Join(root, "encrypted")
	images := filepath.Join(imagesRoot, "images")
	if err := os.MkdirAll(images, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(images, "inventory.img"), []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	versionBin := filepath.Join(root, "criu")
	if err := os.WriteFile(versionBin, []byte("#!/bin/sh\necho 'Version: 4.2'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest, err := digestDirectory(images)
	if err != nil {
		t.Fatal(err)
	}
	capsules := &fakeCapsules{}
	checkpoints := ProcessCheckpoints{
		Capsules: capsules, ImagesRoot: imagesRoot,
		CRIU: CRIU{
			Path: versionBin, ImagesRoot: imagesRoot, WorkRoot: filepath.Join(imagesRoot, "work"),
			RestoreRun: func(_ context.Context, _ string, args ...string) (string, error) {
				if strings.Join(capsules.journal, ",") != "reset" {
					t.Fatalf("restore began without an empty capsule boundary: %v", capsules.journal)
				}
				for index, arg := range args {
					if arg == "--pidfile" && index+1 < len(args) {
						return "", os.WriteFile(args[index+1], []byte("4321\n"), 0o600)
					}
				}
				return "", errors.New("restore pidfile argument missing")
			},
		},
	}
	result, err := checkpoints.RestoreOrCold(context.Background(), images, digest, "Version: 4.2", []ExternalMount{{Key: "workspace", Path: "/work"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Restored || result.ColdFallback || strings.Join(capsules.journal, ",") != "reset,use-restored:4321" {
		t.Fatalf("restore result = %+v, capsule operations = %v", result, capsules.journal)
	}
}

func TestProcessRestoreRetriesColdStartOnlyAfterCleanup(t *testing.T) {
	root := t.TempDir()
	imagesRoot := filepath.Join(root, "encrypted")
	images := filepath.Join(imagesRoot, "images")
	if err := os.MkdirAll(images, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(images, "inventory.img"), []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	versionBin := filepath.Join(root, "criu")
	if err := os.WriteFile(versionBin, []byte("#!/bin/sh\necho 'Version: 4.2'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest, err := digestDirectory(images)
	if err != nil {
		t.Fatal(err)
	}
	capsules := &fakeCapsules{startErrors: []error{errors.New("capsule startup raced"), nil}}
	checkpoints := ProcessCheckpoints{
		Capsules: capsules, ImagesRoot: imagesRoot,
		CRIU: CRIU{
			Path: versionBin, ImagesRoot: imagesRoot, WorkRoot: filepath.Join(imagesRoot, "work"),
			RestoreRun: func(context.Context, string, ...string) (string, error) {
				return "", errors.New("unsupported descriptor")
			},
		},
	}
	result, err := checkpoints.RestoreOrCold(context.Background(), images, digest, "Version: 4.2", []ExternalMount{{Key: "workspace", Path: "/work"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ColdFallback || !result.ProcessInvalidated {
		t.Fatalf("fallback result = %+v", result)
	}
	if strings.Join(capsules.journal, ",") != "reset,reset,start,reset,start" {
		t.Fatalf("capsule operations = %v", capsules.journal)
	}
}

func TestProcessRestoreCleanupFailureNeverStartsCold(t *testing.T) {
	capsules := &fakeCapsules{resetErr: errors.New("still populated")}
	checkpoints := ProcessCheckpoints{Capsules: capsules, ImagesRoot: "/encrypted"}
	checkpoints.CRIU = CRIU{Path: "/missing", ImagesRoot: "/encrypted", WorkRoot: "/encrypted/work"}
	_, err := checkpoints.RestoreOrCold(context.Background(), "/encrypted/images", "sha256:"+strings.Repeat("0", 64), "Version: 4.2", []ExternalMount{{Key: "workspace", Path: "/work"}}, nil)
	class, code := generation.RestoreFailureDetails(err)
	if class != generation.RestoreCleanup || code != "capsule-not-empty" {
		t.Fatalf("cleanup result = %s/%s: %v", class, code, err)
	}
	if strings.Join(capsules.journal, ",") != "reset" {
		t.Fatalf("cleanup failure capsule operations = %v", capsules.journal)
	}
}

func TestCRIURestoreFailureReportsBoundedRedactedDiagnostics(t *testing.T) {
	root := t.TempDir()
	imagesRoot := filepath.Join(root, "encrypted")
	images := filepath.Join(imagesRoot, "images")
	if err := os.MkdirAll(images, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(images, "inventory.img"), []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	versionBin := filepath.Join(root, "criu")
	if err := os.WriteFile(versionBin, []byte("#!/bin/sh\necho 'Version: 4.2'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	engine := CRIU{
		Path: versionBin, ImagesRoot: imagesRoot, WorkRoot: filepath.Join(imagesRoot, "work"),
		RestoreRun: func(_ context.Context, _ string, _ ...string) (string, error) {
			log := filepath.Join(imagesRoot, "work", "restore", "criu.log")
			if err := os.WriteFile(log, []byte("(0.1) Error (criu/mount.c:123): mnt: Can't open /tenant/private/value\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return "", os.ErrInvalid
		},
	}
	digest, err := digestDirectory(images)
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Restore(context.Background(), Capsule{ImagesDir: images}, digest, "Version: 4.2")
	if err == nil || !strings.Contains(err.Error(), "(criu/mount.c:123)") || !strings.Contains(err.Error(), "<guest-root>") {
		t.Fatalf("restore diagnostics = %v", err)
	}
	if strings.Contains(err.Error(), "/tenant/private/value") {
		t.Fatalf("restore diagnostics exposed a guest path: %v", err)
	}
}

func TestCRIUPathDiagnosticsClassifyWithoutDisclosingPaths(t *testing.T) {
	mounts := []ExternalMount{
		{Key: "tool", Path: "/home/runner"},
		{Key: "workspace", Path: "/home/runner/_work/widget/widget"},
	}
	for name, test := range map[string]struct {
		field string
		want  string
	}{
		"workspace":           {field: "</home/runner/_work/widget/widget/private.txt>", want: "<external:workspace>"},
		"runner image":        {field: "/opt/actions-runner/bin/Runner.Worker", want: "<runner-image>"},
		"runner home":         {field: "/home/runner/.cache/secret", want: "<external:tool>"},
		"capsule tmp":         {field: "/tmp/private", want: "<capsule-tmp>"},
		"guest root":          {field: "/tenant/private/value", want: "<guest-root>"},
		"relative workspace":  {field: "home/runner/_work/widget/widget/private.txt", want: "<external:workspace>"},
		"relative runner":     {field: "home/runner/_work/_actions/private.txt", want: "<external:tool>"},
		"relative guest root": {field: "tenant/private/value", want: "<guest-root>"},
	} {
		t.Run(name, func(t *testing.T) {
			got := classifyCRIUPath(test.field, mounts)
			if got != test.want {
				t.Fatalf("class = %q, want %q", got, test.want)
			}
			if strings.Contains(got, "private") || strings.Contains(got, "secret") {
				t.Fatalf("classification disclosed input: %q", got)
			}
		})
	}
}

func TestCRIURejectsUnsafeCapsules(t *testing.T) {
	root := t.TempDir()
	imagesRoot := filepath.Join(root, "encrypted")
	engine := CRIU{Path: "/usr/sbin/criu", ImagesRoot: imagesRoot, WorkRoot: filepath.Join(imagesRoot, "work")}
	for name, capsule := range map[string]Capsule{
		"init":           {RootPID: 1, ImagesDir: filepath.Join(root, "encrypted", "g")},
		"plaintext path": {RootPID: 2, ImagesDir: filepath.Join(root, "elsewhere")},
		"relative mount": {RootPID: 2, ImagesDir: filepath.Join(root, "encrypted", "g"), ExternalMounts: []ExternalMount{{Key: "ws", Path: "relative"}}},
		"injected key":   {RootPID: 2, ImagesDir: filepath.Join(root, "encrypted", "g"), ExternalMounts: []ExternalMount{{Key: "ws]:bad", Path: "/workspace"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := engine.Dump(context.Background(), capsule); err == nil {
				t.Fatal("unsafe capsule accepted: " + strconv.Itoa(capsule.RootPID))
			}
		})
	}
}

func TestCRIURejectsPlaintextDiagnostics(t *testing.T) {
	root := t.TempDir()
	imagesRoot := filepath.Join(root, "encrypted")
	engine := CRIU{
		Path:       "/usr/sbin/criu",
		ImagesRoot: imagesRoot,
		WorkRoot:   filepath.Join(root, "plaintext-work"),
	}
	_, err := engine.Dump(context.Background(), Capsule{
		RootPID:   2,
		ImagesDir: filepath.Join(imagesRoot, "images"),
	})
	if err == nil || !strings.Contains(err.Error(), "diagnostics must be inside") {
		t.Fatalf("plaintext diagnostics error = %v", err)
	}
}
