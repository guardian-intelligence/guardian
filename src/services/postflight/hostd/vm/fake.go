package vm

import (
	"context"
	"fmt"
	"sync"
)

// Fake is the in-memory Driver for agent tests and the sim harness. VM
// phases only advance when a scenario says so (AdvanceBoot, MarkReady,
// MarkExited), which is what makes agent behavior under slow boots, stuck
// guests, and crash-during-assign deterministic to explore.
type Fake struct {
	mu  sync.Mutex
	vms map[ID]*fakeVM

	// Fail, when non-nil, is consulted before every operation.
	Fail func(op string, id ID) error
	// FailAfter, when non-nil, is consulted after an operation has taken
	// effect: the mutation lands and the call still returns an error. This
	// is the ambiguous-failure shape real drivers have (a QMP timeout after
	// the guest acted) that a fail-before-mutating fake cannot model.
	FailAfter func(op string, id ID) error
	// OnAttach and OnDetach let the harness mirror attachment state into the
	// zvol fake so busy-volume semantics stay coherent across the two.
	OnAttach func(device string)
	OnDetach func(device string)

	// Journal records every mutating call in order.
	Journal []string
}

type fakeVM struct {
	status     Status
	assignment *Assignment
}

// NewFake returns an empty fake hypervisor.
func NewFake() *Fake {
	return &Fake{vms: map[ID]*fakeVM{}}
}

func (f *Fake) fail(op string, id ID) error {
	if f.Fail != nil {
		return f.Fail(op, id)
	}
	return nil
}

func (f *Fake) journal(format string, args ...any) {
	f.Journal = append(f.Journal, fmt.Sprintf(format, args...))
}

// Launch implements Driver.
func (f *Fake) Launch(_ context.Context, id ID, class Class) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("launch", id); err != nil {
		return err
	}
	if existing, ok := f.vms[id]; ok {
		if existing.status.Class != class {
			return fmt.Errorf("vm: %s already exists with class %s", id, existing.status.Class)
		}
		return nil
	}
	f.vms[id] = &fakeVM{status: Status{ID: id, Class: class, Phase: PhaseBooting}}
	f.journal("launch %s class=%s", id, class)
	return nil
}

// Assign implements Driver.
func (f *Fake) Assign(_ context.Context, id ID, assignment Assignment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("assign", id); err != nil {
		return err
	}
	instance, ok := f.vms[id]
	if !ok {
		return ErrNotFound
	}
	if instance.assignment != nil {
		if instance.assignment.Lease != assignment.Lease {
			return fmt.Errorf("vm: %s already assigned to lease %s", id, instance.assignment.Lease)
		}
		return nil // same lease; idempotent
	}
	instance.assignment = &assignment
	instance.status.Phase = PhaseAssigned
	instance.status.Lease = assignment.Lease
	if f.OnAttach != nil {
		f.OnAttach(assignment.WorkspaceDevice)
	}
	f.journal("assign %s device=%s", id, assignment.WorkspaceDevice)
	if f.FailAfter != nil {
		return f.FailAfter("assign", id)
	}
	return nil
}

// Status implements Driver.
func (f *Fake) Status(_ context.Context, id ID) (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("status", id); err != nil {
		return Status{}, err
	}
	instance, ok := f.vms[id]
	if !ok {
		return Status{ID: id, Phase: PhaseGone}, nil
	}
	return instance.status, nil
}

// List implements Driver.
func (f *Fake) List(context.Context) ([]Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("list", ""); err != nil {
		return nil, err
	}
	statuses := make([]Status, 0, len(f.vms))
	for _, instance := range f.vms {
		statuses = append(statuses, instance.status)
	}
	return statuses, nil
}

// Quiesce implements Driver.
func (f *Fake) Quiesce(_ context.Context, id ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("quiesce", id); err != nil {
		return err
	}
	if _, ok := f.vms[id]; !ok {
		return ErrNotFound
	}
	f.journal("quiesce %s", id)
	return nil
}

// Destroy implements Driver.
func (f *Fake) Destroy(_ context.Context, id ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("destroy", id); err != nil {
		return err
	}
	instance, ok := f.vms[id]
	if !ok {
		return nil
	}
	if instance.assignment != nil && f.OnDetach != nil {
		f.OnDetach(instance.assignment.WorkspaceDevice)
	}
	delete(f.vms, id)
	f.journal("destroy %s", id)
	return nil
}

// AdvanceBoot moves a booting VM to warm.
func (f *Fake) AdvanceBoot(id ID) bool { return f.advance(id, PhaseBooting, PhaseWarm) }

// MarkReady moves an assigned VM to ready (runner registered).
func (f *Fake) MarkReady(id ID) bool { return f.advance(id, PhaseAssigned, PhaseReady) }

// MarkExited moves a ready or assigned VM to exited with a code.
func (f *Fake) MarkExited(id ID, code int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance, ok := f.vms[id]
	if !ok || (instance.status.Phase != PhaseReady && instance.status.Phase != PhaseAssigned) {
		return false
	}
	instance.status.Phase = PhaseExited
	instance.status.ExitCode = code
	if instance.assignment != nil && f.OnDetach != nil {
		// The guest is dead; the workspace device is no longer held open.
		f.OnDetach(instance.assignment.WorkspaceDevice)
		instance.assignment = nil
	}
	f.journal("exited %s code=%d", id, code)
	return true
}

func (f *Fake) advance(id ID, from, to Phase) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance, ok := f.vms[id]
	if !ok || instance.status.Phase != from {
		return false
	}
	instance.status.Phase = to
	f.journal("phase %s %s->%s", id, from, to)
	return true
}

// Assignment returns the assignment a VM holds, for scenario assertions.
func (f *Fake) Assignment(id ID) (Assignment, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance, ok := f.vms[id]
	if !ok || instance.assignment == nil {
		return Assignment{}, false
	}
	return *instance.assignment, true
}
