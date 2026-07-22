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

	workspaces  map[AssignmentID]WorkspaceVolume
	generations map[GenerationID]GenerationSnapshot
	// attached marks workspaces held open by a VM; DestroyWorkspace on an
	// attached volume returns ErrBusy, mirroring ZFS behavior.
	attached map[AssignmentID]bool
	// clones counts live workspace clones per source generation; a
	// generation with clones refuses DestroyGeneration with ErrBusy.
	clones  map[GenerationID]int
	process *Fake
	tool    *Fake
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
	f.tool = newFakeVolumeTree("fake/tool-state")
	return f
}

func newFakeVolumeTree(prefix string) *Fake {
	return &Fake{
		workspaces:  map[AssignmentID]WorkspaceVolume{},
		generations: map[GenerationID]GenerationSnapshot{},
		attached:    map[AssignmentID]bool{},
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
	if f.tool != nil {
		f.tool.SeedGeneration(generation, bytes)
	}
}

func (f *Fake) EnsureProcess(ctx context.Context, assignment AssignmentID, generation GenerationID, sizeBytes int64) (ProcessVolume, error) {
	f.mu.Lock()
	err := f.fail("ensure-process", string(assignment))
	f.mu.Unlock()
	if err != nil {
		return ProcessVolume{}, err
	}
	volume, err := f.process.EnsureWorkspace(ctx, assignment, generation, sizeBytes)
	if err == nil {
		f.mu.Lock()
		f.journal("ensure-process %s from=%q size=%d", assignment, generation, sizeBytes)
		f.mu.Unlock()
	}
	return volume, err
}

func (f *Fake) EnsureTool(ctx context.Context, assignment AssignmentID, generation GenerationID, sizeBytes int64) (ToolVolume, error) {
	f.mu.Lock()
	err := f.fail("ensure-tool", string(assignment))
	f.mu.Unlock()
	if err != nil {
		return ToolVolume{}, err
	}
	volume, err := f.tool.EnsureWorkspace(ctx, assignment, generation, sizeBytes)
	if err == nil {
		f.mu.Lock()
		f.journal("ensure-tool %s from=%q size=%d", assignment, generation, sizeBytes)
		f.mu.Unlock()
	}
	return volume, err
}

func (f *Fake) DestroyProcess(ctx context.Context, assignment AssignmentID) error {
	f.mu.Lock()
	err := f.fail("destroy-process", string(assignment))
	f.mu.Unlock()
	if err != nil {
		return err
	}
	err = f.process.DestroyWorkspace(ctx, assignment)
	if err == nil {
		f.mu.Lock()
		f.journal("destroy-process %s", assignment)
		f.mu.Unlock()
	}
	return err
}

func (f *Fake) DestroyTool(ctx context.Context, assignment AssignmentID) error {
	f.mu.Lock()
	err := f.fail("destroy-tool", string(assignment))
	f.mu.Unlock()
	if err != nil {
		return err
	}
	err = f.tool.DestroyWorkspace(ctx, assignment)
	if err == nil {
		f.mu.Lock()
		f.journal("destroy-tool %s", assignment)
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

func (f *Fake) DestroyToolGeneration(ctx context.Context, generation GenerationID) error {
	f.mu.Lock()
	err := f.fail("destroy-tool-generation", string(generation))
	f.mu.Unlock()
	if err != nil {
		return err
	}
	err = f.tool.DestroyGeneration(ctx, generation)
	if err == nil {
		f.mu.Lock()
		f.journal("destroy-tool-generation %s", generation)
		f.mu.Unlock()
	}
	return err
}

// SetAttached marks a workspace as held open by a VM. The agent's vm fake
// calls this on attach/detach so the two fakes stay coherent.
func (f *Fake) SetAttached(assignment AssignmentID, attached bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached[assignment] = attached
}

// SetProcessAttached marks a process volume as held open by a VM.
func (f *Fake) SetProcessAttached(assignment AssignmentID, attached bool) {
	f.process.SetAttached(assignment, attached)
}

func (f *Fake) SetToolAttached(assignment AssignmentID, attached bool) {
	f.tool.SetAttached(assignment, attached)
}

// HasWorkspace reports whether an assignment's workspace volume exists.
func (f *Fake) HasWorkspace(assignment AssignmentID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.workspaces[assignment]
	return ok
}

// HasProcess reports whether an assignment's process volume exists.
func (f *Fake) HasProcess(assignment AssignmentID) bool {
	return f.process.HasWorkspace(assignment)
}

func (f *Fake) HasTool(assignment AssignmentID) bool {
	return f.tool.HasWorkspace(assignment)
}

// ProcessAttached reports whether a process volume is held by a VM.
func (f *Fake) ProcessAttached(assignment AssignmentID) bool {
	f.process.mu.Lock()
	defer f.process.mu.Unlock()
	return f.process.attached[assignment]
}

func (f *Fake) ToolAttached(assignment AssignmentID) bool {
	f.tool.mu.Lock()
	defer f.tool.mu.Unlock()
	return f.tool.attached[assignment]
}

// HasGeneration reports whether a generation is resident.
func (f *Fake) HasGeneration(generation GenerationID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.generations[generation]
	return ok
}

// EnsureWorkspace implements Driver.
func (f *Fake) EnsureWorkspace(_ context.Context, assignment AssignmentID, generation GenerationID, sizeBytes int64) (WorkspaceVolume, error) {
	if err := ValidateName("assignment", string(assignment)); err != nil {
		return WorkspaceVolume{}, err
	}
	if generation != "" {
		if err := ValidateName("generation", string(generation)); err != nil {
			return WorkspaceVolume{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("ensure-workspace", string(assignment)); err != nil {
		return WorkspaceVolume{}, err
	}
	if existing, ok := f.workspaces[assignment]; ok {
		return existing, nil
	}
	if generation != "" {
		if _, ok := f.generations[generation]; !ok {
			return WorkspaceVolume{}, fmt.Errorf("clone source %s: %w", generation, ErrNotFound)
		}
		f.clones[generation]++
	}
	volume := WorkspaceVolume{
		Name:   f.prefix + "/ws/" + string(assignment),
		Device: "/dev/zvol/" + f.prefix + "/ws/" + string(assignment),
		Source: generation,
	}
	if generation != "" {
		volume.SourceSnapshotGUID = "guid-" + string(generation)
	}
	f.workspaces[assignment] = volume
	f.journal("ensure-workspace %s from=%q size=%d", assignment, generation, sizeBytes)
	return volume, nil
}

// SealSet implements Driver.
func (f *Fake) SealSet(_ context.Context, assignment AssignmentID, generation GenerationID) (GenerationSet, error) {
	if err := ValidateName("assignment", string(assignment)); err != nil {
		return GenerationSet{}, err
	}
	if err := ValidateName("generation", string(generation)); err != nil {
		return GenerationSet{}, err
	}
	f.mu.Lock()
	f.tool.mu.Lock()
	f.process.mu.Lock()
	defer f.process.mu.Unlock()
	defer f.tool.mu.Unlock()
	defer f.mu.Unlock()
	if err := f.fail("seal-set", string(generation)); err != nil {
		return GenerationSet{}, err
	}
	workspaceGeneration, workspaceExists := f.generations[generation]
	toolGeneration, toolExists := f.tool.generations[generation]
	processGeneration, processExists := f.process.generations[generation]
	if workspaceExists != toolExists || workspaceExists != processExists {
		return GenerationSet{}, fmt.Errorf("zvol: incomplete generation seal")
	}
	if workspaceExists {
		return GenerationSet{Workspace: workspaceGeneration, Tool: toolGeneration, Process: processGeneration}, nil
	}
	if _, ok := f.workspaces[assignment]; !ok {
		return GenerationSet{}, fmt.Errorf("workspace %s: %w", assignment, ErrNotFound)
	}
	if _, ok := f.tool.workspaces[assignment]; !ok {
		return GenerationSet{}, fmt.Errorf("tool workspace %s: %w", assignment, ErrNotFound)
	}
	if _, ok := f.process.workspaces[assignment]; !ok {
		return GenerationSet{}, fmt.Errorf("process workspace %s: %w", assignment, ErrNotFound)
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
	toolGeneration = GenerationSnapshot{
		Generation: generation,
		Snapshot:   f.tool.prefix + "/gen/" + string(generation) + "@sealed",
		Bytes:      1,
	}
	f.generations[generation] = workspaceGeneration
	f.tool.generations[generation] = toolGeneration
	f.process.generations[generation] = processGeneration
	f.journal("seal-set %s generation=%s", assignment, generation)
	return GenerationSet{Workspace: workspaceGeneration, Tool: toolGeneration, Process: processGeneration}, nil
}

// DestroyWorkspace implements Driver.
func (f *Fake) DestroyWorkspace(_ context.Context, assignment AssignmentID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("destroy-workspace", string(assignment)); err != nil {
		return err
	}
	volume, ok := f.workspaces[assignment]
	if !ok {
		return ErrNotFound
	}
	if f.attached[assignment] {
		return ErrBusy
	}
	if volume.Source != "" {
		f.clones[volume.Source]--
	}
	delete(f.workspaces, assignment)
	delete(f.attached, assignment)
	f.journal("destroy-workspace %s", assignment)
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
