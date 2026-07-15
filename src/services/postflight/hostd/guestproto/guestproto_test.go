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
		{Kind: KindAssignment, Assignment: &Assignment{
			Lease: "lease-1",
			Mounts: []Mount{{
				Serial:     "workspace",
				Filesystem: "ext4",
				Mountpoint: "/opt/actions-runner/_work/widget/widget",
				Options:    []string{"discard", "noatime", "nodev", "nosuid"},
			}},
			JITConfig: "opaque-blob",
			Env:       map[string]string{"POSTFLIGHT_EXECUTION_ID": "exec-1"},
		}},
		{Kind: KindRunnerStatus, RunnerStatus: &RunnerStatus{State: RunnerMounting}},
		{Kind: KindRunnerStatus, RunnerStatus: &RunnerStatus{State: RunnerRegistered}},
		{Kind: KindRunnerStatus, RunnerStatus: &RunnerStatus{State: RunnerJobStarted}},
		{Kind: KindRunnerStatus, RunnerStatus: &RunnerStatus{State: RunnerExited, ExitCode: 42}},
		{Kind: KindQuiesce, Quiesce: &Quiesce{Mountpoint: "/opt/actions-runner/_work/widget/widget"}},
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
		case KindAssignment:
			if got.Assignment.Lease != want.Assignment.Lease ||
				!reflect.DeepEqual(got.Assignment.Mounts, want.Assignment.Mounts) ||
				got.Assignment.JITConfig != want.Assignment.JITConfig ||
				got.Assignment.Env["POSTFLIGHT_EXECUTION_ID"] != want.Assignment.Env["POSTFLIGHT_EXECUTION_ID"] {
				t.Fatalf("assignment %+v, want %+v", got.Assignment, want.Assignment)
			}
		case KindRunnerStatus:
			if *got.RunnerStatus != *want.RunnerStatus {
				t.Fatalf("runner status %+v, want %+v", got.RunnerStatus, want.RunnerStatus)
			}
		case KindQuiesce:
			if *got.Quiesce != *want.Quiesce {
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
	frame := `{"kind":"assignment","assignment":{"lease":"` + strings.Repeat("a", MaxMessageBytes) + `"}}`
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
	huge := Message{Kind: KindAssignment, Assignment: &Assignment{Lease: strings.Repeat("a", MaxMessageBytes)}}
	if err := encoder.Write(huge); err == nil {
		t.Fatal("encoded an oversized frame")
	}
}
