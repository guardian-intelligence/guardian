package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

// validateDesired guards the sync boundary: a desired lease the control
// plane sends must be internally coherent and name only zfs-safe
// identifiers before the agent acts on it.
func validateDesired(d syncproto.DesiredLease) error {
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
	case syncproto.DesiredRun, syncproto.DesiredSeal, syncproto.DesiredCancel:
	default:
		return fmt.Errorf("lease %s: unknown desired state %q", d.LeaseID, d.State)
	}
	if d.State == syncproto.DesiredSeal && d.SealGeneration == "" {
		return fmt.Errorf("lease %s: seal without a generation", d.LeaseID)
	}
	if d.State == syncproto.DesiredRun {
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
func (a *Agent) buildReport(ctx context.Context) (syncproto.SyncRequest, error) {
	generations, workspaces, err := a.zvols.Inventory(ctx)
	if err != nil {
		return syncproto.SyncRequest{}, fmt.Errorf("inventory: %w", err)
	}
	vms, err := a.vms.List(ctx)
	if err != nil {
		return syncproto.SyncRequest{}, fmt.Errorf("listing vms: %w", err)
	}

	request := syncproto.SyncRequest{HostID: a.cfg.HostID, BootID: a.bootID}
	for _, g := range generations {
		request.Generations = append(request.Generations, syncproto.GenerationReport{
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

	occupancy := map[vm.Class]*syncproto.SlotReport{}
	for class, total := range a.cfg.Slots {
		occupancy[class] = &syncproto.SlotReport{Class: string(class), Total: total}
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
func (a *Agent) applyDesired(response syncproto.SyncResponse) {
	now := a.now()
	desired := make(map[string]syncproto.DesiredLease, len(response.Leases))
	quarantined := map[string]bool{}
	for _, d := range response.Leases {
		if err := validateDesired(d); err != nil {
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
		record.enter(syncproto.StatePending, now)
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
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.ControlPlaneOrigin+syncproto.SyncPath, bytes.NewReader(body))
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

	var response syncproto.SyncResponse
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
	maxSyncResponseBytes = 4 << 20
	maxPollAfter         = 30 * time.Second
	minPollAfter         = 250 * time.Millisecond
)
