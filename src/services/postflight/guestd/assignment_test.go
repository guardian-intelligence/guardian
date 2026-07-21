package guestd

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/timing"
)

func TestLocalAssignmentGateBlocksWorkerUntilHostRelease(t *testing.T) {
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
		RequestID: "request-7", JobID: "job-9", RunnerName: "runner-2", JobDisplayName: "benchmark",
		Identity: &guestproto.JobIdentity{
			RunID: "42", RunAttempt: 1, RunnerName: "runner-2",
			Repository: "guardian-intelligence/turborepo-tuned", WorkflowJob: "benchmark",
		},
		Timing: []guestproto.TimingPoint{{
			Event: "runner_assignment_received", Source: "listener", BootID: "boot-test",
			Sequence: 1, MonotonicNS: 10, UnixNS: 20,
		}},
	}
	released := make(chan error, 1)
	go func() {
		released <- callLocalAt(context.Background(), socket, localRequest{Kind: "assigned", Assignment: assignment}, nil)
	}()

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
		case err := <-released:
			t.Fatalf("gate released before host authorization: %v", err)
		case <-deadline:
			t.Fatal("guestd did not observe local assignment")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case err := <-released:
		t.Fatalf("gate released before host authorization: %v", err)
	default:
	}
	server.gateOnce.Do(func() { close(server.workerGate) })
	if err := <-released; err != nil {
		t.Fatal(err)
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
