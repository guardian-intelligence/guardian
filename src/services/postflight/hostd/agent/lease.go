package agent

import (
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

// stateDeadlines bounds how long a lease may sit in each non-terminal state
// before hostd fails it and releases its resources. StateReady is bounded
// because an ephemeral runner that never receives its job must not hold a
// slot forever; the control plane sets the job-level deadline tighter.
var stateDeadlines = map[syncproto.State]time.Duration{
	syncproto.StatePending:     30 * time.Second,
	syncproto.StateClaiming:    5 * time.Minute,
	syncproto.StateAssigning:   2 * time.Minute,
	syncproto.StateListening:   30 * time.Minute,
	syncproto.StateHookBlocked: 2 * time.Minute,
	syncproto.StateBinding:     2 * time.Minute,
	syncproto.StateReady:       30 * time.Minute,
	syncproto.StateExited:      30 * time.Minute,
}

// lease is hostd's local record for one desired lease.
type lease struct {
	spec syncproto.DesiredLease

	state            syncproto.State
	since            time.Time // entry time of the current state, for deadlines
	vmID             string    // assigned VM, from claim onward
	device           string    // workspace block device
	exit             int       // runner exit code, valid from StateExited
	reason           string    // failure reason, valid in StateFailed
	sealGen          string    // generation sealed, valid in StateSealed
	identity         *syncproto.JobIdentityReport
	hostBeforeUnixNS int64
	volume           zvol.WorkspaceVolume
	traceSeq         uint64
}

func (l *lease) enter(state syncproto.State, now time.Time) {
	l.state = state
	l.since = now
}

func (l *lease) report() syncproto.LeaseReport {
	report := syncproto.LeaseReport{
		LeaseID:          l.spec.LeaseID,
		ExecutionLeaseID: executionLeaseID(l.spec),
		State:            l.state,
		ExitCode:         l.exit,
		Reason:           l.reason,
		SealedGeneration: l.sealGen,
	}
	if l.identity != nil {
		captured := *l.identity
		report.Identity = &captured
	}
	return report
}

func executionLeaseID(spec syncproto.DesiredLease) string {
	if spec.ExecutionLeaseID != "" {
		return spec.ExecutionLeaseID
	}
	return spec.LeaseID
}
