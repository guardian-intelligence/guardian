package zvol

import (
	"context"
	"fmt"
	"sync"
)

// Fake is the in-memory Driver used by the agent tests and the sim harness.
// It models the state the agent's invariants quantify over (which volumes
// and generations exist, what depends on what) and lets a scenario inject a
// fault at any call site.
type Fake struct {
	mu sync.Mutex

	workspaces  map[LeaseID]WorkspaceVolume
	generations map[GenerationID]GenerationSnapshot
	// attached marks workspaces held open by a VM; DestroyWorkspace on an
	// attached volume returns ErrBusy, mirroring ZFS behavior.
	attached map[LeaseID]bool
	// clones counts live workspace clones per source generation; a
	// generation with clones refuses DestroyGeneration with ErrBusy.
	clones map[GenerationID]int

	// Fail, when non-nil, is consulted before every operation with an
	// operation label like "ensure-workspace" and the primary identifier.
	// Returning a non-nil error makes the call fail with it.
	Fail func(op, id string) error

	// Journal records every mutating call in order, for scenario assertions.
	Journal []string
}

// NewFake returns an empty fake host substrate.
func NewFake() *Fake {
	return &Fake{
		workspaces:  map[LeaseID]WorkspaceVolume{},
		generations: map[GenerationID]GenerationSnapshot{},
		attached:    map[LeaseID]bool{},
		clones:      map[GenerationID]int{},
	}
}

func (f *Fake) fail(op, id string) error {
	if f.Fail != nil {
		return f.Fail(op, id)
	}
	return nil
}

func (f *Fake) journal(format string, args ...any) {
	f.Journal = append(f.Journal, fmt.Sprintf(format, args...))
}

// SeedGeneration makes a sealed generation resident, as if a prior seal or
// an affinity transfer put it there.
func (f *Fake) SeedGeneration(generation GenerationID, bytes int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generations[generation] = GenerationSnapshot{
		Generation: generation,
		Snapshot:   "fake/gen/" + string(generation) + "@sealed",
		Bytes:      bytes,
	}
}

// SetAttached marks a workspace as held open by a VM. The agent's vm fake
// calls this on attach/detach so the two fakes stay coherent.
func (f *Fake) SetAttached(lease LeaseID, attached bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached[lease] = attached
}

// HasWorkspace reports whether a lease's workspace volume exists.
func (f *Fake) HasWorkspace(lease LeaseID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.workspaces[lease]
	return ok
}

// HasGeneration reports whether a generation is resident.
func (f *Fake) HasGeneration(generation GenerationID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.generations[generation]
	return ok
}

// EnsureWorkspace implements Driver.
func (f *Fake) EnsureWorkspace(_ context.Context, lease LeaseID, generation GenerationID, sizeBytes int64) (WorkspaceVolume, error) {
	if err := ValidateName("lease", string(lease)); err != nil {
		return WorkspaceVolume{}, err
	}
	if generation != "" {
		if err := ValidateName("generation", string(generation)); err != nil {
			return WorkspaceVolume{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("ensure-workspace", string(lease)); err != nil {
		return WorkspaceVolume{}, err
	}
	if existing, ok := f.workspaces[lease]; ok {
		return existing, nil
	}
	if generation != "" {
		if _, ok := f.generations[generation]; !ok {
			return WorkspaceVolume{}, fmt.Errorf("clone source %s: %w", generation, ErrNotFound)
		}
		f.clones[generation]++
	}
	volume := WorkspaceVolume{
		Name:   "fake/ws/" + string(lease),
		Device: "/dev/zvol/fake/ws/" + string(lease),
		Source: generation,
	}
	f.workspaces[lease] = volume
	f.journal("ensure-workspace %s from=%q size=%d", lease, generation, sizeBytes)
	return volume, nil
}

// SealWorkspace implements Driver.
func (f *Fake) SealWorkspace(_ context.Context, lease LeaseID, generation GenerationID) (GenerationSnapshot, error) {
	if err := ValidateName("generation", string(generation)); err != nil {
		return GenerationSnapshot{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("seal-workspace", string(generation)); err != nil {
		return GenerationSnapshot{}, err
	}
	if existing, ok := f.generations[generation]; ok {
		return existing, nil
	}
	if _, ok := f.workspaces[lease]; !ok {
		return GenerationSnapshot{}, fmt.Errorf("workspace %s: %w", lease, ErrNotFound)
	}
	snapshot := GenerationSnapshot{
		Generation: generation,
		Snapshot:   "fake/gen/" + string(generation) + "@sealed",
		Bytes:      1,
	}
	f.generations[generation] = snapshot
	f.journal("seal-workspace %s generation=%s", lease, generation)
	return snapshot, nil
}

// DestroyWorkspace implements Driver.
func (f *Fake) DestroyWorkspace(_ context.Context, lease LeaseID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("destroy-workspace", string(lease)); err != nil {
		return err
	}
	volume, ok := f.workspaces[lease]
	if !ok {
		return ErrNotFound
	}
	if f.attached[lease] {
		return ErrBusy
	}
	if volume.Source != "" {
		f.clones[volume.Source]--
	}
	delete(f.workspaces, lease)
	delete(f.attached, lease)
	f.journal("destroy-workspace %s", lease)
	return nil
}

// DestroyGeneration implements Driver.
func (f *Fake) DestroyGeneration(_ context.Context, generation GenerationID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("destroy-generation", string(generation)); err != nil {
		return err
	}
	if _, ok := f.generations[generation]; !ok {
		return ErrNotFound
	}
	if f.clones[generation] > 0 {
		return ErrBusy
	}
	delete(f.generations, generation)
	f.journal("destroy-generation %s", generation)
	return nil
}

// Inventory implements Driver.
func (f *Fake) Inventory(context.Context) ([]GenerationSnapshot, []WorkspaceVolume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("inventory", ""); err != nil {
		return nil, nil, err
	}
	generations := make([]GenerationSnapshot, 0, len(f.generations))
	for _, g := range f.generations {
		generations = append(generations, g)
	}
	workspaces := make([]WorkspaceVolume, 0, len(f.workspaces))
	for _, w := range f.workspaces {
		workspaces = append(workspaces, w)
	}
	return generations, workspaces, nil
}
