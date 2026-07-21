package agent

import (
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

// stateDeadlines bounds transitional states before hostd fails the lease and
// releases its resources. A ready lease is executing customer code and is
// bounded by GitHub's job lifecycle, not by a host-local stall deadline.
var stateDeadlines = map[syncproto.State]time.Duration{
	syncproto.StatePending:     30 * time.Second,
	syncproto.StateClaiming:    5 * time.Minute,
	syncproto.StateAssigning:   2 * time.Minute,
	syncproto.StateListening:   30 * time.Minute,
	syncproto.StateHookBlocked: 2 * time.Minute,
	syncproto.StateBinding:     2 * time.Minute,
	syncproto.StateAuthorizing: 2 * time.Minute,
	syncproto.StateExited:      30 * time.Minute,
}

// lease is hostd's local record for one desired lease.
type lease struct {
	spec syncproto.DesiredLease
	// execution is the desired job GitHub actually routed to this physical
	// listener. It can differ from spec under same-label crossed assignment.
	execution  *syncproto.DesiredLease
	assignment *vm.Assignment

	state            syncproto.State
	since            time.Time // entry time of the current state, for deadlines
	vmID             string    // assigned VM, from claim onward
	device           string    // workspace block device
	toolDevice       string    // durable tool-cache block device
	processDevice    string    // CRIU image block device
	exit             int       // runner exit code, valid from StateExited
	reason           string    // failure reason, valid in StateFailed
	sealGen          string    // generation sealed, valid in StateSealed
	identity         *syncproto.JobIdentityReport
	hostBeforeUnixNS int64
	volume           zvol.WorkspaceVolume
	toolVolume       zvol.ToolVolume
	processVolume    zvol.ProcessVolume
	checkpoint       *syncproto.CheckpointArtifact
	traceSeq         uint64
	traceSeen        map[string]bool
}

func (l *lease) enter(state syncproto.State, now time.Time) {
	l.state = state
	l.since = now
}

func (l *lease) report() syncproto.LeaseReport {
	report := syncproto.LeaseReport{
		LeaseID:          l.spec.LeaseID,
		ExecutionLeaseID: l.executionLeaseID(),
		State:            l.state,
		ExitCode:         l.exit,
		Reason:           l.reason,
		SealedGeneration: l.sealGen,
		Checkpoint:       l.checkpoint,
	}
	if l.identity != nil {
		captured := *l.identity
		report.Identity = &captured
	}
	return report
}

func (l *lease) executionSpec() syncproto.DesiredLease {
	if l.execution != nil {
		return *l.execution
	}
	return l.spec
}

func (l *lease) executionLeaseID() string {
	return executionLeaseID(l.executionSpec())
}

func executionLeaseID(spec syncproto.DesiredLease) string {
	if spec.ExecutionLeaseID != "" {
		return spec.ExecutionLeaseID
	}
	return spec.LeaseID
}
