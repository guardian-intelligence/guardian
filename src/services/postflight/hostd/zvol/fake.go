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
	clones  map[GenerationID]int
	process *Fake
	prefix  string

	// Fail, when non-nil, is consulted before every operation with an
	// operation label like "ensure-workspace" and the primary identifier.
	// Returning a non-nil error makes the call fail with it.
	Fail func(op, id string) error

	// Journal records every mutating call in order, for scenario assertions.
	Journal []string
}

// NewFake returns an empty fake host substrate.
func NewFake() *Fake {
	f := newFakeVolumeTree("fake")
	f.process = newFakeVolumeTree("fake/process-state")
	return f
}

func newFakeVolumeTree(prefix string) *Fake {
	return &Fake{
		workspaces:  map[LeaseID]WorkspaceVolume{},
		generations: map[GenerationID]GenerationSnapshot{},
		attached:    map[LeaseID]bool{},
		clones:      map[GenerationID]int{},
		prefix:      prefix,
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
		Snapshot:   f.prefix + "/gen/" + string(generation) + "@sealed",
		Bytes:      bytes,
	}
	if f.process != nil {
		f.process.SeedGeneration(generation, bytes)
	}
}

func (f *Fake) EnsureProcess(ctx context.Context, lease LeaseID, generation GenerationID, sizeBytes int64) (ProcessVolume, error) {
	f.mu.Lock()
	err := f.fail("ensure-process", string(lease))
	f.mu.Unlock()
	if err != nil {
		return ProcessVolume{}, err
	}
	volume, err := f.process.EnsureWorkspace(ctx, lease, generation, sizeBytes)
	if err == nil {
		f.mu.Lock()
		f.journal("ensure-process %s from=%q size=%d", lease, generation, sizeBytes)
		f.mu.Unlock()
	}
	return volume, err
}

func (f *Fake) DestroyProcess(ctx context.Context, lease LeaseID) error {
	f.mu.Lock()
	err := f.fail("destroy-process", string(lease))
	f.mu.Unlock()
	if err != nil {
		return err
	}
	err = f.process.DestroyWorkspace(ctx, lease)
	if err == nil {
		f.mu.Lock()
		f.journal("destroy-process %s", lease)
		f.mu.Unlock()
	}
	return err
}

func (f *Fake) DestroyProcessGeneration(ctx context.Context, generation GenerationID) error {
	f.mu.Lock()
	err := f.fail("destroy-process-generation", string(generation))
	f.mu.Unlock()
	if err != nil {
		return err
	}
	err = f.process.DestroyGeneration(ctx, generation)
	if err == nil {
		f.mu.Lock()
		f.journal("destroy-process-generation %s", generation)
		f.mu.Unlock()
	}
	return err
}

// SetAttached marks a workspace as held open by a VM. The agent's vm fake
// calls this on attach/detach so the two fakes stay coherent.
func (f *Fake) SetAttached(lease LeaseID, attached bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached[lease] = attached
}

// SetProcessAttached marks a process volume as held open by a VM.
func (f *Fake) SetProcessAttached(lease LeaseID, attached bool) {
	f.process.SetAttached(lease, attached)
}

// HasWorkspace reports whether a lease's workspace volume exists.
func (f *Fake) HasWorkspace(lease LeaseID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.workspaces[lease]
	return ok
}

// HasProcess reports whether a lease's process volume exists.
func (f *Fake) HasProcess(lease LeaseID) bool {
	return f.process.HasWorkspace(lease)
}

// ProcessAttached reports whether a process volume is held by a VM.
func (f *Fake) ProcessAttached(lease LeaseID) bool {
	f.process.mu.Lock()
	defer f.process.mu.Unlock()
	return f.process.attached[lease]
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
		Name:   f.prefix + "/ws/" + string(lease),
		Device: "/dev/zvol/" + f.prefix + "/ws/" + string(lease),
		Source: generation,
	}
	if generation != "" {
		volume.SourceSnapshotGUID = "guid-" + string(generation)
	}
	f.workspaces[lease] = volume
	f.journal("ensure-workspace %s from=%q size=%d", lease, generation, sizeBytes)
	return volume, nil
}

// SealPair implements Driver.
func (f *Fake) SealPair(_ context.Context, lease LeaseID, generation GenerationID) (GenerationPair, error) {
	if err := ValidateName("lease", string(lease)); err != nil {
		return GenerationPair{}, err
	}
	if err := ValidateName("generation", string(generation)); err != nil {
		return GenerationPair{}, err
	}
	f.mu.Lock()
	f.process.mu.Lock()
	defer f.process.mu.Unlock()
	defer f.mu.Unlock()
	if err := f.fail("seal-pair", string(generation)); err != nil {
		return GenerationPair{}, err
	}
	workspaceGeneration, workspaceExists := f.generations[generation]
	processGeneration, processExists := f.process.generations[generation]
	if workspaceExists != processExists {
		return GenerationPair{}, fmt.Errorf("zvol: incomplete paired seal")
	}
	if workspaceExists {
		return GenerationPair{Workspace: workspaceGeneration, Process: processGeneration}, nil
	}
	if _, ok := f.workspaces[lease]; !ok {
		return GenerationPair{}, fmt.Errorf("workspace %s: %w", lease, ErrNotFound)
	}
	if _, ok := f.process.workspaces[lease]; !ok {
		return GenerationPair{}, fmt.Errorf("process workspace %s: %w", lease, ErrNotFound)
	}
	workspaceGeneration = GenerationSnapshot{
		Generation: generation,
		Snapshot:   f.prefix + "/gen/" + string(generation) + "@sealed",
		Bytes:      1,
	}
	processGeneration = GenerationSnapshot{
		Generation: generation,
		Snapshot:   f.process.prefix + "/gen/" + string(generation) + "@sealed",
		Bytes:      1,
	}
	f.generations[generation] = workspaceGeneration
	f.process.generations[generation] = processGeneration
	f.journal("seal-pair %s generation=%s", lease, generation)
	return GenerationPair{Workspace: workspaceGeneration, Process: processGeneration}, nil
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
