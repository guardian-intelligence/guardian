package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

type recordingRunner struct {
	program string
	args    []string
	err     error
	calls   int
}

func (r *recordingRunner) Run(_ context.Context, program string, args []string, _ io.Writer, _ io.Writer) error {
	r.program = program
	r.args = append([]string(nil), args...)
	r.calls++
	return r.err
}

type commandExit struct {
	code int
}

func (e commandExit) Error() string {
	return "exit"
}

func (e commandExit) ExitCode() int {
	return e.code
}

func TestUpManagementBuildsAspectBootstrapCommand(t *testing.T) {
	t.Setenv("GUARDIAN_ASPECT", "/repo/aspect")
	var stdout, stderr bytes.Buffer
	runner := &recordingRunner{}

	code := run(context.Background(), []string{
		"up",
		"management",
		"--revision",
		"abc123",
		"--kubeconfig",
		"/tmp/kubeconfig",
		"--request-timeout",
		"5s",
		"--wait-timeout",
		"30s",
	}, &stdout, &stderr, runner)
	if code != 0 {
		t.Fatalf("run() exit = %d, stderr:\n%s", code, stderr.String())
	}
	if runner.program != "/repo/aspect" {
		t.Fatalf("program = %q, want /repo/aspect", runner.program)
	}
	want := []string{
		"infra",
		"bootstrap",
		"--revision",
		"abc123",
		"--root",
		"src/infrastructure/talm",
		"--endpoints",
		"10.8.0.250",
		"--nodes",
		"10.8.0.11,10.8.0.12,10.8.0.13",
		"--request-timeout",
		"5s",
		"--wait-timeout",
		"30s",
		"--talosconfig",
		"src/infrastructure/talm/talosconfig",
		"--kubeconfig",
		"/tmp/kubeconfig",
	}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args = %#v, want %#v", runner.args, want)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}

func TestUpManagementRequiresRevision(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := &recordingRunner{}

	code := run(context.Background(), []string{"up", "management"}, &stdout, &stderr, runner)
	if code != 2 {
		t.Fatalf("run() exit = %d, want 2", code)
	}
	if runner.calls != 0 {
		t.Fatalf("runner called %d times", runner.calls)
	}
	if !strings.Contains(stderr.String(), "--revision is required") {
		t.Fatalf("stderr missing required revision error:\n%s", stderr.String())
	}
}

func TestUpManagementPassesThroughAspectExitCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := &recordingRunner{err: commandExit{code: 17}}

	code := run(context.Background(), []string{"up", "management", "--revision", "abc123"}, &stdout, &stderr, runner)
	if code != 17 {
		t.Fatalf("run() exit = %d, want 17", code)
	}
}

func TestUpManagementReportsRunnerStartFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := &recordingRunner{err: errors.New("not found")}

	code := run(context.Background(), []string{"up", "management", "--revision", "abc123"}, &stdout, &stderr, runner)
	if code != 1 {
		t.Fatalf("run() exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Fatalf("stderr missing runner error:\n%s", stderr.String())
	}
}

func TestHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := &recordingRunner{}

	code := run(context.Background(), []string{"--help"}, &stdout, &stderr, runner)
	if code != 0 {
		t.Fatalf("run() exit = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "guardian up management") {
		t.Fatalf("stdout missing usage:\n%s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestUpManagementHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := &recordingRunner{}

	code := run(context.Background(), []string{"up", "management", "--help"}, &stdout, &stderr, runner)
	if code != 0 {
		t.Fatalf("run() exit = %d, want 0", code)
	}
	if runner.calls != 0 {
		t.Fatalf("runner called %d times", runner.calls)
	}
	if !strings.Contains(stdout.String(), "aspect infra bootstrap") {
		t.Fatalf("stdout missing up management usage:\n%s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}
