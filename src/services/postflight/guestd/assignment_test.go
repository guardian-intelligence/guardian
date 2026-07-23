package guestd

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/timing"
)

func TestLocalAssignmentPublishesBeforeWorkerGateReleases(t *testing.T) {
	recorder, err := timing.New("guestd-test", "boot-test")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		cfg: Config{
			HookDeadline:         time.Second,
			AssignmentSocketMode: 0o600,
			AssignmentSocketGID:  -1,
			Timing:               recorder,
			Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		workerGate: make(chan struct{}),
	}
	host, guest := net.Pipe()
	server.conn = guest
	go func() { _, _ = io.Copy(io.Discard, host) }()
	t.Cleanup(func() {
		host.Close()
		guest.Close()
	})
	socket := filepath.Join(t.TempDir(), "assignment.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.ServeAssignments(ctx, socket) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	for deadline := time.Now().Add(time.Second); ; {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("assignment socket did not appear")
		}
		time.Sleep(time.Millisecond)
	}

	assignment := &guestproto.Assignment{
		RequestID: "request-7", JobID: "job-9", CheckRunID: 109, RunnerName: "runner-2", JobDisplayName: "benchmark",
		Identity: &guestproto.JobIdentity{
			RunID: "42", RunAttempt: 1, RunnerName: "runner-2",
			Repository: "guardian-intelligence/turborepo-tuned", WorkflowJob: "benchmark",
		},
		Timing: []guestproto.TimingPoint{{
			Event: "runner_assignment_received", Source: "listener", BootID: "boot-test",
			Sequence: 1, MonotonicNS: 10, UnixNS: 20,
		}},
	}
	if err := callLocalAt(context.Background(), socket, localRequest{Kind: "assigned", Assignment: assignment}, nil); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(time.Second)
	for {
		server.mu.Lock()
		observed := server.assignment
		server.mu.Unlock()
		if observed != nil {
			if len(observed.Timing) != 2 || observed.Timing[1].Event != "guest_assignment_received" {
				t.Fatalf("assignment timing = %#v", observed.Timing)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("guestd did not observe local assignment")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	released := make(chan error, 1)
	go func() {
		released <- callLocalAt(context.Background(), socket, localRequest{Kind: "worker-ready"}, nil)
	}()
	select {
	case err := <-released:
		t.Fatalf("worker gate released before host authorization: %v", err)
	default:
	}
	server.mu.Lock()
	server.authorized = &guestproto.Authorize{Identity: assignment.Identity}
	server.clock = &guestproto.ClockSample{UnixNS: 1}
	server.mu.Unlock()
	server.gateOnce.Do(func() { close(server.workerGate) })
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if len(server.statuses) != 3 ||
		server.statuses[0].Timing[0].Event != "guest_assignment_published" ||
		server.statuses[1].Timing[0].Event != "runner_worker_gate_entered" ||
		server.statuses[2].Timing[0].Event != "runner_worker_gate_completed" {
		t.Fatalf("assignment/worker timing = %#v", server.statuses)
	}
}

func TestValidateAssignmentRequiresExactIdentity(t *testing.T) {
	recorder, err := timing.New("guestd-test", "boot-test")
	if err != nil {
		t.Fatal(err)
	}
	identity := &guestproto.JobIdentity{
		RunID: "42", RunAttempt: 1, RunnerName: "runner-2",
		Repository: "guardian-intelligence/turborepo-tuned", WorkflowJob: "benchmark",
	}
	server := &Server{cfg: Config{Timing: recorder, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, authorized: &guestproto.Authorize{
		Identity: identity, Env: map[string]string{"POSTFLIGHT_EXECUTION_ID": "execution-42"},
	}, clock: &guestproto.ClockSample{UnixNS: 1}}

	wrong := *identity
	wrong.RunAttempt = 2
	if reply := server.validateAssignment(&wrong); reply.Error != "job identity does not match assignment: run_attempt" {
		t.Fatalf("mismatched identity reply = %#v", reply)
	}
	reply := server.validateAssignment(identity)
	if !reply.Ready || reply.Env["POSTFLIGHT_EXECUTION_ID"] != "execution-42" {
		t.Fatalf("valid identity reply = %#v", reply)
	}
	if len(server.statuses) != 1 || server.statuses[0].State != guestproto.RunnerHookBlocked {
		t.Fatalf("pre-release status ladder = %#v", server.statuses)
	}
	if reply := server.releaseAssignment(identity); !reply.Ready {
		t.Fatalf("release identity reply = %#v", reply)
	}
	if len(server.statuses) != 2 || server.statuses[0].State != guestproto.RunnerHookBlocked || server.statuses[1].State != guestproto.RunnerReleased {
		t.Fatalf("status ladder = %#v", server.statuses)
	}
}

func TestReleaseAssignmentRequiresValidatedExactIdentity(t *testing.T) {
	recorder, err := timing.New("guestd-test", "boot-test")
	if err != nil {
		t.Fatal(err)
	}
	identity := &guestproto.JobIdentity{
		RunID: "42", RunAttempt: 1, RunnerName: "runner-2",
		Repository: "guardian-intelligence/turborepo-tuned", WorkflowJob: "benchmark",
	}
	server := &Server{cfg: Config{Timing: recorder, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, authorized: &guestproto.Authorize{
		Identity: identity,
	}, clock: &guestproto.ClockSample{UnixNS: 1}}
	if reply := server.releaseAssignment(identity); reply.Error != "job hook was not validated" {
		t.Fatalf("release before validation reply = %#v", reply)
	}
	if reply := server.validateAssignment(identity); !reply.Ready {
		t.Fatalf("validation reply = %#v", reply)
	}
	wrong := *identity
	wrong.WorkflowJob = "different"
	if reply := server.releaseAssignment(&wrong); reply.Error != "job identity does not match assignment: workflow_job" {
		t.Fatalf("wrong release identity reply = %#v", reply)
	}
	if len(server.statuses) != 1 || server.statuses[0].State != guestproto.RunnerHookBlocked {
		t.Fatalf("status ladder = %#v", server.statuses)
	}
}

func TestRunnerWorkerInvocationContract(t *testing.T) {
	if !IsRunnerWorkerExec([]string{"/opt/actions-runner/bin/Runner.Worker", "spawnclient", "10", "11"}) {
		t.Fatal("official spawnclient invocation was not detected")
	}
	for _, args := range [][]string{
		{"/opt/actions-runner/bin/Runner.Worker", "run"},
		{"/opt/actions-runner/bin/Runner.Worker", "spawnclient", "1"},
		{"/opt/actions-runner/bin/Runner.Worker", "spawnclient", "1", "2", "3"},
		{"/opt/actions-runner/bin/Runner.Worker", "spawnclient", "not-a-fd", "2"},
	} {
		if IsRunnerWorkerExec(args) {
			t.Fatalf("unsafe worker invocation was accepted: %q", args)
		}
	}
}

func TestRunnerWorkerNamespaceCommandPreservesImageGroups(t *testing.T) {
	command, args := runnerWorkerNamespaceCommand(42, &syscall.Credential{
		Uid: 1001, Gid: 1001, Groups: []uint32{999, 1001},
	}, []string{"spawnclient", "10", "11"})
	if command != "/usr/bin/nsenter" {
		t.Fatalf("command = %q", command)
	}
	want := []string{
		"/usr/bin/nsenter", "--target", "42", "--pid", "--mount", "--",
		"/usr/bin/setpriv", "--reuid=1001", "--regid=1001", "--groups=999,1001",
		"--inh-caps=-all", "--", "/opt/actions-runner/bin/Runner.Worker.real",
		"spawnclient", "10", "11",
	}
	if len(args) != len(want) {
		t.Fatalf("args = %q, want %q", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %q, want %q", args, want)
		}
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "--setuid") || strings.HasPrefix(arg, "--setgid") {
			t.Fatalf("nsenter credential option drops supplementary groups: %q", args)
		}
	}
}

func TestAppendJobEnvironmentPreservesEmptyValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "github-env")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendJobEnvironment(path, map[string]string{"RUNNER_TRACKING_ID": ""}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "RUNNER_TRACKING_ID=\n" {
		t.Fatalf("job environment = %q", raw)
	}
}

func TestRunnerWorkerStatusReportsStartAndFailure(t *testing.T) {
	recorder, err := timing.New("guestd-test", "boot-test")
	if err != nil {
		t.Fatal(err)
	}
	identity := &guestproto.JobIdentity{RunID: "42", RunAttempt: 1, RunnerName: "runner-2"}
	server := &Server{
		cfg:        Config{Timing: recorder, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		authorized: &guestproto.Authorize{Identity: identity},
	}
	if reply := server.runnerWorkerStarting(); !reply.Ready {
		t.Fatalf("worker start reply = %#v", reply)
	}
	if reply := server.runnerWorkerFailed("nsenter failed"); !reply.Ready {
		t.Fatalf("worker failure reply = %#v", reply)
	}
	if len(server.statuses) != 2 || server.statuses[0].State != guestproto.RunnerWorkerStarted ||
		server.statuses[1].State != guestproto.RunnerWorkerFailed || server.statuses[1].Reason != "nsenter failed" {
		t.Fatalf("worker statuses = %#v", server.statuses)
	}
}
