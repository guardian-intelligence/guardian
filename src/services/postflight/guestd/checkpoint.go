package guestd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/generation"
)

const (
	ProcessMountpoint = "/var/lib/postflight/process"
	ProcessImagesDir  = ProcessMountpoint + "/images"
)

// ProcessCheckpoints couples the generic PID capsule to CRIU. ImagesRoot is
// the mountpoint of the encrypted process zvol; every operation is rejected
// unless its image directory stays beneath that root.
type CapsuleLifecycle interface {
	Start(context.Context) error
	Reset(context.Context) error
	UseRestored(context.Context, int) error
	RootPID() (int, error)
	PrepareForCheckpoint(context.Context) error
}

type ProcessCheckpoints struct {
	Capsules   CapsuleLifecycle
	CRIU       CRIU
	ImagesRoot string
	RecoveryTimeout time.Duration
}

type ProcessRestoreResult struct {
	Restored           bool
	ColdFallback       bool
	ProcessInvalidated bool
	FailureClass       generation.RestoreFailureClass
	FailureCode        string
}

func (p ProcessCheckpoints) validate(imagesDir string, externalMounts []ExternalMount) error {
	switch {
	case p.Capsules == nil:
		return errors.New("guestd: capsule manager is required")
	case p.ImagesRoot == "" || !filepath.IsAbs(p.ImagesRoot):
		return errors.New("guestd: checkpoint images root must be absolute")
	case p.CRIU.ImagesRoot != p.ImagesRoot:
		return errors.New("guestd: CRIU and checkpoint image roots differ")
	case !beneath(p.ImagesRoot, imagesDir):
		return errors.New("guestd: checkpoint images escape the process volume")
	case len(externalMounts) == 0:
		return errors.New("guestd: checkpoint requires an external mount")
	}
	for _, mount := range externalMounts {
		if !validMountpoint(mount.Path) {
			return fmt.Errorf("guestd: unsafe checkpoint external mount %q", mount.Path)
		}
	}
	return nil
}

func (p ProcessCheckpoints) Restore(ctx context.Context, imagesDir, expectedDigest, expectedVersion string, externalMounts []ExternalMount) (int, error) {
	return p.restoreObserved(ctx, imagesDir, expectedDigest, expectedVersion, externalMounts, nil)
}

// RestoreOrCold treats process state as an optimization. Every failed warm
// attempt first destroys and proves empty the capsule cgroup. Only a typed
// incompatibility may then start a cold capsule in the same live guest.
func (p ProcessCheckpoints) RestoreOrCold(ctx context.Context, imagesDir, expectedDigest, expectedVersion string, externalMounts []ExternalMount, observer checkpointObserver) (ProcessRestoreResult, error) {
	_, restoreErr := p.restoreObserved(ctx, imagesDir, expectedDigest, expectedVersion, externalMounts, observer)
	if restoreErr == nil {
		return ProcessRestoreResult{Restored: true}, nil
	}
	class, code := generation.RestoreFailureDetails(restoreErr)
	timeout := p.RecoveryTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	observeCheckpoint(observer, "restore_cleanup_started")
	if err := p.Capsules.Reset(recoveryCtx); err != nil {
		return ProcessRestoreResult{}, generation.NewRestoreFailure(generation.RestoreCleanup, "capsule-not-empty", errors.Join(restoreErr, err))
	}
	observeCheckpoint(observer, "restore_cleanup_completed")
	if class != generation.RestoreIncompatible {
		return ProcessRestoreResult{}, restoreErr
	}
	observeCheckpoint(observer, "restore_fallback_started")
	if err := p.Capsules.Start(recoveryCtx); err != nil {
		return ProcessRestoreResult{}, generation.NewRestoreFailure(generation.RestoreCleanup, "cold-capsule-start", errors.Join(restoreErr, err))
	}
	observeCheckpoint(observer, "restore_fallback_completed")
	return ProcessRestoreResult{
		ColdFallback: true, ProcessInvalidated: true,
		FailureClass: class, FailureCode: code,
	}, nil
}

func (p ProcessCheckpoints) restoreObserved(ctx context.Context, imagesDir, expectedDigest, expectedVersion string, externalMounts []ExternalMount, observer checkpointObserver) (int, error) {
	if err := p.validate(imagesDir, externalMounts); err != nil {
		return 0, generation.NewRestoreFailure(generation.RestoreIntegrity, "invalid-generation", err)
	}
	if expectedDigest == "" {
		return 0, generation.NewRestoreFailure(generation.RestoreIntegrity, "missing-digest", errors.New("guestd: checkpoint restore has no expected digest"))
	}
	if expectedVersion == "" {
		return 0, generation.NewRestoreFailure(generation.RestoreIntegrity, "missing-version", errors.New("guestd: checkpoint restore has no expected version"))
	}
	pid, err := p.CRIU.restoreObserved(ctx, Capsule{
		ImagesDir:      imagesDir,
		ExternalMounts: externalMounts,
	}, expectedDigest, expectedVersion, observer)
	if err != nil {
		return 0, err
	}
	if err := p.Capsules.UseRestored(ctx, pid); err != nil {
		return 0, generation.NewRestoreFailure(generation.RestoreCleanup, "capsule-adoption", err)
	}
	return pid, nil
}

func (p ProcessCheckpoints) Dump(ctx context.Context, imagesDir string, externalMounts []ExternalMount) (CheckpointArtifact, error) {
	return p.dumpObserved(ctx, imagesDir, externalMounts, nil)
}

func (p ProcessCheckpoints) dumpObserved(ctx context.Context, imagesDir string, externalMounts []ExternalMount, observer checkpointObserver) (CheckpointArtifact, error) {
	if err := p.validate(imagesDir, externalMounts); err != nil {
		return CheckpointArtifact{}, err
	}
	observeCheckpoint(observer, "checkpoint_capsule_prepare_started")
	if err := p.Capsules.PrepareForCheckpoint(ctx); err != nil {
		return CheckpointArtifact{}, fmt.Errorf("guestd: preparing capsule for checkpoint: %w", err)
	}
	observeCheckpoint(observer, "checkpoint_capsule_prepare_completed")
	rootPID, err := p.Capsules.RootPID()
	if err != nil {
		return CheckpointArtifact{}, err
	}
	return p.CRIU.dumpObserved(ctx, Capsule{
		RootPID:        rootPID,
		ImagesDir:      imagesDir,
		ExternalMounts: externalMounts,
	}, observer)
}
