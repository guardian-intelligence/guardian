package agent

import "time"

// State is a lease's position in hostd's local lifecycle. hostd only ever
// advances on observed substrate state (a zvol exists, a VM reports a
// phase), never on the assumption that an earlier call worked.
type State string

const (
	// StatePending: accepted from the control plane, nothing done yet.
	StatePending State = "pending"
	// StateClaiming: workspace materialized; waiting for a warm VM.
	StateClaiming State = "claiming"
	// StateAssigning: assignment delivered; the guest is bringing the
	// runner up.
	StateAssigning State = "assigning"
	// StateReady: the runner is registered and listening for its job.
	StateReady State = "ready"
	// StateExited: the runner finished; ExitCode is meaningful. The VM is
	// destroyed on observation; the workspace volume is retained for a
	// possible seal.
	StateExited State = "exited"
	// StateSealed: the control plane asked for a seal and it completed.
	StateSealed State = "sealed"
	// StateFailed: a step failed terminally or its deadline passed.
	StateFailed State = "failed"
	// StateCancelled: the control plane withdrew the lease before exit.
	StateCancelled State = "cancelled"
)

// terminal states are reported until the control plane omits the lease from
// the desired set, which is the ack that lets hostd forget it.
func (s State) terminal() bool {
	return s == StateSealed || s == StateFailed || s == StateCancelled
}

// stateDeadlines bounds how long a lease may sit in each non-terminal state
// before hostd fails it and releases its resources. StateReady is bounded
// because an ephemeral runner that never receives its job must not hold a
// slot forever; the control plane sets the job-level deadline tighter.
var stateDeadlines = map[State]time.Duration{
	StatePending:   30 * time.Second,
	StateClaiming:  5 * time.Minute,
	StateAssigning: 2 * time.Minute,
	StateReady:     30 * time.Minute,
	StateExited:    30 * time.Minute,
}

// lease is hostd's local record for one desired lease.
type lease struct {
	spec DesiredLease

	state   State
	since   time.Time // entry time of the current state, for deadlines
	vmID    string    // assigned VM, from claim onward
	device  string    // workspace block device
	exit    int       // runner exit code, valid from StateExited
	reason  string    // failure reason, valid in StateFailed
	sealGen string    // generation sealed, valid in StateSealed
}

func (l *lease) enter(state State, now time.Time) {
	l.state = state
	l.since = now
}

// LeaseReport is one lease's status line in the sync request.
type LeaseReport struct {
	LeaseID  string `json:"lease_id"`
	State    State  `json:"state"`
	ExitCode int    `json:"exit_code,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// SealedGeneration confirms which generation a seal produced.
	SealedGeneration string `json:"sealed_generation,omitempty"`
}

func (l *lease) report() LeaseReport {
	return LeaseReport{
		LeaseID:          l.spec.LeaseID,
		State:            l.state,
		ExitCode:         l.exit,
		Reason:           l.reason,
		SealedGeneration: l.sealGen,
	}
}
