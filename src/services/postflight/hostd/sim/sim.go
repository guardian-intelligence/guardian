// Package sim runs hostd's production convergence engine against deterministic
// VM and durable-volume fakes. Scenarios advance one observation edge at a
// time, so assignment races and restore failures are reproducible.
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

type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Clock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

type World struct {
	tb    testing.TB
	Zvols *zvol.Fake
	VMs   *vm.Fake
	Agent *agent.Agent
	Clock *Clock

	config    agent.Config
	idCounter int
	desired   syncproto.SyncResponse
	postTick  bool
}

func NewWorld(tb testing.TB, slots map[vm.Class]int) *World {
	tb.Helper()
	images := make(map[vm.Class]string, len(slots))
	for class := range slots {
		images[class] = "golden"
	}
	world := &World{
		tb: tb, Zvols: zvol.NewFake(), VMs: vm.NewFake(),
		Clock: &Clock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		config: agent.Config{
			HostID: "host-1", ControlPlaneOrigin: "http://control.invalid",
			Slots: slots, Images: images, SyncInterval: time.Second,
			CheckoutGuestOrigin: "http://198.51.100.1:8480",
		},
	}
	world.VMs.Images = images
	world.VMs.OnAttach = func(device string) { world.setAttached(device, true) }
	world.VMs.OnDetach = func(device string) { world.setAttached(device, false) }
	world.Agent = world.newAgent()
	return world
}

func (w *World) newAgent() *agent.Agent {
	instance, err := agent.New(w.config, w.Zvols, w.VMs, "credential", make([]byte, 32), agent.Options{
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
	index := strings.LastIndex(device, "/ws/")
	if index < 0 {
		return
	}
	id := zvol.AssignmentID(device[index+len("/ws/"):])
	switch {
	case strings.Contains(device[:index], "/process-state"):
		w.Zvols.SetProcessAttached(id, attached)
	case strings.Contains(device[:index], "/tool-state"):
		w.Zvols.SetToolAttached(id, attached)
	default:
		w.Zvols.SetAttached(id, attached)
	}
}

func (w *World) Restart() { w.Agent = w.newAgent() }

func (w *World) Sync(response syncproto.SyncResponse) {
	if response.BootID == "" {
		response.BootID = w.Report().BootID
	}
	w.desired = response
	w.Agent.HandleSync(response)
	w.postTick = false
	w.CheckInvariants()
}

func (w *World) Tick() {
	w.Agent.Tick(context.Background())
	w.postTick = true
	w.CheckInvariants()
}

func (w *World) TickN(count int) {
	for range count {
		w.Tick()
	}
}

func (w *World) Advance(duration time.Duration) { w.Clock.Advance(duration) }

func (w *World) Report() syncproto.SyncRequest {
	report, err := w.Agent.Report(context.Background())
	if err != nil {
		w.tb.Fatalf("building host report: %v", err)
	}
	return report
}

func (w *World) Assignment(id string) agent.AssignmentSnapshot {
	for _, snapshot := range w.Agent.Snapshot() {
		if snapshot.AssignmentID == id {
			return snapshot
		}
	}
	w.tb.Fatalf("assignment %s is not tracked", id)
	return agent.AssignmentSnapshot{}
}

func (w *World) HasAssignment(id string) bool {
	for _, snapshot := range w.Agent.Snapshot() {
		if snapshot.AssignmentID == id {
			return true
		}
	}
	return false
}

func (w *World) CheckInvariants() {
	w.tb.Helper()
	state := w.observe()
	for _, invariant := range Invariants {
		if violation := invariant.Check(state); violation != "" {
			w.tb.Fatalf("invariant %s violated: %s", invariant.Name, violation)
		}
	}
}

func (w *World) observe() WorldState {
	_, workspaces, err := w.Zvols.Inventory(context.Background())
	if err != nil {
		w.tb.Fatalf("durable-volume inventory: %v", err)
	}
	vms, err := w.VMs.List(context.Background())
	if err != nil {
		w.tb.Fatalf("VM inventory: %v", err)
	}
	return WorldState{
		Assignments: w.Agent.Snapshot(), VMs: vms, Workspaces: workspaces,
		Slots: w.config.Slots, Desired: w.desired, Synced: w.Agent.Synced(), PostTick: w.postTick,
	}
}
