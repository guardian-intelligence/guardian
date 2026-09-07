package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/guardian-intelligence/guardian/src/postflight/hostd/vm"
)

var maintenanceToken = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ProcessIdentity binds an acknowledgement to this Linux process, including
// its boot ID and start ticks so PID reuse cannot authorize a stale file.
func ProcessIdentity() (string, error) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data)[strings.LastIndex(string(data), ")")+1:])
	if len(fields) < 20 {
		return "", fmt.Errorf("invalid process identity")
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%d:%s", strings.TrimSpace(string(bootID)), os.Getpid(), fields[19]), nil
}

func (a *Agent) drainRequest() (string, bool) {
	if a.cfg.MaintenanceDir == "" {
		return "", false
	}
	path := filepath.Join(a.cfg.MaintenanceDir, "request.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", false
	}
	// An unreadable or invalid request closes admission, but cannot mint an
	// acknowledgement that authorizes an installer to stop this process.
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 ||
		int(info.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() || info.Size() > 4096 {
		return "", true
	}
	data, err := os.ReadFile(path)
	var request struct {
		Protocol int    `json:"protocol"`
		Token    string `json:"token"`
	}
	if err != nil || json.Unmarshal(data, &request) != nil || request.Protocol != 1 || !maintenanceToken.MatchString(request.Token) {
		return "", true
	}
	return request.Token, true
}

// Registered listeners can receive a job independently of a hostd tick. Only
// never-prepared VMs are safe to retire locally; listeners and acquired jobs
// finish their single use. There is deliberately no forced drain timeout.
func (a *Agent) drainPool(ctx context.Context, view *vmView) {
	for id, status := range view.byID {
		if status.MemberID != "" || status.Assignment.RequestID != "" ||
			(status.Phase != vm.PhaseBooting && status.Phase != vm.PhaseWarm) {
			continue
		}
		if err := a.vms.Destroy(ctx, id); err != nil {
			a.logger.Error("retiring unregistered vm for maintenance", "vm", id, "err", err)
		}
	}
}

func (a *Agent) acknowledgeDrain(ctx context.Context, token string) {
	if token == "" {
		return
	}
	statuses, err := a.vms.List(ctx)
	if err != nil {
		return
	}
	a.mu.Lock()
	assignments := len(a.assignments)
	a.mu.Unlock()
	state := struct {
		Protocol        int    `json:"protocol"`
		Token           string `json:"token"`
		ProcessIdentity string `json:"process_identity"`
		Drained         bool   `json:"drained"`
		VMs             int    `json:"vms"`
		Assignments     int    `json:"assignments"`
	}{1, token, a.cfg.MaintenanceProcessIdentity, len(statuses) == 0 && assignments == 0, len(statuses), assignments}
	data, _ := json.Marshal(state)
	file, err := os.CreateTemp(a.cfg.MaintenanceDir, ".state-*")
	if err != nil {
		return
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil && closeErr == nil {
		if err := os.Rename(file.Name(), filepath.Join(a.cfg.MaintenanceDir, "state.json")); err == nil {
			a.mu.Lock()
			changed := a.maintenanceState != string(data)
			a.maintenanceState = string(data)
			a.mu.Unlock()
			if changed {
				a.logger.Info("postflight.hostd.maintenance", "request", token,
					"drained", state.Drained, "vms", state.VMs, "assignments", state.Assignments)
			}
		}
	}
}
