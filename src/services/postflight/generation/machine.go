package generation

import (
	"errors"
	"fmt"
)

type State string

const (
	StateRunning       State = "running"
	StateFrozen        State = "frozen"
	StateCheckpointed  State = "checkpointed"
	StateVolumesSealed State = "volumes-sealed"
	StateAuthenticated State = "authenticated"
	StateCandidate     State = "candidate"
	StatePublished     State = "published"
	StateSelected      State = "selected"
	StateAttested      State = "attested"
	StateKeyReleased   State = "key-released"
	StateBound         State = "bound"
	StateRestored      State = "restored"
	StateSanitized     State = "sanitized"
	StateReady         State = "ready"
	StateDiscarded     State = "discarded"
	StateColdFallback  State = "cold-fallback"
)

type Event string

const (
	EventFreeze         Event = "freeze"
	EventCheckpoint     Event = "checkpoint"
	EventSealVolumes    Event = "seal-volumes"
	EventAuthenticate   Event = "authenticate"
	EventStageCandidate Event = "stage-candidate"
	EventPublish        Event = "publish"
	EventSelect         Event = "select"
	EventAttest         Event = "attest"
	EventReleaseKey     Event = "release-key"
	EventBind           Event = "bind"
	EventRestore        Event = "restore"
	EventSanitize       Event = "sanitize"
	EventReleaseJob     Event = "release-job"
	EventFail           Event = "fail"
	EventFallback       Event = "fallback"
)

type Machine struct {
	State          State
	ManifestDigest string
	Verified       bool
	Attestation    Attestation
	Sanitization   Sanitization
}

type Attestation struct {
	Technology     string
	Measurement    string
	TCB            TCB
	Debug          bool
	ManifestDigest string
}

type Sanitization struct {
	ClockSynchronized   bool
	CredentialsReplaced bool
	NetworkRecreated    bool
	EntropyReseeded     bool
	RunnerFresh         bool
}

type RestorePolicy struct {
	Identity                Identity
	MinimumGenerationNumber uint64
}

func NewSeal() Machine { return Machine{State: StateRunning} }

func NewRestore(manifest Manifest, verified bool, policy RestorePolicy) (Machine, error) {
	if err := manifest.Validate(); err != nil {
		return Machine{}, err
	}
	if manifest.Identity != policy.Identity {
		return Machine{}, errors.New("generation: manifest identity is outside the restore scope")
	}
	if manifest.GenerationNumber < policy.MinimumGenerationNumber {
		return Machine{}, errors.New("generation: manifest violates the rollback floor")
	}
	digest, err := manifest.Digest()
	if err != nil {
		return Machine{}, err
	}
	return Machine{State: StateSelected, ManifestDigest: digest, Verified: verified}, nil
}

func (m *Machine) Apply(event Event, manifest Manifest) error {
	if event == EventFail {
		m.State = StateDiscarded
		return nil
	}
	if event == EventFallback {
		switch m.State {
		case StateSelected, StateAttested, StateKeyReleased, StateBound:
			m.State = StateColdFallback
			return nil
		default:
			return fmt.Errorf("generation: cannot cold-fallback from %s", m.State)
		}
	}
	next, ok := transitions[m.State][event]
	if !ok {
		return fmt.Errorf("generation: event %s is invalid in %s", event, m.State)
	}
	if err := m.guard(event, manifest); err != nil {
		return err
	}
	m.State = next
	return nil
}

var transitions = map[State]map[Event]State{
	StateRunning:       {EventFreeze: StateFrozen},
	StateFrozen:        {EventCheckpoint: StateCheckpointed},
	StateCheckpointed:  {EventSealVolumes: StateVolumesSealed},
	StateVolumesSealed: {EventAuthenticate: StateAuthenticated},
	StateAuthenticated: {EventStageCandidate: StateCandidate},
	StateCandidate:     {EventPublish: StatePublished},
	StateSelected:      {EventAttest: StateAttested},
	StateAttested:      {EventReleaseKey: StateKeyReleased},
	StateKeyReleased:   {EventBind: StateBound},
	StateBound:         {EventRestore: StateRestored},
	StateRestored:      {EventSanitize: StateSanitized},
	StateSanitized:     {EventReleaseJob: StateReady},
}

func (m Machine) guard(event Event, manifest Manifest) error {
	switch event {
	case EventAuthenticate, EventStageCandidate, EventPublish:
		if err := manifest.Validate(); err != nil {
			return err
		}
	case EventAttest:
		if !m.Verified {
			return errors.New("generation: unauthenticated manifest cannot be restored")
		}
		if m.Attestation.Technology != ConfidentialTechnology || m.Attestation.Debug || m.Attestation.Measurement != manifest.Confidential.Measurement || !MeetsMinimumTCB(m.Attestation.TCB, manifest.Confidential.MinimumTCB) || m.Attestation.ManifestDigest != m.ManifestDigest {
			return errors.New("generation: attestation does not authorize this manifest")
		}
	case EventSanitize, EventReleaseJob:
		if !m.Sanitization.complete() {
			return errors.New("generation: restored process state is not sanitized")
		}
	}
	return nil
}

func (s Sanitization) complete() bool {
	return s.ClockSynchronized && s.CredentialsReplaced && s.NetworkRecreated && s.EntropyReseeded && s.RunnerFresh
}
