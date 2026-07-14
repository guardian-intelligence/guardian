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

// LeaseID names a lease; workspace volume names derive from it.
type LeaseID string

// WorkspaceVolume identifies a materialized (writable) workspace volume.
type WorkspaceVolume struct {
	// Name is the full dataset name, e.g. guardian/ws/<lease-id>.
	Name string
	// Device is the block-device path the VM attaches, e.g. /dev/zvol/....
	Device string
	// Source is the generation the volume was cloned from; empty for a
	// cache-miss (empty) workspace.
	Source GenerationID
}

// GenerationSnapshot is one sealed generation as observed on this host.
type GenerationSnapshot struct {
	Generation GenerationID
	// Snapshot is the full snapshot name, e.g. guardian/gen/<id>@sealed.
	Snapshot string
	// Bytes is the logical referenced size, for inventory reporting.
	Bytes int64
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
	// EnsureWorkspace makes the workspace volume for a lease exist and
	// returns it. If generation is non-empty the volume is a clone of that
	// generation's sealed snapshot (ErrNotFound if the generation is not
	// resident); otherwise a fresh empty volume of sizeBytes. Calling it
	// again with the same arguments returns the existing volume.
	EnsureWorkspace(ctx context.Context, lease LeaseID, generation GenerationID, sizeBytes int64) (WorkspaceVolume, error)

	// SealWorkspace snapshots a lease's workspace as a generation candidate
	// and promotes it to a first-class generation dataset, so the workspace
	// volume itself can be destroyed independently later. Idempotent: if the
	// generation snapshot already exists it returns it.
	SealWorkspace(ctx context.Context, lease LeaseID, generation GenerationID) (GenerationSnapshot, error)

	// DestroyWorkspace removes a lease's workspace volume. ErrNotFound if
	// already gone; ErrBusy if still attached.
	DestroyWorkspace(ctx context.Context, lease LeaseID) error

	// DestroyGeneration removes a sealed generation and its snapshot. Only
	// ever called on a control-plane reap verb. ErrBusy if clones depend on
	// it.
	DestroyGeneration(ctx context.Context, generation GenerationID) error

	// Inventory lists the generations and workspace volumes present on this
	// host, for the sync report.
	Inventory(ctx context.Context) ([]GenerationSnapshot, []WorkspaceVolume, error)
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]*$`)

// ValidateName rejects identifiers that are unsafe to splice into a ZFS
// dataset name. Both implementations apply it to lease and generation IDs so
// a hostile control-plane payload cannot traverse the dataset tree.
func ValidateName(kind, value string) error {
	if value == "" || len(value) > 128 || !nameRe.MatchString(value) {
		return fmt.Errorf("zvol: invalid %s %q", kind, value)
	}
	return nil
}
