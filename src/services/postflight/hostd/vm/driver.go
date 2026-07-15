package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
)

// QEMU is the real Driver: one QEMU/KVM process per VM, root disk cloned
// from the class's golden snapshot, workspace hot-attached over QMP by
// stable serial, destruction by QMP quit plus clone teardown.
//
// Every VM's identity lives in its state directory (meta.json, written
// before any side effect), never in this struct: Status and List are
// reconstructed from disk plus live probes (launcher liveness, QMP
// query-status, guestd observation), so a restarted hostd adopts running
// VMs — lease binding included — instead of leaking or killing them.
type QEMU struct {
	cfg   Config
	disks rootDisks
	// probeTimeout bounds every per-VM liveness probe (QMP query-status,
	// Guest.Observe) so one wedged VM can never hold the driver mutex — and
	// with it every verb on the host — indefinitely.
	probeTimeout time.Duration
	// quiesceTimeout bounds the guest-side sync+unmount ahead of a seal.
	quiesceTimeout time.Duration

	mu sync.Mutex
}

var _ Driver = (*QEMU)(nil)

// ClassConfig is one runner class's shape on this host.
type ClassConfig struct {
	CPUs      int
	MemoryMiB int
	// Image is the sealed golden snapshot root disks clone from, e.g.
	// tank/postflight/golden/noble@sealed.
	Image string
}

// Config is the driver's static wiring.
type Config struct {
	// StateRoot holds one directory per VM.
	StateRoot string
	// QEMUPath is the QEMU binary to launch.
	QEMUPath string
	// DatasetRoot is the parent dataset for per-VM root clones
	// (<DatasetRoot>/vm-<id>). It must exist.
	DatasetRoot string
	// Classes maps runner classes to their launch shape.
	Classes map[Class]ClassConfig
	// Launcher supervises the QEMU processes.
	Launcher Launcher
	// Guest is the guestd channel seam.
	Guest  Guest
	Logger *slog.Logger
}

func (c *Config) validate() error {
	switch {
	case c.StateRoot == "":
		return errors.New("vm: StateRoot is required")
	case c.QEMUPath == "":
		return errors.New("vm: QEMUPath is required")
	case c.DatasetRoot == "":
		return errors.New("vm: DatasetRoot is required")
	case len(c.Classes) == 0:
		return errors.New("vm: at least one class is required")
	case c.Launcher == nil:
		return errors.New("vm: Launcher is required")
	case c.Guest == nil:
		return errors.New("vm: Guest is required")
	}
	for class, shape := range c.Classes {
		if shape.CPUs <= 0 || shape.MemoryMiB <= 0 || shape.Image == "" {
			return fmt.Errorf("vm: class %s is underspecified", class)
		}
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return nil
}

// NewQEMU wires the driver.
func NewQEMU(cfg Config) (*QEMU, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.StateRoot, 0o750); err != nil {
		return nil, fmt.Errorf("vm: creating state root: %w", err)
	}
	return &QEMU{cfg: cfg, disks: zfsDisks{}, probeTimeout: 5 * time.Second, quiesceTimeout: 30 * time.Second}, nil
}

// meta is a VM's durable identity. It is written before any side effect, so
// everything the driver ever created is discoverable from disk alone.
type meta struct {
	ID    ID     `json:"id"`
	Class Class  `json:"class"`
	Lease string `json:"lease,omitempty"`
	// WorkspaceMountpoint is where the assignment told the guest to mount
	// the workspace; Quiesce needs it after a restart.
	WorkspaceMountpoint string `json:"workspace_mountpoint,omitempty"`
	// CID is the VM's vsock address for the guestd channel.
	CID uint32 `json:"cid"`
	// RootDataset is the per-VM root clone.
	RootDataset string `json:"root_dataset"`
	// Argv is the exact invocation; liveness probes match against it so a
	// recycled pid can never impersonate the VM.
	Argv []string `json:"argv"`
	// ArgvSHA256 fingerprints the invocation for reports and drift checks.
	ArgvSHA256 string `json:"argv_sha256"`
}

// errCorruptMeta marks a meta.json that exists but does not parse (fs
// damage, schema skew from a version change, a stray writer). Corruption of
// one VM must never wedge the driver: observation quarantines the VM as
// dead, destruction proceeds with the identity-safe subset, and CID
// allocation refuses to run blind.
var errCorruptMeta = errors.New("vm: corrupt meta")

var idRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

// validateID rejects VM IDs that are unsafe to splice into dataset names,
// filesystem paths, or QEMU option values.
func validateID(id ID) error {
	if !idRe.MatchString(string(id)) {
		return fmt.Errorf("vm: invalid id %q", id)
	}
	return nil
}

func (q *QEMU) stateDir(id ID) string    { return filepath.Join(q.cfg.StateRoot, string(id)) }
func (q *QEMU) metaPath(id ID) string    { return filepath.Join(q.stateDir(id), "meta.json") }
func (q *QEMU) rootDataset(id ID) string { return q.cfg.DatasetRoot + "/vm-" + string(id) }

func (q *QEMU) readMeta(id ID) (meta, error) {
	raw, err := os.ReadFile(q.metaPath(id))
	if err != nil {
		return meta{}, err
	}
	var m meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return meta{}, fmt.Errorf("%w for %s: %v", errCorruptMeta, id, err)
	}
	return m, nil
}

func (q *QEMU) writeMeta(m meta) error {
	encoded, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("vm: encoding meta for %s: %w", m.ID, err)
	}
	if err := writeFileAtomic(q.metaPath(m.ID), encoded); err != nil {
		return fmt.Errorf("vm: persisting meta for %s: %w", m.ID, err)
	}
	return nil
}

// Launch implements Driver.
func (q *QEMU) Launch(ctx context.Context, id ID, class Class) error {
	if err := validateID(id); err != nil {
		return err
	}
	shape, ok := q.cfg.Classes[class]
	if !ok {
		return fmt.Errorf("vm: unknown class %s", class)
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	if existing, err := q.readMeta(id); err == nil {
		if existing.Class != class {
			return fmt.Errorf("vm: %s already exists with class %s", id, existing.Class)
		}
		alive, err := q.cfg.Launcher.Alive(ctx, id, q.stateDir(id), existing.Argv)
		if err != nil {
			return err
		}
		if alive {
			return nil
		}
		// The process is gone (a crash, or a launch that never got to
		// Start). Never reuse: clear the leftovers and launch fresh below.
		if err := q.destroyLocked(ctx, id); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	cid, err := q.allocateCIDLocked()
	if err != nil {
		return err
	}
	dir := q.stateDir(id)
	dataset := q.rootDataset(id)
	spec := LaunchSpec{
		QEMUPath:   q.cfg.QEMUPath,
		ID:         id,
		CPUs:       shape.CPUs,
		MemoryMiB:  shape.MemoryMiB,
		RootDevice: zvolDevicePath(dataset),
		StateDir:   dir,
		VsockCID:   cid,
	}
	argv := spec.Argv()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("vm: creating state dir for %s: %w", id, err)
	}
	record := meta{
		ID:          id,
		Class:       class,
		CID:         cid,
		RootDataset: dataset,
		Argv:        argv,
		ArgvSHA256:  argvDigest(argv),
	}
	if err := q.writeMeta(record); err != nil {
		return err
	}
	if err := q.disks.Ensure(ctx, dataset, shape.Image); err != nil {
		return err
	}
	return q.cfg.Launcher.Start(ctx, id, dir, argv)
}

func argvDigest(argv []string) string {
	digest := sha256.New()
	for _, arg := range argv {
		digest.Write([]byte(arg))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// allocateCIDLocked picks the lowest vsock CID not claimed by any persisted
// VM. CIDs 0-2 are reserved by the vsock spec.
func (q *QEMU) allocateCIDLocked() (uint32, error) {
	used := map[uint32]bool{}
	entries, err := os.ReadDir(q.cfg.StateRoot)
	if err != nil {
		return 0, fmt.Errorf("vm: scanning state root: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record, err := q.readMeta(ID(entry.Name()))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // a dir with no meta never launched anything
			}
			// An unreadable meta may belong to a live QEMU holding an
			// unknown CID; allocating blind risks a vhost-vsock collision.
			// List collects the corrupt VM, unblocking the next launch.
			return 0, fmt.Errorf("vm: cid inventory: %w", err)
		}
		used[record.CID] = true
	}
	cid := uint32(3)
	for used[cid] {
		cid++
	}
	return cid, nil
}

// Assign implements Driver. The lease binding is persisted before the
// hot-attach: an ambiguous failure (attached or delivered, then an error)
// must leave a VM that still names its lease, so recovery destroys it
// through the lease's failure path instead of stranding a live runner.
func (q *QEMU) Assign(ctx context.Context, id ID, assignment Assignment) error {
	if err := validateID(id); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	record, err := q.readMeta(id)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if record.Lease != "" && record.Lease != assignment.Lease {
		return fmt.Errorf("vm: %s already assigned to lease %s", id, record.Lease)
	}
	if record.Lease == "" {
		record.Lease = assignment.Lease
		record.WorkspaceMountpoint = assignment.WorkspaceMountpoint
		if err := q.writeMeta(record); err != nil {
			return err
		}
	}

	client, err := dialQMP(ctx, qmpSocketPath(q.stateDir(id)))
	if err != nil {
		return err
	}
	defer client.Close()
	if err := q.attachWorkspace(ctx, client, assignment.WorkspaceDevice); err != nil {
		return err
	}
	deliverCtx, cancel := context.WithTimeout(ctx, q.probeTimeout)
	defer cancel()
	return q.cfg.Guest.Deliver(deliverCtx, id, record.CID, guestproto.Assignment{
		Lease: assignment.Lease,
		Mounts: []guestproto.Mount{{
			Serial:     workspaceNode,
			Filesystem: workspaceFilesystem,
			Mountpoint: assignment.WorkspaceMountpoint,
			Options:    workspaceMountOptions,
		}},
		JITConfig: assignment.JITConfig,
		Env:       assignment.Env,
	})
}

// Quiesce implements Driver. The guest call runs outside the driver mutex —
// a sync of dirty pages can take seconds and must not wedge every other
// verb on the host — under its own bound.
func (q *QEMU) Quiesce(ctx context.Context, id ID) error {
	if err := validateID(id); err != nil {
		return err
	}
	q.mu.Lock()
	record, err := q.readMeta(id)
	q.mu.Unlock()
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if record.WorkspaceMountpoint == "" {
		return fmt.Errorf("vm: %s has no workspace to quiesce", id)
	}
	quiesceCtx, cancel := context.WithTimeout(ctx, q.quiesceTimeout)
	defer cancel()
	return q.cfg.Guest.Quiesce(quiesceCtx, id, record.CID, record.WorkspaceMountpoint)
}

// attachWorkspace hot-attaches the workspace device by stable serial,
// observing before acting on both the blockdev and qdev layers so a
// repeated Assign converges instead of erroring.
func (q *QEMU) attachWorkspace(ctx context.Context, client *qmpClient, device string) error {
	attached, err := workspaceBlockdevPresent(ctx, client)
	if err != nil {
		return err
	}
	if !attached {
		arguments := map[string]any{
			"driver":    "raw",
			"node-name": workspaceNode,
			"file": map[string]any{
				"driver":   "host_device",
				"filename": device,
				"cache":    map[string]any{"direct": true},
				"aio":      "native",
			},
		}
		if _, err := client.Execute(ctx, "blockdev-add", arguments); err != nil {
			return err
		}
	}
	present, err := workspaceDevicePresent(ctx, client)
	if err != nil {
		return err
	}
	if !present {
		arguments := map[string]any{
			"driver": "scsi-hd",
			"id":     workspaceDevice,
			"drive":  workspaceNode,
			"bus":    "scsi0.0",
			"serial": workspaceNode,
		}
		if _, err := client.Execute(ctx, "device_add", arguments); err != nil {
			return err
		}
	}
	return nil
}

func workspaceBlockdevPresent(ctx context.Context, client *qmpClient) (bool, error) {
	result, err := client.Execute(ctx, "query-named-block-nodes", nil)
	if err != nil {
		return false, err
	}
	var nodes []struct {
		NodeName string `json:"node-name"`
	}
	if err := json.Unmarshal(result, &nodes); err != nil {
		return false, fmt.Errorf("vm: parsing block nodes: %w", err)
	}
	for _, node := range nodes {
		if node.NodeName == workspaceNode {
			return true, nil
		}
	}
	return false, nil
}

func workspaceDevicePresent(ctx context.Context, client *qmpClient) (bool, error) {
	result, err := client.Execute(ctx, "qom-list", map[string]any{"path": "/machine/peripheral"})
	if err != nil {
		return false, err
	}
	var properties []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(result, &properties); err != nil {
		return false, fmt.Errorf("vm: parsing peripherals: %w", err)
	}
	for _, property := range properties {
		if property.Name == workspaceDevice {
			return true, nil
		}
	}
	return false, nil
}

// detachWorkspace unplugs the workspace and releases its zvol. The guest
// acks the SCSI unplug asynchronously, so blockdev-del reports "in use"
// until it does — typically one to three seconds.
func (q *QEMU) detachWorkspace(ctx context.Context, client *qmpClient) error {
	present, err := workspaceDevicePresent(ctx, client)
	if err != nil {
		return err
	}
	if present {
		if _, err := client.Execute(ctx, "device_del", map[string]any{"id": workspaceDevice}); err != nil {
			return err
		}
	}
	attached, err := workspaceBlockdevPresent(ctx, client)
	if err != nil {
		return err
	}
	if !attached {
		return nil
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := client.Execute(ctx, "blockdev-del", map[string]any{"node-name": workspaceNode})
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("vm: workspace never released: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Status implements Driver.
func (q *QEMU) Status(ctx context.Context, id ID) (Status, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	status, _, err := q.observeLocked(ctx, id)
	return status, err
}

// observeLocked reconstructs one VM's status from disk and live probes.
// dead reports a VM whose meta survives but whose process is gone.
func (q *QEMU) observeLocked(ctx context.Context, id ID) (Status, bool, error) {
	record, err := q.readMeta(id)
	if errors.Is(err, os.ErrNotExist) {
		return Status{ID: id, Phase: PhaseGone}, false, nil
	}
	if errors.Is(err, errCorruptMeta) {
		// Nothing in the file can be trusted; report the VM dead so List
		// collects it instead of erroring every verb on the whole host.
		q.cfg.Logger.Error("quarantining vm with corrupt meta", "vm", id, "err", err)
		return Status{ID: id, Phase: PhaseGone}, true, nil
	}
	if err != nil {
		return Status{}, false, err
	}
	status := Status{ID: id, Class: record.Class, Phase: PhaseBooting, Lease: record.Lease}
	if record.Lease != "" {
		status.Phase = PhaseAssigned
	}
	alive, err := q.cfg.Launcher.Alive(ctx, id, q.stateDir(id), record.Argv)
	if err != nil {
		return Status{}, false, err
	}
	if !alive {
		return Status{ID: id, Class: record.Class, Phase: PhaseGone, Lease: record.Lease}, true, nil
	}
	if !q.vmRunning(ctx, id) {
		// QEMU exists but is not (yet) running the guest; nothing further
		// can be trusted, so report the phase the meta alone supports.
		return status, false, nil
	}
	observeCtx, cancel := context.WithTimeout(ctx, q.probeTimeout)
	observation, err := q.cfg.Guest.Observe(observeCtx, id, record.CID)
	cancel()
	if err != nil {
		q.cfg.Logger.Warn("guest observation failed", "vm", id, "err", err)
		return status, false, nil
	}
	switch {
	case observation.RunnerExited:
		status.Phase = PhaseExited
		status.ExitCode = observation.ExitCode
	case observation.RunnerRegistered:
		status.Phase = PhaseReady
	case record.Lease != "":
		status.Phase = PhaseAssigned
	case observation.Hello:
		status.Phase = PhaseWarm
	default:
		status.Phase = PhaseBooting
	}
	return status, false, nil
}

// vmRunning probes QMP for a running guest.
func (q *QEMU) vmRunning(ctx context.Context, id ID) bool {
	probeCtx, cancel := context.WithTimeout(ctx, q.probeTimeout)
	defer cancel()
	client, err := dialQMP(probeCtx, qmpSocketPath(q.stateDir(id)))
	if err != nil {
		return false
	}
	defer client.Close()
	result, err := client.Execute(probeCtx, "query-status", nil)
	if err != nil {
		return false
	}
	var reply struct {
		Running bool `json:"running"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return false
	}
	return reply.Running
}

// List implements Driver. A VM whose process died is not a VM anymore: its
// leftovers (root clone, state dir) are collected here and it is omitted,
// which is exactly the disappearance the agent's failure paths key on.
func (q *QEMU) List(ctx context.Context) ([]Status, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries, err := os.ReadDir(q.cfg.StateRoot)
	if err != nil {
		return nil, fmt.Errorf("vm: scanning state root: %w", err)
	}
	var statuses []Status
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := ID(entry.Name())
		status, dead, err := q.observeLocked(ctx, id)
		if err != nil {
			return nil, err
		}
		if dead || status.Phase == PhaseGone {
			if err := q.destroyLocked(ctx, id); err != nil {
				q.cfg.Logger.Error("collecting dead vm", "vm", id, "err", err)
				statuses = append(statuses, status)
			}
			continue
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// Destroy implements Driver.
func (q *QEMU) Destroy(ctx context.Context, id ID) error {
	if err := validateID(id); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.destroyLocked(ctx, id)
}

func (q *QEMU) destroyLocked(ctx context.Context, id ID) error {
	dir := q.stateDir(id)
	record, err := q.readMeta(id)
	corrupt := errors.Is(err, errCorruptMeta)
	if err != nil && !corrupt && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil || corrupt {
		// Release the workspace first when QMP still answers: quit would
		// free it too, but only after the process dies, and the seal that
		// follows destruction should never race the kernel's zvol release.
		if client, dialErr := dialQMP(ctx, qmpSocketPath(dir)); dialErr == nil {
			if detachErr := q.detachWorkspace(ctx, client); detachErr != nil {
				q.cfg.Logger.Warn("workspace detach during destroy", "vm", id, "err", detachErr)
			}
			// QEMU may exit before acknowledging quit; that is success.
			_, _ = client.Execute(ctx, "quit", nil)
			client.Close()
		}
		if corrupt {
			// The meta no longer names the argv the Launcher needs to
			// identify the process, so the QMP quit above is the only
			// identity-safe kill. If a live QEMU survived it, the dataset
			// destroy below fails busy, keeping this dir — and its unknown
			// CID claim — in place until the process is really gone.
		} else if err := q.cfg.Launcher.Kill(ctx, id, dir, record.Argv); err != nil {
			return err
		}
		dataset := record.RootDataset
		if corrupt {
			dataset = q.rootDataset(id)
		}
		if err := q.disks.Destroy(ctx, dataset); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("vm: removing state dir for %s: %w", id, err)
	}
	return nil
}
