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

	// Images supplies the immutable image identity recorded for new VMs.
	Images map[Class]string

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
	status      Status
	preparation *Preparation
	rendezvous  *Rendezvous
}

// NewFake returns an empty fake hypervisor.
func NewFake() *Fake {
	return &Fake{vms: map[ID]*fakeVM{}, Images: map[Class]string{}}
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
	f.vms[id] = &fakeVM{status: Status{
		ID: id, Class: class, Image: f.Images[class], Phase: PhaseBooting,
	}}
	f.journal("launch %s class=%s", id, class)
	return nil
}

// Prepare implements Driver.
func (f *Fake) Prepare(_ context.Context, id ID, preparation Preparation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("prepare", id); err != nil {
		return err
	}
	instance, ok := f.vms[id]
	if !ok {
		return ErrNotFound
	}
	if instance.preparation != nil {
		if instance.preparation.Lease != preparation.Lease {
			return fmt.Errorf("vm: %s already assigned to lease %s", id, instance.preparation.Lease)
		}
		return nil // same lease; idempotent
	}
	instance.preparation = &preparation
	instance.status.Phase = PhaseAssigned
	instance.status.Lease = preparation.Lease
	f.journal("prepare %s", id)
	if f.FailAfter != nil {
		return f.FailAfter("prepare", id)
	}
	return nil
}

// Rendezvous implements Driver.
func (f *Fake) Rendezvous(_ context.Context, id ID, rendezvous Rendezvous) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("rendezvous", id); err != nil {
		return err
	}
	instance, ok := f.vms[id]
	if !ok {
		return ErrNotFound
	}
	if instance.preparation == nil || instance.preparation.Lease != rendezvous.Lease {
		return fmt.Errorf("vm: %s is not prepared for lease %s", id, rendezvous.Lease)
	}
	if instance.rendezvous != nil {
		if instance.rendezvous.Lease != rendezvous.Lease {
			return fmt.Errorf("vm: %s already rendezvoused for lease %s", id, instance.rendezvous.Lease)
		}
		return nil
	}
	instance.rendezvous = &rendezvous
	if f.OnAttach != nil {
		f.OnAttach(rendezvous.WorkspaceDevice)
	}
	f.journal("rendezvous %s device=%s", id, rendezvous.WorkspaceDevice)
	if f.FailAfter != nil {
		return f.FailAfter("rendezvous", id)
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
	if instance.rendezvous != nil && f.OnDetach != nil {
		f.OnDetach(instance.rendezvous.WorkspaceDevice)
	}
	delete(f.vms, id)
	f.journal("destroy %s", id)
	return nil
}

// AdvanceBoot moves a booting VM to warm.
func (f *Fake) AdvanceBoot(id ID) bool { return f.advance(id, PhaseBooting, PhaseWarm) }

// MarkListening moves an assigned VM to a registered listener.
func (f *Fake) MarkListening(id ID) bool { return f.advance(id, PhaseAssigned, PhaseListening) }

// MarkHookBlocked records GitHub's selected listener and hook identity.
func (f *Fake) MarkHookBlocked(id ID, identity JobIdentity) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance, ok := f.vms[id]
	if !ok || instance.status.Phase != PhaseListening {
		return false
	}
	instance.status.Phase = PhaseHookBlocked
	instance.status.Identity = identity
	f.journal("phase %s %s->%s", id, PhaseListening, PhaseHookBlocked)
	return true
}

// MarkReady moves a rendezvoused VM to ready after hook release.
func (f *Fake) MarkReady(id ID, clock ClockSample) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance, ok := f.vms[id]
	if !ok || instance.status.Phase != PhaseHookBlocked || instance.rendezvous == nil {
		return false
	}
	instance.status.Phase = PhaseReady
	instance.status.Clock = clock
	f.journal("phase %s %s->%s", id, PhaseHookBlocked, PhaseReady)
	return true
}

// MarkExited moves a ready or assigned VM to exited with a code.
func (f *Fake) MarkExited(id ID, code int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance, ok := f.vms[id]
	if !ok || (instance.status.Phase != PhaseReady &&
		instance.status.Phase != PhaseHookBlocked &&
		instance.status.Phase != PhaseListening &&
		instance.status.Phase != PhaseAssigned) {
		return false
	}
	instance.status.Phase = PhaseExited
	instance.status.ExitCode = code
	if instance.rendezvous != nil && f.OnDetach != nil {
		// The guest is dead; the workspace device is no longer held open.
		f.OnDetach(instance.rendezvous.WorkspaceDevice)
		instance.rendezvous = nil
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
func (f *Fake) Preparation(id ID) (Preparation, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance, ok := f.vms[id]
	if !ok || instance.preparation == nil {
		return Preparation{}, false
	}
	return *instance.preparation, true
}

// RendezvousFor returns the rendezvous a VM holds.
func (f *Fake) RendezvousFor(id ID) (Rendezvous, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance, ok := f.vms[id]
	if !ok || instance.rendezvous == nil {
		return Rendezvous{}, false
	}
	return *instance.rendezvous, true
}
