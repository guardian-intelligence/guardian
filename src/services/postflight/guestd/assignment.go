package guestd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/timing"
)

const (
	AssignmentSocketPath = "/run/postflight/assignment.sock"
	CapsulePIDPath       = "/run/postflight/capsule.pid"
	RunnerWorkerReal     = "/opt/actions-runner/bin/Runner.Worker.real"
	RunnerWorkerWrapper  = "/opt/actions-runner/bin/Runner.Worker"
	RunnerUID            = 1001
	RunnerGID            = 1001
	localMessageLimit    = 1 << 20
)

type localRequest struct {
	Kind       string                  `json:"kind"`
	Assignment *guestproto.Assignment  `json:"assignment,omitempty"`
	Identity   *guestproto.JobIdentity `json:"identity,omitempty"`
	Failure    string                  `json:"failure,omitempty"`
}

type localReply struct {
	Ready bool              `json:"ready"`
	Env   map[string]string `json:"env,omitempty"`
	Error string            `json:"error,omitempty"`
}

// ServeAssignments accepts the runner-side assignment handoff and gates. The
// listener publishes identity before Runner.Worker exists; the privileged
// worker trampoline and documented job-start hook block customer execution.
func (s *Server) ServeAssignments(ctx context.Context, socketPath string) error {
	if socketPath == "" {
		socketPath = AssignmentSocketPath
	}
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath {
		return fmt.Errorf("guestd: unsafe assignment socket %q", socketPath)
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return err
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socketPath)
	if err := os.Chmod(socketPath, s.cfg.AssignmentSocketMode); err != nil {
		return err
	}
	if s.cfg.AssignmentSocketGID >= 0 {
		if err := os.Chown(socketPath, -1, s.cfg.AssignmentSocketGID); err != nil {
			return err
		}
	}
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go s.handleLocal(ctx, conn)
	}
}

func (s *Server) handleLocal(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(s.cfg.HookDeadline))
	requestCtx, cancel := context.WithTimeout(ctx, s.cfg.HookDeadline)
	defer cancel()
	decoder := json.NewDecoder(io.LimitReader(conn, localMessageLimit))
	var request localRequest
	if err := decoder.Decode(&request); err != nil {
		_ = json.NewEncoder(conn).Encode(localReply{Error: "invalid request"})
		return
	}
	var reply localReply
	switch request.Kind {
	case "assigned":
		reply = s.publishAssignment(request.Assignment)
	case "worker-ready":
		reply = s.awaitWorker(requestCtx)
	case "validate":
		reply = s.validateAssignment(request.Identity)
	case "hook-released":
		reply = s.releaseAssignment(request.Identity)
	case "worker-starting":
		reply = s.runnerWorkerStarting()
	case "worker-failed":
		reply = s.runnerWorkerFailed(request.Failure)
	default:
		reply.Error = "unknown request"
	}
	_ = json.NewEncoder(conn).Encode(reply)
}

func (s *Server) runnerWorkerStarting() localReply {
	s.mu.Lock()
	authorized := s.authorized
	s.mu.Unlock()
	if authorized == nil || authorized.Identity == nil {
		return localReply{Error: "worker was not authorized"}
	}
	point := guestTiming(s.cfg.Timing.Point("runner_worker_exec_started"))
	s.sendStatus(guestproto.RunnerStatus{
		State: guestproto.RunnerWorkerStarted, Identity: authorized.Identity,
		Timing: []guestproto.TimingPoint{point},
	})
	return localReply{Ready: true}
}

func (s *Server) runnerWorkerFailed(reason string) localReply {
	if reason == "" {
		reason = "worker trampoline failed"
	}
	if len(reason) > 512 {
		reason = reason[:512]
	}
	point := guestTiming(s.cfg.Timing.Point("runner_worker_exec_failed"))
	s.sendStatus(guestproto.RunnerStatus{
		State: guestproto.RunnerWorkerFailed, ExitCode: SyntheticFailureExitCode,
		Reason: reason, Timing: []guestproto.TimingPoint{point},
	})
	return localReply{Ready: true}
}

func (s *Server) publishAssignment(assignment *guestproto.Assignment) localReply {
	if assignment == nil || assignment.RequestID == "" || assignment.JobID == "" || assignment.RunnerName == "" ||
		assignment.JobDisplayName == "" || assignment.Identity == nil || assignment.Identity.RunID == "" ||
		assignment.Identity.RunAttempt <= 0 || assignment.Identity.RunnerName != assignment.RunnerName ||
		assignment.Identity.Repository == "" || assignment.Identity.WorkflowJob == "" {
		return localReply{Error: "incomplete assignment"}
	}
	assignment.Timing = append(assignment.Timing, guestTiming(s.cfg.Timing.Point("guest_assignment_received")))
	s.mu.Lock()
	if s.assignment != nil {
		duplicate := s.assignment.RequestID == assignment.RequestID && s.assignment.RunnerName == assignment.RunnerName
		s.mu.Unlock()
		if !duplicate {
			return localReply{Error: "conflicting assignment"}
		}
		return localReply{Ready: true}
	} else {
		captured := *assignment
		captured.Timing = append([]guestproto.TimingPoint(nil), assignment.Timing...)
		s.assignment = &captured
		s.mu.Unlock()
		if err := s.send(guestproto.Message{Kind: guestproto.KindAssignment, Assignment: &captured}); err != nil {
			return localReply{Error: "assignment was not delivered to hostd: " + err.Error()}
		}
	}
	published := guestTiming(s.cfg.Timing.Point("guest_assignment_published"))
	s.sendStatus(guestproto.RunnerStatus{
		State: guestproto.RunnerProgress, Timing: []guestproto.TimingPoint{published},
	})
	return localReply{Ready: true}
}

func (s *Server) awaitWorker(ctx context.Context) localReply {
	s.mu.Lock()
	assignment := s.assignment
	s.mu.Unlock()
	if assignment == nil {
		return localReply{Error: "worker arrived before assignment publication"}
	}
	entered := guestTiming(s.cfg.Timing.Point("runner_worker_gate_entered"))
	s.sendStatus(guestproto.RunnerStatus{
		State: guestproto.RunnerProgress, Timing: []guestproto.TimingPoint{entered},
	})
	select {
	case <-s.workerGate:
		s.mu.Lock()
		err := s.gateErr
		authorized := s.authorized
		clock := s.clock
		s.mu.Unlock()
		if err != nil {
			return localReply{Error: err.Error()}
		}
		if authorized == nil || authorized.Identity == nil || clock == nil {
			return localReply{Error: "worker gate opened without authorization"}
		}
		completed := guestTiming(s.cfg.Timing.Point("runner_worker_gate_completed"))
		s.sendStatus(guestproto.RunnerStatus{
			State: guestproto.RunnerProgress, Timing: []guestproto.TimingPoint{completed},
		})
		return localReply{Ready: true}
	case <-ctx.Done():
		return localReply{Error: ctx.Err().Error()}
	}
}

func (s *Server) validateAssignment(identity *guestproto.JobIdentity) localReply {
	if identity == nil {
		return localReply{Error: "missing identity"}
	}
	s.mu.Lock()
	authorized := s.authorized
	clock := s.clock
	alreadyValidated := s.hookValidated
	s.mu.Unlock()
	if authorized == nil || authorized.Identity == nil || clock == nil {
		return localReply{Error: "worker was not authorized"}
	}
	if mismatch := identityMismatch(authorized.Identity, identity); mismatch != "" {
		return localReply{Error: "job identity does not match assignment: " + mismatch}
	}
	if alreadyValidated {
		return localReply{Ready: true, Env: authorized.Env}
	}
	s.mu.Lock()
	if s.hookValidated {
		s.mu.Unlock()
		return localReply{Ready: true, Env: authorized.Env}
	}
	s.hookValidated = true
	s.mu.Unlock()
	blocked := guestTiming(s.cfg.Timing.Point("job_hook_validated"))
	s.sendStatus(guestproto.RunnerStatus{
		State: guestproto.RunnerHookBlocked, Identity: identity, Clock: clock,
		Timing: []guestproto.TimingPoint{blocked},
	})
	return localReply{Ready: true, Env: authorized.Env}
}

func (s *Server) releaseAssignment(identity *guestproto.JobIdentity) localReply {
	if identity == nil {
		return localReply{Error: "missing identity"}
	}
	s.mu.Lock()
	authorized := s.authorized
	clock := s.clock
	validated := s.hookValidated
	alreadyReleased := s.hookReleased
	s.mu.Unlock()
	if authorized == nil || authorized.Identity == nil || clock == nil {
		return localReply{Error: "worker was not authorized"}
	}
	if mismatch := identityMismatch(authorized.Identity, identity); mismatch != "" {
		return localReply{Error: "job identity does not match assignment: " + mismatch}
	}
	if !validated {
		return localReply{Error: "job hook was not validated"}
	}
	if alreadyReleased {
		return localReply{Ready: true}
	}
	s.mu.Lock()
	if s.hookReleased {
		s.mu.Unlock()
		return localReply{Ready: true}
	}
	s.hookReleased = true
	s.mu.Unlock()
	released := guestTiming(s.cfg.Timing.Point("customer_steps_released"))
	s.sendStatus(guestproto.RunnerStatus{
		State: guestproto.RunnerReleased, Identity: identity, Clock: clock,
		Timing: []guestproto.TimingPoint{released},
	})
	return localReply{Ready: true}
}

func identityMismatch(expected, actual *guestproto.JobIdentity) string {
	switch {
	case expected == nil || actual == nil:
		return "missing identity"
	case expected.RunID != actual.RunID:
		return "run_id"
	case expected.RunAttempt != actual.RunAttempt:
		return "run_attempt"
	case expected.RunnerName != actual.RunnerName:
		return "runner_name"
	case expected.Repository != actual.Repository:
		return "repository"
	case expected.WorkflowJob != actual.WorkflowJob:
		return "workflow_job"
	default:
		return ""
	}
}

func guestTiming(point timing.Point) guestproto.TimingPoint {
	return guestproto.TimingPoint{
		Event: point.Event, Source: point.Source, BootID: point.BootID,
		Sequence: point.Sequence, MonotonicNS: point.MonotonicNS, UnixNS: point.UnixNS,
	}
}

// IsRunnerAssigned reports the short-lived command invoked by the patched
// Runner.Listener before it dispatches the job to Runner.Worker.
func IsRunnerAssigned(args []string) bool {
	return len(args) == 10 && args[1] == "runner-assigned"
}

func RunRunnerAssigned(args []string) error {
	if !IsRunnerAssigned(args) {
		return errors.New("guestd: invalid runner-assigned invocation")
	}
	requestID, jobID, runnerName := args[2], args[3], args[4]
	runID, rawAttempt, repository, workflowJob, jobDisplayName := args[5], args[6], args[7], args[8], args[9]
	attempt, err := strconv.Atoi(rawAttempt)
	if err != nil || attempt <= 0 ||
		strings.ContainsAny(strings.Join(args[2:], ""), "\x00\r\n") ||
		requestID == "" || jobID == "" || runnerName == "" || runID == "" || repository == "" || workflowJob == "" || jobDisplayName == "" {
		return errors.New("guestd: unsafe assignment identity")
	}
	recorder, err := commandRecorder("runner-listener:" + runnerName)
	if err != nil {
		return err
	}
	assignment := &guestproto.Assignment{
		RequestID: requestID, JobID: jobID, RunnerName: runnerName, JobDisplayName: jobDisplayName,
		Identity: &guestproto.JobIdentity{
			RunID: runID, RunAttempt: attempt, RunnerName: runnerName,
			Repository: repository, WorkflowJob: workflowJob,
		},
		Timing: []guestproto.TimingPoint{guestTiming(recorder.Point("runner_assignment_received"))},
	}
	return callLocal(localRequest{Kind: "assigned", Assignment: assignment}, nil)
}

func IsValidateAssignment(args []string) bool {
	return len(args) == 2 && args[1] == "validate-assignment"
}

func RunValidateAssignment(args []string) error {
	if !IsValidateAssignment(args) {
		return errors.New("guestd: invalid validate-assignment invocation")
	}
	attempt, err := strconv.Atoi(os.Getenv("GITHUB_RUN_ATTEMPT"))
	if err != nil || attempt <= 0 {
		err := errors.New("guestd: invalid GITHUB_RUN_ATTEMPT")
		ReportRunnerWorkerFailure(err)
		return err
	}
	identity := &guestproto.JobIdentity{
		RunID: os.Getenv("GITHUB_RUN_ID"), RunAttempt: attempt,
		RunnerName: os.Getenv("RUNNER_NAME"), Repository: os.Getenv("GITHUB_REPOSITORY"),
		WorkflowJob: os.Getenv("GITHUB_JOB"),
	}
	var reply localReply
	if err := callLocal(localRequest{Kind: "validate", Identity: identity}, &reply); err != nil {
		err = fmt.Errorf("guestd: validate job-start hook: %w", err)
		ReportRunnerWorkerFailure(err)
		return err
	}
	envPath := os.Getenv("GITHUB_ENV")
	if envPath == "" {
		err := errors.New("guestd: GITHUB_ENV is missing")
		ReportRunnerWorkerFailure(err)
		return err
	}
	if err := appendJobEnvironment(envPath, reply.Env); err != nil {
		err = fmt.Errorf("guestd: write job environment: %w", err)
		ReportRunnerWorkerFailure(err)
		return err
	}
	if err := callLocal(localRequest{Kind: "hook-released", Identity: identity}, nil); err != nil {
		err = fmt.Errorf("guestd: release job-start hook: %w", err)
		ReportRunnerWorkerFailure(err)
		return err
	}
	return nil
}

func callLocal(request localRequest, replyOut *localReply) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return callLocalAt(ctx, AssignmentSocketPath, request, replyOut)
}

// PublishRunnerAssignment is the listener's synchronous assignment handoff.
// The CLI wrapper and transport conformance test share this implementation.
func PublishRunnerAssignment(ctx context.Context, socketPath string, assignment guestproto.Assignment) error {
	return callLocalAt(ctx, socketPath, localRequest{Kind: "assigned", Assignment: &assignment}, nil)
}

// AwaitRunnerWorker blocks the privileged trampoline until hostd has restored
// and authorized the generation selected for the published assignment.
func AwaitRunnerWorker(ctx context.Context, socketPath string) error {
	return callLocalAt(ctx, socketPath, localRequest{Kind: "worker-ready"}, nil)
}

// ValidateRunnerAssignment is the job-start hook's defense-in-depth gate.
func ValidateRunnerAssignment(ctx context.Context, socketPath string, identity guestproto.JobIdentity) (map[string]string, error) {
	var reply localReply
	if err := callLocalAt(ctx, socketPath, localRequest{Kind: "validate", Identity: &identity}, &reply); err != nil {
		return nil, err
	}
	return reply.Env, nil
}

// ReleaseRunnerAssignment records the point after the job-start hook has
// installed hostd's environment and immediately before it exits successfully.
func ReleaseRunnerAssignment(ctx context.Context, socketPath string, identity guestproto.JobIdentity) error {
	return callLocalAt(ctx, socketPath, localRequest{Kind: "hook-released", Identity: &identity}, nil)
}

func callLocalAt(ctx context.Context, socketPath string, request localRequest, replyOut *localReply) error {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return err
	}
	var reply localReply
	if err := json.NewDecoder(io.LimitReader(conn, localMessageLimit)).Decode(&reply); err != nil {
		return err
	}
	if reply.Error != "" {
		return errors.New(reply.Error)
	}
	if !reply.Ready {
		return errors.New("guestd: assignment was not released")
	}
	if replyOut != nil {
		*replyOut = reply
	}
	return nil
}

func appendJobEnvironment(path string, env map[string]string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	keys := make([]string, 0, len(env))
	for key, value := range env {
		if key == "" || strings.ContainsAny(key, "=\r\n") || strings.ContainsAny(value, "\r\n") {
			return errors.New("guestd: unsafe job environment")
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := env[key]
		if _, err := fmt.Fprintf(file, "%s=%s\n", key, value); err != nil {
			return err
		}
	}
	return nil
}

func commandRecorder(source string) (*timing.Recorder, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	return timing.New(source, strings.TrimSpace(string(raw)))
}

// IsRunnerWorkerExec detects the Runner.Worker path replaced by the image
// builder. The wrapper preserves GitHub's inherited IPC descriptors while
// nsenter forks the real worker into the restored PID and mount namespaces.
func IsRunnerWorkerExec(args []string) bool {
	if len(args) != 4 || filepath.Base(args[0]) != "Runner.Worker" || args[1] != "spawnclient" {
		return false
	}
	for _, raw := range args[2:] {
		fd, err := strconv.Atoi(raw)
		if err != nil || fd < 3 {
			return false
		}
	}
	return true
}

func RunRunnerWorkerExec(args []string) error {
	if !IsRunnerWorkerExec(args) {
		return errors.New("guestd: invalid Runner.Worker invocation")
	}
	if os.Getuid() != RunnerUID || os.Getgid() != RunnerGID || os.Geteuid() != 0 {
		return errors.New("guestd: Runner.Worker trampoline has invalid credentials")
	}
	for _, raw := range args[2:] {
		fd, _ := strconv.Atoi(raw)
		var stat syscall.Stat_t
		if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFIFO {
			return fmt.Errorf("guestd: Runner.Worker descriptor %d is not an inherited pipe", fd)
		}
	}
	if err := callLocal(localRequest{Kind: "worker-ready"}, nil); err != nil {
		return fmt.Errorf("guestd: wait for restored worker capsule: %w", err)
	}
	raw, err := os.ReadFile(CapsulePIDPath)
	if err != nil {
		return fmt.Errorf("guestd: read capsule pid: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		return errors.New("guestd: invalid capsule pid")
	}
	if err := syscall.Setgroups([]int{}); err != nil {
		return fmt.Errorf("guestd: clear Runner.Worker supplementary groups: %w", err)
	}
	if err := callLocal(localRequest{Kind: "worker-starting"}, nil); err != nil {
		return fmt.Errorf("guestd: report Runner.Worker start: %w", err)
	}
	nsenter := "/usr/bin/nsenter"
	nsenterArgs := []string{
		nsenter, "--target", strconv.Itoa(pid), "--pid", "--mount",
		"--setuid=" + strconv.Itoa(RunnerUID), "--setgid=" + strconv.Itoa(RunnerGID),
		"--", RunnerWorkerReal,
	}
	nsenterArgs = append(nsenterArgs, args[1:]...)
	return syscall.Exec(nsenter, nsenterArgs, os.Environ())
}

func ReportRunnerWorkerFailure(err error) {
	if err == nil {
		return
	}
	_ = callLocal(localRequest{Kind: "worker-failed", Failure: err.Error()}, nil)
}
