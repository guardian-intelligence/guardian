package agent

import (
	"sort"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
)

// LeaseSnapshot is one lease's full local state, for observability and the
// sim harness's invariant checks.
type LeaseSnapshot struct {
	LeaseID          string
	State            syncproto.State
	Since            time.Time
	VMID             string
	Device           string
	ExecutionID      string
	AttemptID        string
	ExitCode         int
	Reason           string
	SealedGeneration string
	// Quarantined marks a lease whose latest spec was rejected: its
	// lifecycle (and therefore its deadlines) is frozen until a parseable
	// spec arrives.
	Quarantined bool
}

// Snapshot returns every lease the agent currently tracks, ordered by ID.
func (a *Agent) Snapshot() []LeaseSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	snapshots := make([]LeaseSnapshot, 0, len(a.leases))
	for _, record := range a.leases {
		snapshots = append(snapshots, LeaseSnapshot{
			LeaseID:          record.spec.LeaseID,
			State:            record.state,
			Since:            record.since,
			VMID:             record.vmID,
			Device:           record.device,
			ExecutionID:      record.spec.ExecutionID,
			AttemptID:        record.spec.AttemptID,
			ExitCode:         record.exit,
			Reason:           record.reason,
			SealedGeneration: record.sealGen,
			Quarantined:      a.quarantined[record.spec.LeaseID],
		})
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].LeaseID < snapshots[j].LeaseID })
	return snapshots
}

// StateDeadline reports the bound on a state, if it has one.
func StateDeadline(state syncproto.State) (time.Duration, bool) {
	deadline, ok := stateDeadlines[state]
	return deadline, ok
}
