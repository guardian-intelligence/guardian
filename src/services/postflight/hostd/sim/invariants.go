package sim

import (
	"fmt"
	"strings"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/agent"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

type WorldState struct {
	Assignments []agent.AssignmentSnapshot
	VMs         []vm.Status
	Workspaces  []zvol.WorkspaceVolume
	Slots       map[vm.Class]int
	Desired     syncproto.SyncResponse
	Synced      bool
	PostTick    bool
}

type Invariant struct {
	Name  string
	Check func(WorldState) string
}

var Invariants = []Invariant{
	{Name: "one-live-assignment-per-member", Check: checkAssignmentPerMember},
	{Name: "one-live-assignment-per-vm", Check: checkAssignmentPerVM},
	{Name: "workspace-owned-by-assignment", Check: checkWorkspaceOwner},
	{Name: "terminal-assignment-holds-no-vm", Check: checkTerminalVM},
	{Name: "slot-bounds", Check: checkSlotBounds},
	{Name: "member-incarnation-unique", Check: checkMemberIncarnation},
}

func checkAssignmentPerMember(state WorldState) string {
	owners := map[string]string{}
	for _, assignment := range state.Assignments {
		if assignment.State.Terminal() || assignment.MemberID == "" {
			continue
		}
		if other, found := owners[assignment.MemberID]; found {
			return fmt.Sprintf("member %s owns both %s and %s", assignment.MemberID, other, assignment.AssignmentID)
		}
		owners[assignment.MemberID] = assignment.AssignmentID
	}
	return ""
}

func checkAssignmentPerVM(state WorldState) string {
	owners := map[string]string{}
	for _, assignment := range state.Assignments {
		if assignment.State.Terminal() || assignment.VMID == "" {
			continue
		}
		if other, found := owners[assignment.VMID]; found {
			return fmt.Sprintf("vm %s owns both %s and %s", assignment.VMID, other, assignment.AssignmentID)
		}
		owners[assignment.VMID] = assignment.AssignmentID
	}
	return ""
}

func checkWorkspaceOwner(state WorldState) string {
	tracked := map[string]bool{}
	for _, assignment := range state.Assignments {
		tracked[assignment.AssignmentID] = true
	}
	for _, workspace := range state.Workspaces {
		index := strings.LastIndex(workspace.Name, "/ws/")
		if index >= 0 && !tracked[workspace.Name[index+len("/ws/"):]] {
			return fmt.Sprintf("workspace %s has no assignment", workspace.Name)
		}
	}
	return ""
}

func checkTerminalVM(state WorldState) string {
	for _, assignment := range state.Assignments {
		if assignment.State.Terminal() && assignment.VMID != "" {
			return fmt.Sprintf("terminal assignment %s holds vm %s", assignment.AssignmentID, assignment.VMID)
		}
	}
	return ""
}

func checkSlotBounds(state WorldState) string {
	counts := map[vm.Class]int{}
	for _, status := range state.VMs {
		counts[status.Class]++
	}
	for class, count := range counts {
		if total, configured := state.Slots[class]; configured && count > total {
			return fmt.Sprintf("class %s has %d vms for %d slots", class, count, total)
		}
	}
	return ""
}

func checkMemberIncarnation(state WorldState) string {
	seen := map[string]vm.ID{}
	for _, status := range state.VMs {
		if status.Incarnation == "" {
			continue
		}
		if other, found := seen[status.Incarnation]; found {
			return fmt.Sprintf("incarnation %s belongs to %s and %s", status.Incarnation, other, status.ID)
		}
		seen[status.Incarnation] = status.ID
	}
	return ""
}
