package guestproto

import (
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
)

// TestRoundTrip exchanges every message kind over an in-memory transport,
// both directions, exactly as the vsock channel will carry them.
func TestRoundTrip(t *testing.T) {
	messages := []Message{
		{Kind: KindHello, Hello: &Hello{Version: Version}},
		{Kind: KindPrepare, Prepare: &Prepare{
			Lease: "lease-1", JITConfig: "opaque-blob",
			Env: map[string]string{"POSTFLIGHT_RENDEZVOUS_DIR": "/run/postflight-rendezvous"},
		}},
		{Kind: KindRendezvous, Rendezvous: &Rendezvous{
			Lease: "lease-1",
			Mounts: []Mount{{
				Serial:     "workspace",
				Filesystem: "ext4",
				Mountpoint: "/opt/actions-runner/_work/widget/widget",
				Options:    []string{"discard", "noatime", "nodev", "nosuid"},
			}},
		}},
		{Kind: KindAuthorize, Authorize: &Authorize{
			Lease: "lease-1", Env: map[string]string{"POSTFLIGHT_EXECUTION_ID": "exec-1"},
		}},
		{Kind: KindRunnerStatus, RunnerStatus: &RunnerStatus{State: RunnerRegistered}},
		{Kind: KindRunnerStatus, RunnerStatus: &RunnerStatus{
			State: RunnerHookBlocked,
			Identity: &JobIdentity{
				RunID: "1", RunAttempt: 1, RunnerName: "lease-1",
				Repository: "acme/widget", WorkflowJob: "test",
			},
		}},
		{Kind: KindRunnerStatus, RunnerStatus: &RunnerStatus{
			State: RunnerMountsReady,
			Clock: &ClockSample{UnixNS: 1, Synchronized: true, Clocksource: "kvm-clock"},
		}},
		{Kind: KindRunnerStatus, RunnerStatus: &RunnerStatus{State: RunnerReleased}},
		{Kind: KindRunnerStatus, RunnerStatus: &RunnerStatus{State: RunnerWorkerStarted}},
		{Kind: KindRunnerStatus, RunnerStatus: &RunnerStatus{State: RunnerWorkerFailed, ExitCode: 1, Reason: "nsenter failed"}},
		{Kind: KindRunnerStatus, RunnerStatus: &RunnerStatus{State: RunnerExited, ExitCode: 42}},
		{Kind: KindQuiesce, Quiesce: &Quiesce{Mountpoints: []string{"/opt/actions-runner/_work/widget/widget"}}},
		{Kind: KindQuiesced, Quiesced: &Quiesced{}},
		{Kind: KindQuiesceFailed, QuiesceFailed: &QuiesceFailed{Reason: "target is busy"}},
	}
	host, guest := net.Pipe()
	go func() {
		encoder := NewEncoder(host)
		for _, m := range messages {
			if err := encoder.Write(m); err != nil {
				t.Errorf("writing %s: %v", m.Kind, err)
			}
		}
		host.Close()
	}()
	decoder := NewDecoder(guest)
	for _, want := range messages {
		got, err := decoder.Read()
		if err != nil {
			t.Fatalf("reading %s: %v", want.Kind, err)
		}
		if got.Kind != want.Kind {
			t.Fatalf("kind %q, want %q", got.Kind, want.Kind)
		}
		switch want.Kind {
		case KindHello:
			if *got.Hello != *want.Hello {
				t.Fatalf("hello %+v, want %+v", got.Hello, want.Hello)
			}
		case KindPrepare:
			if !reflect.DeepEqual(got.Prepare, want.Prepare) {
				t.Fatalf("prepare %+v, want %+v", got.Prepare, want.Prepare)
			}
		case KindRendezvous:
			if !reflect.DeepEqual(got.Rendezvous, want.Rendezvous) {
				t.Fatalf("rendezvous %+v, want %+v", got.Rendezvous, want.Rendezvous)
			}
		case KindAuthorize:
			if !reflect.DeepEqual(got.Authorize, want.Authorize) {
				t.Fatalf("authorize %+v, want %+v", got.Authorize, want.Authorize)
			}
		case KindRunnerStatus:
			if !reflect.DeepEqual(got.RunnerStatus, want.RunnerStatus) {
				t.Fatalf("runner status %+v, want %+v", got.RunnerStatus, want.RunnerStatus)
			}
		case KindQuiesce:
			if !reflect.DeepEqual(got.Quiesce, want.Quiesce) {
				t.Fatalf("quiesce %+v, want %+v", got.Quiesce, want.Quiesce)
			}
		case KindQuiesceFailed:
			if *got.QuiesceFailed != *want.QuiesceFailed {
				t.Fatalf("quiesce-failed %+v, want %+v", got.QuiesceFailed, want.QuiesceFailed)
			}
		}
	}
	if _, err := decoder.Read(); !errors.Is(err, io.EOF) {
		t.Fatalf("after stream end: %v, want io.EOF", err)
	}
}

func TestDecoderRejectsMalformedFrames(t *testing.T) {
	for name, frame := range map[string]string{
		"unknown kind":       `{"kind":"reboot"}`,
		"missing payload":    `{"kind":"hello"}`,
		"mismatched payload": `{"kind":"hello","runner_status":{"state":"exited"}}`,
		"extra payload":      `{"kind":"hello","hello":{"version":1},"runner_status":{"state":"exited"}}`,
		"not json":           `hello there`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewDecoder(strings.NewReader(frame + "\n")).Read(); err == nil {
				t.Fatalf("accepted %q", frame)
			}
		})
	}
}

func TestDecoderBoundsFrameSize(t *testing.T) {
	frame := `{"kind":"prepare","prepare":{"lease":"` + strings.Repeat("a", MaxMessageBytes) + `"}}`
	if _, err := NewDecoder(strings.NewReader(frame + "\n")).Read(); err == nil {
		t.Fatal("accepted an oversized frame")
	}
}

func TestEncoderRejectsInvalidMessages(t *testing.T) {
	encoder := NewEncoder(io.Discard)
	if err := encoder.Write(Message{Kind: KindHello}); err == nil {
		t.Fatal("encoded a hello without payload")
	}
	if err := encoder.Write(Message{Kind: Kind("boom"), Hello: &Hello{}}); err == nil {
		t.Fatal("encoded an unknown kind")
	}
	huge := Message{Kind: KindPrepare, Prepare: &Prepare{Lease: strings.Repeat("a", MaxMessageBytes)}}
	if err := encoder.Write(huge); err == nil {
		t.Fatal("encoded an oversized frame")
	}
}
