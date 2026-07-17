// Package guestd is the agent inside every runner VM — the only process
// that talks to the host. It accepts the host's vsock connection, converges
// the assignment's mounts (no customer step runs before every mount is up),
// execs the actions runner as the runner user, streams the runner lifecycle
// back, and quiesces the workspace ahead of the host-side seal snapshot.
// A dead guestd is a dead VM by design: the host's probe fails and the slot
// is destroyed and refilled, so nothing here ever restarts itself.
package guestd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path"
	"slices"
	"sync"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vsock"
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
	// RetryInterval paces convergence and quiesce retries.
	RetryInterval time.Duration
	// QuiesceWindow bounds how long a busy unmount is retried before the
	// quiesce is reported failed.
	QuiesceWindow time.Duration
	// Encryption is the baked at-rest mode for workspace volumes; the zero
	// value mounts plaintext. See LoadEncryptionMode.
	Encryption EncryptionMode
	// HostCID is the only vsock peer CID accepted as the host. Anything in
	// the guest — the runner user included — can dial this listener, so a
	// connection from any other CID is dropped before a verb is read.
	HostCID uint32
	Logger  *slog.Logger
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
	if c.HostCID == 0 {
		c.HostCID = vsock.Host
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

	mu       sync.Mutex
	conn     net.Conn
	assigned *guestproto.Assignment
	statuses []guestproto.RunnerStatus

	// writeMu serializes frames. It is separate from mu so a slow host —
	// a blocked status write — can never stall inbound dispatch.
	writeMu sync.Mutex
}

// New wires a Server.
func New(cfg Config) (*Server, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Server{cfg: cfg}, nil
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

// supplant makes conn the current host connection and returns the runner
// statuses the new host must be caught up on.
func (s *Server) supplant(conn net.Conn) []guestproto.RunnerStatus {
	s.mu.Lock()
	old := s.conn
	s.conn = conn
	replay := append([]guestproto.RunnerStatus(nil), s.statuses...)
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
func (s *Server) handle(conn net.Conn, replay []guestproto.RunnerStatus) {
	if err := s.writeTo(conn, guestproto.Message{Kind: guestproto.KindHello, Hello: &guestproto.Hello{Version: guestproto.Version}}); err != nil {
		s.drop(conn)
		return
	}
	for i := range replay {
		if err := s.writeTo(conn, guestproto.Message{Kind: guestproto.KindRunnerStatus, RunnerStatus: &replay[i]}); err != nil {
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
		case guestproto.KindAssignment:
			s.handleAssignment(*message.Assignment)
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

// handleAssignment starts the assignment exactly once; redelivery of the
// same lease (the host converging after a partial apply) is a no-op, a
// different lease is a protocol violation on a single-use VM.
func (s *Server) handleAssignment(assignment guestproto.Assignment) {
	s.mu.Lock()
	if s.assigned != nil {
		duplicate := s.assigned.Lease == assignment.Lease
		s.mu.Unlock()
		if !duplicate {
			s.cfg.Logger.Error("conflicting assignment ignored", "lease", assignment.Lease)
		}
		return
	}
	claimed := assignment
	s.assigned = &claimed
	s.mu.Unlock()
	go s.run(assignment)
}

// run is the assignment's life: converge every mount, then the runner.
func (s *Server) run(assignment guestproto.Assignment) {
	s.sendStatus(guestproto.RunnerStatus{State: guestproto.RunnerMounting})

	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.MountDeadline)
	err := s.convergeMounts(ctx, assignment.Mounts)
	cancel()
	if err != nil {
		s.cfg.Logger.Error("mount convergence failed", "lease", assignment.Lease, "err", err)
		s.sendStatus(guestproto.RunnerStatus{State: guestproto.RunnerExited, ExitCode: SyntheticFailureExitCode})
		return
	}

	code, err := s.cfg.RunRunner(context.Background(), assignment.JITConfig, assignment.Env, func(event RunnerEvent) {
		switch event {
		case EventListening:
			s.sendStatus(guestproto.RunnerStatus{State: guestproto.RunnerRegistered})
		case EventJobStarted:
			s.sendStatus(guestproto.RunnerStatus{State: guestproto.RunnerJobStarted})
		}
	})
	if err != nil {
		s.cfg.Logger.Error("runner failed to run", "lease", assignment.Lease, "err", err)
		s.sendStatus(guestproto.RunnerStatus{State: guestproto.RunnerExited, ExitCode: SyntheticFailureExitCode})
		return
	}
	s.sendStatus(guestproto.RunnerStatus{State: guestproto.RunnerExited, ExitCode: code})
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
// the mapper node the rest of the ladder operates on. A blank device is
// formatted first; a device carrying anything that is not LUKS is refused —
// with encryption on, plaintext must never mount.
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
			return "", fmt.Errorf("device %s carries a plaintext filesystem; refusing to mount under encryption mode %q", device, s.cfg.Encryption)
		}
		if err := system.FormatLUKS(ctx, device, key); err != nil {
			return "", err
		}
	}
	return system.OpenLUKS(ctx, device, "pf-"+serial, key)
}

// handleQuiesce syncs and unmounts the workspace, riding out the busy
// window of exiting stragglers, and reports the outcome. A workspace that
// is already unmounted (a retried quiesce) is a success.
func (s *Server) handleQuiesce(quiesce guestproto.Quiesce) {
	if err := s.quiesce(quiesce.Mountpoint); err != nil {
		s.cfg.Logger.Error("quiesce failed", "mountpoint", quiesce.Mountpoint, "err", err)
		reply := guestproto.Message{Kind: guestproto.KindQuiesceFailed, QuiesceFailed: &guestproto.QuiesceFailed{Reason: err.Error()}}
		if err := s.send(reply); err != nil {
			s.cfg.Logger.Warn("quiesce-failed not delivered", "err", err)
		}
		return
	}
	if err := s.send(guestproto.Message{Kind: guestproto.KindQuiesced, Quiesced: &guestproto.Quiesced{}}); err != nil {
		s.cfg.Logger.Warn("quiesced not delivered", "err", err)
	}
}

func (s *Server) quiesce(mountpoint string) error {
	if !validMountpoint(mountpoint) {
		return fmt.Errorf("unsafe mountpoint %q", mountpoint)
	}
	s.cfg.System.Sync()
	deadline := time.Now().Add(s.cfg.QuiesceWindow)
	for {
		mounted, err := s.cfg.System.IsMounted(mountpoint)
		if err != nil {
			return err
		}
		if !mounted {
			return nil
		}
		err = s.cfg.System.Unmount(mountpoint)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(s.cfg.RetryInterval)
	}
}
