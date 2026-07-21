package guestd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

const (
	ProcessMountpoint = "/var/lib/postflight/process"
	ProcessImagesDir  = ProcessMountpoint + "/images"
)

// ProcessCheckpoints couples the generic PID capsule to CRIU. ImagesRoot is
// the mountpoint of the encrypted process zvol; every operation is rejected
// unless its image directory stays beneath that root.
type ProcessCheckpoints struct {
	Capsules   *CapsuleManager
	CRIU       CRIU
	ImagesRoot string
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

func (p ProcessCheckpoints) restoreObserved(ctx context.Context, imagesDir, expectedDigest, expectedVersion string, externalMounts []ExternalMount, observer checkpointObserver) (int, error) {
	if err := p.validate(imagesDir, externalMounts); err != nil {
		return 0, err
	}
	if expectedDigest == "" {
		return 0, errors.New("guestd: checkpoint restore has no expected digest")
	}
	if expectedVersion == "" {
		return 0, errors.New("guestd: checkpoint restore has no expected version")
	}
	pid, err := p.CRIU.restoreObserved(ctx, Capsule{
		ImagesDir:      imagesDir,
		ExternalMounts: externalMounts,
	}, expectedDigest, expectedVersion, observer)
	if err != nil {
		return 0, err
	}
	if err := p.Capsules.UseRestored(ctx, pid); err != nil {
		return 0, err
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
