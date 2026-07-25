package zvol

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
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
		_, _ = driver.run(context.Background(), "destroy", "-r", root+"/xfer")
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

// Two roots under the scratch dataset stand in for two hosts sharing a pool;
// send/recv between them exercises the same stream mechanics as the VLAN
// transfer lane, including GUID-matched incremental receive.
func execTransferPair(t *testing.T) (*Exec, *Exec) {
	t.Helper()
	root := os.Getenv("HOSTD_ZFS_TEST_ROOT")
	if root == "" {
		t.Skip("set HOSTD_ZFS_TEST_ROOT to a scratch dataset to run Exec tests")
	}
	if _, err := exec.LookPath("zfs"); err != nil {
		t.Skip("zfs binary not available")
	}
	ctx := context.Background()
	pair := make([]*Exec, 0, 2)
	for _, suffix := range []string{"/xfer-host-a", "/xfer-host-b"} {
		driver := &Exec{Root: root + suffix, Timeout: time.Minute}
		if ok, err := driver.exists(ctx, driver.Root); err != nil {
			t.Fatal(err)
		} else if !ok {
			if _, err := driver.run(ctx, "create", "-p", driver.Root); err != nil {
				t.Fatal(err)
			}
		}
		if err := driver.Prepare(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = driver.run(context.Background(), "destroy", "-r", driver.Root)
		})
		pair = append(pair, driver)
	}
	return pair[0], pair[1]
}

func transferSet(t *testing.T, source, destination *Exec, generation GenerationID, from GenerationID, wantIncremental bool) {
	t.Helper()
	ctx := context.Background()
	for _, tree := range TransferTrees {
		plan, err := source.ResolveSend(ctx, generation, tree, from)
		if err != nil {
			t.Fatalf("resolve %s %s: %v", generation, tree, err)
		}
		if plan.Incremental != wantIncremental {
			t.Fatalf("%s %s incremental = %t, want %t", generation, tree, plan.Incremental, wantIncremental)
		}
		reader, writer := io.Pipe()
		sent := make(chan error, 1)
		go func() {
			_, err := source.Send(ctx, plan, writer)
			writer.CloseWithError(err)
			sent <- err
		}()
		if err := destination.ReceiveGeneration(ctx, generation, tree, reader); err != nil {
			t.Fatalf("receive %s %s: %v", generation, tree, err)
		}
		if err := <-sent; err != nil {
			t.Fatalf("send %s %s: %v", generation, tree, err)
		}
	}
}

func TestExecTransferLifecycle(t *testing.T) {
	hostA, hostB := execTransferPair(t)
	ctx := context.Background()

	// Host A seals gen-t1 from a cold build.
	if _, err := hostA.EnsureWorkspace(ctx, "xfer-assignment-1", "", 64<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := hostA.EnsureProcess(ctx, "xfer-assignment-1", "", 64<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := hostA.EnsureTool(ctx, "xfer-assignment-1", "", 64<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := hostA.SealSet(ctx, "xfer-assignment-1", "gen-t1"); err != nil {
		t.Fatal(err)
	}
	if err := hostA.DestroyWorkspace(ctx, "xfer-assignment-1"); err != nil {
		t.Fatal(err)
	}
	if err := hostA.DestroyProcess(ctx, "xfer-assignment-1"); err != nil {
		t.Fatal(err)
	}
	if err := hostA.DestroyTool(ctx, "xfer-assignment-1"); err != nil {
		t.Fatal(err)
	}

	// Full transfer to host B; the cache is not residency.
	transferSet(t, hostA, hostB, "gen-t1", "", false)
	resident, cached, err := hostB.GenerationState(ctx, "gen-t1")
	if err != nil || resident || !cached {
		t.Fatalf("host B state resident=%t cached=%t err=%v", resident, cached, err)
	}
	generations, _, err := hostB.Inventory(ctx)
	if err != nil || len(generations) != 0 {
		t.Fatalf("host B inventory saw the transfer cache: %+v, %v", generations, err)
	}
	// Receiving again is a no-op.
	if err := hostB.ReceiveGeneration(ctx, "gen-t1", TreeWorkspace, strings.NewReader("")); err != nil {
		t.Fatalf("idempotent receive: %v", err)
	}

	// Host B materializes warm from the cache and the provenance survives an
	// idempotent re-ensure.
	clone, err := hostB.EnsureWorkspace(ctx, "xfer-assignment-2", "gen-t1", 64<<20)
	if err != nil || clone.Source != "gen-t1" || clone.SourceSnapshotGUID == "" {
		t.Fatalf("clone from cache = %+v, %v", clone, err)
	}
	again, err := hostB.EnsureWorkspace(ctx, "xfer-assignment-2", "gen-t1", 64<<20)
	if err != nil || again.Source != "gen-t1" || again.SourceSnapshotGUID != clone.SourceSnapshotGUID {
		t.Fatalf("re-ensure from cache = %+v, %v", again, err)
	}
	if _, err := hostB.EnsureProcess(ctx, "xfer-assignment-2", "gen-t1", 64<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := hostB.EnsureTool(ctx, "xfer-assignment-2", "gen-t1", 64<<20); err != nil {
		t.Fatal(err)
	}

	// The cache refuses to die under a live clone, then B seals gen-t2 whose
	// lineage descends from the cached snapshot.
	if err := hostB.DestroyTransfer(ctx, "gen-t1"); err == nil {
		t.Fatal("destroyed the transfer cache under a live workspace clone")
	}
	if _, err := hostB.SealSet(ctx, "xfer-assignment-2", "gen-t2"); err != nil {
		t.Fatal(err)
	}
	if err := hostB.DestroyWorkspace(ctx, "xfer-assignment-2"); err != nil {
		t.Fatal(err)
	}
	if err := hostB.DestroyProcess(ctx, "xfer-assignment-2"); err != nil {
		t.Fatal(err)
	}
	if err := hostB.DestroyTool(ctx, "xfer-assignment-2"); err != nil {
		t.Fatal(err)
	}
	// Post-seal the promoted generation owns the lineage; the cache is still
	// its origin and stays busy.
	if err := hostB.DestroyTransfer(ctx, "gen-t1"); !errors.Is(err, ErrBusy) {
		t.Fatalf("transfer cache destroy under sealed descendant = %v, want busy", err)
	}

	// Incremental hop back: A still holds gen-t1 resident, so B's gen-t2 —
	// whose origin is the GUID-preserved copy of gen-t1@sealed — transfers
	// incrementally and A's recv resolves the origin by GUID.
	transferSet(t, hostB, hostA, "gen-t2", "gen-t1", true)
	if _, cached, err := hostA.GenerationState(ctx, "gen-t2"); err != nil || !cached {
		t.Fatalf("host A incremental receive cached=%t err=%v", cached, err)
	}
	warm, err := hostA.EnsureWorkspace(ctx, "xfer-assignment-3", "gen-t2", 64<<20)
	if err != nil || warm.Source != "gen-t2" {
		t.Fatalf("host A clone from incremental cache = %+v, %v", warm, err)
	}
	if err := hostA.DestroyWorkspace(ctx, "xfer-assignment-3"); err != nil {
		t.Fatal(err)
	}

	// A base the source cannot prove as the direct parent degrades to full.
	plan, err := hostB.ResolveSend(ctx, "gen-t2", TreeWorkspace, "gen-unrelated")
	if err != nil || plan.Incremental {
		t.Fatalf("unrelated base plan = %+v, %v", plan, err)
	}

	// With no dependents left the caches reap; absent twice is ErrNotFound.
	if err := hostA.DestroyTransfer(ctx, "gen-t2"); err != nil {
		t.Fatalf("reap host A cache: %v", err)
	}
	if err := hostA.DestroyTransfer(ctx, "gen-t2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second reap = %v", err)
	}
	if transfers, err := hostA.ListTransfers(ctx); err != nil || len(transfers) != 0 {
		t.Fatalf("host A transfers after reap = %v, %v", transfers, err)
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
