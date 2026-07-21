package guestd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// CRIU is the one checkpoint implementation for the initial confidential
// runner. It checkpoints a generic process tree; it has no workload-specific
// knowledge. ImagesRoot must be an already-open encrypted generation volume.
type CRIU struct {
	Path       string
	ImagesRoot string
	WorkRoot   string
	// RestoreRun isolates CRIU's mount reconstruction from the base VM.
	RestoreRun func(context.Context, string, ...string) (string, error)
}

type Capsule struct {
	RootPID        int
	ImagesDir      string
	ExternalMounts []ExternalMount
}

type ExternalMount struct {
	Key  string
	Path string
}

type CheckpointArtifact struct {
	Digest  string
	Version string
}

type checkpointObserver func(string)

func observeCheckpoint(observer checkpointObserver, event string) {
	if observer != nil {
		observer(event)
	}
}

func (c CRIU) Check(ctx context.Context) error {
	_, err := c.run(ctx, "check")
	return err
}

func (c CRIU) Version(ctx context.Context) (string, error) {
	output, err := c.run(ctx, "--version")
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(strings.TrimSpace(output), "\n")
	if line == "" {
		return "", errors.New("guestd: CRIU returned an empty version")
	}
	return line, nil
}

// Dump leaves the process tree stopped after a successful checkpoint. The
// caller must either atomically seal every coupled volume or retire the donor;
// resuming it and publishing the images would fork one logical generation.
func (c CRIU) Dump(ctx context.Context, capsule Capsule) (CheckpointArtifact, error) {
	return c.dumpObserved(ctx, capsule, nil)
}

func (c CRIU) dumpObserved(ctx context.Context, capsule Capsule, observer checkpointObserver) (CheckpointArtifact, error) {
	if err := c.validate(capsule, true); err != nil {
		return CheckpointArtifact{}, err
	}
	observeCheckpoint(observer, "checkpoint_version_started")
	version, err := c.Version(ctx)
	if err != nil {
		return CheckpointArtifact{}, err
	}
	observeCheckpoint(observer, "checkpoint_version_completed")
	if err := os.RemoveAll(capsule.ImagesDir); err != nil {
		return CheckpointArtifact{}, fmt.Errorf("guestd: clearing CRIU images: %w", err)
	}
	if err := os.MkdirAll(capsule.ImagesDir, 0o700); err != nil {
		return CheckpointArtifact{}, fmt.Errorf("guestd: creating CRIU images: %w", err)
	}
	workDir, err := c.workDir("dump")
	if err != nil {
		return CheckpointArtifact{}, err
	}
	args := []string{
		"dump", "--tree", strconv.Itoa(capsule.RootPID),
		"--images-dir", capsule.ImagesDir,
		"--work-dir", workDir,
		"--log-file", "criu.log",
		"--leave-stopped", "--file-locks", "--shell-job", "--tcp-close",
		"--manage-cgroups=ignore",
		"--ext-mount-map", "auto", "--enable-external-masters",
	}
	for _, mount := range sortedMounts(capsule.ExternalMounts) {
		args = append(args, "--external", "mnt["+mount.Path+"]:"+mount.Key)
	}
	observeCheckpoint(observer, "checkpoint_criu_dump_started")
	if _, err := c.run(ctx, args...); err != nil {
		return CheckpointArtifact{}, err
	}
	observeCheckpoint(observer, "checkpoint_criu_dump_completed")
	observeCheckpoint(observer, "checkpoint_digest_started")
	digest, err := digestDirectory(capsule.ImagesDir)
	if err != nil {
		return CheckpointArtifact{}, err
	}
	observeCheckpoint(observer, "checkpoint_digest_completed")
	return CheckpointArtifact{Digest: digest, Version: version}, nil
}

func (c CRIU) Restore(ctx context.Context, capsule Capsule, expectedDigest, expectedVersion string) (int, error) {
	return c.restoreObserved(ctx, capsule, expectedDigest, expectedVersion, nil)
}

func (c CRIU) restoreObserved(ctx context.Context, capsule Capsule, expectedDigest, expectedVersion string, observer checkpointObserver) (int, error) {
	if err := c.validate(capsule, false); err != nil {
		return 0, err
	}
	observeCheckpoint(observer, "restore_version_started")
	version, err := c.Version(ctx)
	if err != nil {
		return 0, err
	}
	if version != expectedVersion {
		return 0, fmt.Errorf("guestd: CRIU version %q does not match checkpoint version %q", version, expectedVersion)
	}
	observeCheckpoint(observer, "restore_version_completed")
	observeCheckpoint(observer, "restore_digest_started")
	digest, err := digestDirectory(capsule.ImagesDir)
	if err != nil {
		return 0, err
	}
	if digest != expectedDigest {
		return 0, fmt.Errorf("guestd: CRIU artifact digest %s does not match %s", digest, expectedDigest)
	}
	observeCheckpoint(observer, "restore_digest_completed")
	workDir, err := c.workDir("restore")
	if err != nil {
		return 0, err
	}
	pidfile := filepath.Join(workDir, "root.pid")
	if err := os.Remove(pidfile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	args := []string{
		"restore", "--images-dir", capsule.ImagesDir,
		"--work-dir", workDir,
		"--log-file", "criu.log",
		"--restore-detached", "--pidfile", pidfile,
		"--root", "/",
		"--file-locks", "--shell-job", "--tcp-close",
		"--manage-cgroups=ignore",
		"--ext-mount-map", "auto", "--enable-external-masters",
	}
	for _, mount := range sortedMounts(capsule.ExternalMounts) {
		args = append(args, "--external", "mnt["+mount.Key+"]:"+mount.Path)
	}
	observeCheckpoint(observer, "restore_criu_started")
	if _, err := c.runRestore(ctx, args...); err != nil {
		return 0, c.restoreFailure(workDir, capsule.ExternalMounts, err)
	}
	observeCheckpoint(observer, "restore_criu_completed")
	raw, err := os.ReadFile(pidfile)
	if err != nil {
		return 0, fmt.Errorf("guestd: reading restored root pid: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		return 0, errors.New("guestd: CRIU returned an invalid root pid")
	}
	return pid, nil
}

func (c CRIU) restoreFailure(workDir string, externalMounts []ExternalMount, restoreErr error) error {
	file, err := os.Open(filepath.Join(workDir, "criu.log"))
	if err != nil {
		return restoreErr
	}
	defer file.Close()
	const diagnosticTailBytes = 64 << 10
	if info, err := file.Stat(); err == nil && info.Size() > diagnosticTailBytes {
		_, _ = file.Seek(info.Size()-diagnosticTailBytes, io.SeekStart)
	}
	raw, err := io.ReadAll(io.LimitReader(file, diagnosticTailBytes))
	if err != nil {
		return restoreErr
	}
	var diagnostics []string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.Contains(line, "Error (") {
			continue
		}
		diagnostics = append(diagnostics, safeCRIUError(line, externalMounts))
		if len(diagnostics) > 8 {
			diagnostics = diagnostics[len(diagnostics)-8:]
		}
	}
	if len(diagnostics) == 0 {
		return restoreErr
	}
	return fmt.Errorf("%w; CRIU diagnostics: %s", restoreErr, strings.Join(diagnostics, " | "))
}

func safeCRIUError(line string, externalMounts []ExternalMount) string {
	if offset := strings.Index(line, "Error ("); offset >= 0 {
		line = line[offset:]
	}
	fields := strings.Fields(line)
	for index, field := range fields {
		if strings.Contains(field, "/") && !strings.HasPrefix(field, "(criu/") {
			fields[index] = classifyCRIUPath(field, externalMounts)
		}
	}
	line = strings.Join(fields, " ")
	if len(line) > 512 {
		line = line[:512]
	}
	return line
}

func classifyCRIUPath(field string, externalMounts []ExternalMount) string {
	path := strings.Trim(field, "<>[](){}\"',:")
	matchedKey := ""
	matchedLength := -1
	for _, mount := range externalMounts {
		if (path == mount.Path || strings.HasPrefix(path, mount.Path+"/")) && len(mount.Path) > matchedLength {
			matchedKey = mount.Key
			matchedLength = len(mount.Path)
		}
	}
	if matchedKey != "" {
		return "<external:" + matchedKey + ">"
	}
	for _, class := range []struct {
		root string
		name string
	}{
		{root: "/tmp", name: "capsule-tmp"},
		{root: "/opt/actions-runner/_work", name: "runner-work"},
		{root: "/opt/actions-runner", name: "runner-image"},
		{root: "/home/runner", name: "runner-home"},
		{root: ProcessMountpoint, name: "process-volume"},
	} {
		if path == class.root || strings.HasPrefix(path, class.root+"/") {
			return "<" + class.name + ">"
		}
	}
	if filepath.IsAbs(path) {
		return "<guest-root>"
	}
	return "<relative-path>"
}

func (c CRIU) validate(capsule Capsule, requireRoot bool) error {
	switch {
	case c.Path == "" || !filepath.IsAbs(c.Path):
		return errors.New("guestd: CRIU path must be absolute")
	case c.ImagesRoot == "" || !filepath.IsAbs(c.ImagesRoot):
		return errors.New("guestd: CRIU images root must be absolute")
	case c.WorkRoot == "" || !filepath.IsAbs(c.WorkRoot):
		return errors.New("guestd: CRIU work root must be absolute")
	case requireRoot && capsule.RootPID <= 1:
		return errors.New("guestd: capsule root pid must not be init")
	}
	if !beneath(c.ImagesRoot, capsule.ImagesDir) {
		return errors.New("guestd: CRIU images must be inside the encrypted images root")
	}
	if !beneath(c.ImagesRoot, c.WorkRoot) {
		return errors.New("guestd: CRIU diagnostics must be inside the encrypted images root")
	}
	seen := map[string]bool{}
	for _, mount := range capsule.ExternalMounts {
		if mount.Key == "" || strings.ContainsAny(mount.Key, "[]:/") || !filepath.IsAbs(mount.Path) || filepath.Clean(mount.Path) != mount.Path {
			return errors.New("guestd: invalid CRIU external mount")
		}
		if seen[mount.Key] {
			return fmt.Errorf("guestd: duplicate CRIU external mount %q", mount.Key)
		}
		seen[mount.Key] = true
	}
	return nil
}

func beneath(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (c CRIU) workDir(operation string) (string, error) {
	dir := filepath.Join(c.WorkRoot, operation)
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func (c CRIU) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.Path, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("guestd: CRIU %s failed: %w", args[0], err)
	}
	return string(output), nil
}

func (c CRIU) runRestore(ctx context.Context, args ...string) (string, error) {
	if c.RestoreRun == nil {
		return c.run(ctx, args...)
	}
	output, err := c.RestoreRun(ctx, c.Path, args...)
	if err != nil {
		return "", fmt.Errorf("guestd: CRIU restore failed: %w", err)
	}
	return output, nil
}

// RunRestorePrivate gives CRIU a disposable copy of the guest mount table.
// Its mount reconstruction may then detach inherited system mounts without
// altering the base VM namespace that keeps guestd alive.
func RunRestorePrivate(ctx context.Context, path string, args ...string) (string, error) {
	commandArgs := []string{"--mount", "--propagation", "private", "--", path}
	commandArgs = append(commandArgs, args...)
	cmd := exec.CommandContext(ctx, "/usr/bin/unshare", commandArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("private mount restore failed: %w", err)
	}
	return string(output), nil
}

func sortedMounts(mounts []ExternalMount) []ExternalMount {
	ordered := append([]ExternalMount(nil), mounts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Key < ordered[j].Key })
	return ordered
}

// digestDirectory authenticates names and contents without depending on file
// traversal order. CRIU images contain no acceptable symlinks or directories
// below the images root.
func digestDirectory(root string) (string, error) {
	var names []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if entry.IsDir() {
			return fmt.Errorf("guestd: nested CRIU image directory %s", path)
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("guestd: unsupported CRIU image entry %s", path)
		}
		names = append(names, filepath.Base(path))
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", errors.New("guestd: CRIU image directory is empty")
	}
	sort.Strings(names)
	digest := sha256.New()
	for _, name := range names {
		if err := binary.Write(digest, binary.BigEndian, uint32(len(name))); err != nil {
			return "", err
		}
		_, _ = io.WriteString(digest, name)
		file, err := os.Open(filepath.Join(root, name))
		if err != nil {
			return "", err
		}
		reader := bufio.NewReader(file)
		_, copyErr := io.Copy(digest, reader)
		closeErr := file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}
