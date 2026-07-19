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
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
	// RendezvousDir is the root-owned exchange directory shared with the
	// runner's synchronous job-start hook.
	RendezvousDir string
	// HookDeadline bounds a hook that GitHub itself would otherwise allow
	// to block forever.
	HookDeadline time.Duration
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
	if c.RendezvousDir == "" {
		c.RendezvousDir = "/run/postflight-rendezvous"
	}
	if !path.IsAbs(c.RendezvousDir) || path.Clean(c.RendezvousDir) != c.RendezvousDir {
		return fmt.Errorf("guestd: unsafe rendezvous directory %q", c.RendezvousDir)
	}
	if c.HookDeadline <= 0 {
		c.HookDeadline = 2 * time.Minute
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

	mu         sync.Mutex
	conn       net.Conn
	prepared   *guestproto.Prepare
	rendezvous *guestproto.Rendezvous
	identity   *guestproto.JobIdentity
	statuses   []guestproto.RunnerStatus

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
		case guestproto.KindPrepare:
			s.handlePrepare(*message.Prepare)
		case guestproto.KindRendezvous:
			s.handleRendezvous(*message.Rendezvous)
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

// handlePrepare starts one generic runner listener without customer mounts.
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
	if err := s.resetRendezvousDir(); err != nil {
		s.cfg.Logger.Error("rendezvous directory preparation failed", "lease", prepare.Lease, "err", err)
		s.sendStatus(guestproto.RunnerStatus{State: guestproto.RunnerExited, ExitCode: SyntheticFailureExitCode})
		return
	}
	go s.run(prepare)
}

func (s *Server) run(prepare guestproto.Prepare) {
	code, err := s.cfg.RunRunner(context.Background(), prepare.JITConfig, prepare.Env, func(event RunnerEvent) {
		switch event {
		case EventListening:
			s.sendStatus(guestproto.RunnerStatus{State: guestproto.RunnerRegistered})
		case EventJobStarted:
			go s.observeBlockedHook(prepare.Lease)
		}
	})
	if err != nil {
		s.cfg.Logger.Error("runner failed to run", "lease", prepare.Lease, "err", err)
		s.sendStatus(guestproto.RunnerStatus{State: guestproto.RunnerExited, ExitCode: SyntheticFailureExitCode})
		return
	}
	s.sendStatus(guestproto.RunnerStatus{State: guestproto.RunnerExited, ExitCode: code})
}

func (s *Server) resetRendezvousDir() error {
	if err := os.RemoveAll(s.cfg.RendezvousDir); err != nil {
		return err
	}
	return os.MkdirAll(s.cfg.RendezvousDir, 0o733)
}

func (s *Server) observeBlockedHook(lease string) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.HookDeadline)
	defer cancel()
	request := filepath.Join(s.cfg.RendezvousDir, "request")
	var lastErr error
	for {
		raw, err := os.ReadFile(request)
		if err == nil {
			identity, parseErr := parseHookIdentity(raw)
			if parseErr != nil {
				lastErr = parseErr
			} else {
				s.mu.Lock()
				if s.prepared == nil || s.prepared.Lease != lease {
					s.mu.Unlock()
					return
				}
				captured := identity
				s.identity = &captured
				s.mu.Unlock()
				s.sendStatus(guestproto.RunnerStatus{State: guestproto.RunnerHookBlocked, Identity: &identity})
				return
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			s.cfg.Logger.Error("job-start hook did not report valid identity", "lease", lease, "err", lastErr)
			return
		case <-time.After(s.cfg.RetryInterval):
		}
	}
}

func parseHookIdentity(raw []byte) (guestproto.JobIdentity, error) {
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" || value == "" {
			return guestproto.JobIdentity{}, fmt.Errorf("invalid hook identity line")
		}
		if _, duplicate := values[key]; duplicate {
			return guestproto.JobIdentity{}, fmt.Errorf("duplicate hook identity key %q", key)
		}
		values[key] = value
	}
	for key := range values {
		switch key {
		case "run_id", "run_attempt", "runner_name", "repository", "workflow_job":
		default:
			return guestproto.JobIdentity{}, fmt.Errorf("unknown hook identity key %q", key)
		}
	}
	attempt, err := strconv.Atoi(values["run_attempt"])
	if err != nil || attempt <= 0 {
		return guestproto.JobIdentity{}, fmt.Errorf("invalid run attempt")
	}
	identity := guestproto.JobIdentity{
		RunID: values["run_id"], RunAttempt: attempt, RunnerName: values["runner_name"],
		Repository: values["repository"], WorkflowJob: values["workflow_job"],
	}
	if identity.RunID == "" || identity.RunnerName == "" || identity.Repository == "" || identity.WorkflowJob == "" {
		return guestproto.JobIdentity{}, fmt.Errorf("incomplete hook identity")
	}
	return identity, nil
}

func (s *Server) handleRendezvous(rendezvous guestproto.Rendezvous) {
	s.mu.Lock()
	if s.prepared == nil || s.prepared.Lease != rendezvous.Lease || s.identity == nil {
		s.mu.Unlock()
		s.cfg.Logger.Error("rendezvous arrived before matching blocked hook", "lease", rendezvous.Lease)
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
	identity := *s.identity
	s.rendezvous = &claimed
	s.mu.Unlock()
	go s.bindAndRelease(rendezvous, identity)
}

func (s *Server) bindAndRelease(rendezvous guestproto.Rendezvous, identity guestproto.JobIdentity) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.MountDeadline)
	err := s.convergeMounts(ctx, rendezvous.Mounts)
	cancel()
	if err != nil {
		s.cfg.Logger.Error("mount convergence failed", "lease", rendezvous.Lease, "err", err)
		s.abortHook(err)
		return
	}
	if err := writeJobEnvironment(filepath.Join(s.cfg.RendezvousDir, "job-env"), rendezvous.Env); err != nil {
		s.cfg.Logger.Error("writing rendezvous job environment", "lease", rendezvous.Lease, "err", err)
		s.abortHook(err)
		return
	}
	clock := sampleClock()
	s.sendStatus(guestproto.RunnerStatus{
		State: guestproto.RunnerMountsReady, Identity: &identity, Clock: &clock,
	})
	if err := os.WriteFile(filepath.Join(s.cfg.RendezvousDir, "release"), []byte("ready\n"), 0o644); err != nil {
		s.cfg.Logger.Error("releasing job-start hook", "lease", rendezvous.Lease, "err", err)
		s.abortHook(err)
		return
	}
	s.sendStatus(guestproto.RunnerStatus{
		State: guestproto.RunnerReleased, Identity: &identity, Clock: &clock,
	})
}

func (s *Server) abortHook(reason error) {
	message := strings.ReplaceAll(reason.Error(), "\n", " ")
	_ = os.WriteFile(filepath.Join(s.cfg.RendezvousDir, "abort"), []byte(message+"\n"), 0o644)
}

func writeJobEnvironment(path string, env map[string]string) error {
	keys := make([]string, 0, len(env))
	for key, value := range env {
		if key == "" || strings.ContainsAny(key, "=\r\n") || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("unsafe job environment entry")
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var out strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&out, "%s=%s\n", key, env[key])
	}
	return os.WriteFile(path, []byte(out.String()), 0o644)
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
