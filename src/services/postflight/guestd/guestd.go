// Package guestd is the agent inside every runner VM — the only process
// that talks to the host. It accepts the host's vsock connection, converges
// the assignment's mounts (no customer step runs before every mount is up),
// execs the actions runner as the runner user, streams the runner lifecycle
// back, and checkpoints and flushes the attached generation ahead of the
// host-side seal snapshot.
// A dead guestd is a dead VM by design: the host's probe fails and the slot
// is destroyed and refilled, so nothing here ever restarts itself.
package guestd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vsock"
	"github.com/guardian-intelligence/guardian/src/services/postflight/timing"
)

// WorkspaceMarker is the file guestd drops at a converged mountpoint's
// root; the checkout action refuses to run on a workspace without it.
const WorkspaceMarker = guestproto.WorkspaceReadyMarker

// SyntheticFailureExitCode reports a runner that never ran: mounts that
// would not converge, or an exec that failed. The host destroys the slot
// either way; the code only distinguishes "we failed" from a real runner
// exit in the lease report.
const SyntheticFailureExitCode = 254

// Config is guestd's static shape.
type Config struct {
	// System is the privileged-operation seam.
	System System
	// RunRunner starts the actions runner and blocks until it exits.
	RunRunner RunRunner
	// MountDeadline bounds convergence of the whole assignment's mounts; a
	// mount that cannot converge within it reports a synthetic failure exit
	// and the job never starts against a partial workspace.
	MountDeadline time.Duration
	// RetryInterval paces mount convergence retries.
	RetryInterval time.Duration
	// QuiesceWindow bounds the process checkpoint operation.
	QuiesceWindow time.Duration
	// HookDeadline bounds a hook that GitHub itself would otherwise allow
	// to block forever.
	HookDeadline time.Duration
	// AssignmentSocketMode and AssignmentSocketGID restrict the runner's
	// local synchronous assignment gate. Production uses 0660 and the
	// runner account's primary group; tests default to the current process.
	AssignmentSocketMode os.FileMode
	AssignmentSocketGID  int
	// Encryption is the baked at-rest mode for workspace volumes; the zero
	// value mounts plaintext. See LoadEncryptionMode.
	Encryption EncryptionMode
	// Checkpoints is the single generic process checkpoint implementation.
	// Nil disables process restore while retaining workspace-only behavior.
	Checkpoints *ProcessCheckpoints
	// HostCID is the only vsock peer CID accepted as the host. Anything in
	// the guest — the runner user included — can dial this listener, so a
	// connection from any other CID is dropped before a verb is read.
	HostCID uint32
	// Timing is the process-local high-resolution event recorder.
	Timing *timing.Recorder
	Logger *slog.Logger
}

func (c *Config) validate() error {
	if c.System == nil {
		return errors.New("guestd: System is required")
	}
	if c.RunRunner == nil {
		return errors.New("guestd: RunRunner is required")
	}
	if c.MountDeadline <= 0 {
		c.MountDeadline = 60 * time.Second
	}
	if c.RetryInterval <= 0 {
		c.RetryInterval = 200 * time.Millisecond
	}
	if c.QuiesceWindow <= 0 {
		c.QuiesceWindow = 10 * time.Second
	}
	if c.HookDeadline <= 0 {
		c.HookDeadline = 2 * time.Minute
	}
	if c.AssignmentSocketMode == 0 {
		c.AssignmentSocketMode = 0o600
	}
	if c.AssignmentSocketGID == 0 {
		c.AssignmentSocketGID = -1
	}
	if c.HostCID == 0 {
		c.HostCID = vsock.Host
	}
	if c.Timing == nil {
		bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
		if err != nil {
			return fmt.Errorf("guestd: read boot id: %w", err)
		}
		c.Timing, err = timing.New("guestd", strings.TrimSpace(string(bootID)))
		if err != nil {
			return err
		}
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return nil
}

// Server is the guest agent. One VM runs exactly one Server for its whole
// life, serving one host connection at a time; a newer connection always
// supplants an older one, which is what makes host restarts converge.
type Server struct {
	cfg Config

	mu         sync.Mutex
	conn       net.Conn
	prepared   *guestproto.Prepare
	rendezvous *guestproto.Rendezvous
	authorized *guestproto.Authorize
	clock      *guestproto.ClockSample
	bound      bool
	statuses   []guestproto.RunnerStatus
	assignment *guestproto.Assignment
	workerGate chan struct{}
	gateOnce   sync.Once
	gateErr    error

	// writeMu serializes frames. It is separate from mu so a slow host —
	// a blocked status write — can never stall inbound dispatch.
	writeMu sync.Mutex
}

// New wires a Server.
func New(cfg Config) (*Server, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, workerGate: make(chan struct{})}, nil
}

// Serve accepts host connections until the context ends or the listener
// fails. Rejection and supplanting happen here, on the accept goroutine:
// "newer connection wins" is only well defined in arrival order, and a
// concurrent handler for a dying dial must never get to close a healthy
// successor.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("guestd: accept: %w", err)
		}
		if addr, ok := conn.RemoteAddr().(vsock.Addr); ok && addr.CID != s.cfg.HostCID {
			s.cfg.Logger.Error("rejected non-host peer", "peer", addr.String())
			conn.Close()
			continue
		}
		go s.handle(conn, s.supplant(conn))
	}
}

type replayState struct {
	assignment *guestproto.Assignment
	statuses   []guestproto.RunnerStatus
}

// supplant makes conn the current host connection and returns the runner
// statuses the new host must be caught up on.
func (s *Server) supplant(conn net.Conn) replayState {
	s.mu.Lock()
	old := s.conn
	s.conn = conn
	replay := replayState{statuses: append([]guestproto.RunnerStatus(nil), s.statuses...)}
	if s.assignment != nil {
		captured := *s.assignment
		replay.assignment = &captured
	}
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return replay
}

// handle serves one host connection: greet, replay the runner status ladder
// so a reconnecting host catches up (the latest status alone is not enough —
// an exit replayed without its registration would un-happen the job on the
// host's fold), then dispatch inbound verbs.
func (s *Server) handle(conn net.Conn, replay replayState) {
	if err := s.writeTo(conn, guestproto.Message{Kind: guestproto.KindHello, Hello: &guestproto.Hello{Version: guestproto.Version}}); err != nil {
		s.drop(conn)
		return
	}
	if replay.assignment != nil {
		if err := s.writeTo(conn, guestproto.Message{Kind: guestproto.KindAssignment, Assignment: replay.assignment}); err != nil {
			s.drop(conn)
			return
		}
	}
	for i := range replay.statuses {
		if err := s.writeTo(conn, guestproto.Message{Kind: guestproto.KindRunnerStatus, RunnerStatus: &replay.statuses[i]}); err != nil {
			s.drop(conn)
			return
		}
	}

	decoder := guestproto.NewDecoder(conn)
	for {
		message, err := decoder.Read()
		if err != nil {
			s.drop(conn)
			return
		}
		switch message.Kind {
		case guestproto.KindPrepare:
			s.handlePrepare(*message.Prepare)
		case guestproto.KindRendezvous:
			s.handleRendezvous(*message.Rendezvous)
		case guestproto.KindAuthorize:
			s.handleAuthorize(*message.Authorize)
		case guestproto.KindQuiesce:
			s.handleQuiesce(*message.Quiesce)
		default:
			s.cfg.Logger.Error("host sent a guest-bound verb", "kind", message.Kind)
			s.drop(conn)
			return
		}
	}
}

// drop retires a connection if it is still the current one.
func (s *Server) drop(conn net.Conn) {
	s.mu.Lock()
	if s.conn == conn {
		s.conn = nil
	}
	s.mu.Unlock()
	conn.Close()
}

// send writes one frame to the current connection. Statuses are level
// state and the greeting replay catches a host up on reconnect, so a lost
// frame here is survivable — callers log, never fail.
func (s *Server) send(message guestproto.Message) error {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return errors.New("guestd: no host connection")
	}
	return s.writeTo(conn, message)
}

func (s *Server) writeTo(conn net.Conn, message guestproto.Message) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := guestproto.NewEncoder(conn).Write(message); err != nil {
		return err
	}
	return conn.SetWriteDeadline(time.Time{})
}

// sendStatus records the runner status and streams it if a host is
// connected.
func (s *Server) sendStatus(status guestproto.RunnerStatus) {
	s.mu.Lock()
	s.statuses = append(s.statuses, status)
	s.mu.Unlock()
	if err := s.send(guestproto.Message{Kind: guestproto.KindRunnerStatus, RunnerStatus: &status}); err != nil {
		s.cfg.Logger.Warn("runner status not delivered", "state", status.State, "err", err)
	}
}

// handlePrepare starts the outer one-job listener in an empty warm VM. The
// listener itself is never checkpointed and blocks at the assignment gate
// before Runner.Worker is created.
func (s *Server) handlePrepare(prepare guestproto.Prepare) {
	s.mu.Lock()
	if s.prepared != nil {
		duplicate := s.prepared.Lease == prepare.Lease
		s.mu.Unlock()
		if !duplicate {
			s.cfg.Logger.Error("conflicting preparation ignored", "lease", prepare.Lease)
		}
		return
	}
	claimed := prepare
	s.prepared = &claimed
	s.mu.Unlock()
	started := guestTiming(s.cfg.Timing.Point("listener_prepare_received"))
	go s.run(prepare, started)
}

func (s *Server) run(prepare guestproto.Prepare, started guestproto.TimingPoint) {
	code, err := s.cfg.RunRunner(context.Background(), prepare.JITConfig, prepare.Env, func(event RunnerEvent) {
		switch event {
		case EventListening:
			point := guestTiming(s.cfg.Timing.Point("runner_registered"))
			s.sendStatus(guestproto.RunnerStatus{State: guestproto.RunnerRegistered, Timing: []guestproto.TimingPoint{started, point}})
		}
	})
	if err != nil {
		s.cfg.Logger.Error("runner failed to run", "lease", prepare.Lease, "err", err)
		point := guestTiming(s.cfg.Timing.Point("runner_exited"))
		s.sendStatus(guestproto.RunnerStatus{
			State: guestproto.RunnerExited, ExitCode: SyntheticFailureExitCode,
			Timing: []guestproto.TimingPoint{point},
		})
		return
	}
	point := guestTiming(s.cfg.Timing.Point("runner_exited"))
	s.sendStatus(guestproto.RunnerStatus{
		State: guestproto.RunnerExited, ExitCode: code,
		Timing: []guestproto.TimingPoint{point},
	})
}

func (s *Server) handleRendezvous(rendezvous guestproto.Rendezvous) {
	s.mu.Lock()
	if s.assignment == nil || s.prepared == nil || s.prepared.Lease != rendezvous.Lease {
		s.mu.Unlock()
		s.cfg.Logger.Error("rendezvous arrived before local assignment", "lease", rendezvous.Lease)
		return
	}
	if s.rendezvous != nil {
		duplicate := s.rendezvous.Lease == rendezvous.Lease
		s.mu.Unlock()
		if !duplicate {
			s.cfg.Logger.Error("conflicting rendezvous ignored", "lease", rendezvous.Lease)
		}
		return
	}
	claimed := rendezvous
	s.rendezvous = &claimed
	s.mu.Unlock()
	go s.bindGeneration(rendezvous)
}

func (s *Server) bindGeneration(rendezvous guestproto.Rendezvous) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.MountDeadline)
	fail := func(stage string, err error) {
		cancel()
		s.cfg.Logger.Error(stage, "lease", rendezvous.Lease, "err", err)
		s.failWorkerGate(err)
		s.sendStatus(guestproto.RunnerStatus{State: guestproto.RunnerExited, ExitCode: SyntheticFailureExitCode})
	}
	received := guestTiming(s.cfg.Timing.Point("guest_rendezvous_received"))
	points := []guestproto.TimingPoint{received, guestTiming(s.cfg.Timing.Point("mount_convergence_started"))}
	s.cfg.Logger.Info("rendezvous timing", "event", received.Event, "monotonic_ns", received.MonotonicNS)
	err := s.convergeMounts(ctx, rendezvous.Mounts)
	if err != nil {
		fail("mount convergence failed", err)
		return
	}
	points = append(points, guestTiming(s.cfg.Timing.Point("mount_convergence_completed")))
	restored := false
	if rendezvous.Checkpoint != nil {
		points = append(points, guestTiming(s.cfg.Timing.Point("criu_restore_started")))
		if s.cfg.Checkpoints == nil {
			err := errors.New("process checkpoint requested but guest support is disabled")
			fail("checkpoint restore failed", err)
			return
		}
		checkpoint := rendezvous.Checkpoint
		if _, err := s.cfg.Checkpoints.Restore(ctx, checkpoint.ImagesDir, checkpoint.ExpectedDigest, checkpoint.ExternalMountAt); err != nil {
			fail("checkpoint restore failed", err)
			return
		}
		restored = true
		points = append(points, guestTiming(s.cfg.Timing.Point("criu_restore_completed")))
	} else if s.cfg.Checkpoints != nil {
		points = append(points, guestTiming(s.cfg.Timing.Point("cold_capsule_start_started")))
		if err := s.cfg.Checkpoints.Capsules.Start(ctx); err != nil {
			fail("cold capsule start failed", err)
			return
		}
		points = append(points, guestTiming(s.cfg.Timing.Point("cold_capsule_start_completed")))
	}
	cancel()
	clock := sampleClock()
	clock.AfterRestore = restored
	s.mu.Lock()
	s.bound = true
	s.clock = &clock
	s.mu.Unlock()
	ready := guestTiming(s.cfg.Timing.Point("generation_restore_completed"))
	points = append(points, ready)
	s.sendStatus(guestproto.RunnerStatus{
		State: guestproto.RunnerMountsReady, Clock: &clock, Timing: points,
	})
}

func (s *Server) handleAuthorize(authorize guestproto.Authorize) {
	s.mu.Lock()
	if s.prepared == nil || s.prepared.Lease != authorize.Lease || s.assignment == nil ||
		s.assignment.RequestID != authorize.RequestID || authorize.Identity == nil || !s.bound || s.clock == nil {
		s.mu.Unlock()
		s.cfg.Logger.Error("authorization arrived before matching restored assignment", "lease", authorize.Lease)
		return
	}
	if s.authorized != nil {
		duplicate := s.authorized.Lease == authorize.Lease
		s.mu.Unlock()
		if !duplicate {
			s.cfg.Logger.Error("conflicting authorization ignored", "lease", authorize.Lease)
		}
		return
	}
	claimed := authorize
	clock := *s.clock
	s.authorized = &claimed
	s.mu.Unlock()
	if s.cfg.Checkpoints != nil {
		rootPID, err := s.cfg.Checkpoints.Capsules.RootPID()
		if err != nil {
			s.failWorkerGate(fmt.Errorf("locating restored capsule: %w", err))
			return
		}
		if err := os.WriteFile(CapsulePIDPath, []byte(strconv.Itoa(rootPID)+"\n"), 0o644); err != nil {
			s.failWorkerGate(fmt.Errorf("publishing restored capsule: %w", err))
			return
		}
	} else if err := os.Remove(CapsulePIDPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.failWorkerGate(fmt.Errorf("selecting workspace-only worker: %w", err))
		return
	}
	point := guestTiming(s.cfg.Timing.Point("runner_worker_released"))
	s.sendStatus(guestproto.RunnerStatus{
		State: guestproto.RunnerWorkerReady, Identity: authorize.Identity, Clock: &clock,
		Timing: []guestproto.TimingPoint{point},
	})
	s.gateOnce.Do(func() { close(s.workerGate) })
}

func (s *Server) failWorkerGate(err error) {
	s.mu.Lock()
	s.gateErr = err
	s.mu.Unlock()
	s.gateOnce.Do(func() { close(s.workerGate) })
}

func sampleClock() guestproto.ClockSample {
	source, _ := os.ReadFile("/sys/devices/system/clocksource/clocksource0/current_clocksource")
	_, err := os.Stat("/run/systemd/timesync/synchronized")
	return guestproto.ClockSample{
		UnixNS: time.Now().UnixNano(), Synchronized: err == nil,
		Clocksource: strings.TrimSpace(string(source)),
	}
}

func (s *Server) convergeMounts(ctx context.Context, mounts []guestproto.Mount) error {
	if len(mounts) == 0 {
		return errors.New("assignment carries no mounts")
	}
	for _, mount := range mounts {
		if err := s.convergeMount(ctx, mount); err != nil {
			return fmt.Errorf("serial %s at %s: %w", mount.Serial, mount.Mountpoint, err)
		}
	}
	return nil
}

func validateMount(mount guestproto.Mount) error {
	switch {
	case mount.Serial == "":
		return errors.New("mount without a serial")
	case mount.Filesystem == "":
		return errors.New("mount without a filesystem")
	case !validMountpoint(mount.Mountpoint):
		return fmt.Errorf("unsafe mountpoint %q", mount.Mountpoint)
	}
	return nil
}

func validMountpoint(mountpoint string) bool {
	return path.IsAbs(mountpoint) && path.Clean(mountpoint) == mountpoint && mountpoint != "/"
}

// convergeMount retries the whole observe-then-act ladder until the mount
// is up or the deadline passes. Every attempt re-observes from scratch, so
// a partial earlier attempt (device located, mkfs done, mount failed)
// converges instead of erroring.
func (s *Server) convergeMount(ctx context.Context, mount guestproto.Mount) error {
	if err := validateMount(mount); err != nil {
		return err
	}
	options := mount.Options
	if !slices.Contains(options, "discard") {
		options = append([]string{"discard"}, options...)
	}
	var lastErr error
	for {
		lastErr = s.tryMount(ctx, mount, options)
		if lastErr == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return lastErr
		case <-time.After(s.cfg.RetryInterval):
		}
	}
}

func (s *Server) tryMount(ctx context.Context, mount guestproto.Mount, options []string) error {
	system := s.cfg.System
	mounted, err := system.IsMounted(mount.Mountpoint)
	if err != nil {
		return err
	}
	if !mounted {
		device, err := system.LocateDevice(ctx, mount.Serial)
		if err != nil {
			return err
		}
		if s.cfg.Encryption.enabled() {
			device, err = s.openEncrypted(ctx, device, mount.Serial)
			if err != nil {
				return err
			}
		}
		blank, err := system.IsBlank(ctx, device)
		if err != nil {
			return err
		}
		if blank {
			if err := system.MakeFilesystem(ctx, device, mount.Filesystem); err != nil {
				return err
			}
		}
		if err := system.Mount(ctx, device, mount.Mountpoint, mount.Filesystem, options); err != nil {
			return err
		}
	}
	return system.Adopt(mount.Mountpoint)
}

// openEncrypted converges the device to an open LUKS2 mapper and returns
// the mapper node the rest of the ladder operates on. Anything that is not
// already LUKS is formatted — with encryption on, plaintext must never
// mount, and a workspace only ever carries rebuildable cache, so the right
// response to a plaintext lineage (a generation sealed before the
// encryption cutover) is a loud reformat and a cold build, not a wedged
// slot.
func (s *Server) openEncrypted(ctx context.Context, device, serial string) (string, error) {
	system := s.cfg.System
	luks, err := system.IsLUKS(ctx, device)
	if err != nil {
		return "", err
	}
	key, err := workspaceKey(s.cfg.Encryption)
	if err != nil {
		return "", err
	}
	defer clear(key)
	if !luks {
		blank, err := system.IsBlank(ctx, device)
		if err != nil {
			return "", err
		}
		if !blank {
			s.cfg.Logger.Warn("plaintext workspace lineage under encryption; erasing and reformatting, cache rebuilds cold", "device", device, "mode", string(s.cfg.Encryption))
		}
		// A LUKS header over unerased blocks is not encryption: every block
		// the new filesystem has not yet written still reads as the old
		// plaintext. Discard the whole device first; a lineage that cannot
		// be erased must never be sealed as ciphertext.
		if err := system.Discard(ctx, device); err != nil {
			if !blank {
				return "", fmt.Errorf("cannot erase plaintext lineage: %w", err)
			}
			s.cfg.Logger.Warn("discard failed on a blank device; continuing", "device", device, "err", err)
		}
		if err := system.FormatLUKS(ctx, device, key); err != nil {
			return "", err
		}
	}
	return system.OpenLUKS(ctx, device, "pf-"+serial, key)
}

// handleQuiesce proves that every member of the selected generation is
// mounted, checkpoints the capsule, flushes the filesystems, and reports the
// artifact. The host destroys QEMU before sealing the zvols, which releases
// the mounted devices without thawing the deliberately stopped capsule.
func (s *Server) handleQuiesce(quiesce guestproto.Quiesce) {
	points := []guestproto.TimingPoint{guestTiming(s.cfg.Timing.Point("quiesce_received"))}
	if len(quiesce.Mountpoints) == 0 {
		s.quiesceFailed(errors.New("quiesce requires at least one mounted volume"))
		return
	}
	for _, mountpoint := range quiesce.Mountpoints {
		if err := s.requireMounted(mountpoint); err != nil {
			s.cfg.Logger.Error("quiesce failed", "mountpoint", mountpoint, "err", err)
			s.quiesceFailed(err)
			return
		}
	}
	points = append(points, guestTiming(s.cfg.Timing.Point("quiesce_mounts_checked")))
	var artifact *guestproto.CheckpointArtifact
	if quiesce.Checkpoint != nil {
		if s.cfg.Checkpoints == nil {
			s.quiesceFailed(errors.New("process checkpoint requested but guest support is disabled"))
			return
		}
		checkpoint := quiesce.Checkpoint
		if !slices.Contains(quiesce.Mountpoints, checkpoint.ExternalMountAt) {
			s.quiesceFailed(errors.New("checkpoint external mount is not in the generation tuple"))
			return
		}
		if !slices.Contains(quiesce.Mountpoints, s.cfg.Checkpoints.ImagesRoot) {
			s.quiesceFailed(errors.New("checkpoint process volume is not in the generation tuple"))
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.QuiesceWindow)
		points = append(points, guestTiming(s.cfg.Timing.Point("checkpoint_dump_started")))
		result, err := s.cfg.Checkpoints.Dump(ctx, checkpoint.ImagesDir, checkpoint.ExternalMountAt)
		cancel()
		if err != nil {
			s.quiesceFailed(fmt.Errorf("checkpoint dump: %w", err))
			return
		}
		points = append(points, guestTiming(s.cfg.Timing.Point("checkpoint_dump_completed")))
		artifact = &guestproto.CheckpointArtifact{Digest: result.Digest, Version: result.Version}
	}
	// CRIU has stopped every process in the capsule. Flush both mounted
	// filesystems, then let the host destroy the VM before it snapshots the
	// zvols. Keeping the mounts attached until QEMU exits avoids a busy
	// unmount caused by the deliberately stopped process tree.
	points = append(points, guestTiming(s.cfg.Timing.Point("filesystem_sync_started")))
	s.cfg.System.Sync()
	points = append(points, guestTiming(s.cfg.Timing.Point("filesystem_sync_completed")))
	if err := s.send(guestproto.Message{Kind: guestproto.KindQuiesced, Quiesced: &guestproto.Quiesced{
		Checkpoint: artifact, Timing: points,
	}}); err != nil {
		s.cfg.Logger.Warn("quiesced not delivered", "err", err)
	}
}

func (s *Server) quiesceFailed(reason error) {
	s.cfg.Logger.Error("quiesce failed", "err", reason)
	reply := guestproto.Message{Kind: guestproto.KindQuiesceFailed, QuiesceFailed: &guestproto.QuiesceFailed{Reason: reason.Error()}}
	if err := s.send(reply); err != nil {
		s.cfg.Logger.Warn("quiesce-failed not delivered", "err", err)
	}
}

func (s *Server) requireMounted(mountpoint string) error {
	if !validMountpoint(mountpoint) {
		return fmt.Errorf("unsafe mountpoint %q", mountpoint)
	}
	mounted, err := s.cfg.System.IsMounted(mountpoint)
	if err != nil {
		return err
	}
	if !mounted {
		return fmt.Errorf("required volume is not mounted at %s", mountpoint)
	}
	return nil
}
