// Package zvol drives ZFS volumes for hostd: workspace materialization
// (clone-from-generation or create-empty), generation sealing (snapshot),
// inventory observation, and destruction. The Driver interface has two
// implementations: Exec (real zfs argv) and Fake (in-memory, for the agent
// core's tests and the sim harness). Everything above this interface is
// hermetically testable; everything below it is verified tracer-style on a
// real host.
package zvol

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

// GenerationID names a sealed workspace generation. It is minted by the
// control plane and appears in snapshot names, so it must be zfs-name safe.
type GenerationID string

// AssignmentID names an immutable job assignment; writable volume names
// derive from it.
type AssignmentID string

// WorkspaceVolume identifies a materialized (writable) workspace volume.
type WorkspaceVolume struct {
	// Name is the full dataset name, e.g. guardian/ws/<assignment-id>.
	Name string
	// Device is the block-device path the VM attaches, e.g. /dev/zvol/....
	Device string
	// Source is the generation the volume was cloned from; empty for a
	// cache-miss (empty) workspace.
	Source GenerationID
	// SourceSnapshotGUID is ZFS's immutable GUID for Source's @sealed
	// snapshot. It is empty for a cold workspace.
	SourceSnapshotGUID string
}

// ProcessVolume is the encrypted CRIU image volume paired with a workspace.
// It uses the same lifecycle and provenance shape but a separate ZFS tree so
// a process artifact can be sized independently.
type ProcessVolume = WorkspaceVolume

// ToolVolume holds workflow-installed toolchains that a restored process may
// still have mapped or open. It is generation-coupled to the workspace and
// process image instead of living on the disposable VM root disk.
type ToolVolume = WorkspaceVolume

// GenerationSnapshot is one sealed generation as observed on this host.
type GenerationSnapshot struct {
	Generation GenerationID
	// Snapshot is the full snapshot name, e.g. guardian/gen/<id>@sealed.
	Snapshot string
	// Bytes is the logical referenced size, for inventory reporting.
	Bytes int64
}

// GenerationSet is the indivisible workspace/tool/process generation tuple.
type GenerationSet struct {
	Workspace GenerationSnapshot
	Tool      GenerationSnapshot
	Process   GenerationSnapshot
}

// Capacity is the allocation headroom ZFS will honor after quotas and
// reservations on the hostd dataset and its ancestors.
type Capacity struct {
	AvailableBytes int64
}

// CapacitySource lets the agent cordon storage-starved hosts before it starts
// or offers another listener. It is separate from Driver so substrate fakes
// and alternate durable-volume implementations can opt in independently.
type CapacitySource interface {
	Capacity(ctx context.Context) (Capacity, error)
}

// Errors the agent's convergence logic branches on. Exec wraps zfs stderr
// into one of these classes; Fake returns them directly.
var (
	// ErrNotFound: the named volume, snapshot, or generation does not exist.
	ErrNotFound = errors.New("zvol: not found")
	// ErrBusy: the dataset is held open (VM still attached, clone exists).
	ErrBusy = errors.New("zvol: busy")
)

// Driver is the ZFS surface hostd needs. Every method is idempotent at the
// call site the agent uses it from: Ensure* observes before acting, and
// destroy of an absent dataset returns ErrNotFound, which callers treat as
// success.
type Driver interface {
	// EnsureWorkspace makes the workspace volume for an assignment exist and
	// returns it. If generation is non-empty the volume is a clone of that
	// generation's sealed snapshot (ErrNotFound if the generation is not
	// resident); otherwise a fresh empty volume of sizeBytes. Calling it
	// again with the same arguments returns the existing volume.
	EnsureWorkspace(ctx context.Context, assignment AssignmentID, generation GenerationID, sizeBytes int64) (WorkspaceVolume, error)

	// EnsureProcess materializes the writable CRIU image volume paired with
	// the workspace. A missing process generation is a cold process cache,
	// just as a missing workspace generation is a cold filesystem cache.
	EnsureProcess(ctx context.Context, assignment AssignmentID, generation GenerationID, sizeBytes int64) (ProcessVolume, error)
	EnsureTool(ctx context.Context, assignment AssignmentID, generation GenerationID, sizeBytes int64) (ToolVolume, error)

	// SealSet takes the workspace, tool, and stopped-process source snapshots
	// in one ZFS transaction, then promotes every lineage. A generation can
	// never be published from snapshots taken at different points in time.
	SealSet(ctx context.Context, assignment AssignmentID, generation GenerationID) (GenerationSet, error)

	// DestroyWorkspace removes an assignment's workspace volume. ErrNotFound if
	// already gone; ErrBusy if still attached.
	DestroyWorkspace(ctx context.Context, assignment AssignmentID) error
	DestroyTool(ctx context.Context, assignment AssignmentID) error
	DestroyProcess(ctx context.Context, assignment AssignmentID) error

	// DestroyGeneration removes a sealed generation and its snapshot. Only
	// ever called on a control-plane reap verb. ErrBusy if clones depend on
	// it.
	DestroyGeneration(ctx context.Context, generation GenerationID) error
	DestroyToolGeneration(ctx context.Context, generation GenerationID) error
	DestroyProcessGeneration(ctx context.Context, generation GenerationID) error

	// Inventory lists the generations and workspace volumes present on this
	// host, for the sync report.
	Inventory(ctx context.Context) ([]GenerationSnapshot, []WorkspaceVolume, error)
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]*$`)

// ValidateName rejects identifiers that are unsafe to splice into a ZFS
// dataset name. Both implementations apply it to assignment and generation IDs so
// a hostile control-plane payload cannot traverse the dataset tree.
func ValidateName(kind, value string) error {
	if value == "" || len(value) > 128 || !nameRe.MatchString(value) {
		return fmt.Errorf("zvol: invalid %s %q", kind, value)
	}
	return nil
}
