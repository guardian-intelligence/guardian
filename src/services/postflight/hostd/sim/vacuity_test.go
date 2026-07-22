package sim

import (
	"strings"
	"testing"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/agent"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

func TestEveryInvariantRejectsItsCounterexample(t *testing.T) {
	terminal := syncproto.AssignmentFailedClosed
	cases := map[string]WorldState{
		"one-live-assignment-per-member": {Assignments: []agent.AssignmentSnapshot{
			{AssignmentID: "a", MemberID: "member"}, {AssignmentID: "b", MemberID: "member"},
		}},
		"one-live-assignment-per-vm": {Assignments: []agent.AssignmentSnapshot{
			{AssignmentID: "a", MemberID: "one", VMID: "vm"}, {AssignmentID: "b", MemberID: "two", VMID: "vm"},
		}},
		"workspace-owned-by-assignment":   {Workspaces: []zvol.WorkspaceVolume{{Name: "/dev/zvol/fake/ws/orphan"}}},
		"terminal-assignment-holds-no-vm": {Assignments: []agent.AssignmentSnapshot{{AssignmentID: "a", State: terminal, VMID: "vm"}}},
		"slot-bounds":                     {Slots: map[vm.Class]int{"class": 1}, VMs: []vm.Status{{ID: "one", Class: "class"}, {ID: "two", Class: "class"}}},
		"member-incarnation-unique":       {VMs: []vm.Status{{ID: "one", Incarnation: "same"}, {ID: "two", Incarnation: "same"}}},
	}
	for _, invariant := range Invariants {
		state, found := cases[invariant.Name]
		if !found {
			t.Fatalf("invariant %s has no counterexample", invariant.Name)
		}
		if violation := invariant.Check(state); strings.TrimSpace(violation) == "" {
			t.Fatalf("invariant %s accepted its counterexample", invariant.Name)
		}
	}
}
