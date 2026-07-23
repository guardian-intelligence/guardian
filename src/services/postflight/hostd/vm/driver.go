package vm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/timing"
)

// QEMU is the real Driver: one QEMU/KVM process per VM, root disk cloned
// from the class's golden snapshot, workspace hot-attached over QMP by
// stable serial, destruction by QMP quit plus clone teardown.
//
// Every VM's identity lives in its state directory (meta.json, written
// before any side effect), never in this struct: Status and List are
// reconstructed from disk plus live probes (launcher liveness, QMP
// query-status, guestd observation), so a restarted hostd adopts running
// VMs — local assignment binding included — instead of leaking or killing them.
type QEMU struct {
	cfg   Config
	disks rootDisks
	// probeTimeout bounds QMP control operations. guestProbeTimeout is much
	// shorter because List probes every pool member serially and an absent
	// guest agent is an expected state while firmware is booting.
	probeTimeout      time.Duration
	guestProbeTimeout time.Duration
	// bootTimeout bounds the interval from QEMU launch to the first guestd
	// hello. A live QEMU process parked in firmware is not a usable worker and
	// must be collected so the pool controller can replace it.
	bootTimeout time.Duration
	// quiesceTimeout bounds the guest-side checkpoint and flush ahead of a seal.
	quiesceTimeout time.Duration

	mu       sync.Mutex
	lockMu   sync.Mutex
	locks    map[ID]*vmOperationLock
	timingMu sync.Mutex
	timing   *timing.Recorder
	timings  map[ID][]TimingPoint
}

type vmOperationLock struct {
	mu   sync.Mutex
	refs int
}

var _ Driver = (*QEMU)(nil)
var _ UpdateSource = (*QEMU)(nil)

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
	// Firmware is the pinned AmdSev OVMF.fd used by every confidential VM.
	Firmware string
	// DatasetRoot is the parent dataset for per-VM root clones
	// (<DatasetRoot>/vm-<id>). It must exist.
	DatasetRoot string
	// Classes maps runner classes to their launch shape.
	Classes map[Class]ClassConfig
	// Launcher supervises the QEMU processes.
	Launcher Launcher
	// Guest is the guestd channel seam.
	Guest Guest
	// GuestNetwork selects every VM's egress datapath (see LaunchSpec).
	GuestNetwork string
	// TapLifecycle creates and destroys the host interface owned by a tap VM.
	// It is required for the tap datapath and may also clean up tap VMs adopted
	// across a hostd configuration change.
	TapLifecycle TapLifecycle
	Logger       *slog.Logger
	Timing       *timing.Recorder
}

func (c *Config) validate() error {
	switch {
	case c.StateRoot == "":
		return errors.New("vm: StateRoot is required")
	case c.QEMUPath == "":
		return errors.New("vm: QEMUPath is required")
	case c.Firmware == "":
		return errors.New("vm: Firmware is required")
	case c.DatasetRoot == "":
		return errors.New("vm: DatasetRoot is required")
	case len(c.Classes) == 0:
		return errors.New("vm: at least one class is required")
	case c.Launcher == nil:
		return errors.New("vm: Launcher is required")
	case c.Guest == nil:
		return errors.New("vm: Guest is required")
	case c.GuestNetwork != "" && c.GuestNetwork != "none" && c.GuestNetwork != guestNetworkUser && c.GuestNetwork != guestNetworkTap:
		return fmt.Errorf("vm: unknown GuestNetwork %q", c.GuestNetwork)
	case c.GuestNetwork == guestNetworkTap && c.TapLifecycle == nil:
		return errors.New("vm: TapLifecycle is required for tap networking")
	}
	for class, shape := range c.Classes {
		if shape.CPUs <= 0 || shape.MemoryMiB <= 0 || shape.Image == "" {
			return fmt.Errorf("vm: class %s is underspecified", class)
		}
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Timing == nil {
		bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
		if err != nil {
			return fmt.Errorf("vm: read boot id: %w", err)
		}
		recorderID, err := newIncarnation()
		if err != nil {
			return err
		}
		c.Timing, err = timing.New("hostd-qemu:"+recorderID, strings.TrimSpace(string(bootID)))
		if err != nil {
			return err
		}
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
	return &QEMU{
		cfg: cfg, disks: zfsDisks{}, probeTimeout: 5 * time.Second,
		guestProbeTimeout: 250 * time.Millisecond, bootTimeout: 2 * time.Minute,
		quiesceTimeout: 5*time.Minute + 30*time.Second,
		timing:         cfg.Timing, timings: map[ID][]TimingPoint{}, locks: map[ID]*vmOperationLock{},
	}, nil
}

func (q *QEMU) lockVM(id ID) func() {
	q.lockMu.Lock()
	lock := q.locks[id]
	if lock == nil {
		lock = &vmOperationLock{}
		q.locks[id] = lock
	}
	lock.refs++
	q.lockMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		q.lockMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(q.locks, id)
		}
		q.lockMu.Unlock()
	}
}

func (q *QEMU) recordTiming(id ID, event string) {
	q.timingMu.Lock()
	defer q.timingMu.Unlock()
	q.recordTimingLocked(id, event)
}

func (q *QEMU) recordTimingLocked(id ID, event string) {
	point := q.timing.Point(event)
	q.timings[id] = append(q.timings[id], TimingPoint{
		Event: point.Event, Source: point.Source, BootID: point.BootID,
		Sequence: point.Sequence, MonotonicNS: point.MonotonicNS, UnixNS: point.UnixNS,
	})
}

func (q *QEMU) recordTimingOnce(id ID, event string) {
	q.timingMu.Lock()
	defer q.timingMu.Unlock()
	for _, point := range q.timings[id] {
		if point.Event == event {
			return
		}
	}
	q.recordTimingLocked(id, event)
}

func (q *QEMU) timingFor(id ID) []TimingPoint {
	q.timingMu.Lock()
	defer q.timingMu.Unlock()
	return append([]TimingPoint(nil), q.timings[id]...)
}

// Updates delegates guest-local lifecycle edges. QMP-only transitions are
// initiated by hostd itself and are observed by the convergence pass that
// follows the verb.
func (q *QEMU) Updates() <-chan ID {
	if source, ok := q.cfg.Guest.(UpdateSource); ok {
		return source.Updates()
	}
	return nil
}

// meta is a VM's durable identity. It is written before any side effect, so
// everything the driver ever created is discoverable from disk alone.
type meta struct {
	ID            ID          `json:"id"`
	Class         Class       `json:"class"`
	Image         string      `json:"image,omitempty"`
	Incarnation   string      `json:"incarnation"`
	MemberID      string      `json:"member_id,omitempty"`
	Assignment    *Assignment `json:"assignment,omitempty"`
	AssignmentID  string      `json:"assignment_id,omitempty"`
	CreatedUnixNS int64       `json:"created_unix_ns,omitempty"`
	// WorkspaceMountpoint is where the assignment told the guest to mount
	// the workspace; Quiesce needs it after a restart.
	WorkspaceMountpoint string `json:"workspace_mountpoint,omitempty"`
	ToolMountpoint      string `json:"tool_mountpoint,omitempty"`
	ProcessMountpoint   string `json:"process_mountpoint,omitempty"`
	// CID is the VM's vsock address for the guestd channel.
	CID uint32 `json:"cid"`
	// TapName is the host interface this VM owns. Persisting it makes cleanup
	// independent of hostd's current network mode after a restart.
	TapName string `json:"tap_name,omitempty"`
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
	unlockVM := q.lockVM(id)
	defer unlockVM()

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
		QEMUPath:     q.cfg.QEMUPath,
		ID:           id,
		CPUs:         shape.CPUs,
		MemoryMiB:    shape.MemoryMiB,
		RootDevice:   zvolDevicePath(dataset),
		Firmware:     q.cfg.Firmware,
		StateDir:     dir,
		VsockCID:     cid,
		GuestNetwork: q.cfg.GuestNetwork,
	}
	argv := spec.Argv()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("vm: creating state dir for %s: %w", id, err)
	}
	incarnation, err := newIncarnation()
	if err != nil {
		return err
	}
	record := meta{
		ID: id, Class: class, Image: shape.Image, CreatedUnixNS: time.Now().UnixNano(),
		Incarnation: incarnation, CID: cid, RootDataset: dataset, Argv: argv, ArgvSHA256: argvDigest(argv),
	}
	if q.cfg.GuestNetwork == guestNetworkTap {
		record.TapName, _ = tapIdentity(id, cid)
	}
	q.recordTiming(id, "vm_launch_started")
	if err := q.writeMeta(record); err != nil {
		return err
	}
	rootDevice, err := q.disks.Ensure(ctx, dataset, shape.Image)
	if err != nil {
		return err
	}
	// The durable pre-side-effect record above makes a crash during cloning
	// collectable. Once udev has published the zvol, replace its mutable link
	// with the canonical whole-disk path before QEMU can observe it.
	spec.RootDevice = rootDevice
	argv = spec.Argv()
	record.Argv = argv
	record.ArgvSHA256 = argvDigest(argv)
	if err := q.writeMeta(record); err != nil {
		return err
	}
	if record.TapName != "" {
		if err := q.cfg.TapLifecycle.Up(ctx, record.TapName); err != nil {
			return errors.Join(
				fmt.Errorf("vm: preparing tap %s for %s: %w", record.TapName, id, err),
				q.cfg.TapLifecycle.Down(ctx, record.TapName),
			)
		}
	}
	if err := q.cfg.Launcher.Start(ctx, id, dir, argv); err != nil {
		if record.TapName == "" {
			return err
		}
		return errors.Join(err, q.cfg.TapLifecycle.Down(ctx, record.TapName))
	}
	q.recordTiming(id, "qemu_started")
	return nil
}

func newIncarnation() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("vm: generating incarnation: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
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

// Prepare implements Driver. The opaque pool-member identity is persisted
// before delivery so recovery never mistakes a registered runner for an idle
// VM.
func (q *QEMU) Prepare(ctx context.Context, id ID, preparation Preparation) error {
	if err := validateID(id); err != nil {
		return err
	}
	unlockVM := q.lockVM(id)
	defer unlockVM()
	record, err := q.readMeta(id)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if record.MemberID != "" && record.MemberID != preparation.MemberID {
		return fmt.Errorf("vm: %s already prepared as member %s", id, record.MemberID)
	}
	if record.MemberID == "" {
		record.MemberID = preparation.MemberID
		if err := q.writeMeta(record); err != nil {
			return err
		}
	}
	deliverCtx, cancel := context.WithTimeout(ctx, q.probeTimeout)
	defer cancel()
	q.recordTimingOnce(id, "listener_prepare_started")
	if err := q.cfg.Guest.Prepare(deliverCtx, id, record.CID, guestproto.Prepare{
		MemberID: preparation.MemberID, JITConfig: preparation.JITConfig, Env: preparation.Env,
	}); err != nil {
		return err
	}
	q.recordTimingOnce(id, "listener_prepare_sent")
	return nil
}

// Rendezvous implements Driver. The repository-scoped generation is attached
// and restored after local assignment and before Runner.Worker is released.
func (q *QEMU) Rendezvous(ctx context.Context, id ID, rendezvous Rendezvous) error {
	if err := validateID(id); err != nil {
		return err
	}
	if rendezvous.WorkspaceDevice == "" || rendezvous.ToolDevice == "" || rendezvous.ProcessDevice == "" || rendezvous.WorkspaceMountpoint == "" {
		return errors.New("vm: rendezvous requires workspace, tool, and process devices plus a workspace mountpoint")
	}
	if rendezvous.WorkspaceDevice == rendezvous.ToolDevice || rendezvous.WorkspaceDevice == rendezvous.ProcessDevice || rendezvous.ToolDevice == rendezvous.ProcessDevice {
		return errors.New("vm: workspace, tool, and process devices must be distinct")
	}
	if (rendezvous.CheckpointDigest == "") != (rendezvous.CheckpointVersion == "") {
		return errors.New("vm: checkpoint digest and version must be supplied together")
	}
	unlockVM := q.lockVM(id)
	defer unlockVM()
	q.recordTiming(id, "qmp_rendezvous_started")
	record, err := q.readMeta(id)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if record.MemberID != rendezvous.MemberID || rendezvous.AssignmentID == "" {
		return fmt.Errorf("vm: %s is member %s, not %s", id, record.MemberID, rendezvous.MemberID)
	}
	if record.Assignment == nil {
		return fmt.Errorf("vm: %s has no locally observed assignment", id)
	}
	if record.AssignmentID != "" && record.AssignmentID != rendezvous.AssignmentID {
		return fmt.Errorf("vm: %s already bound to assignment %s", id, record.AssignmentID)
	}
	if record.AssignmentID == "" {
		record.AssignmentID = rendezvous.AssignmentID
		if err := q.writeMeta(record); err != nil {
			return err
		}
	}
	if record.WorkspaceMountpoint == "" {
		record.WorkspaceMountpoint = rendezvous.WorkspaceMountpoint
		record.ToolMountpoint = runnerStateMountpoint
		record.ProcessMountpoint = processMountpoint
		if err := q.writeMeta(record); err != nil {
			return err
		}
	} else if record.WorkspaceMountpoint != rendezvous.WorkspaceMountpoint {
		return fmt.Errorf("vm: %s already bound at %s", id, record.WorkspaceMountpoint)
	}

	client, err := dialQMP(ctx, qmpSocketPath(q.stateDir(id)))
	if err != nil {
		return err
	}
	defer client.Close()
	q.recordTiming(id, "qmp_connected")
	attachments := []struct {
		role, node, qdev, device string
	}{
		{"workspace", workspaceNode, workspaceDevice, rendezvous.WorkspaceDevice},
		{"tool", toolNode, toolDevice, rendezvous.ToolDevice},
		{"process", processNode, processDevice, rendezvous.ProcessDevice},
	}
	attachCtx, cancelAttach := context.WithCancel(ctx)
	defer cancelAttach()
	errs := make([]error, len(attachments))
	var attachWait sync.WaitGroup
	attachWait.Add(len(attachments))
	for index := range attachments {
		index := index
		go func() {
			defer attachWait.Done()
			attachment := attachments[index]
			started := time.Now()
			errs[index] = q.attachVolume(attachCtx, client, attachment.node, attachment.qdev, attachment.device)
			if errs[index] != nil {
				cancelAttach()
			}
			q.cfg.Logger.Info("postflight.hostd.qmp.volume_attached", "vm", id, "role", attachment.role,
				"duration_ns", time.Since(started).Nanoseconds(), "error", errs[index])
		}()
	}
	attachWait.Wait()
	for index, err := range errs {
		if err != nil {
			return fmt.Errorf("attach %s volume: %w", attachments[index].role, err)
		}
		q.recordTiming(id, attachments[index].role+"_device_attached")
	}
	deliverCtx, cancel := context.WithTimeout(ctx, q.probeTimeout)
	defer cancel()
	request := guestproto.Rendezvous{
		MemberID: rendezvous.MemberID, AssignmentID: rendezvous.AssignmentID,
		Mounts: []guestproto.Mount{
			{
				Serial:     toolNode,
				Filesystem: workspaceFilesystem,
				Mountpoint: runnerStateMountpoint,
				Options:    toolMountOptions,
			}, {
				Serial:     workspaceNode,
				Filesystem: workspaceFilesystem,
				Mountpoint: rendezvous.WorkspaceMountpoint,
				Options:    workspaceMountOptions,
			}, {
				Serial:     processNode,
				Filesystem: workspaceFilesystem,
				Mountpoint: processMountpoint,
				Options:    processMountOptions,
			}},
	}
	if rendezvous.CheckpointDigest != "" {
		request.Checkpoint = &guestproto.CheckpointRestore{
			ImagesDir: processImagesDir, ExpectedDigest: rendezvous.CheckpointDigest,
			ExpectedVersion: rendezvous.CheckpointVersion,
			ExternalMounts: []guestproto.CheckpointMount{
				{Key: workspaceNode, Path: rendezvous.WorkspaceMountpoint},
				{Key: toolNode, Path: runnerStateMountpoint},
			},
		}
	}
	if err := q.cfg.Guest.Rendezvous(deliverCtx, id, record.CID, request); err != nil {
		return err
	}
	q.recordTiming(id, "guest_rendezvous_sent")
	return nil
}

// Authorize implements Driver.
func (q *QEMU) Authorize(ctx context.Context, id ID, authorization Authorization) error {
	if err := validateID(id); err != nil {
		return err
	}
	unlockVM := q.lockVM(id)
	defer unlockVM()
	record, err := q.readMeta(id)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if record.MemberID != authorization.MemberID || record.AssignmentID != authorization.AssignmentID ||
		record.Assignment == nil || record.Assignment.RequestID != authorization.RequestID || record.WorkspaceMountpoint == "" {
		return fmt.Errorf("vm: %s is not generation-bound for member %s", id, authorization.MemberID)
	}
	deliverCtx, cancel := context.WithTimeout(ctx, q.probeTimeout)
	defer cancel()
	return q.cfg.Guest.Authorize(deliverCtx, id, record.CID, guestproto.Authorize{
		MemberID: authorization.MemberID, AssignmentID: authorization.AssignmentID, RequestID: authorization.RequestID,
		Identity: &guestproto.JobIdentity{
			RunID: authorization.Identity.RunID, RunAttempt: authorization.Identity.RunAttempt,
			RunnerName: authorization.Identity.RunnerName, Repository: authorization.Identity.Repository,
			WorkflowJob: authorization.Identity.WorkflowJob,
		},
		Env: authorization.Env,
	})
}

// Quiesce implements Driver. The guest call runs outside the driver mutex —
// a sync of dirty pages can take seconds and must not wedge every other
// verb on the host — under its own bound.
func (q *QEMU) Quiesce(ctx context.Context, id ID) (CheckpointArtifact, error) {
	if err := validateID(id); err != nil {
		return CheckpointArtifact{}, err
	}
	unlockVM := q.lockVM(id)
	defer unlockVM()
	record, err := q.readMeta(id)
	if err == nil {
		q.recordTiming(id, "quiesce_rpc_started")
	}
	if errors.Is(err, os.ErrNotExist) {
		return CheckpointArtifact{}, ErrNotFound
	}
	if err != nil {
		return CheckpointArtifact{}, err
	}
	if record.WorkspaceMountpoint == "" {
		return CheckpointArtifact{}, fmt.Errorf("vm: %s has no workspace to quiesce", id)
	}
	quiesceCtx, cancel := context.WithTimeout(ctx, q.quiesceTimeout)
	defer cancel()
	reply, err := q.cfg.Guest.Quiesce(quiesceCtx, id, record.CID, guestproto.Quiesce{
		Mountpoints: []string{runnerStateMountpoint, record.WorkspaceMountpoint, processMountpoint},
		Checkpoint: &guestproto.CheckpointDump{
			ImagesDir: processImagesDir,
			ExternalMounts: []guestproto.CheckpointMount{
				{Key: workspaceNode, Path: record.WorkspaceMountpoint},
				{Key: toolNode, Path: runnerStateMountpoint},
			},
		},
	})
	q.timingMu.Lock()
	q.timings[id] = append(q.timings[id], timingPoints(reply.Timing)...)
	if err != nil {
		q.recordTimingLocked(id, "quiesce_rpc_failed")
		checkpointTiming := append([]TimingPoint(nil), q.timings[id]...)
		q.timingMu.Unlock()
		return CheckpointArtifact{Timing: checkpointTiming}, err
	}
	q.recordTimingLocked(id, "quiesce_rpc_completed")
	checkpointTiming := append([]TimingPoint(nil), q.timings[id]...)
	q.timingMu.Unlock()
	if reply.Checkpoint == nil {
		return CheckpointArtifact{}, errors.New("vm: guest quiesced without a checkpoint artifact")
	}
	return CheckpointArtifact{
		Digest: reply.Checkpoint.Digest, Version: reply.Checkpoint.Version,
		Timing: checkpointTiming,
	}, nil
}

// attachVolume hot-attaches a device by stable serial,
// observing before acting on both the blockdev and qdev layers so a
// repeated Assign converges instead of erroring.
func (q *QEMU) attachVolume(ctx context.Context, client *qmpClient, node, qdev, device string) error {
	attached, err := blockdevPresent(ctx, client, node)
	if err != nil {
		return err
	}
	if !attached {
		arguments := map[string]any{
			"driver":    "raw",
			"node-name": node,
			"file": map[string]any{
				"driver":   "host_device",
				"filename": device,
				"cache":    map[string]any{"direct": true},
				"aio":      "native",
				// Guest discards must reach the sparse zvol: the mount
				// options promise TRIM, and the encryption ladder erases
				// plaintext lineages with a whole-device discard before
				// formatting. QEMU's default is discard=ignore.
				"discard": "unmap",
			},
		}
		if _, err := client.Execute(ctx, "blockdev-add", arguments); err != nil {
			return err
		}
	}
	present, err := devicePresent(ctx, client, qdev)
	if err != nil {
		return err
	}
	if !present {
		arguments := map[string]any{
			"driver": "scsi-hd",
			"id":     qdev,
			"drive":  node,
			"bus":    "scsi0.0",
			"serial": node,
		}
		if _, err := client.Execute(ctx, "device_add", arguments); err != nil {
			return err
		}
	}
	return nil
}

func blockdevPresent(ctx context.Context, client *qmpClient, expected string) (bool, error) {
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
		if node.NodeName == expected {
			return true, nil
		}
	}
	return false, nil
}

func devicePresent(ctx context.Context, client *qmpClient, expected string) (bool, error) {
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
		if property.Name == expected {
			return true, nil
		}
	}
	return false, nil
}

// detachVolume unplugs a volume and releases its zvol. The guest
// acks the SCSI unplug asynchronously, so blockdev-del reports "in use"
// until it does — typically one to three seconds.
func (q *QEMU) detachVolume(ctx context.Context, client *qmpClient, node, qdev string) error {
	present, err := devicePresent(ctx, client, qdev)
	if err != nil {
		return err
	}
	if present {
		if _, err := client.Execute(ctx, "device_del", map[string]any{"id": qdev}); err != nil {
			return err
		}
	}
	attached, err := blockdevPresent(ctx, client, node)
	if err != nil {
		return err
	}
	if !attached {
		return nil
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := client.Execute(ctx, "blockdev-del", map[string]any{"node-name": node})
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("vm: volume %s never released: %w", node, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Status implements Driver.
func (q *QEMU) Status(ctx context.Context, id ID) (Status, error) {
	unlockVM := q.lockVM(id)
	defer unlockVM()
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
	status := Status{
		ID: id, Class: record.Class, Image: record.Image,
		Phase: PhaseBooting, Incarnation: record.Incarnation, MemberID: record.MemberID,
	}
	if record.Assignment != nil {
		status.Assignment = *record.Assignment
	}
	if record.MemberID != "" {
		status.Phase = PhaseAssigned
	}
	alive, err := q.cfg.Launcher.Alive(ctx, id, q.stateDir(id), record.Argv)
	if err != nil {
		return Status{}, false, err
	}
	if !alive {
		return Status{
			ID: id, Class: record.Class, Image: record.Image,
			Phase: PhaseGone, Incarnation: record.Incarnation, MemberID: record.MemberID,
		}, true, nil
	}
	hypervisor := q.hypervisorStatus(ctx, id)
	if hypervisor.Status == "io-error" {
		q.recordTimingOnce(id, "qmp_io_error_observed")
		status.Phase = PhaseRecycleRequired
		status.FailureReason = hypervisor.failureReason()
		status.Timing = append(status.Timing, q.timingFor(id)...)
		q.cfg.Logger.Error("postflight.hostd.vm.io_error",
			"vm", id, "member_id", record.MemberID, "assignment_id", record.AssignmentID,
			"qmp_status", hypervisor.Status, "block_io_errors", hypervisor.blockErrorSummary())
		return status, false, nil
	}
	if !hypervisor.Running {
		// QEMU exists but is not (yet) running the guest; nothing further
		// can be trusted, so report the phase the meta alone supports.
		if record.MemberID == "" && q.bootExpired(id, record) {
			q.cfg.Logger.Error("guest boot deadline exceeded", "vm", id, "deadline", q.bootTimeout)
			return Status{ID: id, Class: record.Class, Image: record.Image, Phase: PhaseGone}, true, nil
		}
		status.Timing = append(status.Timing, q.timingFor(id)...)
		return status, false, nil
	}
	observeCtx, cancel := context.WithTimeout(ctx, q.guestProbeTimeout)
	observation, err := q.cfg.Guest.Observe(observeCtx, id, record.CID)
	cancel()
	if err != nil {
		q.cfg.Logger.Warn("guest observation failed", "vm", id, "err", err)
		if record.MemberID == "" && q.bootExpired(id, record) {
			q.cfg.Logger.Error("guest boot deadline exceeded", "vm", id, "deadline", q.bootTimeout)
			return Status{ID: id, Class: record.Class, Image: record.Image, Phase: PhaseGone}, true, nil
		}
		status.Timing = append(status.Timing, q.timingFor(id)...)
		return status, false, nil
	}
	if observation.Hello {
		q.recordTimingOnce(id, "guest_hello_observed")
	} else if record.MemberID == "" && q.bootExpired(id, record) {
		q.cfg.Logger.Error("guest boot deadline exceeded", "vm", id, "deadline", q.bootTimeout)
		return Status{ID: id, Class: record.Class, Image: record.Image, Phase: PhaseGone}, true, nil
	}
	if observation.Assignment != nil {
		q.recordTimingOnce(id, "host_assignment_observed")
		observed := assignment(*observation.Assignment)
		if record.Assignment != nil && !sameAssignment(*record.Assignment, observed) {
			status.Phase = PhaseRecycleRequired
			status.FailureReason = "local assignment changed within one VM incarnation"
			status.Timing = append(status.Timing, q.timingFor(id)...)
			status.Timing = append(status.Timing, timingPoints(observation.Timing)...)
			return status, false, nil
		}
		if record.Assignment == nil {
			persisted := observed
			record.Assignment = &persisted
			if err := q.writeMeta(record); err != nil {
				return Status{}, false, err
			}
		}
		status.Assignment = observed
	}
	if observation.Restore != nil {
		restore := *observation.Restore
		status.Restore = &restore
	}
	switch {
	case observation.RecycleRequired:
		status.Phase = PhaseRecycleRequired
		status.FailureReason = observation.FailureReason
	case observation.RunnerExited:
		status.Phase = PhaseExited
		status.ExitCode = observation.ExitCode
		status.FailureReason = observation.FailureReason
	case observation.Released:
		status.Phase = PhaseReady
		status.Identity = JobIdentity{
			RunID: observation.Identity.RunID, RunAttempt: observation.Identity.RunAttempt,
			RunnerName: observation.Identity.RunnerName, Repository: observation.Identity.Repository,
			WorkflowJob: observation.Identity.WorkflowJob,
		}
		status.Clock = ClockSample{
			UnixNS: observation.Clock.UnixNS, Synchronized: observation.Clock.Synchronized,
			Clocksource: observation.Clock.Clocksource, AfterRestore: observation.Clock.AfterRestore,
		}
	case observation.HookBlocked:
		status.Phase = PhaseHookBlocked
		status.Identity = jobIdentity(observation.Identity)
	case observation.WorkerReady:
		status.Phase = PhaseWorkerReady
		status.Identity = jobIdentity(observation.Identity)
		status.Clock = clockSample(observation.Clock)
	case observation.MountsReady:
		status.Phase = PhaseBound
		status.Clock = clockSample(observation.Clock)
	case observation.Assignment != nil:
		status.Phase = PhaseJobAssigned
		status.Assignment = assignment(*observation.Assignment)
	case observation.RunnerRegistered:
		status.Phase = PhaseListening
	case record.MemberID != "":
		status.Phase = PhaseAssigned
	case observation.Hello:
		status.Phase = PhaseWarm
	default:
		status.Phase = PhaseBooting
	}
	status.Timing = append(status.Timing, q.timingFor(id)...)
	status.Timing = append(status.Timing, timingPoints(observation.Timing)...)
	status.CustomerStepsReleased = observation.Released
	return status, false, nil
}

func (q *QEMU) bootExpired(id ID, record meta) bool {
	created := time.Unix(0, record.CreatedUnixNS)
	if record.CreatedUnixNS == 0 {
		// Metadata written by a previous hostd predates CreatedUnixNS. Its
		// mtime is the closest durable launch boundary and lets an upgraded
		// host collect a worker that was already stuck before the restart.
		info, err := os.Stat(q.metaPath(id))
		if err != nil {
			return false
		}
		created = info.ModTime()
	}
	return time.Since(created) > q.bootTimeout
}

func jobIdentity(identity guestproto.JobIdentity) JobIdentity {
	return JobIdentity{
		RunID: identity.RunID, RunAttempt: identity.RunAttempt, RunnerName: identity.RunnerName,
		Repository: identity.Repository, WorkflowJob: identity.WorkflowJob,
	}
}

func clockSample(clock guestproto.ClockSample) ClockSample {
	return ClockSample{
		UnixNS: clock.UnixNS, Synchronized: clock.Synchronized,
		Clocksource: clock.Clocksource, AfterRestore: clock.AfterRestore,
	}
}

func assignment(value guestproto.Assignment) Assignment {
	result := Assignment{
		RequestID: value.RequestID, JobID: value.JobID, CheckRunID: value.CheckRunID, RunnerName: value.RunnerName,
		JobDisplayName: value.JobDisplayName,
	}
	if value.Identity != nil {
		result.Identity = jobIdentity(*value.Identity)
	}
	result.Timing = timingPoints(value.Timing)
	return result
}

func sameAssignment(left, right Assignment) bool {
	return left.RequestID == right.RequestID && left.JobID == right.JobID && left.CheckRunID == right.CheckRunID &&
		left.RunnerName == right.RunnerName && left.JobDisplayName == right.JobDisplayName &&
		left.Identity == right.Identity
}

func timingPoints(points []guestproto.TimingPoint) []TimingPoint {
	out := make([]TimingPoint, 0, len(points))
	for _, point := range points {
		out = append(out, TimingPoint{
			Event: point.Event, Source: point.Source, BootID: point.BootID,
			Sequence: point.Sequence, MonotonicNS: point.MonotonicNS, UnixNS: point.UnixNS,
		})
	}
	return out
}

type qmpBlockStatus struct {
	Device   string `json:"device"`
	QDev     string `json:"qdev"`
	IOStatus string `json:"io-status"`
	Inserted *struct {
		NodeName string `json:"node-name"`
	} `json:"inserted"`
}

type qmpVMStatus struct {
	Running bool   `json:"running"`
	Status  string `json:"status"`
	Blocks  []qmpBlockStatus
}

func (s qmpVMStatus) blockErrorSummary() string {
	var failures []string
	for _, block := range s.Blocks {
		if block.IOStatus == "" || block.IOStatus == "ok" {
			continue
		}
		name := block.QDev
		if name == "" {
			name = block.Device
		}
		if name == "" && block.Inserted != nil {
			name = block.Inserted.NodeName
		}
		failures = append(failures, name+"="+block.IOStatus)
	}
	sort.Strings(failures)
	return strings.Join(failures, ",")
}

func (s qmpVMStatus) failureReason() string {
	if failures := s.blockErrorSummary(); failures != "" {
		return "qemu entered io-error: " + failures
	}
	return "qemu entered io-error"
}

// hypervisorStatus probes QMP for the VM's run state and, when QEMU has
// stopped for a block failure, captures the per-device error before recycle.
func (q *QEMU) hypervisorStatus(ctx context.Context, id ID) qmpVMStatus {
	probeCtx, cancel := context.WithTimeout(ctx, q.probeTimeout)
	defer cancel()
	client, err := dialQMP(probeCtx, qmpSocketPath(q.stateDir(id)))
	if err != nil {
		return qmpVMStatus{}
	}
	defer client.Close()
	result, err := client.Execute(probeCtx, "query-status", nil)
	if err != nil {
		return qmpVMStatus{}
	}
	var reply qmpVMStatus
	if err := json.Unmarshal(result, &reply); err != nil {
		return qmpVMStatus{}
	}
	if reply.Status != "io-error" {
		return reply
	}
	result, err = client.Execute(probeCtx, "query-block", nil)
	if err == nil {
		_ = json.Unmarshal(result, &reply.Blocks)
	}
	return reply
}

// List implements Driver. A VM whose process died is not a VM anymore: its
// leftovers (root clone, state dir) are collected here and it is omitted,
// which is exactly the disappearance the agent's failure paths key on.
func (q *QEMU) List(ctx context.Context) ([]Status, error) {
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
		unlockVM := q.lockVM(id)
		status, dead, err := q.observeLocked(ctx, id)
		if err != nil {
			unlockVM()
			return nil, err
		}
		if dead || status.Phase == PhaseGone {
			if err := q.destroyLocked(ctx, id); err != nil {
				q.cfg.Logger.Error("collecting dead vm", "vm", id, "err", err)
				statuses = append(statuses, status)
			}
			unlockVM()
			continue
		}
		statuses = append(statuses, status)
		unlockVM()
	}
	return statuses, nil
}

// Destroy implements Driver.
func (q *QEMU) Destroy(ctx context.Context, id ID) error {
	if err := validateID(id); err != nil {
		return err
	}
	unlockVM := q.lockVM(id)
	defer unlockVM()
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
			if detachErr := q.detachVolume(ctx, client, workspaceNode, workspaceDevice); detachErr != nil {
				q.cfg.Logger.Warn("workspace detach during destroy", "vm", id, "err", detachErr)
			}
			if detachErr := q.detachVolume(ctx, client, toolNode, toolDevice); detachErr != nil {
				q.cfg.Logger.Warn("tool volume detach during destroy", "vm", id, "err", detachErr)
			}
			if detachErr := q.detachVolume(ctx, client, processNode, processDevice); detachErr != nil {
				q.cfg.Logger.Warn("process volume detach during destroy", "vm", id, "err", detachErr)
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
		tapName := record.TapName
		if corrupt && q.cfg.TapLifecycle != nil {
			tapName, _ = tapIdentity(id, 0)
		}
		if tapName != "" {
			if q.cfg.TapLifecycle == nil {
				return fmt.Errorf("vm: cannot remove tap %s without a tap lifecycle", tapName)
			}
			if err := q.cfg.TapLifecycle.Down(ctx, tapName); err != nil {
				return fmt.Errorf("vm: removing tap %s for %s: %w", tapName, id, err)
			}
		}
		if err := q.disks.Destroy(ctx, dataset); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("vm: removing state dir for %s: %w", id, err)
	}
	q.timingMu.Lock()
	delete(q.timings, id)
	q.timingMu.Unlock()
	return nil
}
