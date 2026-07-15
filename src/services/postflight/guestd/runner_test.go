package guestd

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncedBuffer captures log output safely across the observer goroutine.
type syncedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRedactorScrubsAssignmentValues(t *testing.T) {
	redact := redactor("jit-secret-blob", map[string]string{
		"POSTFLIGHT_CHECKOUT_TOKEN": "tok-3c9f",
		"POSTFLIGHT_EXECUTION_ID":   "exec-1",
		"EMPTY":                     "",
	})
	line := redact.Replace("+ env has tok-3c9f and jit-secret-blob during exec-1")
	if strings.Contains(line, "tok-3c9f") || strings.Contains(line, "jit-secret-blob") || strings.Contains(line, "exec-1") {
		t.Fatalf("secret survived redaction: %q", line)
	}
	if !strings.Contains(line, "[redacted]") {
		t.Fatalf("no redaction marker: %q", line)
	}
}

func TestObserverRedactsMirroredOutputAndFiresEventsOnce(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	var events []RunnerEvent
	var mu sync.Mutex
	observer := observeRunnerOutput(read, redactor("", map[string]string{"POSTFLIGHT_CHECKOUT_TOKEN": "tok-3c9f"}), logger, func(e RunnerEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	lines := []string{
		"√ Connected to GitHub",
		"Listening for Jobs",
		"POSTFLIGHT_CHECKOUT_TOKEN=tok-3c9f",
		"Running job: build",
		"Running job: build",
	}
	if _, err := write.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
	write.Close()
	observer.drain(5 * time.Second)

	if logged := logs.String(); strings.Contains(logged, "tok-3c9f") || !strings.Contains(logged, "[redacted]") {
		t.Fatalf("token not redacted from mirrored output: %q", logged)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0] != EventListening || events[1] != EventJobStarted {
		t.Fatalf("events %v, want one listening then one job-started", events)
	}
}

// TestObserverDrainOutlivesStragglers: a leaked child that inherits the
// runner's stdout keeps the pipe open past the runner's exit; drain must
// return after its grace instead of waiting for the straggler.
func TestObserverDrainOutlivesStragglers(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer write.Close() // the straggler's copy stays open throughout
	logger := slog.New(slog.NewTextHandler(&syncedBuffer{}, nil))
	observer := observeRunnerOutput(read, redactor("", nil), logger, func(RunnerEvent) {})
	if _, err := write.WriteString("Listening for Jobs\n"); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		observer.drain(20 * time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drain never returned while a writer held the pipe")
	}
}
