package vm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// rootDisks materializes and destroys per-VM root volumes. It is a seam so
// the driver's lifecycle logic is testable without a zpool; zfsDisks is the
// real implementation.
type rootDisks interface {
	// Ensure makes the dataset exist as a clone of image and its block
	// device usable. Idempotent.
	Ensure(ctx context.Context, dataset, image string) error
	// Destroy removes the dataset and its snapshots; absent datasets
	// succeed. It absorbs the post-detach window where the kernel still
	// holds the zvol open.
	Destroy(ctx context.Context, dataset string) error
}

type zfsDisks struct{}

const (
	zfsTimeout      = 30 * time.Second
	zvolDeviceWait  = 15 * time.Second
	zvolBusyRetries = 15 * time.Second
)

func (zfsDisks) run(ctx context.Context, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, zfsTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "zfs", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// Ensure implements rootDisks.
func (d zfsDisks) Ensure(ctx context.Context, dataset, image string) error {
	if _, _, err := d.run(ctx, "list", "-H", "-o", "name", dataset); err != nil {
		if _, stderr, err := d.run(ctx, "clone", "-o", "volmode=dev", image, dataset); err != nil {
			return fmt.Errorf("vm: cloning %s from %s: %s: %w", dataset, image, strings.TrimSpace(stderr), err)
		}
	}
	return d.waitDevice(ctx, dataset)
}

// waitDevice blocks until the zvol's device node exists and reports the full
// volume size — the node appears via udev noticeably after the clone
// returns, and briefly with a stale size.
func (d zfsDisks) waitDevice(ctx context.Context, dataset string) error {
	out, stderr, err := d.run(ctx, "get", "-Hp", "-o", "value", "volsize", dataset)
	if err != nil {
		return fmt.Errorf("vm: reading volsize of %s: %s: %w", dataset, strings.TrimSpace(stderr), err)
	}
	volsize, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return fmt.Errorf("vm: parsing volsize of %s: %w", dataset, err)
	}
	device := zvolDevicePath(dataset)
	deadline := time.Now().Add(zvolDeviceWait)
	for {
		if size, err := blockDeviceSize(device); err == nil && size == volsize {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("vm: device %s never became usable", device)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Destroy implements rootDisks.
func (d zfsDisks) Destroy(ctx context.Context, dataset string) error {
	deadline := time.Now().Add(zvolBusyRetries)
	for {
		_, stderr, err := d.run(ctx, "destroy", "-r", dataset)
		trimmed := strings.TrimSpace(stderr)
		switch {
		case err == nil, strings.Contains(trimmed, "does not exist"):
			return nil
		case strings.Contains(trimmed, "dataset is busy"):
			// The zvol stays open for ~1s after the guest releases it.
			if time.Now().After(deadline) {
				return fmt.Errorf("vm: destroying %s: still busy after %s", dataset, zvolBusyRetries)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		default:
			return fmt.Errorf("vm: destroying %s: %s: %w", dataset, trimmed, err)
		}
	}
}

func zvolDevicePath(dataset string) string { return "/dev/zvol/" + dataset }

// blockDeviceSize resolves a (possibly symlinked) block device and reads its
// size from sysfs.
func blockDeviceSize(device string) (int64, error) {
	resolved, err := filepath.EvalSymlinks(device)
	if err != nil {
		return 0, err
	}
	raw, err := os.ReadFile("/sys/class/block/" + filepath.Base(resolved) + "/size")
	if err != nil {
		return 0, err
	}
	sectors, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, errors.New("vm: unparseable sysfs block size")
	}
	return sectors * 512, nil
}
