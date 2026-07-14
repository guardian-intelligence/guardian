package sim

import (
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/agent"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

// Every invariant must fire on the world state that embodies its violation
// — an invariant that cannot be made to fail proves nothing. This table is
// the vacuity proof: one violating state per invariant, plus a healthy
// state that every invariant must accept.

func healthyState() WorldState {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return WorldState{
		Now: now,
		Leases: []agent.LeaseSnapshot{
			{LeaseID: "l1", State: agent.StateReady, Since: now, VMID: "vm-1", ExecutionID: "e1", AttemptID: "a1"},
			{LeaseID: "l2", State: agent.StateFailed, Since: now, ExecutionID: "e2", AttemptID: "a2"},
		},
		VMs: []vm.Status{
			{ID: "vm-1", Class: "c", Phase: vm.PhaseReady, Lease: "l1"},
			{ID: "pool-1", Class: "c", Phase: vm.PhaseWarm},
		},
		Generations: []zvol.GenerationSnapshot{{Generation: "g1"}},
		Slots:       map[vm.Class]int{"c": 4},
		SealedEver:  map[string]bool{"g1": true},
		Reaped:      map[string]bool{},
		Resolves: func(executionID, _ string) bool {
			return executionID == "e1" // only the live lease resolves
		},
	}
}

func TestInvariantsAcceptHealthyState(t *testing.T) {
	state := healthyState()
	for _, invariant := range Invariants {
		if violation := invariant.Check(state); violation != "" {
			t.Errorf("%s rejected a healthy state: %s", invariant.Name, violation)
		}
	}
}

func TestInvariantVacuity(t *testing.T) {
	violations := map[string]func(*WorldState){
		"vm-per-lease": func(state *WorldState) {
			state.Leases = append(state.Leases, agent.LeaseSnapshot{
				LeaseID: "l3", State: agent.StateReady, Since: state.Now, VMID: "vm-1",
			})
		},
		"slot-bounds": func(state *WorldState) {
			for i := 0; i < 5; i++ {
				state.VMs = append(state.VMs, vm.Status{ID: vm.ID(rune('a' + i)), Class: "c", Phase: vm.PhaseWarm})
			}
		},
		"sealed-survives": func(state *WorldState) {
			state.Generations = nil // g1 vanished, no reap verb recorded
		},
		"deadline-release": func(state *WorldState) {
			deadline, _ := agent.StateDeadline(agent.StateClaiming)
			state.Leases = append(state.Leases, agent.LeaseSnapshot{
				LeaseID: "stuck", State: agent.StateClaiming,
				Since: state.Now.Add(-deadline - time.Minute),
			})
		},
		"terminal-never-resolves": func(state *WorldState) {
			state.Resolves = func(string, string) bool { return true } // e2 resolves though l2 failed
		},
		"failed-holds-no-vm": func(state *WorldState) {
			state.Leases[1].VMID = "vm-9"
		},
	}

	for _, invariant := range Invariants {
		violate, ok := violations[invariant.Name]
		if !ok {
			t.Fatalf("invariant %s has no vacuity proof — decorative invariants are build failures", invariant.Name)
		}
		state := healthyState()
		violate(&state)
		if invariant.Check(state) == "" {
			t.Errorf("%s failed to fire on its violating state", invariant.Name)
		}
	}

	for name := range violations {
		found := false
		for _, invariant := range Invariants {
			if invariant.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("vacuity proof %s has no matching invariant", name)
		}
	}
}
