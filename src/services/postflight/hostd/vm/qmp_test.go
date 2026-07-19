package vm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// qmpServer is a scripted fake QMP endpoint. Each received command is
// answered by its handler; anything the handler emits before the response
// line (events, noise) is written first, exactly as a real QEMU interleaves
// them.
type qmpServer struct {
	socket   string
	greeting string
	// handle maps a command to (pre-response lines, response). The response
	// template's %d receives the request id.
	handle func(command string, arguments json.RawMessage) (before []string, response string)

	listener net.Listener
}

const defaultGreeting = `{"QMP": {"version": {"qemu": {"micro": 2, "minor": 2, "major": 8}}, "capabilities": ["oob"]}}`

func startQMPServer(t *testing.T, server *qmpServer) string {
	t.Helper()
	if server.socket == "" {
		server.socket = filepath.Join(shortTempDir(t), "qmp.sock")
	}
	if server.greeting == "" {
		server.greeting = defaultGreeting
	}
	listener, err := net.Listen("unix", server.socket)
	if err != nil {
		t.Fatalf("listening on %s: %v", server.socket, err)
	}
	server.listener = listener
	t.Cleanup(func() { listener.Close() })
	go server.serve()
	return server.socket
}

func (s *qmpServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.session(conn)
	}
}

func (s *qmpServer) session(conn net.Conn) {
	defer conn.Close()
	if _, err := fmt.Fprintln(conn, s.greeting); err != nil {
		return
	}
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var request struct {
			Execute   string          `json:"execute"`
			Arguments json.RawMessage `json:"arguments"`
			ID        uint64          `json:"id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			continue
		}
		if request.Execute == "qmp_capabilities" {
			fmt.Fprintf(conn, `{"return": {}, "id": %d}`+"\n", request.ID)
			continue
		}
		before, response := s.handle(request.Execute, request.Arguments)
		for _, line := range before {
			fmt.Fprintln(conn, line)
		}
		if response == "" {
			continue // scripted silence
		}
		if strings.Contains(response, "%d") {
			response = fmt.Sprintf(response, request.ID)
		}
		fmt.Fprintln(conn, response)
		if request.Execute == "quit" {
			return
		}
	}
}

// shortTempDir returns a temp dir with a path short enough for a unix
// socket, which t.TempDir cannot guarantee under Bazel.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "vm-test-*")
	if err != nil {
		t.Fatalf("making temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestQMPExecute(t *testing.T) {
	for name, testCase := range map[string]struct {
		handle    func(command string, arguments json.RawMessage) ([]string, string)
		wantValue string
		wantErr   string
	}{
		"result returned": {
			handle: func(command string, _ json.RawMessage) ([]string, string) {
				return nil, `{"return": {"status": "running"}, "id": %d}`
			},
			wantValue: "running",
		},
		"events interleaved before the response": {
			handle: func(command string, _ json.RawMessage) ([]string, string) {
				return []string{
					`{"event": "NIC_RX_FILTER_CHANGED", "timestamp": {"seconds": 1, "microseconds": 2}}`,
					`{"event": "DEVICE_DELETED", "data": {"device": "dev-workspace"}, "timestamp": {"seconds": 1, "microseconds": 3}}`,
				}, `{"return": {"status": "running"}, "id": %d}`
			},
			wantValue: "running",
		},
		"command error": {
			handle: func(command string, _ json.RawMessage) ([]string, string) {
				return nil, `{"error": {"class": "GenericError", "desc": "Node 'workspace' is busy"}, "id": %d}`
			},
			wantErr: "Node 'workspace' is busy",
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := &qmpServer{handle: testCase.handle}
			socket := startQMPServer(t, server)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, err := dialQMP(ctx, socket)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer client.Close()
			result, err := client.Execute(ctx, "query-status", nil)
			if testCase.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
					t.Fatalf("error %v, want %q", err, testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			var reply struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(result, &reply); err != nil {
				t.Fatalf("parsing result: %v", err)
			}
			if reply.Status != testCase.wantValue {
				t.Fatalf("status %q, want %q", reply.Status, testCase.wantValue)
			}
		})
	}
}

func TestQMPExecutePrefersQueuedResponseOverConnectionClose(t *testing.T) {
	done := make(chan struct{})
	close(done)
	waiter := make(chan qmpMessage, 1)
	waiter <- qmpMessage{Return: json.RawMessage(`{"acknowledged":true}`)}
	client := &qmpClient{
		done:    done,
		readErr: errors.New("qmp: connection closed"),
		pending: map[uint64]chan qmpMessage{1: waiter},
	}

	result, err := client.awaitResponse(context.Background(), "quit", 1, waiter)
	if err != nil {
		t.Fatalf("acknowledged quit reported close error: %v", err)
	}
	if string(result) != `{"acknowledged":true}` {
		t.Fatalf("result = %s", result)
	}
}

func TestQMPEventsReachTheEventChannel(t *testing.T) {
	server := &qmpServer{handle: func(command string, _ json.RawMessage) ([]string, string) {
		return []string{`{"event": "DEVICE_DELETED", "data": {"device": "dev-workspace"}, "timestamp": {"seconds": 1, "microseconds": 3}}`},
			`{"return": {}, "id": %d}`
	}}
	socket := startQMPServer(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := dialQMP(ctx, socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	if _, err := client.Execute(ctx, "device_del", map[string]any{"id": "dev-workspace"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	select {
	case event := <-client.Events():
		if event.Event != "DEVICE_DELETED" {
			t.Fatalf("event %q, want DEVICE_DELETED", event.Event)
		}
	case <-ctx.Done():
		t.Fatal("no event arrived")
	}
}

func TestQMPMalformedGreeting(t *testing.T) {
	for name, greeting := range map[string]string{
		"not the QMP object": `{"hello": true}`,
		"not json at all":    `SSH-2.0-OpenSSH_9.6`,
	} {
		t.Run(name, func(t *testing.T) {
			socket := startQMPServer(t, &qmpServer{greeting: greeting, handle: func(string, json.RawMessage) ([]string, string) {
				return nil, `{"return": {}, "id": %d}`
			}})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := dialQMP(ctx, socket); err == nil {
				t.Fatal("accepted a malformed greeting")
			}
		})
	}
}

func TestQMPShortRead(t *testing.T) {
	// The server dies immediately after the greeting: capability negotiation
	// must fail with a transport error, not hang.
	dir := shortTempDir(t)
	socket := filepath.Join(dir, "qmp.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		fmt.Fprintln(conn, defaultGreeting)
		conn.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := dialQMP(ctx, socket); err == nil {
		t.Fatal("dial succeeded against a half-dead server")
	}
}

func TestQMPConnectionDiesMidCommand(t *testing.T) {
	// The server answers capability negotiation, then dies on the next
	// command: the in-flight Execute must fail promptly, not hang.
	dir := shortTempDir(t)
	socket := filepath.Join(dir, "qmp.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprintln(conn, defaultGreeting)
		scanner := bufio.NewScanner(conn)
		if !scanner.Scan() {
			return
		}
		var request struct {
			ID uint64 `json:"id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return
		}
		fmt.Fprintf(conn, `{"return": {}, "id": %d}`+"\n", request.ID)
		scanner.Scan() // swallow the next command, then close without answering
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := dialQMP(ctx, socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	if _, err := client.Execute(ctx, "query-status", nil); err == nil {
		t.Fatal("execute succeeded on a dead connection")
	}
}

// TestQMPErrorWithoutIDFailsTheInflightCommand: QMP answers a request it
// could not parse far enough to extract the id with an id-less error; the
// waiting Execute must fail promptly with it, not burn its full timeout.
func TestQMPErrorWithoutIDFailsTheInflightCommand(t *testing.T) {
	socket := startQMPServer(t, &qmpServer{handle: func(string, json.RawMessage) ([]string, string) {
		return nil, `{"error": {"class": "GenericError", "desc": "JSON parse error, invalid keyword"}}`
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := dialQMP(ctx, socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	if _, err := client.Execute(ctx, "query-status", nil); err == nil || !strings.Contains(err.Error(), "JSON parse error") {
		t.Fatalf("error %v, want the server's parse error", err)
	}
}

func TestQMPExecuteTimeout(t *testing.T) {
	socket := startQMPServer(t, &qmpServer{handle: func(string, json.RawMessage) ([]string, string) {
		return nil, "" // scripted silence
	}})
	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := dialQMP(dialCtx, socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	ctx, cancelExecute := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelExecute()
	if _, err := client.Execute(ctx, "query-status", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v, want deadline exceeded", err)
	}
}
