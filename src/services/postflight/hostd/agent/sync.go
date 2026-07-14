package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

// The sync exchange is the single wire contract between hostd and the
// control plane. It is level-triggered in both directions: the request
// carries this host's full observed state, the response carries the full
// desired state for this host. Either side can restart at any point and the
// next exchange converges. The control plane acknowledges a terminal lease
// by omitting it from the next response, which licenses hostd to forget it
// and reclaim its resources.

// SyncRequest is what hostd reports.
type SyncRequest struct {
	HostID string `json:"host_id"`
	// BootID changes when hostd restarts, so the control plane can tell a
	// fresh process from a silent one.
	BootID string        `json:"boot_id"`
	Slots  []SlotReport  `json:"slots"`
	Leases []LeaseReport `json:"leases"`
	// Generations is the observed inventory of sealed generations resident
	// on this host — the hints-vs-truth channel for the catalog.
	Generations []GenerationReport `json:"generations"`
	// Workspaces lists lease workspace volumes present on disk, so the
	// control plane can spot orphans hostd's own GC missed.
	Workspaces []string `json:"workspaces"`
}

// SlotReport is per-class capacity: fixed totals from provisioning, and the
// current occupancy split.
type SlotReport struct {
	Class string `json:"class"`
	Total int    `json:"total"`
	Warm  int    `json:"warm"`
	Used  int    `json:"used"`
}

// GenerationReport is one resident generation.
type GenerationReport struct {
	Generation string `json:"generation"`
	Bytes      int64  `json:"bytes"`
}

// SyncResponse is the control plane's full desired state for this host.
//
// Full state cuts both ways: an authenticated response with zero leases
// means "cancel everything on this host", by design — there is no separate
// drain verb. The BootID echo is the guard that confines that power to
// responses actually computed for this request: a stale, misrouted, or
// default-constructed response fails the echo and is dropped whole.
type SyncResponse struct {
	// BootID must echo the request's boot_id; syncOnce drops the response
	// otherwise.
	BootID string         `json:"boot_id"`
	Leases []DesiredLease `json:"leases"`
	// Reap names generations to destroy. Reaping is exclusively a
	// control-plane decision: node-local generations are the only copy.
	Reap []string `json:"reap"`
	// PoolTargets is the desired warm-VM count per class.
	PoolTargets map[string]int `json:"pool_targets"`
	// PollAfterMillis suggests when to sync next; 0 means the default.
	PollAfterMillis int `json:"poll_after_millis"`
}

// DesiredState is what the control plane wants done with a lease.
type DesiredState string

const (
	// DesiredRun: bring the lease to a running runner and report its exit.
	DesiredRun DesiredState = "run"
	// DesiredSeal: the exited workspace should be sealed as a generation.
	DesiredSeal DesiredState = "seal"
	// DesiredCancel: withdraw the lease; destroy its VM.
	DesiredCancel DesiredState = "cancel"
)

// DesiredLease is one lease as the control plane wants it on this host.
type DesiredLease struct {
	LeaseID string       `json:"lease_id"`
	State   DesiredState `json:"state"`

	// Identity, forwarded into the checkout endpoint's lease table.
	ExecutionID        string `json:"execution_id"`
	AttemptID          string `json:"attempt_id"`
	OrgID              string `json:"org_id"`
	InstallationID     int64  `json:"installation_id"`
	RepositoryID       int64  `json:"repository_id"`
	RepositoryFullName string `json:"repository_full_name"`

	RunnerClass string `json:"runner_class"`
	// JITConfig is the encoded single-use runner registration blob, minted
	// by the control plane.
	JITConfig string `json:"jit_config"`

	Workspace WorkspaceSpec `json:"workspace"`
	// SealGeneration names the generation a seal must produce; set when
	// State is DesiredSeal.
	SealGeneration string `json:"seal_generation,omitempty"`
}

// WorkspaceSpec says how to materialize the workspace volume.
type WorkspaceSpec struct {
	// Generation to clone from; empty means a cache miss, which
	// materializes an empty volume — never an error.
	Generation string `json:"generation,omitempty"`
	// SizeBytes for an empty volume; ignored for clones.
	SizeBytes int64 `json:"size_bytes,omitempty"`
}

func (d DesiredLease) validate() error {
	if d.LeaseID == "" {
		return fmt.Errorf("lease without an id")
	}
	if err := zvol.ValidateName("lease", d.LeaseID); err != nil {
		return err
	}
	if d.Workspace.Generation != "" {
		if err := zvol.ValidateName("generation", d.Workspace.Generation); err != nil {
			return err
		}
	}
	switch d.State {
	case DesiredRun, DesiredSeal, DesiredCancel:
	default:
		return fmt.Errorf("lease %s: unknown desired state %q", d.LeaseID, d.State)
	}
	if d.State == DesiredSeal && d.SealGeneration == "" {
		return fmt.Errorf("lease %s: seal without a generation", d.LeaseID)
	}
	if d.State == DesiredRun {
		if d.ExecutionID == "" || d.AttemptID == "" {
			return fmt.Errorf("lease %s: missing execution identity", d.LeaseID)
		}
		if d.RunnerClass == "" || d.JITConfig == "" {
			return fmt.Errorf("lease %s: missing runner class or jit config", d.LeaseID)
		}
		if d.RepositoryFullName == "" {
			return fmt.Errorf("lease %s: missing repository", d.LeaseID)
		}
	}
	return nil
}

// buildReport assembles the sync request from current state. Callers hold
// the agent lock.
func (a *Agent) buildReport(ctx context.Context) (SyncRequest, error) {
	generations, workspaces, err := a.zvols.Inventory(ctx)
	if err != nil {
		return SyncRequest{}, fmt.Errorf("inventory: %w", err)
	}
	vms, err := a.vms.List(ctx)
	if err != nil {
		return SyncRequest{}, fmt.Errorf("listing vms: %w", err)
	}

	request := SyncRequest{HostID: a.cfg.HostID, BootID: a.bootID}
	for _, g := range generations {
		request.Generations = append(request.Generations, GenerationReport{
			Generation: string(g.Generation),
			Bytes:      g.Bytes,
		})
	}
	for _, w := range workspaces {
		request.Workspaces = append(request.Workspaces, w.Name)
	}
	for id := range a.leases {
		request.Leases = append(request.Leases, a.leases[id].report())
	}

	occupancy := map[vm.Class]*SlotReport{}
	for class, total := range a.cfg.Slots {
		occupancy[class] = &SlotReport{Class: string(class), Total: total}
	}
	for _, status := range vms {
		slot, ok := occupancy[status.Class]
		if !ok {
			continue
		}
		switch status.Phase {
		case vm.PhaseBooting, vm.PhaseWarm:
			slot.Warm++
		case vm.PhaseAssigned, vm.PhaseReady, vm.PhaseExited:
			slot.Used++
		}
	}
	for _, slot := range occupancy {
		request.Slots = append(request.Slots, *slot)
	}
	return request, nil
}

// applyDesired ingests a sync response. Invalid leases are skipped and
// counted, never partially applied. Callers hold the agent lock.
func (a *Agent) applyDesired(response SyncResponse) {
	now := a.now()
	desired := make(map[string]DesiredLease, len(response.Leases))
	quarantined := map[string]bool{}
	for _, d := range response.Leases {
		if err := d.validate(); err != nil {
			// The control plane named this lease; we could not understand
			// the spec (version skew is the realistic cause). Quarantine
			// its ID so GC never mistakes a lease the control plane still
			// wants for an orphan and destroys live customer state — a
			// rejected input must never escalate to destruction.
			a.logger.Error("rejecting desired lease", "err", err)
			a.metrics.RejectedLeases.Add(1)
			if d.LeaseID != "" {
				quarantined[d.LeaseID] = true
			}
			continue
		}
		desired[d.LeaseID] = d
		if existing, ok := a.leases[d.LeaseID]; ok {
			existing.spec = d
			if a.quarantined[d.LeaseID] {
				// Quarantine froze the lifecycle; the time it consumed must
				// not count against the state deadline, or the first
				// parseable sync would execute a stale deadline against a
				// healthy job.
				existing.since = now
			}
			continue
		}
		record := &lease{spec: d}
		record.enter(StatePending, now)
		a.leases[d.LeaseID] = record
	}
	a.desired = desired
	a.quarantined = quarantined

	a.reap = a.reap[:0]
	for _, generation := range response.Reap {
		if err := zvol.ValidateName("generation", generation); err != nil {
			a.logger.Error("rejecting reap verb", "err", err)
			continue
		}
		a.reap = append(a.reap, zvol.GenerationID(generation))
	}

	targets := make(map[vm.Class]int, len(response.PoolTargets))
	for class, count := range response.PoolTargets {
		if count < 0 {
			continue
		}
		targets[vm.Class(class)] = count
	}
	a.poolTargets = targets
	a.synced = true
}

// syncOnce performs one report/desire exchange with the control plane.
func (a *Agent) syncOnce(ctx context.Context) (time.Duration, error) {
	a.mu.Lock()
	request, err := a.buildReport(ctx)
	a.mu.Unlock()
	if err != nil {
		return 0, err
	}

	body, err := json.Marshal(request)
	if err != nil {
		return 0, fmt.Errorf("encoding sync request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.ControlPlaneOrigin+syncPath, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+a.credential)
	httpRequest.Header.Set("Content-Type", "application/json")

	httpResponse, err := a.httpClient.Do(httpRequest)
	if err != nil {
		return 0, fmt.Errorf("sync: %w", err)
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(httpResponse.Body, 4096))
		return 0, fmt.Errorf("sync: control plane returned %d", httpResponse.StatusCode)
	}

	var response SyncResponse
	decoder := json.NewDecoder(io.LimitReader(httpResponse.Body, maxSyncResponseBytes))
	if err := decoder.Decode(&response); err != nil {
		return 0, fmt.Errorf("decoding sync response: %w", err)
	}
	if response.BootID != a.bootID {
		// A full-state response with zero leases cancels every job on this
		// host, so a response that was not computed for this exact request
		// must never be applied.
		return 0, fmt.Errorf("sync: response boot_id %q does not echo %q", response.BootID, a.bootID)
	}

	a.mu.Lock()
	a.applyDesired(response)
	a.mu.Unlock()

	if response.PollAfterMillis > 0 {
		// Clamp both ways: a bad or hostile control-plane value must neither
		// stall the tick loop long enough to starve deadline enforcement nor
		// spin sync exchanges at machine speed.
		poll := time.Duration(response.PollAfterMillis) * time.Millisecond
		if poll > maxPollAfter {
			poll = maxPollAfter
		}
		if poll < minPollAfter {
			poll = minPollAfter
		}
		return poll, nil
	}
	return 0, nil
}

const (
	syncPath             = "/api/v1/hostd/sync"
	maxSyncResponseBytes = 4 << 20
	maxPollAfter         = 30 * time.Second
	minPollAfter         = 250 * time.Millisecond
)
