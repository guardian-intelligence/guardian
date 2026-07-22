// Package syncproto is the level-triggered wire contract between hostd and
// the control plane. Pool capacity, GitHub job intent, local runner
// assignment, and durable generation selection remain separate identities.
package syncproto

const SyncPath = "/api/v1/hostd/sync"

type SyncRequest struct {
	HostID      string             `json:"host_id"`
	BootID      string             `json:"boot_id"`
	Platform    PlatformReport     `json:"platform"`
	Slots       []SlotReport       `json:"slots"`
	Members     []PoolMemberReport `json:"members"`
	Assignments []AssignmentReport `json:"assignments"`
	Generations []GenerationReport `json:"generations"`
	Workspaces  []string           `json:"workspaces"`
}

type PlatformReport struct {
	QEMUVersion   string `json:"qemu_version"`
	KernelRelease string `json:"kernel_release"`
	OSImageID     string `json:"os_image_id"`
	MachineType   string `json:"machine_type"`
	CPUModel      string `json:"cpu_model"`
	CRIUVersion   string `json:"criu_version"`
}

type SlotReport struct {
	Class     string `json:"class"`
	Total     int    `json:"total"`
	Booting   int    `json:"booting"`
	Listening int    `json:"listening"`
	Busy      int    `json:"busy"`
}

type GenerationReport struct {
	Generation string `json:"generation"`
	Bytes      int64  `json:"bytes"`
}

type PoolMemberState string

const (
	MemberProvisioning PoolMemberState = "provisioning"
	MemberWarm         PoolMemberState = "warm"
	MemberPreparing    PoolMemberState = "preparing"
	MemberListening    PoolMemberState = "listening"
	MemberAssigned     PoolMemberState = "assigned"
	MemberRendezvous   PoolMemberState = "rendezvous"
	MemberRunning      PoolMemberState = "running"
	MemberRecycling    PoolMemberState = "recycling"
	MemberLost         PoolMemberState = "lost"
)

func (s PoolMemberState) Terminal() bool {
	return s == MemberRecycling || s == MemberLost
}

// PoolMemberReport is one physical VM incarnation and its generic, single-use
// GitHub listener. MemberID changes whenever the VM is destroyed and refilled.
type PoolMemberReport struct {
	MemberID   string              `json:"member_id"`
	VMID       string              `json:"vm_id"`
	RunnerName string              `json:"runner_name,omitempty"`
	Class      string              `json:"class"`
	Image      string              `json:"image"`
	State      PoolMemberState     `json:"state"`
	Assignment *ObservedAssignment `json:"assignment,omitempty"`
	Reason     string              `json:"reason,omitempty"`
}

// ObservedAssignment comes from Runner.Listener inside the selected guest,
// before Runner.Worker dispatch. RequestID and JobID are GitHub runner-protocol
// identities, not the numeric REST workflow-job ID.
type ObservedAssignment struct {
	RequestID      string        `json:"request_id"`
	JobID          string        `json:"job_id"`
	CheckRunID     int64         `json:"check_run_id"`
	RunnerName     string        `json:"runner_name"`
	JobDisplayName string        `json:"job_display_name"`
	Identity       JobIdentity   `json:"identity"`
	Timing         []TimingPoint `json:"timing,omitempty"`
}

type JobIdentity struct {
	RunID       string `json:"run_id"`
	RunAttempt  int    `json:"run_attempt"`
	RunnerName  string `json:"runner_name"`
	Repository  string `json:"repository"`
	WorkflowJob string `json:"workflow_job"`
}

type TimingPoint struct {
	Event       string `json:"event"`
	Source      string `json:"source"`
	BootID      string `json:"boot_id"`
	Sequence    uint64 `json:"sequence"`
	MonotonicNS int64  `json:"monotonic_ns"`
	UnixNS      int64  `json:"unix_ns"`
}

type AssignmentState string

const (
	AssignmentObserved     AssignmentState = "observed"
	AssignmentBinding      AssignmentState = "binding"
	AssignmentAuthorizing  AssignmentState = "authorizing"
	AssignmentRunning      AssignmentState = "running"
	AssignmentExited       AssignmentState = "exited"
	AssignmentSealed       AssignmentState = "sealed"
	AssignmentCompleted    AssignmentState = "completed"
	AssignmentRequeued     AssignmentState = "requeued"
	AssignmentFailedClosed AssignmentState = "failed-closed"
)

func (s AssignmentState) Terminal() bool {
	return s == AssignmentSealed || s == AssignmentCompleted || s == AssignmentRequeued || s == AssignmentFailedClosed
}

type AssignmentReport struct {
	AssignmentID     string              `json:"assignment_id"`
	MemberID         string              `json:"member_id"`
	RequestID        string              `json:"request_id"`
	JobID            string              `json:"job_id"`
	State            AssignmentState     `json:"state"`
	ExitCode         int                 `json:"exit_code,omitempty"`
	Reason           string              `json:"reason,omitempty"`
	Restore          *RestoreReport      `json:"restore,omitempty"`
	Checkpoint       *CheckpointArtifact `json:"checkpoint,omitempty"`
	SealedGeneration string              `json:"sealed_generation,omitempty"`
	Timing           []TimingPoint       `json:"timing,omitempty"`
}

type RestoreReport struct {
	Outcome            string `json:"outcome"`
	ProcessInvalidated bool   `json:"process_invalidated,omitempty"`
	FailureClass       string `json:"failure_class,omitempty"`
	FailureCode        string `json:"failure_code,omitempty"`
}

type CheckpointArtifact struct {
	Digest  string `json:"digest"`
	Version string `json:"version"`
}

type SyncResponse struct {
	BootID          string              `json:"boot_id"`
	Members         []DesiredPoolMember `json:"members"`
	Assignments     []DesiredAssignment `json:"assignments"`
	Reap            []string            `json:"reap"`
	PoolTargets     map[string]int      `json:"pool_targets"`
	PollAfterMillis int                 `json:"poll_after_millis"`
}

type DesiredMemberState string

const (
	DesiredMemberListen  DesiredMemberState = "listen"
	DesiredMemberRecycle DesiredMemberState = "recycle"
)

type DesiredPoolMember struct {
	MemberID    string             `json:"member_id"`
	VMID        string             `json:"vm_id"`
	State       DesiredMemberState `json:"state"`
	RunnerName  string             `json:"runner_name"`
	RunnerClass string             `json:"runner_class"`
	JITConfig   string             `json:"jit_config,omitempty"`
}

type DesiredAssignmentState string

const (
	DesiredAssignmentRun   DesiredAssignmentState = "run"
	DesiredAssignmentSeal  DesiredAssignmentState = "seal"
	DesiredAssignmentAbort DesiredAssignmentState = "abort"
)

// DesiredAssignment is immutable except for State and seal fields. hostd
// accepts it only when every local protocol identity matches the selected
// member's observed assignment.
type DesiredAssignment struct {
	AssignmentID string                 `json:"assignment_id"`
	MemberID     string                 `json:"member_id"`
	RequestID    string                 `json:"request_id"`
	JobID        string                 `json:"job_id"`
	CheckRunID   int64                  `json:"check_run_id"`
	State        DesiredAssignmentState `json:"state"`

	ExecutionID        string      `json:"execution_id"`
	AttemptID          string      `json:"attempt_id"`
	OrgID              string      `json:"org_id"`
	InstallationID     int64       `json:"installation_id"`
	RepositoryID       int64       `json:"repository_id"`
	RepositoryFullName string      `json:"repository_full_name"`
	RunnerClass        string      `json:"runner_class"`
	Identity           JobIdentity `json:"identity"`

	Workspace WorkspaceSpec `json:"workspace"`
	Tool      WorkspaceSpec `json:"tool"`
	Process   ProcessSpec   `json:"process"`

	SealGeneration string              `json:"seal_generation,omitempty"`
	SealCheckpoint *CheckpointArtifact `json:"seal_checkpoint,omitempty"`
}

type WorkspaceSpec struct {
	Generation string `json:"generation,omitempty"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
}

type ProcessSpec struct {
	Generation      string `json:"generation,omitempty"`
	SizeBytes       int64  `json:"size_bytes,omitempty"`
	ExpectedDigest  string `json:"expected_digest,omitempty"`
	ExpectedVersion string `json:"expected_version,omitempty"`
}
