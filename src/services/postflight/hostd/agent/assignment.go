package agent

import (
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

var assignmentDeadlines = map[syncproto.AssignmentState]time.Duration{
	syncproto.AssignmentObserved:    30 * time.Second,
	syncproto.AssignmentBinding:     2 * time.Minute,
	syncproto.AssignmentAuthorizing: 2 * time.Minute,
	syncproto.AssignmentExited:      10 * time.Minute,
}

type assignment struct {
	spec  syncproto.DesiredAssignment
	state syncproto.AssignmentState
	since time.Time
	vmID  string

	device        string
	toolDevice    string
	processDevice string
	volume        zvol.WorkspaceVolume
	toolVolume    zvol.ToolVolume
	processVolume zvol.ProcessVolume

	exit             int
	reason           string
	restore          *syncproto.RestoreReport
	checkpoint       *syncproto.CheckpointArtifact
	sealGen          string
	timing           []syncproto.TimingPoint
	hostBeforeUnixNS int64
	updateTiming     vm.TimingPoint
	trace            *traceState
	termination      syncproto.AssignmentState
}

func (a *assignment) enter(state syncproto.AssignmentState, now time.Time) {
	a.state = state
	a.since = now
}

func (a *assignment) report() syncproto.AssignmentReport {
	report := syncproto.AssignmentReport{
		AssignmentID: a.spec.AssignmentID, MemberID: a.spec.MemberID,
		RequestID: a.spec.RequestID, JobID: a.spec.JobID, State: a.state,
		ExitCode: a.exit, Reason: a.reason, SealedGeneration: a.sealGen,
		Checkpoint: a.checkpoint, Restore: a.restore,
		Timing: append([]syncproto.TimingPoint(nil), a.timing...),
	}
	return report
}
