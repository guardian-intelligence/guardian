package agent

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

type jobPlanResolveError struct {
	status int
	detail string
}

func (e *jobPlanResolveError) Error() string {
	return fmt.Sprintf("job-plan resolver returned %d: %s", e.status, e.detail)
}

func validateJobPlan(plan syncproto.JobPlan) error {
	if err := zvol.ValidateName("plan", plan.PlanID); err != nil {
		return err
	}
	if plan.ExecutionID == "" || plan.AttemptID == "" || plan.CheckRunID <= 0 ||
		plan.RunID == "" || plan.RunAttempt <= 0 || plan.JobDisplayName == "" ||
		plan.InstallationID <= 0 || plan.RepositoryID <= 0 || plan.RepositoryFullName == "" || plan.RunnerClass == "" {
		return errors.New("job plan identity is incomplete")
	}
	if plan.Process.ExpectedDigest != "" || plan.Process.ExpectedVersion != "" || plan.Process.Generation != "" {
		if plan.Process.ExpectedDigest == "" || plan.Process.ExpectedVersion == "" || plan.Process.Generation == "" {
			return errors.New("job plan process snapshot identity is incomplete")
		}
	}
	return nil
}

func (a *Agent) replaceJobPlans(ctx context.Context, snapshot syncproto.JobPlanSnapshot) error {
	if len(snapshot.Cursor) != 64 {
		a.metrics.RejectedJobPlans.Add(1)
		return errors.New("job-plan snapshot cursor is invalid")
	}
	if _, err := hex.DecodeString(snapshot.Cursor); err != nil {
		a.metrics.RejectedJobPlans.Add(1)
		return errors.New("job-plan snapshot cursor is invalid")
	}
	plans := make(map[int64][]syncproto.JobPlan, len(snapshot.Plans))
	for _, plan := range snapshot.Plans {
		if err := validateJobPlan(plan); err != nil {
			a.metrics.RejectedJobPlans.Add(1)
			a.logger.Error("rejecting job plan", "plan_id", plan.PlanID, "err", err)
			continue
		}
		plans[plan.CheckRunID] = append(plans[plan.CheckRunID], plan)
	}
	a.planMu.Lock()
	a.jobPlans = plans
	a.planCursor = snapshot.Cursor
	a.planMu.Unlock()
	a.retryDeferredUpdates(ctx)
	return nil
}

func (a *Agent) jobPlanFor(status vm.Status) (syncproto.JobPlan, bool) {
	assignment := status.Assignment
	a.planMu.RLock()
	candidates := append([]syncproto.JobPlan(nil), a.jobPlans[assignment.CheckRunID]...)
	a.planMu.RUnlock()
	var matched syncproto.JobPlan
	for _, plan := range candidates {
		if !jobPlanMatchesStatus(plan, status) {
			continue
		}
		if matched.PlanID != "" {
			return syncproto.JobPlan{}, false
		}
		matched = plan
	}
	return matched, matched.PlanID != ""
}

func jobPlanMatchesStatus(plan syncproto.JobPlan, status vm.Status) bool {
	assignment := status.Assignment
	return plan.CheckRunID == assignment.CheckRunID && plan.RunnerClass == string(status.Class) &&
		plan.RunID == assignment.Identity.RunID && plan.RunAttempt == assignment.Identity.RunAttempt &&
		plan.RepositoryFullName == assignment.Identity.Repository && plan.JobDisplayName == assignment.JobDisplayName
}

func (a *Agent) resolveJobPlan(ctx context.Context, status vm.Status) (syncproto.JobPlan, error) {
	endpoint, err := url.JoinPath(a.cfg.ControlPlaneOrigin, syncproto.JobPlanResolvePath)
	if err != nil {
		return syncproto.JobPlan{}, err
	}
	body, err := json.Marshal(syncproto.JobPlanResolveRequest{
		HostID: a.cfg.HostID, BootID: a.bootID, MemberID: status.Incarnation,
		VMID: string(status.ID), Assignment: *observedAssignment(status.Assignment),
	})
	if err != nil {
		return syncproto.JobPlan{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return syncproto.JobPlan{}, err
	}
	request.Header.Set("Authorization", "Bearer "+a.credential)
	request.Header.Set("Content-Type", "application/json")
	response, err := a.httpClient.Do(request)
	if err != nil {
		return syncproto.JobPlan{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return syncproto.JobPlan{}, &jobPlanResolveError{status: response.StatusCode, detail: string(detail)}
	}
	var resolved syncproto.JobPlanResolveResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&resolved); err != nil {
		return syncproto.JobPlan{}, err
	}
	if err := validateJobPlan(resolved.Plan); err != nil {
		a.metrics.RejectedJobPlans.Add(1)
		return syncproto.JobPlan{}, fmt.Errorf("resolved job plan is invalid: %w", err)
	}
	if !jobPlanMatchesStatus(resolved.Plan, status) {
		a.metrics.RejectedJobPlans.Add(1)
		return syncproto.JobPlan{}, errors.New("resolved job plan differs from the local assignment")
	}
	return resolved.Plan, nil
}

func desiredFromPlan(plan syncproto.JobPlan, status vm.Status) syncproto.DesiredAssignment {
	assignment := status.Assignment
	return syncproto.DesiredAssignment{
		AssignmentID: plan.PlanID, MemberID: status.Incarnation,
		RequestID: assignment.RequestID, JobID: assignment.JobID,
		CheckRunID: assignment.CheckRunID, State: syncproto.DesiredAssignmentRun,
		ExecutionID: plan.ExecutionID, AttemptID: plan.AttemptID, OrgID: plan.OrgID,
		InstallationID: plan.InstallationID, RepositoryID: plan.RepositoryID,
		RepositoryFullName: plan.RepositoryFullName, RunnerClass: plan.RunnerClass,
		Identity: syncproto.JobIdentity{
			RunID: assignment.Identity.RunID, RunAttempt: assignment.Identity.RunAttempt,
			RunnerName: assignment.Identity.RunnerName, Repository: assignment.Identity.Repository,
			WorkflowJob: assignment.Identity.WorkflowJob,
		},
		Workspace: plan.Workspace, Tool: plan.Tool, Process: plan.Process,
	}
}

func (a *Agent) watchJobPlans(ctx context.Context) {
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		if err := a.fetchJobPlans(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			a.metrics.JobPlanWatchFailures.Add(1)
			a.logger.Error("job-plan subscription", "err", err)
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if backoff < 5*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 100 * time.Millisecond
	}
}

func (a *Agent) fetchJobPlans(ctx context.Context) error {
	a.planMu.RLock()
	cursor := a.planCursor
	a.planMu.RUnlock()
	endpoint, err := url.JoinPath(a.cfg.ControlPlaneOrigin, syncproto.JobPlanPath)
	if err != nil {
		return err
	}
	query := url.Values{"host_id": []string{a.cfg.HostID}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+a.credential)
	response, err := a.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("control plane returned %s: %s", response.Status, detail)
	}
	var snapshot syncproto.JobPlanSnapshot
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&snapshot); err != nil {
		return err
	}
	if err := a.replaceJobPlans(ctx, snapshot); err != nil {
		return err
	}
	a.logger.Info("postflight.hostd.job_plans.updated", "cursor", snapshot.Cursor, "plans", len(snapshot.Plans))
	return nil
}

func (a *Agent) deferVMUpdate(id vm.ID) {
	a.deferredUpdateMu.Lock()
	a.deferredUpdates[id] = struct{}{}
	a.deferredUpdateMu.Unlock()
}

func (a *Agent) clearDeferredUpdate(id vm.ID) {
	a.deferredUpdateMu.Lock()
	delete(a.deferredUpdates, id)
	a.deferredUpdateMu.Unlock()
}

func (a *Agent) retryDeferredUpdates(ctx context.Context) {
	a.deferredUpdateMu.Lock()
	ids := make([]vm.ID, 0, len(a.deferredUpdates))
	for id := range a.deferredUpdates {
		ids = append(ids, id)
	}
	a.deferredUpdateMu.Unlock()
	for _, id := range ids {
		a.dispatchVMUpdate(ctx, id)
	}
}
