package vm

import (
	"context"
	"fmt"
	"sync"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
)

// Fake is the in-memory Driver for agent tests and the sim harness. VM
// phases only advance when a scenario says so (AdvanceBoot, MarkReady,
// MarkExited), which is what makes agent behavior under slow boots, stuck
// guests, and crash-during-assign deterministic to explore.
type Fake struct {
	mu      sync.Mutex
	vms     map[ID]*fakeVM
	updates chan ID

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
	authorized  *Authorization
}

// NewFake returns an empty fake hypervisor.
func NewFake() *Fake {
	return &Fake{vms: map[ID]*fakeVM{}, Images: map[Class]string{}, updates: make(chan ID, 256)}
}

func (f *Fake) Updates() <-chan ID { return f.updates }

func (f *Fake) notify(id ID) {
	select {
	case f.updates <- id:
	default:
	}
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
		Incarnation: string(id) + "-incarnation",
	}}
	f.journal("launch %s class=%s", id, class)
	f.notify(id)
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
		if instance.preparation.MemberID != preparation.MemberID {
			return fmt.Errorf("vm: %s already prepared as member %s", id, instance.preparation.MemberID)
		}
		return nil // same member; idempotent
	}
	if instance.status.Phase != PhaseWarm {
		return fmt.Errorf("vm: %s is not an idle warm VM", id)
	}
	instance.preparation = &preparation
	instance.status.MemberID = preparation.MemberID
	instance.status.Phase = PhaseAssigned
	f.journal("prepare %s", id)
	f.notify(id)
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
	if instance.status.MemberID != rendezvous.MemberID || rendezvous.AssignmentID == "" || instance.status.Phase != PhaseJobAssigned {
		return fmt.Errorf("vm: %s is not the selected member %s", id, rendezvous.MemberID)
	}
	if instance.rendezvous != nil {
		if instance.rendezvous.AssignmentID != rendezvous.AssignmentID {
			return fmt.Errorf("vm: %s already rendezvoused for assignment %s", id, instance.rendezvous.AssignmentID)
		}
		return nil
	}
	instance.rendezvous = &rendezvous
	if f.OnAttach != nil {
		f.OnAttach(rendezvous.WorkspaceDevice)
		f.OnAttach(rendezvous.ToolDevice)
		f.OnAttach(rendezvous.ProcessDevice)
	}
	f.journal("rendezvous %s device=%s", id, rendezvous.WorkspaceDevice)
	f.notify(id)
	if f.FailAfter != nil {
		return f.FailAfter("rendezvous", id)
	}
	return nil
}

// Authorize implements Driver.
func (f *Fake) Authorize(_ context.Context, id ID, authorization Authorization) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("authorize", id); err != nil {
		return err
	}
	instance, ok := f.vms[id]
	if !ok {
		return ErrNotFound
	}
	if instance.status.Phase != PhaseBound || instance.status.MemberID != authorization.MemberID ||
		instance.rendezvous == nil || instance.rendezvous.AssignmentID != authorization.AssignmentID ||
		instance.status.Assignment.RequestID != authorization.RequestID {
		return fmt.Errorf("vm: %s has no matching restored assignment", id)
	}
	if instance.authorized != nil {
		return nil
	}
	instance.authorized = &authorization
	f.journal("authorize %s", id)
	f.notify(id)
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
func (f *Fake) Quiesce(_ context.Context, id ID) (CheckpointArtifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("quiesce", id); err != nil {
		return CheckpointArtifact{}, err
	}
	if _, ok := f.vms[id]; !ok {
		return CheckpointArtifact{}, ErrNotFound
	}
	f.journal("quiesce %s", id)
	return CheckpointArtifact{
		Digest:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Version: "Version: 4.2",
	}, nil
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
		f.OnDetach(instance.rendezvous.ToolDevice)
		f.OnDetach(instance.rendezvous.ProcessDevice)
	}
	delete(f.vms, id)
	f.journal("destroy %s", id)
	f.notify(id)
	return nil
}

// AdvanceBoot moves a booting VM to warm.
func (f *Fake) AdvanceBoot(id ID) bool { return f.advance(id, PhaseBooting, PhaseWarm) }

// MarkBound records completed mount and process restore.
func (f *Fake) MarkBound(id ID) bool { return f.advance(id, PhaseJobAssigned, PhaseBound) }

// MarkBoundWithRestore records completed binding plus the process restore
// evidence guestd would report.
func (f *Fake) MarkBoundWithRestore(id ID, restore guestproto.RestoreStatus) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance, ok := f.vms[id]
	if !ok || instance.status.Phase != PhaseJobAssigned {
		return false
	}
	instance.status.Phase = PhaseBound
	copy := restore
	instance.status.Restore = &copy
	f.journal("phase %s %s->%s restore=%s", id, PhaseJobAssigned, PhaseBound, restore.Outcome)
	f.notify(id)
	return true
}

// MarkRecycleRequired records an unsafe guest-local restore outcome while
// Worker remains blocked.
func (f *Fake) MarkRecycleRequired(id ID, restore guestproto.RestoreStatus, reason string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance, ok := f.vms[id]
	if !ok {
		return false
	}
	instance.status.Phase = PhaseRecycleRequired
	copy := restore
	instance.status.Restore = &copy
	instance.status.FailureReason = reason
	f.journal("recycle-required %s", id)
	f.notify(id)
	return true
}

// MarkListening moves a prepared VM to a registered listener.
func (f *Fake) MarkListening(id ID) bool { return f.advance(id, PhaseAssigned, PhaseListening) }

// MarkAssigned records the exact job observed by the local listener.
func (f *Fake) MarkAssigned(id ID, assignment Assignment) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance, ok := f.vms[id]
	if !ok || instance.status.Phase != PhaseListening || assignment.RequestID == "" {
		return false
	}
	instance.status.Phase = PhaseJobAssigned
	instance.status.Assignment = assignment
	f.journal("phase %s %s->%s request=%s", id, PhaseListening, PhaseJobAssigned, assignment.RequestID)
	f.notify(id)
	return true
}

// MarkWorkerReady records that host authorization released Runner.Listener.
func (f *Fake) MarkWorkerReady(id ID, clock ClockSample) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance, ok := f.vms[id]
	if !ok || instance.status.Phase != PhaseBound || instance.authorized == nil {
		return false
	}
	instance.status.Phase = PhaseWorkerReady
	instance.status.Clock = clock
	f.journal("phase %s %s->%s", id, PhaseBound, PhaseWorkerReady)
	f.notify(id)
	return true
}

// MarkHookBlocked records GitHub's selected listener and hook identity.
func (f *Fake) MarkHookBlocked(id ID, identity JobIdentity) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance, ok := f.vms[id]
	if !ok || instance.status.Phase != PhaseWorkerReady {
		return false
	}
	instance.status.Phase = PhaseHookBlocked
	instance.status.Identity = identity
	f.journal("phase %s %s->%s", id, PhaseWorkerReady, PhaseHookBlocked)
	f.notify(id)
	return true
}

// MarkReady moves a rendezvoused VM to ready after hook release.
func (f *Fake) MarkReady(id ID, clock ClockSample) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance, ok := f.vms[id]
	if !ok || instance.status.Phase != PhaseHookBlocked || instance.authorized == nil {
		return false
	}
	instance.status.Phase = PhaseReady
	instance.status.Clock = clock
	instance.status.CustomerStepsReleased = true
	f.journal("phase %s %s->%s", id, PhaseHookBlocked, PhaseReady)
	f.notify(id)
	return true
}

// MarkExited moves a ready or assigned VM to exited with a code.
func (f *Fake) MarkExited(id ID, code int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance, ok := f.vms[id]
	if !ok || (instance.status.Phase != PhaseReady &&
		instance.status.Phase != PhaseHookBlocked &&
		instance.status.Phase != PhaseWorkerReady &&
		instance.status.Phase != PhaseListening &&
		instance.status.Phase != PhaseJobAssigned &&
		instance.status.Phase != PhaseBound &&
		instance.status.Phase != PhaseAssigned) {
		return false
	}
	instance.status.Phase = PhaseExited
	instance.status.ExitCode = code
	if instance.rendezvous != nil && f.OnDetach != nil {
		// The guest is dead; its devices are no longer held open.
		f.OnDetach(instance.rendezvous.WorkspaceDevice)
		f.OnDetach(instance.rendezvous.ToolDevice)
		f.OnDetach(instance.rendezvous.ProcessDevice)
		instance.rendezvous = nil
	}
	f.journal("exited %s code=%d", id, code)
	f.notify(id)
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
	f.notify(id)
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

// AuthorizationFor returns the authorization a VM holds.
func (f *Fake) AuthorizationFor(id ID) (Authorization, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance, ok := f.vms[id]
	if !ok || instance.authorized == nil {
		return Authorization{}, false
	}
	return *instance.authorized, true
}
