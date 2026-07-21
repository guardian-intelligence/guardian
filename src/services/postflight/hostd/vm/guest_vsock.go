package vm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vsock"
	"github.com/guardian-intelligence/guardian/src/services/postflight/timing"
)

// VsockGuest is the production Guest: one dialed AF_VSOCK connection per
// VM, guestproto JSON-lines on it, a reader goroutine folding what the
// guest says into an observation. Connections are dialed lazily — a dial
// failure is a guest that is not up yet, which Observe reports as the zero
// observation; the caller's boot deadline owns retries. State lives on the
// connection, never beyond it: a new connection starts from a fresh hello,
// so a recycled CID can never inherit a previous guest's runner status.
type VsockGuest struct {
	port uint32
	dial func(ctx context.Context, cid, port uint32) (net.Conn, error)

	mu      sync.Mutex
	chans   map[ID]*guestChannel
	updates chan ID
	timing  *timing.Recorder
}

var _ Guest = (*VsockGuest)(nil)

// NewVsockGuest wires the transport against the guestd listening port.
func NewVsockGuest() (*VsockGuest, error) {
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, fmt.Errorf("vm: read host boot id: %w", err)
	}
	recorder, err := timing.New("hostd-vsock", strings.TrimSpace(string(bootID)))
	if err != nil {
		return nil, err
	}
	return &VsockGuest{
		port:    guestproto.VsockPort,
		dial:    vsock.Dial,
		chans:   map[ID]*guestChannel{},
		updates: make(chan ID, 256),
		timing:  recorder,
	}, nil
}

// Updates emits a coalescible hint whenever guestd advances a VM's level
// state. hostd immediately re-observes through List instead of waiting for
// its control-plane polling interval.
func (g *VsockGuest) Updates() <-chan ID { return g.updates }

func (g *VsockGuest) notify(id ID) {
	select {
	case g.updates <- id:
	default:
	}
}

// Prepare implements Guest.
func (g *VsockGuest) Prepare(ctx context.Context, id ID, cid uint32, prepare guestproto.Prepare) error {
	channel, err := g.channel(ctx, id, cid)
	if err != nil {
		return fmt.Errorf("vm: guest channel for %s: %w", id, err)
	}
	if err := channel.awaitHello(ctx); err != nil {
		return fmt.Errorf("vm: guest %s never said hello: %w", id, err)
	}
	return channel.write(ctx, guestproto.Message{Kind: guestproto.KindPrepare, Prepare: &prepare})
}

// Rendezvous implements Guest.
func (g *VsockGuest) Rendezvous(ctx context.Context, id ID, cid uint32, rendezvous guestproto.Rendezvous) error {
	channel, err := g.channel(ctx, id, cid)
	if err != nil {
		return fmt.Errorf("vm: guest channel for %s: %w", id, err)
	}
	if err := channel.awaitHello(ctx); err != nil {
		return fmt.Errorf("vm: guest %s never said hello: %w", id, err)
	}
	return channel.write(ctx, guestproto.Message{Kind: guestproto.KindRendezvous, Rendezvous: &rendezvous})
}

// Authorize implements Guest.
func (g *VsockGuest) Authorize(ctx context.Context, id ID, cid uint32, authorize guestproto.Authorize) error {
	channel, err := g.channel(ctx, id, cid)
	if err != nil {
		return fmt.Errorf("vm: guest channel for %s: %w", id, err)
	}
	if err := channel.awaitHello(ctx); err != nil {
		return fmt.Errorf("vm: guest %s never said hello: %w", id, err)
	}
	return channel.write(ctx, guestproto.Message{Kind: guestproto.KindAuthorize, Authorize: &authorize})
}

// Observe implements Guest.
func (g *VsockGuest) Observe(ctx context.Context, id ID, cid uint32) (GuestObservation, error) {
	channel, err := g.channel(ctx, id, cid)
	if err != nil {
		return GuestObservation{}, nil // not up yet: the zero observation
	}
	if err := channel.awaitHello(ctx); err != nil {
		return GuestObservation{}, nil
	}
	return channel.snapshot(), nil
}

// Quiesce implements Guest.
func (g *VsockGuest) Quiesce(ctx context.Context, id ID, cid uint32, request guestproto.Quiesce) (guestproto.Quiesced, error) {
	channel, err := g.channel(ctx, id, cid)
	if err != nil {
		return guestproto.Quiesced{}, fmt.Errorf("vm: guest channel for %s: %w", id, err)
	}
	if err := channel.awaitHello(ctx); err != nil {
		return guestproto.Quiesced{}, fmt.Errorf("vm: guest %s never said hello: %w", id, err)
	}
	reply, err := channel.registerQuiesce()
	if err != nil {
		return guestproto.Quiesced{}, fmt.Errorf("vm: quiesce %s: %w", id, err)
	}
	message := guestproto.Message{Kind: guestproto.KindQuiesce, Quiesce: &request}
	if err := channel.write(ctx, message); err != nil {
		channel.abandonQuiesce(reply)
		return guestproto.Quiesced{}, fmt.Errorf("vm: quiesce %s: %w", id, err)
	}
	select {
	case result := <-reply:
		if result.err != nil {
			return guestproto.Quiesced{}, fmt.Errorf("vm: quiesce %s: %w", id, result.err)
		}
		return result.reply, nil
	case <-ctx.Done():
		// Free the reply slot: the lease is failing anyway, and a stale
		// claim must not poison a later quiesce on this channel.
		channel.abandonQuiesce(reply)
		return guestproto.Quiesced{}, fmt.Errorf("vm: quiesce %s: %w", id, ctx.Err())
	}
}

// channel returns the live channel for a VM, dialing one if needed. Dials
// for the same ID are single-flighted so concurrent verbs share one
// connection instead of racing the guest's one-at-a-time accept.
func (g *VsockGuest) channel(ctx context.Context, id ID, cid uint32) (*guestChannel, error) {
	for {
		g.mu.Lock()
		if existing, ok := g.chans[id]; ok {
			if existing.cid == cid {
				g.mu.Unlock()
				if err := existing.awaitDialed(ctx); err != nil {
					return nil, err
				}
				return existing, nil
			}
			// The CID changed: this entry belongs to a previous VM life.
			delete(g.chans, id)
			g.mu.Unlock()
			existing.shutdown(errors.New("vm: superseded guest channel"))
			continue
		}
		channel := newGuestChannel(cid)
		g.chans[id] = channel
		g.mu.Unlock()

		conn, err := g.dial(ctx, cid, g.port)
		if err != nil {
			g.remove(id, channel)
			channel.shutdown(err)
			return nil, err
		}
		channel.attach(conn)
		go g.read(id, channel)
		return channel, nil
	}
}

func (g *VsockGuest) remove(id ID, channel *guestChannel) {
	g.mu.Lock()
	if g.chans[id] == channel {
		delete(g.chans, id)
	}
	g.mu.Unlock()
}

// read consumes the guest's stream until the connection dies, then retires
// the channel so the next verb dials fresh.
func (g *VsockGuest) read(id ID, channel *guestChannel) {
	decoder := guestproto.NewDecoder(channel.conn)
	for {
		message, err := decoder.Read()
		if err != nil {
			g.remove(id, channel)
			channel.shutdown(err)
			return
		}
		switch message.Kind {
		case guestproto.KindHello:
			if message.Hello.Version != guestproto.Version {
				g.remove(id, channel)
				channel.shutdown(fmt.Errorf(
					"vm: guest protocol version %d, want %d",
					message.Hello.Version, guestproto.Version))
				return
			}
			channel.markHello()
			g.notify(id)
		case guestproto.KindAssignment:
			point := g.timing.Point("vsock_assignment_received")
			channel.foldAssignment(*message.Assignment, guestproto.TimingPoint{
				Event: point.Event, Source: point.Source, BootID: point.BootID,
				Sequence: point.Sequence, MonotonicNS: point.MonotonicNS, UnixNS: point.UnixNS,
			})
			g.notify(id)
		case guestproto.KindRunnerStatus:
			channel.fold(*message.RunnerStatus)
			g.notify(id)
		case guestproto.KindQuiesced:
			channel.resolveQuiesce(*message.Quiesced, nil)
		case guestproto.KindQuiesceFailed:
			channel.resolveQuiesce(guestproto.Quiesced{}, errors.New(message.QuiesceFailed.Reason))
		default:
			// A host-bound stream carrying host→guest verbs is a broken
			// peer; nothing on this connection can be trusted anymore.
			g.remove(id, channel)
			channel.shutdown(fmt.Errorf("vm: guest sent %s", message.Kind))
			return
		}
	}
}

// guestChannel is one VM's connection state. dialed gates attach (the
// single-flight latch), hello gates use, dead retires it.
type guestChannel struct {
	cid uint32

	dialed chan struct{}
	conn   net.Conn

	writeMu sync.Mutex

	mu          sync.Mutex
	helloSeen   bool
	observation GuestObservation
	pending     chan quiesceResult
	failure     error

	hello chan struct{}
	dead  chan struct{}
}

func newGuestChannel(cid uint32) *guestChannel {
	return &guestChannel{
		cid:    cid,
		dialed: make(chan struct{}),
		hello:  make(chan struct{}),
		dead:   make(chan struct{}),
	}
}

func (c *guestChannel) attach(conn net.Conn) {
	c.conn = conn
	close(c.dialed)
}

// awaitDialed waits out another caller's in-flight dial of this channel.
func (c *guestChannel) awaitDialed(ctx context.Context) error {
	select {
	case <-c.dialed:
	case <-c.dead:
		return c.failed()
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-c.dead:
		return c.failed()
	default:
		return nil
	}
}

// awaitHello blocks until the guest announced itself, the channel died, or
// the context expired — the probe = dial + hello within deadline contract.
func (c *guestChannel) awaitHello(ctx context.Context) error {
	select {
	case <-c.hello:
		return nil
	case <-c.dead:
		return c.failed()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *guestChannel) markHello() {
	c.mu.Lock()
	c.observation.Hello = true
	first := !c.helloSeen
	c.helloSeen = true
	c.mu.Unlock()
	if first {
		close(c.hello)
	}
}

func (c *guestChannel) fold(status guestproto.RunnerStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch status.State {
	case guestproto.RunnerRegistered:
		c.observation.RunnerRegistered = true
	case guestproto.RunnerHookBlocked:
		c.observation.RunnerRegistered = true
		c.observation.HookBlocked = true
		if status.Identity != nil {
			c.observation.Identity = *status.Identity
		}
	case guestproto.RunnerMountsReady:
		c.observation.MountsReady = true
		if status.Clock != nil {
			c.observation.Clock = *status.Clock
		}
	case guestproto.RunnerWorkerReady:
		c.observation.RunnerRegistered = true
		c.observation.MountsReady = true
		c.observation.WorkerReady = true
		if status.Identity != nil {
			c.observation.Identity = *status.Identity
		}
		if status.Clock != nil {
			c.observation.Clock = *status.Clock
		}
	case guestproto.RunnerWorkerStarted:
		c.observation.WorkerStarted = true
	case guestproto.RunnerWorkerFailed:
		c.observation.WorkerFailed = true
		c.observation.RunnerExited = true
		c.observation.ExitCode = status.ExitCode
		c.observation.FailureReason = status.Reason
	case guestproto.RunnerReleased:
		c.observation.RunnerRegistered = true
		c.observation.HookBlocked = true
		c.observation.MountsReady = true
		c.observation.Released = true
		if status.Identity != nil {
			c.observation.Identity = *status.Identity
		}
		if status.Clock != nil {
			c.observation.Clock = *status.Clock
		}
	case guestproto.RunnerExited:
		c.observation.RunnerExited = true
		c.observation.ExitCode = status.ExitCode
	}
	c.observation.Timing = append(c.observation.Timing, status.Timing...)
}

func (c *guestChannel) foldAssignment(assignment guestproto.Assignment, received guestproto.TimingPoint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	captured := assignment
	captured.Timing = append([]guestproto.TimingPoint(nil), assignment.Timing...)
	if assignment.Identity != nil {
		identity := *assignment.Identity
		captured.Identity = &identity
	}
	c.observation.Assignment = &captured
	c.observation.Timing = append(c.observation.Timing, assignment.Timing...)
	c.observation.Timing = append(c.observation.Timing, received)
}

func (c *guestChannel) snapshot() GuestObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	copy := c.observation
	copy.Timing = append([]guestproto.TimingPoint(nil), c.observation.Timing...)
	if c.observation.Assignment != nil {
		assignment := *c.observation.Assignment
		assignment.Timing = append([]guestproto.TimingPoint(nil), c.observation.Assignment.Timing...)
		if c.observation.Assignment.Identity != nil {
			identity := *c.observation.Assignment.Identity
			assignment.Identity = &identity
		}
		copy.Assignment = &assignment
	}
	return copy
}

// registerQuiesce claims the single quiesce-reply slot.
type quiesceResult struct {
	reply guestproto.Quiesced
	err   error
}

func (c *guestChannel) registerQuiesce() (chan quiesceResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending != nil {
		return nil, errors.New("quiesce already in flight")
	}
	c.pending = make(chan quiesceResult, 1)
	return c.pending, nil
}

func (c *guestChannel) resolveQuiesce(reply guestproto.Quiesced, err error) {
	c.mu.Lock()
	pending := c.pending
	c.pending = nil
	c.mu.Unlock()
	if pending != nil {
		pending <- quiesceResult{reply: reply, err: err}
	}
}

// abandonQuiesce releases a claimed reply slot whose waiter gave up; a late
// reply then resolves into nothing instead of a stale claim blocking the
// next quiesce.
func (c *guestChannel) abandonQuiesce(pending chan quiesceResult) {
	c.mu.Lock()
	if c.pending == pending {
		c.pending = nil
	}
	c.mu.Unlock()
}

// write frames one message under the context's deadline. The bound matters:
// a guest that stopped reading must not wedge the caller — and with it the
// driver mutex — on a full socket buffer.
func (c *guestChannel) write(ctx context.Context, message guestproto.Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.dead:
		return c.failed()
	default:
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetWriteDeadline(deadline)
		defer c.conn.SetWriteDeadline(time.Time{})
	}
	return guestproto.NewEncoder(c.conn).Write(message)
}

func (c *guestChannel) failed() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failure
}

func (c *guestChannel) shutdown(err error) {
	c.mu.Lock()
	alreadyDead := c.failure != nil
	if !alreadyDead {
		c.failure = err
	}
	c.mu.Unlock()
	if alreadyDead {
		return
	}
	close(c.dead)
	c.resolveQuiesce(guestproto.Quiesced{}, err)
	if c.conn != nil {
		c.conn.Close()
	}
}
