package zvol

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

// The Exec driver can only be exercised against a real pool. CI has no ZFS;
// on a host, point HOSTD_ZFS_TEST_ROOT at a scratch dataset (it will be
// created and destroyed) and run the package tests. The full lifecycle it
// covers — empty create, seal, clone, busy-refusal, reap, inventory — is
// the same sequence the agent drives in production.
func execDriver(t *testing.T) *Exec {
	t.Helper()
	root := os.Getenv("HOSTD_ZFS_TEST_ROOT")
	if root == "" {
		t.Skip("set HOSTD_ZFS_TEST_ROOT to a scratch dataset to run Exec tests")
	}
	if _, err := exec.LookPath("zfs"); err != nil {
		t.Skip("zfs binary not available")
	}
	ctx := context.Background()
	driver := &Exec{Root: root, Timeout: time.Minute}
	if ok, err := driver.exists(ctx, root); err != nil {
		t.Fatal(err)
	} else if !ok {
		if _, err := driver.run(ctx, "create", "-p", root); err != nil {
			t.Fatal(err)
		}
	}
	if err := driver.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err := driver.Prepare(ctx); err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	t.Cleanup(func() {
		_, _ = driver.run(context.Background(), "destroy", "-r", root+"/ws")
		_, _ = driver.run(context.Background(), "destroy", "-r", root+"/gen")
		_, _ = driver.run(context.Background(), "destroy", "-r", root+"/process-state")
		_, _ = driver.run(context.Background(), "destroy", "-r", root+"/tool-state")
	})
	return driver
}

func TestExecLifecycle(t *testing.T) {
	driver := execDriver(t)
	ctx := context.Background()

	// Empty workspace (cache miss).
	first, err := driver.EnsureWorkspace(ctx, "assignment-a", "", 64<<20)
	if err != nil {
		t.Fatalf("empty workspace: %v", err)
	}
	if again, err := driver.EnsureWorkspace(ctx, "assignment-a", "", 64<<20); err != nil || again.Name != first.Name {
		t.Fatalf("ensure not idempotent: %v %v", again, err)
	}
	if _, err := os.Stat(first.Device); err != nil {
		t.Fatalf("workspace device is not ready: %v", err)
	}
	if _, err := driver.EnsureProcess(ctx, "assignment-a", "", 64<<20); err != nil {
		t.Fatalf("empty process volume: %v", err)
	}
	if _, err := driver.EnsureTool(ctx, "assignment-a", "", 64<<20); err != nil {
		t.Fatalf("empty tool volume: %v", err)
	}

	// Seal the set as a generation; sealing twice is a no-op.
	sealed, err := driver.SealSet(ctx, "assignment-a", "gen-1")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if again, err := driver.SealSet(ctx, "assignment-a", "gen-1"); err != nil || again.Workspace.Snapshot != sealed.Workspace.Snapshot || again.Tool.Snapshot != sealed.Tool.Snapshot || again.Process.Snapshot != sealed.Process.Snapshot {
		t.Fatalf("seal not idempotent: %v %v", again, err)
	}

	// The sealed workspace can die first (that is what promote buys).
	if err := driver.DestroyWorkspace(ctx, "assignment-a"); err != nil {
		t.Fatalf("destroying sealed-from workspace: %v", err)
	}
	if err := driver.DestroyProcess(ctx, "assignment-a"); err != nil {
		t.Fatalf("destroying sealed-from process volume: %v", err)
	}
	if err := driver.DestroyTool(ctx, "assignment-a"); err != nil {
		t.Fatalf("destroying sealed-from tool volume: %v", err)
	}

	// Clone the generation into a new workspace (cache hit).
	clone, err := driver.EnsureWorkspace(ctx, "assignment-b", "gen-1", 0)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if clone.Source != "gen-1" {
		t.Fatalf("clone source %q", clone.Source)
	}
	if _, err := driver.EnsureProcess(ctx, "assignment-b", "gen-1", 0); err != nil {
		t.Fatalf("process clone: %v", err)
	}
	if _, err := driver.EnsureTool(ctx, "assignment-b", "gen-1", 0); err != nil {
		t.Fatalf("tool clone: %v", err)
	}

	// A generation with a live clone refuses to die.
	if err := driver.DestroyGeneration(ctx, "gen-1"); err == nil {
		t.Fatal("destroyed a generation with a dependent clone")
	}

	// Inventory sees both.
	generations, workspaces, err := driver.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(generations) != 1 || generations[0].Generation != "gen-1" {
		t.Fatalf("generations: %+v", generations)
	}
	if len(workspaces) != 1 || workspaces[0].Source != "gen-1" {
		t.Fatalf("workspaces: %+v", workspaces)
	}

	// Clone gone → reap lands. Absent things report ErrNotFound.
	if err := driver.DestroyWorkspace(ctx, "assignment-b"); err != nil {
		t.Fatal(err)
	}
	if err := driver.DestroyProcess(ctx, "assignment-b"); err != nil {
		t.Fatal(err)
	}
	if err := driver.DestroyTool(ctx, "assignment-b"); err != nil {
		t.Fatal(err)
	}
	if err := driver.DestroyGeneration(ctx, "gen-1"); err != nil {
		t.Fatalf("reap after clone removal: %v", err)
	}
	if err := driver.DestroyProcessGeneration(ctx, "gen-1"); err != nil {
		t.Fatalf("reap process generation: %v", err)
	}
	if err := driver.DestroyToolGeneration(ctx, "gen-1"); err != nil {
		t.Fatalf("reap tool generation: %v", err)
	}
	if err := driver.DestroyWorkspace(ctx, "assignment-b"); !isNotFound(err) {
		t.Fatalf("second destroy: %v", err)
	}
}

func TestExecSealRetryAfterPartialSeal(t *testing.T) {
	driver := execDriver(t)
	ctx := context.Background()
	if _, err := driver.EnsureWorkspace(ctx, "assignment-c", "", 64<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.EnsureProcess(ctx, "assignment-c", "", 64<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.EnsureTool(ctx, "assignment-c", "", 64<<20); err != nil {
		t.Fatal(err)
	}
	// Reproduce the crash window: snapshot, clone, and promote have run but
	// the @sealed snapshot never landed. A retrying SealSet must
	// finish the job instead of failing on the second promote.
	workspace := driver.workspaceDataset("assignment-c")
	processWorkspace := driver.processDriver().workspaceDataset("assignment-c")
	toolWorkspace := driver.toolDriver().workspaceDataset("assignment-c")
	genDataset := driver.generationDataset("gen-2")
	if _, err := driver.run(ctx, "snapshot", workspace+"@seal-gen-2", toolWorkspace+"@seal-gen-2", processWorkspace+"@seal-gen-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.run(ctx, "clone", workspace+"@seal-gen-2", genDataset); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.run(ctx, "promote", genDataset); err != nil {
		t.Fatal(err)
	}
	sealed, err := driver.SealSet(ctx, "assignment-c", "gen-2")
	if err != nil {
		t.Fatalf("seal retry after partial seal: %v", err)
	}
	if sealed.Workspace.Generation != "gen-2" || sealed.Tool.Generation != "gen-2" || sealed.Process.Generation != "gen-2" {
		t.Fatalf("sealed %+v", sealed)
	}
	if err := driver.DestroyWorkspace(ctx, "assignment-c"); err != nil {
		t.Fatal(err)
	}
	if err := driver.DestroyProcess(ctx, "assignment-c"); err != nil {
		t.Fatal(err)
	}
	if err := driver.DestroyTool(ctx, "assignment-c"); err != nil {
		t.Fatal(err)
	}
	if err := driver.DestroyGeneration(ctx, "gen-2"); err != nil {
		t.Fatal(err)
	}
	if err := driver.DestroyProcessGeneration(ctx, "gen-2"); err != nil {
		t.Fatal(err)
	}
	if err := driver.DestroyToolGeneration(ctx, "gen-2"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateNameRejectsHostileIdentifiers(t *testing.T) {
	for _, hostile := range []string{"", "../up", "a/b", "a@b", "a b", "-leading", string(make([]byte, 200))} {
		if err := ValidateName("assignment", hostile); err == nil {
			t.Errorf("accepted %q", hostile)
		}
	}
	for _, fine := range []string{"assignment-1", "gen.2026.07", "A_b:c"} {
		if err := ValidateName("assignment", fine); err != nil {
			t.Errorf("rejected %q: %v", fine, err)
		}
	}
}

func TestReadyWorkspaceWaitsForDevicePublication(t *testing.T) {
	path := t.TempDir() + "/device"
	driver := &Exec{Timeout: time.Second}
	published := make(chan error, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		published <- os.WriteFile(path, nil, 0o600)
	}()

	if err := driver.waitForDevice(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if err := <-published; err != nil {
		t.Fatal(err)
	}
}

func TestReadyWorkspaceHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	driver := &Exec{Root: "missing", Timeout: time.Second}

	err := driver.waitForDevice(ctx, t.TempDir()+"/missing")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("readyWorkspace() error = %v, want context cancellation", err)
	}
}

func TestExecMissingCloneSourceFallsBackCold(t *testing.T) {
	driver := execDriver(t)
	ctx := context.Background()

	// The scope pointer can outlive its generation; a missing clone source
	// must cold-build, not fail the assignment.
	volume, err := driver.EnsureWorkspace(ctx, "assignment-cold", "gen-vanished", 64<<20)
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if volume.Source != "" {
		t.Fatalf("cold fallback reported source %q", volume.Source)
	}
	if ok, err := driver.exists(ctx, volume.Name); err != nil || !ok {
		t.Fatalf("workspace not created: %v %v", ok, err)
	}
}
