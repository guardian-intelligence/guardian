package vm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/guardian-intelligence/guardian/src/postflight/hostd/guestproto"
)

// WarmTemplate contains only a generic, never-registered VM. Tenant disks,
// GitHub registration, and customer process state cannot enter this path.
// The manifest and memory file are root-owned; their digest is checked once
// before the class starts serving jobs.
type WarmTemplate struct {
	Key          string `json:"key"`
	RootSnapshot string `json:"root_snapshot"`
	MemoryPath   string `json:"memory_path"`
	MemorySHA256 string `json:"memory_sha256"`
}

// EnableWarmTemplate runs before the scheduling agent starts. A template is
// local to this host boot, exact QEMU and firmware bytes, image and geometry.
// Upgrades mint a new template; no running job is a template donor.
func (q *QEMU) EnableWarmTemplate(ctx context.Context, class Class, directory string) error {
	shape, ok := q.cfg.Classes[class]
	if !ok || shape.Flavor != FlavorTurbo {
		return errors.New("vm: warm templates require an explicit Turbo class")
	}
	if q.cfg.GuestNetwork != guestNetworkTap && q.cfg.GuestNetwork != guestNetworkUser {
		return errors.New("vm: restored Turbo templates require a managed network interface")
	}
	if !filepath.IsAbs(directory) {
		return errors.New("vm: warm template directory must be absolute")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := rootOwnedTemplatePath(directory, true); err != nil {
		return err
	}
	key, err := q.templateKey(shape)
	if err != nil {
		return err
	}
	directory = filepath.Join(directory, key)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := rootOwnedTemplatePath(directory, true); err != nil {
		return err
	}
	manifestPath := filepath.Join(directory, "manifest.json")
	expected := WarmTemplate{
		Key: key, RootSnapshot: q.cfg.DatasetRoot + "/templates/t-" + key + "@warm",
		MemoryPath: filepath.Join(directory, "memory"),
	}
	var template WarmTemplate
	if err := rootOwnedTemplatePath(manifestPath, false); err == nil {
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &template); err != nil {
			return fmt.Errorf("vm: invalid warm template manifest: %w", err)
		}
		if template.Key != expected.Key || template.RootSnapshot != expected.RootSnapshot || template.MemoryPath != expected.MemoryPath {
			return errors.New("vm: warm template identity mismatch")
		}
		if err := rootOwnedTemplatePath(template.MemoryPath, false); err != nil {
			return err
		}
		digest, err := fileDigest(template.MemoryPath)
		if err != nil || digest != template.MemorySHA256 {
			return errors.New("vm: warm template memory digest mismatch")
		}
		if err := runTemplateZFS(ctx, "list", "-H", "-o", "name", template.RootSnapshot); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		template = expected
		if err := q.buildWarmTemplate(ctx, class, &template); err != nil {
			return err
		}
		raw, err := json.MarshalIndent(template, "", "  ")
		if err != nil {
			return err
		}
		if err := writeFileAtomic(manifestPath, raw); err != nil {
			return err
		}
	}
	shape.WarmTemplate = &template
	q.cfg.Classes[class] = shape
	q.cfg.Logger.Info("postflight.hostd.warm_template.ready", "class", class, "key", key, "snapshot", template.RootSnapshot)
	return nil
}

func rootOwnedTemplatePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || info.Mode().Perm()&0o022 != 0 ||
		(directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return fmt.Errorf("vm: template path must be root-owned, immutable to other users, and not a symlink: %s", path)
	}
	return nil
}

func (q *QEMU) templateKey(shape ClassConfig) (string, error) {
	hash := sha256.New()
	for _, path := range []string{q.cfg.QEMUPath, q.cfg.Firmware, "/proc/sys/kernel/random/boot_id"} {
		digest, err := fileDigest(path)
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintln(hash, digest)
	}
	_, _ = fmt.Fprintf(hash, "turbo-warm-v2\n%s\n%d\n%d\n%s\n%s\n%s\n", shape.Image, shape.CPUs, shape.MemoryMiB, TurboMachineType, TurboCPUModel, q.cfg.GuestNetwork)
	return hex.EncodeToString(hash.Sum(nil))[:24], nil
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func templateDonorEligible(record meta, status Status) bool {
	return record.MemberID == "" && record.Assignment == nil && record.AssignmentID == "" &&
		record.WorkspaceMountpoint == "" && record.ToolMountpoint == "" && record.ProcessMountpoint == "" &&
		!record.Restored && status.Phase == PhaseWarm && status.MemberID == "" && status.Assignment.RequestID == ""
}

func (q *QEMU) buildWarmTemplate(ctx context.Context, class Class, template *WarmTemplate) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	id := ID("template-" + template.Key)
	// A previous interrupted build has no member and is never adopted into
	// production. Destroy only this deterministic, template-owned donor.
	if record, err := q.readMeta(id); err == nil {
		if record.MemberID != "" || record.Assignment != nil || record.AssignmentID != "" {
			return errors.New("vm: template donor unexpectedly carries assignment state")
		}
		if err := q.Destroy(ctx, id); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := q.Launch(ctx, id, class); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = q.Destroy(cleanup, id)
	}()
	for {
		status, err := q.Status(ctx, id)
		if err != nil {
			return err
		}
		if status.Phase == PhaseWarm {
			record, err := q.readMeta(id)
			if err != nil || !templateDonorEligible(record, status) {
				return errors.New("vm: refusing a credential-bearing template donor")
			}
			break
		}
		if status.Phase != PhaseBooting {
			return fmt.Errorf("vm: template donor entered %s: %s", status.Phase, status.FailureReason)
		}
		if err := templatePoll(ctx); err != nil {
			return err
		}
	}
	client, err := dialQMP(ctx, qmpSocketPath(q.stateDir(id)))
	if err != nil {
		return err
	}
	defer client.Close()
	if _, err := client.Execute(ctx, "stop", nil); err != nil {
		return err
	}
	socketPath, err := q.migrationSocket(id)
	if err != nil {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := migrationSocketAccess(socketPath); err != nil {
		return err
	}
	partial := template.MemoryPath + ".partial"
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	copied := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			deadline, _ := ctx.Deadline()
			_ = conn.SetDeadline(deadline)
			_, err = io.Copy(file, conn)
		}
		copied <- err
	}()
	if _, err := client.Execute(ctx, "migrate", map[string]any{"uri": "unix:" + socketPath}); err != nil {
		return err
	}
	if err := waitMigration(ctx, client); err != nil {
		return err
	}
	select {
	case err := <-copied:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	record, err := q.readMeta(id)
	if err != nil {
		return err
	}
	if err := q.cfg.Launcher.Kill(ctx, id, q.stateDir(id), record.Argv); err != nil {
		return err
	}
	target := strings.TrimSuffix(template.RootSnapshot, "@warm")
	if err := runTemplateZFS(ctx, "create", "-p", q.cfg.DatasetRoot+"/templates"); err != nil {
		// Existing parent is normal. The read distinguishes it from a real
		// namespace or permission failure without ignoring that failure.
		if err := runTemplateZFS(ctx, "list", "-H", "-o", "name", q.cfg.DatasetRoot+"/templates"); err != nil {
			return err
		}
	}
	// An interrupted, never-published build is disposable.
	if err := runTemplateZFS(ctx, "list", "-H", "-o", "name", target); err == nil {
		if err := runTemplateZFS(ctx, "destroy", "-r", target); err != nil {
			return err
		}
	}
	if err := runTemplateZFS(ctx, "rename", record.RootDataset, target); err != nil {
		return err
	}
	if err := runTemplateZFS(ctx, "snapshot", template.RootSnapshot); err != nil {
		return err
	}
	if err := os.Rename(partial, template.MemoryPath); err != nil {
		return err
	}
	template.MemorySHA256, err = fileDigest(template.MemoryPath)
	return err
}

func (q *QEMU) migrationSocket(id ID) (string, error) {
	directory := filepath.Join(q.stateDir(id), "migration")
	if err := os.MkdirAll(directory, 0o770); err != nil {
		return "", err
	}
	account, err := user.Lookup("postflight-vm")
	if err != nil {
		return "", err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil || gid <= 0 {
		return "", errors.New("vm: invalid QEMU group")
	}
	if err := os.Chown(directory, 0, gid); err != nil {
		return "", err
	}
	if err := os.Chmod(directory, 0o770); err != nil {
		return "", err
	}
	return filepath.Join(directory, "io.sock"), nil
}

func migrationSocketAccess(path string) error {
	account, err := user.Lookup("postflight-vm")
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}
	if err := os.Chown(path, 0, gid); err != nil {
		return err
	}
	return os.Chmod(path, 0o660)
}

func (q *QEMU) restoreTemplate(ctx context.Context, id ID, record meta, template WarmTemplate) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// The launcher proves exec/liveness, not that QMP finished binding.
	var client *qmpClient
	for {
		var err error
		client, err = dialQMP(ctx, qmpSocketPath(q.stateDir(id)))
		if err == nil {
			break
		}
		if err := templatePoll(ctx); err != nil {
			return err
		}
	}
	defer client.Close()
	path, err := q.migrationSocket(id)
	if err != nil {
		return err
	}
	if _, err := client.Execute(ctx, "migrate-incoming", map[string]any{"uri": "unix:" + path}); err != nil {
		return err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	file, err := os.Open(template.MemoryPath)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.Copy(conn, file); err != nil {
		return err
	}
	if unixConn, ok := conn.(*net.UnixConn); ok {
		_ = unixConn.CloseWrite()
	}
	if err := waitMigration(ctx, client); err != nil {
		return err
	}
	// The migrated NIC contains the donor's MAC and DHCP state. Keep its
	// link down until guestd acknowledges replacing both, before registration.
	if _, err := client.Execute(ctx, "set_link", map[string]any{"name": "nic0", "up": false}); err != nil {
		return err
	}
	if _, err := client.Execute(ctx, "cont", nil); err != nil {
		return err
	}
	var seed [32]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return err
	}
	_, mac := tapIdentity(id, record.CID)
	request := guestproto.Prepare{
		InitializeOnly: true, Restored: true, MemberID: record.Incarnation,
		HostUnixNS: time.Now().UnixNano(), Entropy: hex.EncodeToString(seed[:]), MACAddress: mac,
	}
	for {
		observeCtx, cancel := context.WithTimeout(ctx, time.Second)
		observation, err := q.cfg.Guest.Observe(observeCtx, id, record.CID)
		cancel()
		if err == nil && observation.Hello {
			break
		}
		if err := templatePoll(ctx); err != nil {
			return err
		}
	}
	if err := q.cfg.Guest.Prepare(ctx, id, record.CID, request); err != nil {
		return err
	}
	linkReleased := false
	for {
		observation, err := q.cfg.Guest.Observe(ctx, id, record.CID)
		if err != nil {
			return err
		}
		if observation.RunnerExited {
			return errors.New("vm: restored guest initialization failed")
		}
		if observation.NetworkIdentityReady && !linkReleased {
			if _, err := client.Execute(ctx, "set_link", map[string]any{"name": "nic0", "up": true}); err != nil {
				return err
			}
			linkReleased = true
		}
		if observation.Initialized && linkReleased {
			return nil
		}
		if err := templatePoll(ctx); err != nil {
			return err
		}
	}
}

func waitMigration(ctx context.Context, client *qmpClient) error {
	for {
		raw, err := client.Execute(ctx, "query-migrate", nil)
		if err != nil {
			return err
		}
		var result struct {
			Status string `json:"status"`
			Error  string `json:"error-desc"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return err
		}
		switch result.Status {
		case "completed":
			return nil
		case "failed", "cancelled":
			return fmt.Errorf("vm: migration %s: %s", result.Status, result.Error)
		}
		if err := templatePoll(ctx); err != nil {
			return err
		}
	}
}

func templatePoll(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(50 * time.Millisecond):
		return nil
	}
}

func runTemplateZFS(ctx context.Context, args ...string) error {
	output, err := exec.CommandContext(ctx, "zfs", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("vm: zfs %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(output)), err)
	}
	return nil
}
