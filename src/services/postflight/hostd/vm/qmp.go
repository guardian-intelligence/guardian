package vm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// qmpClient is a minimal QMP client: unix-socket dial, greeting and
// capability negotiation, synchronous Execute with id matching, and an event
// stream. It is deliberately small — the driver needs exactly the tracer-
// proven verbs (query-status, blockdev-add/del, device_add/del, qom-list,
// quit) and nothing else.
type qmpClient struct {
	conn   net.Conn
	events chan qmpEvent

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  uint64
	pending map[uint64]chan qmpMessage
	readErr error
	done    chan struct{}
}

// qmpEvent is one asynchronous QMP event.
type qmpEvent struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// qmpError is a QMP command failure.
type qmpError struct {
	Class string `json:"class"`
	Desc  string `json:"desc"`
}

func (e *qmpError) Error() string { return fmt.Sprintf("qmp: %s: %s", e.Class, e.Desc) }

// qmpMessage is the union of everything the server sends.
type qmpMessage struct {
	Greeting *json.RawMessage `json:"QMP"`
	Event    string           `json:"event"`
	Data     json.RawMessage  `json:"data"`
	Return   json.RawMessage  `json:"return"`
	Error    *qmpError        `json:"error"`
	ID       *uint64          `json:"id"`
}

const qmpDialTimeout = 5 * time.Second

// dialQMP connects to a QMP unix socket, verifies the greeting, and
// negotiates capabilities.
func dialQMP(ctx context.Context, socketPath string) (*qmpClient, error) {
	dialer := net.Dialer{Timeout: qmpDialTimeout}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("qmp: dialing %s: %w", socketPath, err)
	}
	deadline := time.Now().Add(qmpDialTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	reader := bufio.NewReaderSize(conn, 64<<10)
	greeting, err := reader.ReadBytes('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("qmp: reading greeting: %w", err)
	}
	var hello qmpMessage
	if err := json.Unmarshal(greeting, &hello); err != nil || hello.Greeting == nil {
		conn.Close()
		return nil, fmt.Errorf("qmp: malformed greeting %.128q", greeting)
	}
	_ = conn.SetReadDeadline(time.Time{})
	client := &qmpClient{
		conn:    conn,
		events:  make(chan qmpEvent, 32),
		pending: map[uint64]chan qmpMessage{},
		done:    make(chan struct{}),
	}
	go client.read(reader)
	if _, err := client.Execute(ctx, "qmp_capabilities", nil); err != nil {
		client.Close()
		return nil, fmt.Errorf("qmp: negotiating capabilities: %w", err)
	}
	return client, nil
}

// read demultiplexes the server stream: responses to their waiting Execute,
// events to the event channel (dropped when nobody drains them).
func (c *qmpClient) read(reader *bufio.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		var message qmpMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			continue
		}
		switch {
		case message.Event != "":
			select {
			case c.events <- qmpEvent{Event: message.Event, Data: message.Data}:
			default:
			}
		case message.ID != nil:
			c.mu.Lock()
			waiter := c.pending[*message.ID]
			delete(c.pending, *message.ID)
			c.mu.Unlock()
			if waiter != nil {
				waiter <- message
			}
		case message.Error != nil:
			// QMP answers a request it could not parse far enough to
			// extract the id with an id-less error. Commands are processed
			// in order, so it belongs to the oldest in-flight one; dropping
			// it would burn that Execute's full timeout instead.
			c.mu.Lock()
			var waiter chan qmpMessage
			var oldest uint64
			for id, pending := range c.pending {
				if waiter == nil || id < oldest {
					oldest, waiter = id, pending
				}
			}
			delete(c.pending, oldest)
			c.mu.Unlock()
			if waiter != nil {
				waiter <- message
			}
		}
	}
	err := scanner.Err()
	if err == nil {
		err = errors.New("qmp: connection closed")
	}
	c.mu.Lock()
	c.readErr = err
	close(c.done)
	c.mu.Unlock()
}

const qmpExecuteTimeout = 30 * time.Second

// Execute runs one command synchronously and returns its result. Responses
// are matched by id, so interleaved events never confuse a caller.
func (c *qmpClient) Execute(ctx context.Context, command string, arguments any) (json.RawMessage, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, qmpExecuteTimeout)
		defer cancel()
	}
	c.mu.Lock()
	select {
	case <-c.done:
		err := c.readErr
		c.mu.Unlock()
		return nil, fmt.Errorf("qmp: executing %s: %w", command, err)
	default:
	}
	c.nextID++
	id := c.nextID
	waiter := make(chan qmpMessage, 1)
	c.pending[id] = waiter
	c.mu.Unlock()

	request := struct {
		Execute   string  `json:"execute"`
		Arguments any     `json:"arguments,omitempty"`
		ID        *uint64 `json:"id"`
	}{Execute: command, Arguments: arguments, ID: &id}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("qmp: encoding %s: %w", command, err)
	}
	c.writeMu.Lock()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetWriteDeadline(deadline)
	}
	_, err = c.conn.Write(append(encoded, '\n'))
	c.writeMu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("qmp: sending %s: %w", command, err)
	}

	return c.awaitResponse(ctx, command, id, waiter)
}

func (c *qmpClient) awaitResponse(ctx context.Context, command string, id uint64, waiter chan qmpMessage) (json.RawMessage, error) {
	result := func(response qmpMessage) (json.RawMessage, error) {
		if response.Error != nil {
			return nil, fmt.Errorf("qmp: executing %s: %w", command, response.Error)
		}
		return response.Return, nil
	}
	select {
	case response := <-waiter:
		return result(response)
	case <-c.done:
		// QEMU closes QMP immediately after replying to quit. The reader
		// queues that matching response before it closes done, so both
		// cases can be ready here. Preserve the acknowledged command
		// instead of randomly reporting the subsequent EOF.
		select {
		case response := <-waiter:
			return result(response)
		default:
		}
		c.mu.Lock()
		err := c.readErr
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("qmp: executing %s: %w", command, err)
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("qmp: executing %s: %w", command, ctx.Err())
	}
}

// Events exposes the asynchronous event stream.
func (c *qmpClient) Events() <-chan qmpEvent { return c.events }

// Close tears the connection down; in-flight Executes fail.
func (c *qmpClient) Close() error { return c.conn.Close() }
