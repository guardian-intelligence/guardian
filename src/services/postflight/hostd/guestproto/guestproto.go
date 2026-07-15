// Package guestproto is the wire contract between hostd and guestd: JSON
// lines over the per-VM vsock channel. guestd reports hello (booted and
// idle) and runner status (registered, exited) up; hostd delivers the runner
// assignment down. Both ends frame through Encoder/Decoder so the message
// vocabulary and size bounds live in exactly one place.
package guestproto

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// Version is the protocol revision a Hello announces.
const Version = 1

// MaxMessageBytes bounds one encoded message line. The guest is untrusted:
// a reader must never buffer an attacker-chosen amount.
const MaxMessageBytes = 1 << 20

// Kind discriminates the message union.
type Kind string

const (
	// KindHello (guest → host): guestd is up and idle.
	KindHello Kind = "hello"
	// KindAssignment (host → guest): everything the guest needs to become a
	// job runner.
	KindAssignment Kind = "assignment"
	// KindRunnerStatus (guest → host): the runner registered or exited.
	KindRunnerStatus Kind = "runner-status"
)

// Message is one frame on the channel: a kind and exactly the matching
// payload.
type Message struct {
	Kind         Kind          `json:"kind"`
	Hello        *Hello        `json:"hello,omitempty"`
	Assignment   *Assignment   `json:"assignment,omitempty"`
	RunnerStatus *RunnerStatus `json:"runner_status,omitempty"`
}

// Hello announces a booted guest.
type Hello struct {
	Version int `json:"version"`
}

// Assignment carries the runner assignment down to the guest.
type Assignment struct {
	// Lease names the lease this assignment serves; guestd deduplicates
	// redelivery on it.
	Lease string `json:"lease"`
	// WorkspaceSerial is the SCSI serial of the hot-attached workspace
	// device; the guest mounts it via /dev/disk/by-id, never by probe order.
	WorkspaceSerial string `json:"workspace_serial"`
	// JITConfig is the encoded single-use runner registration blob.
	JITConfig string `json:"jit_config"`
	// Env is the runner environment.
	Env map[string]string `json:"env,omitempty"`
}

// RunnerState is the runner lifecycle as guestd observes it.
type RunnerState string

const (
	// RunnerRegistered: the runner registered and is listening for its job.
	RunnerRegistered RunnerState = "registered"
	// RunnerExited: the runner finished; ExitCode is meaningful.
	RunnerExited RunnerState = "exited"
)

// RunnerStatus reports the runner lifecycle up to the host.
type RunnerStatus struct {
	State    RunnerState `json:"state"`
	ExitCode int         `json:"exit_code,omitempty"`
}

// Validate rejects frames whose payload does not match their kind, so a
// malformed peer fails at the seam instead of deep in a consumer.
func (m Message) Validate() error {
	payloads := 0
	for _, present := range []bool{m.Hello != nil, m.Assignment != nil, m.RunnerStatus != nil} {
		if present {
			payloads++
		}
	}
	var matched bool
	switch m.Kind {
	case KindHello:
		matched = m.Hello != nil
	case KindAssignment:
		matched = m.Assignment != nil
	case KindRunnerStatus:
		matched = m.RunnerStatus != nil
	default:
		return fmt.Errorf("guestproto: unknown kind %q", m.Kind)
	}
	if !matched || payloads != 1 {
		return fmt.Errorf("guestproto: %s frame with mismatched payload", m.Kind)
	}
	return nil
}

// Encoder writes messages as JSON lines.
type Encoder struct {
	w io.Writer
}

// NewEncoder wraps a transport writer.
func NewEncoder(w io.Writer) *Encoder { return &Encoder{w: w} }

// Write frames one message.
func (e *Encoder) Write(m Message) error {
	if err := m.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("guestproto: encoding %s: %w", m.Kind, err)
	}
	if len(encoded) > MaxMessageBytes {
		return fmt.Errorf("guestproto: %s frame is %d bytes, limit %d", m.Kind, len(encoded), MaxMessageBytes)
	}
	_, err = e.w.Write(append(encoded, '\n'))
	return err
}

// Decoder reads messages from a stream of JSON lines.
type Decoder struct {
	scanner *bufio.Scanner
}

// NewDecoder wraps a transport reader.
func NewDecoder(r io.Reader) *Decoder {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), MaxMessageBytes)
	return &Decoder{scanner: scanner}
}

// Read returns the next message, io.EOF at a clean end of stream.
func (d *Decoder) Read() (Message, error) {
	if !d.scanner.Scan() {
		if err := d.scanner.Err(); err != nil {
			return Message{}, fmt.Errorf("guestproto: reading frame: %w", err)
		}
		return Message{}, io.EOF
	}
	var m Message
	if err := json.Unmarshal(d.scanner.Bytes(), &m); err != nil {
		return Message{}, fmt.Errorf("guestproto: decoding frame: %w", err)
	}
	if err := m.Validate(); err != nil {
		return Message{}, err
	}
	return m, nil
}
