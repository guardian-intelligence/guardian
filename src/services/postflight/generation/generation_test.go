package generation

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
)

func testManifest() Manifest {
	digest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	return Manifest{
		SchemaVersion: SchemaVersion, GenerationID: "gen-2", ParentGenerationID: "gen-1", GenerationNumber: 2,
		Identity:     Identity{Tenant: "tenant-1", Repository: "guardian/repo", Branch: "main"},
		Workspace:    Volume{Role: "workspace", Generation: "ws-2", SnapshotGUID: "1002", ContentDigest: digest},
		Root:         Volume{Role: "root", Generation: "root-1", SnapshotGUID: "2001", ContentDigest: digest},
		Tools:        []Volume{{Role: "tools", Generation: "tools-1", SnapshotGUID: "3001", ContentDigest: digest}},
		Process:      ProcessSnapshot{CiphertextDigest: digest, Engine: CheckpointEngine, FormatVersion: "4.1"},
		Platform:     Platform{RunnerClass: RunnerClass, OperatingSystem: OperatingSystem, OperatingSystemVersion: OperatingSystemVersion, GuestImageDigest: digest, KernelDigest: digest, QEMUVersion: "11.0.2", CRIUVersion: "4.1", MachineType: "pc-q35-8.2", CPUModel: "host", CPUIDDigest: digest},
		Confidential: ConfidentialPolicy{Technology: ConfidentialTechnology, Measurement: digest, MinimumTCB: TCB{Bootloader: 10, TEE: 1, SNP: 25, Microcode: 84}},
		WrappedDEK:   make([]byte, 48), SignerKeyID: "generation-signer-1",
	}
}

func TestRestoreFailurePolicy(t *testing.T) {
	cause := errors.New("redacted CRIU failure")
	recoverable := NewRestoreFailure(RestoreIncompatible, "kernel-feature", cause)
	if !IsColdFallback(recoverable) {
		t.Fatal("ordinary incompatibility did not permit cold fallback")
	}
	class, code := RestoreFailureDetails(recoverable)
	if class != RestoreIncompatible || code != "kernel-feature" || !errors.Is(recoverable, cause) {
		t.Fatalf("recoverable details = %s/%s, error = %v", class, code, recoverable)
	}
	for _, class := range []RestoreFailureClass{RestoreIntegrity, RestoreCleanup} {
		if IsColdFallback(NewRestoreFailure(class, "unsafe", cause)) {
			t.Fatalf("%s failure permitted cold fallback", class)
		}
	}
	if class, code := RestoreFailureDetails(cause); class != RestoreCleanup || code != "unclassified" {
		t.Fatalf("untyped failure = %s/%s", class, code)
	}
}

func TestManifestAuthentication(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := testManifest()
	if err := manifest.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Verify(publicKey); err != nil {
		t.Fatal(err)
	}
	manifest.GenerationNumber++
	if err := manifest.Verify(publicKey); err == nil {
		t.Fatal("modified manifest verified")
	}
}

func TestSealRequiresAtomicOrdering(t *testing.T) {
	machine := NewSeal()
	manifest := testManifest()
	for _, event := range []Event{EventFreeze, EventCheckpoint, EventSealVolumes, EventAuthenticate, EventStageCandidate, EventPublish} {
		if err := machine.Apply(event, manifest); err != nil {
			t.Fatalf("%s: %v", event, err)
		}
	}
	if machine.State != StatePublished {
		t.Fatalf("state = %s", machine.State)
	}

	machine = NewSeal()
	if err := machine.Apply(EventSealVolumes, manifest); err == nil {
		t.Fatal("volumes sealed before process checkpoint")
	}
}

func TestRestoreRequiresAttestationAndSanitization(t *testing.T) {
	manifest := testManifest()
	machine, err := NewRestore(manifest, true, RestorePolicy{Identity: manifest.Identity, MinimumGenerationNumber: 2})
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := manifest.Digest()
	machine.Attestation = Attestation{Technology: ConfidentialTechnology, Measurement: manifest.Confidential.Measurement, TCB: manifest.Confidential.MinimumTCB, ManifestDigest: digest}
	if err := machine.Apply(EventAttest, manifest); err != nil {
		t.Fatal(err)
	}
	for _, event := range []Event{EventReleaseKey, EventBind, EventRestore} {
		if err := machine.Apply(event, manifest); err != nil {
			t.Fatalf("%s: %v", event, err)
		}
	}
	if err := machine.Apply(EventSanitize, manifest); err == nil {
		t.Fatal("unsanitized restore advanced")
	}
	machine.Sanitization = Sanitization{ClockSynchronized: true, CredentialsReplaced: true, NetworkRecreated: true, EntropyReseeded: true, RunnerFresh: true}
	if err := machine.Apply(EventSanitize, manifest); err != nil {
		t.Fatal(err)
	}
	if err := machine.Apply(EventReleaseJob, manifest); err != nil {
		t.Fatal(err)
	}
	if machine.State != StateReady {
		t.Fatalf("state = %s", machine.State)
	}
}

func TestRestoreRejectsRollbackAndCrossIdentity(t *testing.T) {
	manifest := testManifest()
	if _, err := NewRestore(manifest, true, RestorePolicy{Identity: manifest.Identity, MinimumGenerationNumber: 3}); err == nil {
		t.Fatal("rollback floor accepted an older generation")
	}
	other := manifest.Identity
	other.Repository = "other/repo"
	if _, err := NewRestore(manifest, true, RestorePolicy{Identity: other, MinimumGenerationNumber: 2}); err == nil {
		t.Fatal("cross-repository generation was selected")
	}
	if MeetsMinimumTCB(TCB{Bootloader: 10, TEE: 1, SNP: 24, Microcode: 999}, manifest.Confidential.MinimumTCB) {
		t.Fatal("packed-style TCB comparison accepted downgraded SNP component")
	}

	machine, err := NewRestore(manifest, true, RestorePolicy{Identity: manifest.Identity, MinimumGenerationNumber: 2})
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := manifest.Digest()
	machine.Attestation = Attestation{Technology: ConfidentialTechnology, Measurement: manifest.Confidential.Measurement, TCB: TCB{Bootloader: 10, TEE: 1, SNP: 24, Microcode: 999}, ManifestDigest: digest}
	if err := machine.Apply(EventAttest, manifest); err == nil {
		t.Fatal("downgraded TCB attested")
	}
}

func TestFallbackStopsAfterRestore(t *testing.T) {
	manifest := testManifest()
	machine, err := NewRestore(manifest, true, RestorePolicy{Identity: manifest.Identity, MinimumGenerationNumber: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := machine.Apply(EventFallback, manifest); err != nil {
		t.Fatal(err)
	}
	if machine.State != StateColdFallback {
		t.Fatalf("state = %s", machine.State)
	}

	machine.State = StateRestored
	if err := machine.Apply(EventFallback, manifest); err == nil {
		t.Fatal("restored state fell back in-place")
	}
}
