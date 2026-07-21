package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
)

// fakeLauncher tracks started processes in memory; liveness is a settable
// flag so tests can simulate crashes.
type fakeLauncher struct {
	mu      sync.Mutex
	alive   map[ID]bool
	argv    map[ID][]string
	starts  []ID
	kills   []ID
	failing bool
}

func newFakeLauncher() *fakeLauncher {
	return &fakeLauncher{alive: map[ID]bool{}, argv: map[ID][]string{}}
}

func (l *fakeLauncher) Start(_ context.Context, id ID, _ string, argv []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failing {
		return fmt.Errorf("fake launcher: refusing to start %s", id)
	}
	l.alive[id] = true
	l.argv[id] = argv
	l.starts = append(l.starts, id)
	return nil
}

func (l *fakeLauncher) Alive(_ context.Context, id ID, _ string, _ []string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.alive[id], nil
}

func (l *fakeLauncher) Kill(_ context.Context, id ID, _ string, _ []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.alive, id)
	l.kills = append(l.kills, id)
	return nil
}

func (l *fakeLauncher) setAlive(id ID, alive bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if alive {
		l.alive[id] = true
	} else {
		delete(l.alive, id)
	}
}

// fakeDisks records dataset operations without a zpool.
type fakeDisks struct {
	mu        sync.Mutex
	ensured   map[string]string // dataset -> image
	destroyed []string
	onEnsure  func(dataset string)
}

func newFakeDisks() *fakeDisks { return &fakeDisks{ensured: map[string]string{}} }

func (d *fakeDisks) Ensure(_ context.Context, dataset, image string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensured[dataset] = image
	if d.onEnsure != nil {
		d.onEnsure(dataset)
	}
	return nil
}

func (d *fakeDisks) Destroy(_ context.Context, dataset string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.ensured, dataset)
	d.destroyed = append(d.destroyed, dataset)
	return nil
}

// scriptedGuest is the guestd seam for tests: observations are set by the
// test, deliveries are recorded.
type scriptedGuest struct {
	mu                   sync.Mutex
	observation          map[ID]GuestObservation
	cids                 map[ID]uint32
	prepared             map[ID][]guestproto.Prepare
	rendezvoused         map[ID][]guestproto.Rendezvous
	authorized           map[ID][]guestproto.Authorize
	quiesced             map[ID][]guestproto.Quiesce
	quiesceErr           error
	quiesceFailureTiming []guestproto.TimingPoint
}

func newScriptedGuest() *scriptedGuest {
	return &scriptedGuest{
		observation:  map[ID]GuestObservation{},
		cids:         map[ID]uint32{},
		prepared:     map[ID][]guestproto.Prepare{},
		rendezvoused: map[ID][]guestproto.Rendezvous{},
		authorized:   map[ID][]guestproto.Authorize{},
		quiesced:     map[ID][]guestproto.Quiesce{},
	}
}

func (g *scriptedGuest) Prepare(_ context.Context, id ID, cid uint32, prepare guestproto.Prepare) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cids[id] = cid
	g.prepared[id] = append(g.prepared[id], prepare)
	return nil
}

func (g *scriptedGuest) Rendezvous(_ context.Context, id ID, cid uint32, rendezvous guestproto.Rendezvous) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cids[id] = cid
	g.rendezvoused[id] = append(g.rendezvoused[id], rendezvous)
	return nil
}

func (g *scriptedGuest) Authorize(_ context.Context, id ID, cid uint32, authorize guestproto.Authorize) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cids[id] = cid
	g.authorized[id] = append(g.authorized[id], authorize)
	return nil
}

func (g *scriptedGuest) Observe(_ context.Context, id ID, cid uint32) (GuestObservation, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cids[id] = cid
	return g.observation[id], nil
}

func (g *scriptedGuest) Quiesce(_ context.Context, id ID, cid uint32, request guestproto.Quiesce) (guestproto.Quiesced, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cids[id] = cid
	if g.quiesceErr != nil {
		return guestproto.Quiesced{Timing: g.quiesceFailureTiming}, g.quiesceErr
	}
	g.quiesced[id] = append(g.quiesced[id], request)
	return guestproto.Quiesced{Checkpoint: &guestproto.CheckpointArtifact{
		Digest:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Version: "Version: 4.2",
	}, Timing: []guestproto.TimingPoint{{
		Event: "checkpoint_dump_completed", Source: "guestd", BootID: "guest-boot",
		Sequence: 7, MonotonicNS: 11, UnixNS: 12,
	}}}, nil
}

func (g *scriptedGuest) set(id ID, observation GuestObservation) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.observation[id] = observation
}

func (g *scriptedGuest) preparations(id ID) []guestproto.Prepare {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]guestproto.Prepare(nil), g.prepared[id]...)
}

func (g *scriptedGuest) rendezvouses(id ID) []guestproto.Rendezvous {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]guestproto.Rendezvous(nil), g.rendezvoused[id]...)
}

func (g *scriptedGuest) quiesces(id ID) []guestproto.Quiesce {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]guestproto.Quiesce(nil), g.quiesced[id]...)
}

func (g *scriptedGuest) cid(id ID) uint32 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cids[id]
}

const testClass = Class("postflight-4cpu-ubuntu-2404")

type testDriver struct {
	q        *QEMU
	launcher *fakeLauncher
	disks    *fakeDisks
	guest    *scriptedGuest
}

func newTestDriver(t *testing.T, stateRoot string) *testDriver {
	t.Helper()
	launcher := newFakeLauncher()
	guest := newScriptedGuest()
	q, err := NewQEMU(Config{
		StateRoot:   stateRoot,
		QEMUPath:    "/usr/bin/qemu-system-x86_64",
		Firmware:    "/usr/share/postflight/OVMF.fd",
		DatasetRoot: "tank/postflight",
		Classes: map[Class]ClassConfig{
			testClass: {CPUs: 4, MemoryMiB: 16384, Image: "tank/postflight/golden/noble@sealed"},
		},
		Launcher: launcher,
		Guest:    guest,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("building driver: %v", err)
	}
	disks := newFakeDisks()
	q.disks = disks
	return &testDriver{q: q, launcher: launcher, disks: disks, guest: guest}
}

// qemuHandler scripts the QMP surface Assign and detach use, tracking the
// attachment state machine like a real QEMU would.
type qemuHandler struct {
	mu       sync.Mutex
	running  bool
	blockdev map[string]bool
	qdev     map[string]string
	commands []string
}

func (h *qemuHandler) handle(command string, arguments json.RawMessage) ([]string, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.blockdev == nil {
		h.blockdev = map[string]bool{}
	}
	if h.qdev == nil {
		h.qdev = map[string]string{}
	}
	h.commands = append(h.commands, command)
	switch command {
	case "query-status":
		return nil, fmt.Sprintf(`{"return": {"status": "running", "running": %t}, "id": %%d}`, h.running)
	case "query-named-block-nodes":
		nodes := []string{`{"node-name":"root"}`}
		for _, node := range []string{workspaceNode, processNode} {
			if h.blockdev[node] {
				nodes = append(nodes, fmt.Sprintf(`{"node-name":%q}`, node))
			}
		}
		return nil, `{"return":[` + strings.Join(nodes, ",") + `],"id":%d}`
	case "qom-list":
		devices := []string{}
		for _, device := range []string{workspaceDevice, processDevice} {
			if _, ok := h.qdev[device]; ok {
				devices = append(devices, fmt.Sprintf(`{"name":%q,"type":"child<scsi-hd>"}`, device))
			}
		}
		return nil, `{"return":[` + strings.Join(devices, ",") + `],"id":%d}`
	case "blockdev-add":
		var request struct {
			Node string `json:"node-name"`
		}
		_ = json.Unmarshal(arguments, &request)
		h.blockdev[request.Node] = true
		return nil, `{"return": {}, "id": %d}`
	case "device_add":
		var request struct {
			ID    string `json:"id"`
			Drive string `json:"drive"`
		}
		_ = json.Unmarshal(arguments, &request)
		h.qdev[request.ID] = request.Drive
		return nil, `{"return": {}, "id": %d}`
	case "device_del":
		var request struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(arguments, &request)
		delete(h.qdev, request.ID)
		return nil, `{"return": {}, "id": %d}`
	case "blockdev-del":
		var request struct {
			Node string `json:"node-name"`
		}
		_ = json.Unmarshal(arguments, &request)
		for _, node := range h.qdev {
			if node == request.Node {
				return nil, `{"error": {"class": "GenericError", "desc": "Node is busy"}, "id": %d}`
			}
		}
		delete(h.blockdev, request.Node)
		return nil, `{"return": {}, "id": %d}`
	case "quit":
		return nil, `{"return": {}, "id": %d}`
	}
	return nil, `{"error": {"class": "CommandNotFound", "desc": "unscripted"}, "id": %d}`
}

func (h *qemuHandler) log() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.commands...)
}

func (h *qemuHandler) setRunning(running bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running = running
}

// serveQEMU parks a scripted QMP server on a VM's socket path.
func serveQEMU(t *testing.T, driver *testDriver, id ID, handler *qemuHandler) {
	t.Helper()
	stateDir := driver.q.stateDir(id)
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		t.Fatal(err)
	}
	startQMPServer(t, &qmpServer{socket: qmpSocketPath(stateDir), handle: handler.handle})
}

func TestLaunchPersistsMetaBeforeSideEffects(t *testing.T) {
	root := shortTempDir(t)
	driver := newTestDriver(t, root)
	metaAtEnsure := false
	driver.disks.onEnsure = func(string) {
		_, err := os.Stat(filepath.Join(root, "vm-a", "meta.json"))
		metaAtEnsure = err == nil
	}
	if err := driver.q.Launch(context.Background(), "vm-a", testClass); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if !metaAtEnsure {
		t.Fatal("root disk materialized before meta.json was durable")
	}
}

func TestLaunchFailureLeavesAdoptableMeta(t *testing.T) {
	root := shortTempDir(t)
	driver := newTestDriver(t, root)
	driver.launcher.failing = true
	if err := driver.q.Launch(context.Background(), "vm-a", testClass); err == nil {
		t.Fatal("launch succeeded with a failing launcher")
	}
	if _, err := os.Stat(filepath.Join(root, "vm-a", "meta.json")); err != nil {
		t.Fatalf("meta.json missing after failed launch: %v", err)
	}
	// The dead not-quite-VM is collected, not resurrected, by List.
	driver.launcher.failing = false
	statuses, err := driver.q.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(statuses) != 0 {
		t.Fatalf("listed %+v, want nothing", statuses)
	}
	if _, err := os.Stat(filepath.Join(root, "vm-a")); !os.IsNotExist(err) {
		t.Fatal("dead vm state dir survived collection")
	}
	if len(driver.disks.destroyed) == 0 {
		t.Fatal("dead vm root dataset was not destroyed")
	}
}

func TestLaunchIsIdempotentWhileRunning(t *testing.T) {
	driver := newTestDriver(t, shortTempDir(t))
	ctx := context.Background()
	if err := driver.q.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatal(err)
	}
	if err := driver.q.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatal(err)
	}
	if len(driver.launcher.starts) != 1 {
		t.Fatalf("started %d times, want 1", len(driver.launcher.starts))
	}
	if err := driver.q.Launch(ctx, "vm-a", Class("other-class")); err == nil {
		t.Fatal("relaunched an existing id under a different class")
	}
}

func TestLaunchRejectsHostileIDs(t *testing.T) {
	driver := newTestDriver(t, shortTempDir(t))
	for _, hostile := range []string{"", "../up", "a/b", "a b", "a,b", "-leading", strings.Repeat("a", 65)} {
		if err := driver.q.Launch(context.Background(), ID(hostile), testClass); err == nil {
			t.Errorf("accepted id %q", hostile)
		}
	}
}

func TestCIDAllocation(t *testing.T) {
	driver := newTestDriver(t, shortTempDir(t))
	ctx := context.Background()
	for _, id := range []ID{"vm-a", "vm-b", "vm-c"} {
		if err := driver.q.Launch(ctx, id, testClass); err != nil {
			t.Fatal(err)
		}
	}
	cids := map[uint32]ID{}
	for _, id := range []ID{"vm-a", "vm-b", "vm-c"} {
		record, err := driver.q.readMeta(id)
		if err != nil {
			t.Fatal(err)
		}
		if record.CID < 3 {
			t.Fatalf("vm %s got reserved cid %d", id, record.CID)
		}
		if other, taken := cids[record.CID]; taken {
			t.Fatalf("cid %d assigned to both %s and %s", record.CID, other, id)
		}
		cids[record.CID] = id
	}
	if err := driver.q.Destroy(ctx, "vm-b"); err != nil {
		t.Fatal(err)
	}
	if err := driver.q.Launch(ctx, "vm-d", testClass); err != nil {
		t.Fatal(err)
	}
	record, err := driver.q.readMeta("vm-d")
	if err != nil {
		t.Fatal(err)
	}
	if record.CID != 4 {
		t.Fatalf("vm-d got cid %d, want the freed 4", record.CID)
	}
}

// TestCorruptMetaDoesNotWedgeTheDriver: a single unparseable meta.json must
// never take the whole host's driver down. Status quarantines the VM as
// gone, List still reports the healthy fleet and collects the corrupt VM
// (state dir and derived root clone included), and Destroy succeeds.
func TestCorruptMetaDoesNotWedgeTheDriver(t *testing.T) {
	root := shortTempDir(t)
	driver := newTestDriver(t, root)
	ctx := context.Background()
	if err := driver.q.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatal(err)
	}
	if err := driver.q.Launch(ctx, "vm-bad", testClass); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vm-bad", "meta.json"), []byte(`{"id": "vm-bad", "cl`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, err := driver.q.Status(ctx, "vm-bad")
	if err != nil {
		t.Fatalf("status on corrupt meta: %v", err)
	}
	if status.Phase != PhaseGone {
		t.Fatalf("phase %s, want gone", status.Phase)
	}

	statuses, err := driver.q.List(ctx)
	if err != nil {
		t.Fatalf("list with corrupt meta present: %v", err)
	}
	if len(statuses) != 1 || statuses[0].ID != "vm-a" {
		t.Fatalf("listed %+v, want just vm-a", statuses)
	}
	if _, err := os.Stat(filepath.Join(root, "vm-bad")); !os.IsNotExist(err) {
		t.Fatal("corrupt vm state dir survived collection")
	}
	collected := false
	for _, dataset := range driver.disks.destroyed {
		if dataset == "tank/postflight/vm-vm-bad" {
			collected = true
		}
	}
	if !collected {
		t.Fatalf("corrupt vm root clone not destroyed: %v", driver.disks.destroyed)
	}
}

func TestDestroyCorruptMetaSucceeds(t *testing.T) {
	root := shortTempDir(t)
	driver := newTestDriver(t, root)
	ctx := context.Background()
	if err := driver.q.Launch(ctx, "vm-bad", testClass); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vm-bad", "meta.json"), []byte(`not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := driver.q.Destroy(ctx, "vm-bad"); err != nil {
		t.Fatalf("destroy with corrupt meta: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "vm-bad")); !os.IsNotExist(err) {
		t.Fatal("state dir survived destroy")
	}
}

// TestLaunchRefusesBlindCIDAllocation: an unreadable meta may belong to a
// live QEMU holding an unknown vsock CID, so launching anything new must
// fail until collection has cleared the corruption — never mint a CID that
// may collide with a running vhost-vsock device.
func TestLaunchRefusesBlindCIDAllocation(t *testing.T) {
	root := shortTempDir(t)
	driver := newTestDriver(t, root)
	ctx := context.Background()
	if err := driver.q.Launch(ctx, "vm-bad", testClass); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vm-bad", "meta.json"), []byte(`{"cid": "three"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := driver.q.Launch(ctx, "vm-new", testClass); err == nil {
		t.Fatal("launched while a corrupt meta may hold an unknown cid")
	}
	if _, err := driver.q.List(ctx); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := driver.q.Launch(ctx, "vm-new", testClass); err != nil {
		t.Fatalf("launch after collection: %v", err)
	}
}

// blockingGuest wedges Observe until its context fires, the shape of a
// vsock channel that accepts but never answers.
type blockingGuest struct{}

func (blockingGuest) Prepare(context.Context, ID, uint32, guestproto.Prepare) error {
	return nil
}

func (blockingGuest) Rendezvous(context.Context, ID, uint32, guestproto.Rendezvous) error {
	return nil
}

func (blockingGuest) Authorize(context.Context, ID, uint32, guestproto.Authorize) error {
	return nil
}

func (blockingGuest) Quiesce(context.Context, ID, uint32, guestproto.Quiesce) (guestproto.Quiesced, error) {
	return guestproto.Quiesced{}, nil
}

func (blockingGuest) Observe(ctx context.Context, _ ID, _ uint32) (GuestObservation, error) {
	select {
	case <-ctx.Done():
		return GuestObservation{}, ctx.Err()
	case <-time.After(10 * time.Second):
		return GuestObservation{Hello: true}, nil
	}
}

// TestObserveIsBoundedByProbeTimeout: a wedged guest channel must not hold
// the driver mutex hostage; the probe times out and Status falls back to
// the phase the meta alone supports.
func TestObserveIsBoundedByProbeTimeout(t *testing.T) {
	driver := newTestDriver(t, shortTempDir(t))
	driver.q.guestProbeTimeout = 50 * time.Millisecond
	ctx := context.Background()
	if err := driver.q.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatal(err)
	}
	serveQEMU(t, driver, "vm-a", &qemuHandler{running: true})
	driver.q.cfg.Guest = blockingGuest{}
	start := time.Now()
	status, err := driver.q.Status(ctx, "vm-a")
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("status took %s against a wedged guest", elapsed)
	}
	if status.Phase != PhaseBooting {
		t.Fatalf("phase %s, want the meta-supported booting", status.Phase)
	}
}

func TestListCollectsQEMUThatNeverReachesGuestHello(t *testing.T) {
	driver := newTestDriver(t, shortTempDir(t))
	driver.q.bootTimeout = time.Nanosecond
	ctx := context.Background()
	if err := driver.q.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatal(err)
	}
	serveQEMU(t, driver, "vm-a", &qemuHandler{running: true})

	statuses, err := driver.q.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 0 {
		t.Fatalf("statuses = %+v, want failed boot omitted", statuses)
	}
	if driver.launcher.alive["vm-a"] {
		t.Fatal("failed boot process was not killed")
	}
	if _, err := os.Stat(driver.q.stateDir("vm-a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state dir remains after failed boot: %v", err)
	}
	if got := driver.disks.destroyed; len(got) != 1 || got[0] != driver.q.rootDataset("vm-a") {
		t.Fatalf("destroyed datasets = %v", got)
	}
}

func TestBootDeadlineAdoptsMetadataWithoutCreatedTimestamp(t *testing.T) {
	driver := newTestDriver(t, shortTempDir(t))
	driver.q.bootTimeout = time.Second
	ctx := context.Background()
	if err := driver.q.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatal(err)
	}
	record, err := driver.q.readMeta("vm-a")
	if err != nil {
		t.Fatal(err)
	}
	record.CreatedUnixNS = 0
	if err := driver.q.writeMeta(record); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(driver.q.metaPath("vm-a"), old, old); err != nil {
		t.Fatal(err)
	}
	serveQEMU(t, driver, "vm-a", &qemuHandler{running: true})
	statuses, err := driver.q.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 0 {
		t.Fatalf("statuses = %+v, want stale adopted boot omitted", statuses)
	}
}

func TestPrepareUnknownVM(t *testing.T) {
	driver := newTestDriver(t, shortTempDir(t))
	err := driver.q.Prepare(context.Background(), "vm-a", Preparation{Lease: "lease-1"})
	if err != ErrNotFound {
		t.Fatalf("error %v, want ErrNotFound", err)
	}
}

func TestPreparePersistsLeaseBeforeRendezvous(t *testing.T) {
	driver := newTestDriver(t, shortTempDir(t))
	ctx := context.Background()
	if err := driver.q.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatal(err)
	}
	if err := driver.q.Prepare(ctx, "vm-a", Preparation{Lease: "lease-1", JITConfig: "jit"}); err != nil {
		t.Fatal(err)
	}
	// No QMP server is running: the bind fails after the listener claim is
	// durable — the ambiguous-failure shape the agent's failure path needs.
	if err := driver.q.Rendezvous(ctx, "vm-a", Rendezvous{
		Lease: "lease-1", WorkspaceDevice: "/dev/zvol/tank/ws/lease-1",
		ProcessDevice: "/dev/zvol/tank/process/lease-1", WorkspaceMountpoint: "/work",
	}); err == nil {
		t.Fatal("rendezvous succeeded without a QMP endpoint")
	}
	record, err := driver.q.readMeta("vm-a")
	if err != nil {
		t.Fatal(err)
	}
	if record.Lease != "lease-1" {
		t.Fatalf("lease %q, want lease-1", record.Lease)
	}
}

func TestRendezvousAttachesDeliversAndConverges(t *testing.T) {
	driver := newTestDriver(t, shortTempDir(t))
	ctx := context.Background()
	if err := driver.q.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatal(err)
	}
	handler := &qemuHandler{running: true}
	serveQEMU(t, driver, "vm-a", handler)
	preparation := Preparation{Lease: "lease-1", JITConfig: "jit-blob"}
	rendezvous := Rendezvous{
		Lease:               "lease-1",
		WorkspaceDevice:     "/dev/zvol/tank/ws/lease-1",
		WorkspaceMountpoint: "/opt/actions-runner/_work/widget/widget",
		ProcessDevice:       "/dev/zvol/tank/process/lease-1",
	}
	if err := driver.q.Prepare(ctx, "vm-a", preparation); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := driver.q.Rendezvous(ctx, "vm-a", rendezvous); err != nil {
		t.Fatalf("rendezvous: %v", err)
	}
	if err := driver.q.Rendezvous(ctx, "vm-a", rendezvous); err != nil {
		t.Fatalf("repeat rendezvous: %v", err)
	}
	adds := 0
	for _, command := range handler.log() {
		if command == "blockdev-add" || command == "device_add" {
			adds++
		}
	}
	if adds != 4 {
		t.Fatalf("attach commands ran %d times, want one blockdev-add and device_add per volume", adds)
	}
	preparations := driver.guest.preparations("vm-a")
	if len(preparations) == 0 || preparations[0].JITConfig != "jit-blob" {
		t.Fatalf("preparations %+v", preparations)
	}
	deliveries := driver.guest.rendezvouses("vm-a")
	if len(deliveries) == 0 {
		t.Fatal("rendezvous never delivered")
	}
	if len(deliveries[0].Mounts) != 2 {
		t.Fatalf("delivered mounts %+v", deliveries[0].Mounts)
	}
	mount := deliveries[0].Mounts[0]
	if mount.Serial != workspaceNode || mount.Filesystem != workspaceFilesystem ||
		mount.Mountpoint != "/opt/actions-runner/_work/widget/widget" {
		t.Fatalf("delivered mount %+v", mount)
	}
	discard := false
	for _, option := range mount.Options {
		if option == "discard" {
			discard = true
		}
	}
	if !discard {
		t.Fatalf("mount options %v carry no discard", mount.Options)
	}
	record, err := driver.q.readMeta("vm-a")
	if err != nil {
		t.Fatal(err)
	}
	if driver.guest.cid("vm-a") != record.CID {
		t.Fatalf("delivered to cid %d, meta says %d", driver.guest.cid("vm-a"), record.CID)
	}
	if err := driver.q.Prepare(ctx, "vm-a", Preparation{Lease: "lease-2"}); err == nil {
		t.Fatal("reassigned a vm to a different lease")
	}
}

// TestQuiesceUsesTheAssignedMountpoint: Quiesce reaches the guest with the
// mountpoint the assignment persisted — including through a driver restart,
// which only has the meta on disk to go by.
func TestQuiesceUsesTheAssignedMountpoint(t *testing.T) {
	root := shortTempDir(t)
	driver := newTestDriver(t, root)
	ctx := context.Background()
	if err := driver.q.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.q.Quiesce(ctx, "vm-a"); err == nil {
		t.Fatal("quiesced a vm with no workspace")
	}
	if _, err := driver.q.Quiesce(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("error %v, want ErrNotFound", err)
	}
	serveQEMU(t, driver, "vm-a", &qemuHandler{running: true})
	rendezvous := Rendezvous{
		Lease:               "lease-1",
		WorkspaceDevice:     "/dev/zvol/tank/ws/lease-1",
		WorkspaceMountpoint: "/opt/actions-runner/_work/widget/widget",
		ProcessDevice:       "/dev/zvol/tank/process/lease-1",
	}
	if err := driver.q.Prepare(ctx, "vm-a", Preparation{Lease: "lease-1", JITConfig: "jit"}); err != nil {
		t.Fatal(err)
	}
	if err := driver.q.Rendezvous(ctx, "vm-a", rendezvous); err != nil {
		t.Fatal(err)
	}

	second := newTestDriver(t, root)
	second.launcher.setAlive("vm-a", true)
	artifact, err := second.q.Quiesce(ctx, "vm-a")
	if err != nil {
		t.Fatalf("quiesce: %v", err)
	}
	var events []string
	for _, point := range artifact.Timing {
		events = append(events, point.Event)
	}
	if got := strings.Join(events, ","); got != "quiesce_rpc_started,checkpoint_dump_completed,quiesce_rpc_completed" {
		t.Fatalf("quiesce timing %s", got)
	}
	if got := second.guest.quiesces("vm-a"); len(got) != 1 || len(got[0].Mountpoints) != 2 || got[0].Mountpoints[0] != rendezvous.WorkspaceMountpoint || got[0].Mountpoints[1] != processMountpoint {
		t.Fatalf("quiesced %v, want the assigned mountpoint", got)
	}

	second.guest.quiesceErr = errors.New("dump timed out")
	second.guest.quiesceFailureTiming = []guestproto.TimingPoint{{
		Event: "checkpoint_criu_dump_started", Source: "guestd", BootID: "guest-boot",
		Sequence: 8, MonotonicNS: 13, UnixNS: 14,
	}}
	failed, err := second.q.Quiesce(ctx, "vm-a")
	if err == nil || !strings.Contains(err.Error(), "dump timed out") {
		t.Fatalf("failed quiesce error = %v", err)
	}
	events = events[:0]
	for _, point := range failed.Timing {
		events = append(events, point.Event)
	}
	wantSuffix := "quiesce_rpc_started,checkpoint_criu_dump_started,quiesce_rpc_failed"
	if got := strings.Join(events[len(events)-3:], ","); got != wantSuffix {
		t.Fatalf("failed quiesce timing suffix %s, want %s", got, wantSuffix)
	}
}

func TestStatusPhaseLadder(t *testing.T) {
	driver := newTestDriver(t, shortTempDir(t))
	ctx := context.Background()
	if err := driver.q.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatal(err)
	}
	handler := &qemuHandler{}
	serveQEMU(t, driver, "vm-a", handler)

	expect := func(phase Phase, exitCode int) {
		t.Helper()
		status, err := driver.q.Status(ctx, "vm-a")
		if err != nil {
			t.Fatal(err)
		}
		if status.Phase != phase || status.ExitCode != exitCode {
			t.Fatalf("phase %s exit %d, want %s %d", status.Phase, status.ExitCode, phase, exitCode)
		}
	}

	expect(PhaseBooting, 0) // guest not running yet
	handler.setRunning(true)
	expect(PhaseBooting, 0) // running but guestd silent
	driver.guest.set("vm-a", GuestObservation{Hello: true})
	expect(PhaseWarm, 0)

	if err := driver.q.Prepare(ctx, "vm-a", Preparation{Lease: "lease-1", JITConfig: "jit"}); err != nil {
		t.Fatal(err)
	}
	expect(PhaseAssigned, 0)
	driver.guest.set("vm-a", GuestObservation{Hello: true, RunnerRegistered: true})
	expect(PhaseListening, 0)
	driver.guest.set("vm-a", GuestObservation{
		Hello: true, RunnerRegistered: true, HookBlocked: true,
		Identity: guestproto.JobIdentity{RunID: "1", RunAttempt: 1, RunnerName: "lease-1", Repository: "acme/widget"},
	})
	expect(PhaseHookBlocked, 0)
	driver.guest.set("vm-a", GuestObservation{
		Hello: true, Released: true, RunnerExited: true, ExitCode: 7,
		FailureReason: "worker transport closed",
	})
	expect(PhaseExited, 7)

	status, err := driver.q.Status(ctx, "vm-a")
	if err != nil {
		t.Fatal(err)
	}
	if status.Lease != "lease-1" {
		t.Fatalf("lease %q, want lease-1", status.Lease)
	}
	if !status.CustomerStepsReleased || status.FailureReason != "worker transport closed" {
		t.Fatalf("exit lifecycle evidence = %+v", status)
	}
}

func TestStatusUnknownIsGone(t *testing.T) {
	driver := newTestDriver(t, shortTempDir(t))
	status, err := driver.q.Status(context.Background(), "nope")
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != PhaseGone {
		t.Fatalf("phase %s, want gone", status.Phase)
	}
}

func TestRestartedDriverAdoptsRunningVMs(t *testing.T) {
	root := shortTempDir(t)
	first := newTestDriver(t, root)
	ctx := context.Background()
	if err := first.q.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatal(err)
	}
	handler := &qemuHandler{running: true}
	serveQEMU(t, first, "vm-a", handler)
	if err := first.q.Prepare(ctx, "vm-a", Preparation{Lease: "lease-1", JITConfig: "jit"}); err != nil {
		t.Fatal(err)
	}
	originalMeta, err := first.q.readMeta("vm-a")
	if err != nil {
		t.Fatal(err)
	}

	// A brand-new driver instance over the same state root: no memory of the
	// VM, only disk and probes. The fake launcher's liveness is in-memory,
	// so hand the adopted instance one that still says alive.
	second := newTestDriver(t, root)
	second.launcher.setAlive("vm-a", true)
	statuses, err := second.q.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 {
		t.Fatalf("adopted %d vms, want 1", len(statuses))
	}
	adopted := statuses[0]
	if adopted.ID != "vm-a" || adopted.Lease != "lease-1" || adopted.Class != testClass {
		t.Fatalf("adopted %+v", adopted)
	}
	if adopted.Phase != PhaseAssigned {
		t.Fatalf("adopted phase %s, want assigned", adopted.Phase)
	}
	adoptedMeta, err := second.q.readMeta("vm-a")
	if err != nil {
		t.Fatal(err)
	}
	if adoptedMeta.CID != originalMeta.CID || adoptedMeta.ArgvSHA256 != originalMeta.ArgvSHA256 {
		t.Fatalf("adoption changed identity: %+v vs %+v", adoptedMeta, originalMeta)
	}

	// A newly launched VM must not collide with the adopted one's CID.
	if err := second.q.Launch(ctx, "vm-b", testClass); err != nil {
		t.Fatal(err)
	}
	freshMeta, err := second.q.readMeta("vm-b")
	if err != nil {
		t.Fatal(err)
	}
	if freshMeta.CID == adoptedMeta.CID {
		t.Fatalf("cid %d reused while vm-a still holds it", freshMeta.CID)
	}

	// Destroy through the adopted instance releases everything.
	if err := second.q.Destroy(ctx, "vm-a"); err != nil {
		t.Fatal(err)
	}
	status, err := second.q.Status(ctx, "vm-a")
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != PhaseGone {
		t.Fatalf("phase %s after destroy, want gone", status.Phase)
	}
}

func TestListCollectsDeadVMs(t *testing.T) {
	root := shortTempDir(t)
	driver := newTestDriver(t, root)
	ctx := context.Background()
	if err := driver.q.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatal(err)
	}
	driver.launcher.setAlive("vm-a", false) // QEMU crashed
	statuses, err := driver.q.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 0 {
		t.Fatalf("listed %+v, want nothing", statuses)
	}
	if _, err := os.Stat(filepath.Join(root, "vm-a")); !os.IsNotExist(err) {
		t.Fatal("crashed vm state dir survived")
	}
	found := false
	for _, dataset := range driver.disks.destroyed {
		if dataset == "tank/postflight/vm-vm-a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("crashed vm root clone not destroyed: %v", driver.disks.destroyed)
	}
}

func TestDestroyIsIdempotent(t *testing.T) {
	driver := newTestDriver(t, shortTempDir(t))
	ctx := context.Background()
	if err := driver.q.Destroy(ctx, "never-existed"); err != nil {
		t.Fatalf("destroying an absent vm: %v", err)
	}
	if err := driver.q.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := driver.q.Destroy(ctx, "vm-a"); err != nil {
			t.Fatalf("destroy #%d: %v", i+1, err)
		}
	}
	if got := len(driver.launcher.kills); got != 1 {
		t.Fatalf("killed %d times, want 1", got)
	}
}

func TestDestroyDetachesBeforeQuit(t *testing.T) {
	driver := newTestDriver(t, shortTempDir(t))
	ctx := context.Background()
	if err := driver.q.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatal(err)
	}
	handler := &qemuHandler{running: true}
	serveQEMU(t, driver, "vm-a", handler)
	if err := driver.q.Prepare(ctx, "vm-a", Preparation{Lease: "lease-1", JITConfig: "jit"}); err != nil {
		t.Fatal(err)
	}
	if err := driver.q.Rendezvous(ctx, "vm-a", Rendezvous{
		Lease: "lease-1", WorkspaceDevice: "/dev/zvol/tank/ws/lease-1",
		ProcessDevice: "/dev/zvol/tank/process/lease-1", WorkspaceMountpoint: "/work",
	}); err != nil {
		t.Fatal(err)
	}
	if err := driver.q.Destroy(ctx, "vm-a"); err != nil {
		t.Fatal(err)
	}
	var sequence []string
	for _, command := range handler.log() {
		switch command {
		case "device_del", "blockdev-del", "quit":
			sequence = append(sequence, command)
		}
	}
	want := []string{"device_del", "blockdev-del", "device_del", "blockdev-del", "quit"}
	if len(sequence) < len(want) {
		t.Fatalf("teardown sequence %v, want %v", sequence, want)
	}
	for i, command := range want {
		if sequence[i] != command {
			t.Fatalf("teardown sequence %v, want %v", sequence, want)
		}
	}
}

// TestDetachWaitsOutTheGuestAck covers the tracer-measured shape: the guest
// acknowledges the SCSI unplug asynchronously, so blockdev-del is refused
// with "busy" until the qdev is really gone.
func TestDetachWaitsOutTheGuestAck(t *testing.T) {
	driver := newTestDriver(t, shortTempDir(t))
	ctx := context.Background()
	if err := driver.q.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatal(err)
	}
	handler := &lazyAckHandler{inner: &qemuHandler{running: true}, acksAfter: 3}
	stateDir := driver.q.stateDir("vm-a")
	startQMPServer(t, &qmpServer{socket: qmpSocketPath(stateDir), handle: handler.handle})
	if err := driver.q.Prepare(ctx, "vm-a", Preparation{Lease: "lease-1", JITConfig: "jit"}); err != nil {
		t.Fatal(err)
	}
	if err := driver.q.Rendezvous(ctx, "vm-a", Rendezvous{
		Lease: "lease-1", WorkspaceDevice: "/dev/zvol/tank/ws/lease-1",
		ProcessDevice: "/dev/zvol/tank/process/lease-1", WorkspaceMountpoint: "/work",
	}); err != nil {
		t.Fatal(err)
	}
	client, err := dialQMP(ctx, qmpSocketPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := driver.q.detachVolume(deadline, client, workspaceNode, workspaceDevice); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if got := handler.deleteAttempts(); got < 3 {
		t.Fatalf("blockdev-del attempted %d times, want the busy window ridden out", got)
	}
}

// lazyAckHandler refuses blockdev-del the first acksAfter attempts even
// though device_del already returned, mimicking the guest's slow unplug ack.
type lazyAckHandler struct {
	inner     *qemuHandler
	mu        sync.Mutex
	acksAfter int
	deletes   int
}

func (h *lazyAckHandler) handle(command string, arguments json.RawMessage) ([]string, string) {
	if command == "blockdev-del" {
		h.mu.Lock()
		h.deletes++
		refuse := h.deletes < h.acksAfter
		h.mu.Unlock()
		if refuse {
			return nil, `{"error": {"class": "GenericError", "desc": "Node 'workspace' is in use"}, "id": %d}`
		}
	}
	return h.inner.handle(command, arguments)
}

func (h *lazyAckHandler) deleteAttempts() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.deletes
}
