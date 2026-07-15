// Package sim is hostd's deterministic simulation harness. It runs the real
// agent package against fake substrate drivers and a scripted control
// plane, on a virtual clock, one Tick at a time — the code under test is
// exactly what production runs; only the world around it is simulated.
//
// Invariants (invariants.go) are evaluated after every step of every
// scenario. Each invariant is proven non-vacuous in vacuity_test.go by
// constructing the world state that violates it and asserting the predicate
// fires — an invariant without a demonstrated violation is decorative, and
// the doctrine treats decorative invariants as build failures.
package sim

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/agent"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

// Clock is the virtual time source. Only Advance moves it.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// Now implements the agent's clock seam.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves virtual time forward.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// World is one simulated host: the real agent, fake substrate, virtual
// clock, and the ledger the invariants quantify over.
type World struct {
	tb    testing.TB
	Zvols *zvol.Fake
	VMs   *vm.Fake
	Agent *agent.Agent
	Clock *Clock

	hostSecret []byte
	config     agent.Config
	idCounter  int

	// SealedEver records every generation that has ever been resident on
	// this host (seeded or sealed); Reaped records every generation the
	// scripted control plane has ordered destroyed. I2 quantifies over the
	// difference.
	SealedEver map[string]bool
	Reaped     map[string]bool

	// named is every lease ID the control plane put in the most recent sync,
	// accepted or rejected; hadResource records leases that ever had a VM or
	// workspace. The desired-not-collected invariant quantifies over both.
	named       map[string]bool
	hadResource map[string]bool

	// postTick is true when the world is being observed right after a
	// completed convergence pass; orphan cleanliness only holds there.
	postTick bool
}

// NewWorld builds a world with the given per-class slot totals.
func NewWorld(tb testing.TB, slots map[vm.Class]int) *World {
	tb.Helper()
	world := &World{
		tb:          tb,
		Zvols:       zvol.NewFake(),
		VMs:         vm.NewFake(),
		Clock:       &Clock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		hostSecret:  []byte("0123456789abcdef0123456789abcdef"),
		SealedEver:  map[string]bool{},
		Reaped:      map[string]bool{},
		named:       map[string]bool{},
		hadResource: map[string]bool{},
	}
	// Keep attachment state coherent across the two fakes: assigning a VM
	// holds its workspace volume open; VM death or destruction releases it.
	world.VMs.OnAttach = func(device string) { world.setAttached(device, true) }
	world.VMs.OnDetach = func(device string) { world.setAttached(device, false) }
	world.config = agent.Config{
		HostID:              "host-1",
		ControlPlaneOrigin:  "http://control-plane.invalid",
		Slots:               slots,
		SyncInterval:        time.Second,
		CheckoutGuestOrigin: "http://198.51.100.1:8480",
	}
	world.Agent = world.newAgent()
	return world
}

func (w *World) newAgent() *agent.Agent {
	instance, err := agent.New(w.config, w.Zvols, w.VMs, "sim-credential", w.hostSecret, agent.Options{
		Now: w.Clock.Now,
		NewID: func() string {
			w.idCounter++
			return fmt.Sprintf("id-%03d", w.idCounter)
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		w.tb.Fatalf("building agent: %v", err)
	}
	return instance
}

func (w *World) setAttached(device string, attached bool) {
	// Devices look like /dev/zvol/fake/ws/<lease>.
	if i := strings.LastIndex(device, "/ws/"); i >= 0 {
		w.Zvols.SetAttached(zvol.LeaseID(device[i+len("/ws/"):]), attached)
	}
}

// Restart simulates a hostd crash: a brand-new agent process over the same
// surviving substrate. The new agent must not destroy anything until its
// first sync.
func (w *World) Restart() {
	w.Agent = w.newAgent()
}

// Sync delivers a desired-state snapshot from the scripted control plane
// and records it in the ledger.
func (w *World) Sync(response syncproto.SyncResponse) {
	w.named = map[string]bool{}
	for _, desired := range response.Leases {
		if desired.LeaseID != "" {
			w.named[desired.LeaseID] = true
		}
	}
	for _, generation := range response.Reap {
		w.Reaped[generation] = true
	}
	w.Agent.HandleSync(response)
	w.postTick = false
	w.CheckInvariants()
}

// SeedGeneration makes a generation resident, as a prior seal would have.
func (w *World) SeedGeneration(generation string, bytes int64) {
	w.Zvols.SeedGeneration(zvol.GenerationID(generation), bytes)
	w.SealedEver[generation] = true
}

// Tick advances the agent one convergence step and checks every invariant.
func (w *World) Tick() {
	w.Agent.Tick(context.Background())
	for _, snapshot := range w.Agent.Snapshot() {
		if snapshot.SealedGeneration != "" {
			w.SealedEver[snapshot.SealedGeneration] = true
		}
	}
	w.postTick = true
	w.CheckInvariants()
}

// TickN advances n convergence steps.
func (w *World) TickN(n int) {
	for i := 0; i < n; i++ {
		w.Tick()
	}
}

// Advance moves virtual time.
func (w *World) Advance(d time.Duration) { w.Clock.Advance(d) }

// Report builds the sync request the agent would send now.
func (w *World) Report() syncproto.SyncRequest {
	request, err := w.Agent.Report(context.Background())
	if err != nil {
		w.tb.Fatalf("building report: %v", err)
	}
	return request
}

// Lease returns the snapshot for one lease, failing the test if unknown.
func (w *World) Lease(id string) agent.LeaseSnapshot {
	for _, snapshot := range w.Agent.Snapshot() {
		if snapshot.LeaseID == id {
			return snapshot
		}
	}
	w.tb.Fatalf("lease %s not tracked", id)
	return agent.LeaseSnapshot{}
}

// HasLease reports whether the agent still tracks a lease.
func (w *World) HasLease(id string) bool {
	for _, snapshot := range w.Agent.Snapshot() {
		if snapshot.LeaseID == id {
			return true
		}
	}
	return false
}

// CheckInvariants evaluates every invariant against current world state and
// fails the test on the first violation.
func (w *World) CheckInvariants() {
	w.tb.Helper()
	state := w.observe()
	for _, invariant := range Invariants {
		if violation := invariant.Check(state); violation != "" {
			w.tb.Fatalf("invariant %s violated: %s", invariant.Name, violation)
		}
	}
}

// observe assembles the WorldState the invariant predicates quantify over.
func (w *World) observe() WorldState {
	generations, workspaces, err := w.Zvols.Inventory(context.Background())
	if err != nil {
		w.tb.Fatalf("fake inventory: %v", err)
	}
	vms, err := w.VMs.List(context.Background())
	if err != nil {
		w.tb.Fatalf("fake vm list: %v", err)
	}
	resolve := func(executionID, attemptID string) bool {
		_, ok, err := w.Agent.ResolveActiveLease(context.Background(), executionID, attemptID)
		if err != nil {
			w.tb.Fatalf("resolving lease: %v", err)
		}
		return ok
	}
	for _, status := range vms {
		if status.Lease != "" {
			w.hadResource[status.Lease] = true
		}
	}
	for _, workspace := range workspaces {
		if lease := workspaceLease(workspace.Name); lease != "" {
			w.hadResource[lease] = true
		}
	}
	return WorldState{
		Now:         w.Clock.Now(),
		Leases:      w.Agent.Snapshot(),
		VMs:         vms,
		Generations: generations,
		Workspaces:  workspaces,
		Slots:       w.config.Slots,
		SealedEver:  w.SealedEver,
		Reaped:      w.Reaped,
		Named:       w.named,
		HadResource: w.hadResource,
		Resolves:    resolve,
		Synced:      w.Agent.Synced(),
		PostTick:    w.postTick,
	}
}
