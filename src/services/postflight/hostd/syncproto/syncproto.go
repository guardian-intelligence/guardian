// Package syncproto is the sync wire contract between hostd and the control
// plane — the single source of truth both sides compile against. The
// exchange is level-triggered in both directions: the request carries a
// host's full observed state, the response carries the full desired state
// for that host. Either side can restart at any point and the next exchange
// converges. The control plane acknowledges a terminal lease by omitting it
// from the next response, which licenses hostd to forget it and reclaim its
// resources.
package syncproto

// SyncPath is the control-plane endpoint hostd POSTs the exchange to.
const SyncPath = "/api/v1/hostd/sync"

// SyncRequest is what hostd reports.
type SyncRequest struct {
	HostID string `json:"host_id"`
	// BootID changes when hostd restarts, so the control plane can tell a
	// fresh process from a silent one.
	BootID string        `json:"boot_id"`
	Slots  []SlotReport  `json:"slots"`
	Leases []LeaseReport `json:"leases"`
	// Generations is the observed inventory of sealed generations resident
	// on this host — the hints-vs-truth channel for the catalog.
	Generations []GenerationReport `json:"generations"`
	// Workspaces lists lease workspace volumes present on disk, so the
	// control plane can spot orphans hostd's own GC missed.
	Workspaces []string `json:"workspaces"`
}

// SlotReport is per-class capacity: fixed totals from provisioning, and the
// current occupancy split.
type SlotReport struct {
	Class string `json:"class"`
	Total int    `json:"total"`
	Warm  int    `json:"warm"`
	Used  int    `json:"used"`
}

// GenerationReport is one resident generation.
type GenerationReport struct {
	Generation string `json:"generation"`
	Bytes      int64  `json:"bytes"`
}

// State is a lease's position in hostd's local lifecycle, as reported in the
// sync request. hostd only ever advances on observed substrate state (a zvol
// exists, a VM reports a phase), never on the assumption that an earlier
// call worked.
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

// Terminal states are reported until the control plane omits the lease from
// the desired set, which is the ack that lets hostd forget it.
func (s State) Terminal() bool {
	return s == StateSealed || s == StateFailed || s == StateCancelled
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

// SyncResponse is the control plane's full desired state for one host.
//
// Full state cuts both ways: an authenticated response with zero leases
// means "cancel everything on this host", by design — there is no separate
// drain verb. The BootID echo is the guard that confines that power to
// responses actually computed for this request: a stale, misrouted, or
// default-constructed response fails the echo and is dropped whole.
type SyncResponse struct {
	// BootID must echo the request's boot_id; hostd drops the response
	// otherwise.
	BootID string         `json:"boot_id"`
	Leases []DesiredLease `json:"leases"`
	// Reap names generations to destroy. Reaping is exclusively a
	// control-plane decision: node-local generations are the only copy.
	Reap []string `json:"reap"`
	// PoolTargets is the desired warm-VM count per class.
	PoolTargets map[string]int `json:"pool_targets"`
	// PollAfterMillis suggests when to sync next; 0 means the default.
	PollAfterMillis int `json:"poll_after_millis"`
}

// DesiredState is what the control plane wants done with a lease.
type DesiredState string

const (
	// DesiredRun: bring the lease to a running runner and report its exit.
	DesiredRun DesiredState = "run"
	// DesiredSeal: the exited workspace should be sealed as a generation.
	DesiredSeal DesiredState = "seal"
	// DesiredCancel: withdraw the lease; destroy its VM.
	DesiredCancel DesiredState = "cancel"
)

// DesiredLease is one lease as the control plane wants it on a host.
type DesiredLease struct {
	LeaseID string       `json:"lease_id"`
	State   DesiredState `json:"state"`

	// Identity, forwarded into the checkout endpoint's lease table.
	ExecutionID        string `json:"execution_id"`
	AttemptID          string `json:"attempt_id"`
	OrgID              string `json:"org_id"`
	InstallationID     int64  `json:"installation_id"`
	RepositoryID       int64  `json:"repository_id"`
	RepositoryFullName string `json:"repository_full_name"`

	RunnerClass string `json:"runner_class"`
	// JITConfig is the encoded single-use runner registration blob, minted
	// by the control plane.
	JITConfig string `json:"jit_config"`

	Workspace WorkspaceSpec `json:"workspace"`
	// SealGeneration names the generation a seal must produce; set when
	// State is DesiredSeal.
	SealGeneration string `json:"seal_generation,omitempty"`
}

// WorkspaceSpec says how to materialize the workspace volume.
type WorkspaceSpec struct {
	// Generation to clone from; empty means a cache miss, which
	// materializes an empty volume — never an error.
	Generation string `json:"generation,omitempty"`
	// SizeBytes for an empty volume; ignored for clones.
	SizeBytes int64 `json:"size_bytes,omitempty"`
}
