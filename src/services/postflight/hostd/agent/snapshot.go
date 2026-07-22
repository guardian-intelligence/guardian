package agent

import (
	"sort"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
)

type AssignmentSnapshot struct {
	AssignmentID     string
	MemberID         string
	RequestID        string
	JobID            string
	CheckRunID       int64
	State            syncproto.AssignmentState
	Since            time.Time
	VMID             string
	Device           string
	ToolDevice       string
	ProcessDevice    string
	ExitCode         int
	Reason           string
	Restore          *syncproto.RestoreReport
	SealedGeneration string
	Quarantined      bool
}

func (a *Agent) Snapshot() []AssignmentSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	snapshots := make([]AssignmentSnapshot, 0, len(a.assignments))
	for _, record := range a.assignments {
		snapshots = append(snapshots, AssignmentSnapshot{
			AssignmentID: record.spec.AssignmentID, MemberID: record.spec.MemberID,
			RequestID: record.spec.RequestID, JobID: record.spec.JobID, CheckRunID: record.spec.CheckRunID,
			State: record.state, Since: record.since, VMID: record.vmID,
			Device: record.device, ToolDevice: record.toolDevice, ProcessDevice: record.processDevice,
			ExitCode: record.exit, Reason: record.reason, Restore: record.restore,
			SealedGeneration: record.sealGen, Quarantined: a.quarantinedJobs[record.spec.AssignmentID],
		})
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].AssignmentID < snapshots[j].AssignmentID })
	return snapshots
}

func AssignmentDeadline(state syncproto.AssignmentState) (time.Duration, bool) {
	deadline, ok := assignmentDeadlines[state]
	return deadline, ok
}
