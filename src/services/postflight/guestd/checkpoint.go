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

func (p ProcessCheckpoints) validate(imagesDir, externalMount string) error {
	switch {
	case p.Capsules == nil:
		return errors.New("guestd: capsule manager is required")
	case p.ImagesRoot == "" || !filepath.IsAbs(p.ImagesRoot):
		return errors.New("guestd: checkpoint images root must be absolute")
	case p.CRIU.ImagesRoot != p.ImagesRoot:
		return errors.New("guestd: CRIU and checkpoint image roots differ")
	case !beneath(p.ImagesRoot, imagesDir):
		return errors.New("guestd: checkpoint images escape the process volume")
	case !validMountpoint(externalMount):
		return fmt.Errorf("guestd: unsafe checkpoint external mount %q", externalMount)
	}
	return nil
}

func (p ProcessCheckpoints) Restore(ctx context.Context, imagesDir, expectedDigest, externalMount string) (int, error) {
	if err := p.validate(imagesDir, externalMount); err != nil {
		return 0, err
	}
	if expectedDigest == "" {
		return 0, errors.New("guestd: checkpoint restore has no expected digest")
	}
	pid, err := p.CRIU.Restore(ctx, Capsule{
		ImagesDir: imagesDir,
		ExternalMounts: []ExternalMount{
			{Key: "process", Path: p.ImagesRoot},
			{Key: "root", Path: "/"},
			{Key: "workspace", Path: externalMount},
		},
	}, expectedDigest)
	if err != nil {
		return 0, err
	}
	if err := p.Capsules.UseRestored(ctx, pid); err != nil {
		return 0, err
	}
	return pid, nil
}

func (p ProcessCheckpoints) Dump(ctx context.Context, imagesDir, externalMount string) (CheckpointArtifact, error) {
	return p.dumpObserved(ctx, imagesDir, externalMount, nil)
}

func (p ProcessCheckpoints) dumpObserved(ctx context.Context, imagesDir, externalMount string, observer checkpointObserver) (CheckpointArtifact, error) {
	if err := p.validate(imagesDir, externalMount); err != nil {
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
		RootPID:   rootPID,
		ImagesDir: imagesDir,
		ExternalMounts: []ExternalMount{
			{Key: "process", Path: p.ImagesRoot},
			{Key: "root", Path: "/"},
			{Key: "workspace", Path: externalMount},
		},
	}, observer)
}
