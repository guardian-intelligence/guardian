package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	// A delivery stuck in 'processing' this long was orphaned by a crashed
	// worker and is reclaimed.
	deliveryProcessingStaleAfter = 2 * time.Minute
	// Stage (a) has no capacity path, so a healthy queued demand stays
	// 'demand_recorded' until GitHub moves the job — without a floor the
	// reconciler would re-read every queued job from the API every tick.
	// Each reconcile re-persists the job row (bumping updated_at), so this
	// self-throttles to one API read per queued job per period.
	reconcileQuietPeriod = 30 * time.Second
)

// retryDelay is the delivery backoff: 1s << (attempt-1), capped at 32s.
func retryDelay(attempt int32) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<uint(attempt-1)) * time.Second
}

// ignoredError marks a delivery ignored (recorded with its problem, never
// retried): unsupported events, mismatched installations.
type ignoredError struct{ p problem }

func (e ignoredError) Error() string { return e.p.Code + ": " + e.p.Detail }

// terminalError fails a delivery immediately (no retries): unfixable
// payloads.
type terminalError struct{ p problem }

func (e terminalError) Error() string { return e.p.Code + ": " + e.p.Detail }

// workflowJobPayload is the normalized workflow_job shape shared by webhook
// payloads, API reads, and the reconciler's synthetic events.
type workflowJobPayload struct {
	ID           int64
	RunID        int64
	RunAttempt   int64
	Name         string
	Status       string
	Conclusion   string
	Labels       []string
	RunnerID     int64
	RunnerName   string
	HeadSHA      string
	HeadBranch   string
	WorkflowName string
	StartedAt    time.Time
	CompletedAt  time.Time
}

type jobEvent struct {
	Action             string
	InstallationID     int64
	RepositoryID       int64
	RepositoryFullName string
	Job                workflowJobPayload
}

// workflowJobWebhook is the wire shape of a workflow_job delivery.
type workflowJobWebhook struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
	WorkflowJob struct {
		ID           int64     `json:"id"`
		RunID        int64     `json:"run_id"`
		RunAttempt   int64     `json:"run_attempt"`
		Name         string    `json:"name"`
		Status       string    `json:"status"`
		Conclusion   string    `json:"conclusion"`
		Labels       []string  `json:"labels"`
		RunnerID     int64     `json:"runner_id"`
		RunnerName   string    `json:"runner_name"`
		HeadSHA      string    `json:"head_sha"`
		HeadBranch   string    `json:"head_branch"`
		WorkflowName string    `json:"workflow_name"`
		StartedAt    time.Time `json:"started_at"`
		CompletedAt  time.Time `json:"completed_at"`
	} `json:"workflow_job"`
}

type worker struct {
	st     *pgStore
	gh     *githubClient
	cfg    config
	tracer trace.Tracer
}

// run drives all sweeper loops in ONE goroutine, each tick in a fixed order:
// stale reclaim before ready processing (a crashed batch is retried within
// the same tick window), then the queued-jobs reconciler.
//
// FIXME(two-workers): a second replica is safe by construction (FOR UPDATE
// SKIP LOCKED + per-job advisory locks) but has never run; prove it before
// scaling past one.
func (w *worker) run(ctx context.Context) {
	// Tick work runs on a non-cancelable child: a SIGTERM between ticks stops
	// the loop, but an in-flight batch finishes its delivery transitions
	// (bounded by the GitHub client timeout) instead of leaving rows stuck in
	// 'processing' to burn an attempt and wait out the 2m stale reclaim on
	// every deploy.
	work := context.WithoutCancel(ctx)
	ticker := time.NewTicker(w.cfg.workerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		w.reclaimStale(work)
		w.processReady(work)
		w.reconcileQueued(work)
	}
}

func (w *worker) reclaimStale(ctx context.Context) {
	stale, err := w.st.ListStaleDeliveries(ctx, w.cfg.workerBatchSize, deliveryProcessingStaleAfter)
	if err != nil {
		slog.Error("worker: list stale deliveries", "err", err)
		return
	}
	for _, d := range stale {
		attrs := eventAttrs{DeliveryID: d.DeliveryID, JobID: d.ProviderJobID}
		if d.AttemptCount >= w.cfg.maxDeliveryTries {
			probs := []problem{problemProcessingStale(), problemAttemptsExhausted(d.AttemptCount)}
			if err := w.st.MarkDeliveryFailed(ctx, d.DeliveryID, probs); err != nil {
				slog.Error("worker: fail stale delivery", "delivery_id", d.DeliveryID, "err", err)
				continue
			}
			attrs.Result, attrs.Reason = "failed", "provider_webhook.processing_attempts_exhausted"
			emitEvent(ctx, evDeliveryFailed, attrs)
			w.terminalizeDeliveryFailure(ctx, d.EventName, d.ProviderJobID, d.DeliveryID, probs)
			continue
		}
		next := time.Now().Add(retryDelay(d.AttemptCount))
		if err := w.st.MarkDeliveryRetryable(ctx, d.DeliveryID, next, []problem{problemProcessingStale()}); err != nil {
			slog.Error("worker: reclaim stale delivery", "delivery_id", d.DeliveryID, "err", err)
			continue
		}
		attrs.Result, attrs.Reason = "retryable", "provider_webhook.processing_stale"
		emitEvent(ctx, evDeliveryRetryable, attrs)
	}
}

func (w *worker) processReady(ctx context.Context) {
	batch, err := w.st.LockReadyDeliveries(ctx, w.cfg.workerBatchSize)
	if err != nil {
		slog.Error("worker: lock ready deliveries", "err", err)
		return
	}
	for _, d := range batch {
		w.processDelivery(ctx, d)
	}
}

func (w *worker) processDelivery(ctx context.Context, d lockedDelivery) {
	ctx, span := w.tracer.Start(ctx, "delivery.process", trace.WithAttributes(
		attribute.String("delivery_id", d.DeliveryID),
		attribute.String("event", d.EventName),
		attribute.Int("attempt", int(d.AttemptCount)),
	))
	defer span.End()

	err := w.handleDelivery(ctx, d)
	attrs := eventAttrs{
		DeliveryID: d.DeliveryID,
		Repo:       d.RepositoryFullName,
		RunID:      d.ProviderRunID,
		RunAttempt: d.ProviderRunAttempt,
		JobID:      d.ProviderJobID,
	}
	var ign ignoredError
	var term terminalError
	switch {
	case err == nil:
		if serr := w.st.MarkDeliveryProcessed(ctx, d.DeliveryID); serr != nil {
			slog.Error("worker: mark processed", "delivery_id", d.DeliveryID, "err", serr)
			return
		}
		attrs.Result = "succeeded"
		emitEvent(ctx, evDeliveryProcessed, attrs)
	case errors.As(err, &ign):
		if serr := w.st.MarkDeliveryIgnored(ctx, d.DeliveryID, []problem{ign.p}); serr != nil {
			slog.Error("worker: mark ignored", "delivery_id", d.DeliveryID, "err", serr)
			return
		}
		attrs.Result, attrs.Reason = "ignored", ign.p.Code
		emitEvent(ctx, evDeliveryIgnored, attrs)
	case errors.As(err, &term):
		w.failDelivery(ctx, d, []problem{term.p}, attrs)
	case d.AttemptCount >= w.cfg.maxDeliveryTries:
		w.failDelivery(ctx, d, []problem{problemProcessingFailed(err), problemAttemptsExhausted(d.AttemptCount)}, attrs)
	default:
		next := time.Now().Add(retryDelay(d.AttemptCount))
		if serr := w.st.MarkDeliveryRetryable(ctx, d.DeliveryID, next, []problem{problemProcessingFailed(err)}); serr != nil {
			slog.Error("worker: mark retryable", "delivery_id", d.DeliveryID, "err", serr)
			return
		}
		attrs.Result, attrs.Reason = "retryable", err.Error()
		emitEvent(ctx, evDeliveryRetryable, attrs)
	}
}

func (w *worker) failDelivery(ctx context.Context, d lockedDelivery, probs []problem, attrs eventAttrs) {
	if serr := w.st.MarkDeliveryFailed(ctx, d.DeliveryID, probs); serr != nil {
		slog.Error("worker: mark failed", "delivery_id", d.DeliveryID, "err", serr)
		return
	}
	attrs.Result, attrs.Reason = "failed", probs[0].Code
	emitEvent(ctx, evDeliveryFailed, attrs)
	w.terminalizeDeliveryFailure(ctx, d.EventName, d.ProviderJobID, d.DeliveryID, probs)
}

// terminalizeDeliveryFailure: every terminal 'failed' on a workflow_job
// delivery also fails its demand (capacity_failed), so the sweeper never
// hot-loops on a job whose delivery is unprocessable.
func (w *worker) terminalizeDeliveryFailure(ctx context.Context, eventName string, providerJobID int64, deliveryID string, probs []problem) {
	if eventName != "workflow_job" || providerJobID == 0 {
		return
	}
	if err := w.st.MarkProviderDemandFailed(ctx, providerJobID, probs); err != nil {
		slog.Error("worker: terminalize demand", "delivery_id", deliveryID, "job_id", providerJobID, "err", err)
		return
	}
	emitEvent(ctx, evDemandFailed, eventAttrs{
		DeliveryID: deliveryID,
		JobID:      providerJobID,
		Result:     "failed",
		Reason:     probs[0].Code,
	})
}

func (w *worker) handleDelivery(ctx context.Context, d lockedDelivery) error {
	switch d.EventName {
	case "ping":
		var p struct {
			Zen string `json:"zen"`
		}
		_ = json.Unmarshal(d.PayloadJSON, &p)
		slog.Info("github ping", "delivery_id", d.DeliveryID, "zen", p.Zen)
		return nil
	case "workflow_job":
		return w.handleWorkflowJob(ctx, d)
	default:
		// Recorded-ignored, never dropped: the ledger is also the audit of
		// what GitHub sent that was deliberately not processed.
		return ignoredError{problemUnsupportedEvent(d.EventName)}
	}
}

// repoFullNameRe: owner/name — exactly one slash, GitHub's name charset on
// both sides.
var repoFullNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// validRepoFullName is the single choke point guarding every place
// repository.full_name is interpolated into a GitHub API path: the worker,
// the reconciler, and the commenter all operate on names this handler
// persisted. A signed-but-hostile payload (webhook-secret compromise) must
// not be able to steer installation-token requests at other API endpoints
// via "..", extra "/", "?" or "#" in the name.
func validRepoFullName(s string) bool {
	if !repoFullNameRe.MatchString(s) {
		return false
	}
	owner, name, _ := strings.Cut(s, "/")
	return owner != "." && owner != ".." && name != "." && name != ".."
}

func (w *worker) handleWorkflowJob(ctx context.Context, d lockedDelivery) error {
	var payload workflowJobWebhook
	if err := json.Unmarshal(d.PayloadJSON, &payload); err != nil {
		return terminalError{problemPayloadInvalid("workflow_job payload does not parse: " + err.Error())}
	}
	if payload.WorkflowJob.ID <= 0 || payload.Repository.ID <= 0 ||
		payload.Installation.ID <= 0 || payload.Repository.FullName == "" {
		return terminalError{problemPayloadInvalid("workflow_job envelope incomplete")}
	}
	if !validRepoFullName(payload.Repository.FullName) {
		return terminalError{problemPayloadInvalid("repository.full_name is not a valid owner/name")}
	}
	// FIXME(multi-tenant): postflight resolved installation+repository bindings
	// here (lookupRuntimeBinding); stage (a) verifies against the single
	// config-pinned installation instead.
	if payload.Installation.ID != w.cfg.installationID {
		return ignoredError{problemInstallationMismatch(payload.Installation.ID)}
	}

	ev := jobEvent{
		Action:             firstNonEmpty(payload.Action, d.Action),
		InstallationID:     payload.Installation.ID,
		RepositoryID:       payload.Repository.ID,
		RepositoryFullName: payload.Repository.FullName,
		Job: workflowJobPayload{
			ID:           payload.WorkflowJob.ID,
			RunID:        payload.WorkflowJob.RunID,
			RunAttempt:   payload.WorkflowJob.RunAttempt,
			Name:         payload.WorkflowJob.Name,
			Status:       payload.WorkflowJob.Status,
			Conclusion:   payload.WorkflowJob.Conclusion,
			Labels:       payload.WorkflowJob.Labels,
			RunnerID:     payload.WorkflowJob.RunnerID,
			RunnerName:   payload.WorkflowJob.RunnerName,
			HeadSHA:      payload.WorkflowJob.HeadSHA,
			HeadBranch:   payload.WorkflowJob.HeadBranch,
			WorkflowName: payload.WorkflowJob.WorkflowName,
			StartedAt:    payload.WorkflowJob.StartedAt,
			CompletedAt:  payload.WorkflowJob.CompletedAt,
		},
	}
	if ev.Job.RunAttempt == 0 {
		ev.Job.RunAttempt = 1 // absent run_attempt means first attempt
	}

	// Persist the webhook hint BEFORE the action switch: even ignored
	// actions leave provider evidence.
	class := runnerClassForLabels(ev.Job.Labels, w.cfg.runnerClassPrefix)
	if err := w.st.UpsertWorkflowJob(ctx, jobRowFrom(ev, class, nil)); err != nil {
		return fmt.Errorf("persist workflow job: %w", err)
	}

	switch ev.Action {
	case "queued":
		return w.submitQueuedJob(ctx, ev, d.DeliveryID)
	case "in_progress", "completed":
		return w.refreshRunAndJobs(ctx, ev, d.DeliveryID)
	default:
		return nil
	}
}

// submitQueuedJob is the shared queued path (webhook + reconciler). The
// payload is only a hint: the run and the exact job within the run attempt
// are re-read from the API before any demand is recorded, and the API
// version replaces the payload.
func (w *worker) submitQueuedJob(ctx context.Context, ev jobEvent, deliveryID string) error {
	run, err := w.gh.workflowRun(ctx, ev.RepositoryFullName, ev.Job.RunID)
	if err != nil {
		return fmt.Errorf("fetch workflow run: %w", err)
	}
	obs := runObservation{
		Event:                  run.Event,
		RepositoryFullName:     ev.RepositoryFullName,
		HeadRepositoryFullName: run.HeadRepository.FullName,
		HeadBranch:             run.HeadBranch,
		HeadSHA:                run.HeadSHA,
	}

	jobs, err := w.gh.workflowRunAttemptJobs(ctx, ev.RepositoryFullName, ev.Job.RunID, ev.Job.RunAttempt)
	if err != nil {
		return fmt.Errorf("fetch run attempt jobs: %w", err)
	}
	var apiJob *apiWorkflowJob
	for i := range jobs {
		if jobs[i].ID == ev.Job.ID {
			apiJob = &jobs[i]
			break
		}
	}
	if apiJob == nil {
		// API lag: the delivery retries with backoff until the job is listable.
		return fmt.Errorf("job %d not yet in attempt %d listing", ev.Job.ID, ev.Job.RunAttempt)
	}

	apiEv := jobEvent{
		Action:             ev.Action,
		InstallationID:     ev.InstallationID,
		RepositoryID:       ev.RepositoryID,
		RepositoryFullName: ev.RepositoryFullName,
		Job:                payloadFromAPIJob(*apiJob),
	}
	class := runnerClassForLabels(apiEv.Job.Labels, w.cfg.runnerClassPrefix)
	now := time.Now()
	if err := w.st.UpsertWorkflowJob(ctx, jobRowFrom(apiEv, class, &now)); err != nil {
		return fmt.Errorf("persist API job: %w", err)
	}

	// PR resolution is comment plumbing: best-effort, never blocks demand.
	prNumber, prBaseRef, prResolved := w.resolveAndStampPullRequest(ctx, ev.RepositoryFullName, ev.Job.RunID, run, &obs)

	attrs := eventAttrs{
		DeliveryID:  deliveryID,
		Repo:        ev.RepositoryFullName,
		RunID:       apiEv.Job.RunID,
		RunAttempt:  apiEv.Job.RunAttempt,
		JobID:       apiEv.Job.ID,
		RunnerClass: class,
	}
	if apiEv.Job.RunnerName != "" {
		if err := w.st.UpsertJobAssignment(ctx, jobAssignment{
			ProviderJobID: apiEv.Job.ID,
			RunnerName:    apiEv.Job.RunnerName,
			RunnerID:      apiEv.Job.RunnerID,
			DeliveryID:    deliveryID,
		}); err != nil {
			return fmt.Errorf("record assignment: %w", err)
		}
		a := attrs
		a.Result, a.Reason = "succeeded", "runner:"+apiEv.Job.RunnerName
		emitEvent(ctx, evAssignmentObserved, a)
	}

	markDirty := func() {
		if class == "" || !prResolved || prNumber <= 0 {
			return
		}
		if err := w.st.MarkPRCommentDirty(ctx, ev.RepositoryID, ev.RepositoryFullName, prNumber); err != nil {
			slog.Error("worker: mark comment dirty", "repo", ev.RepositoryFullName, "pr", prNumber, "err", err)
		}
	}

	if apiEv.Job.Status != "queued" {
		// API is truth: the demand path is abandoned; the API version of the
		// job already replaced the payload above.
		if apiEv.Job.Status == "completed" {
			a := attrs
			a.Result, a.Reason = "succeeded", apiEv.Job.Conclusion
			emitEvent(ctx, evJobTerminalObserved, a)
		}
		a := attrs
		a.Result, a.Reason = "ignored", "provider_status:"+apiEv.Job.Status
		emitEvent(ctx, evDemandIgnored, a)
		markDirty()
		return nil
	}
	if class == "" {
		a := attrs
		a.Result, a.Reason = "ignored", "github runner class is unresolved"
		emitEvent(ctx, evDemandIgnored, a)
		return nil
	}

	trust := trustClassForRun(obs)
	if _, err := w.st.EnsureProviderDemand(ctx, demandRow{
		ProviderJobID:        apiEv.Job.ID,
		ProviderRepositoryID: ev.RepositoryID,
		RepositoryFullName:   ev.RepositoryFullName,
		ProviderRunID:        apiEv.Job.RunID,
		ProviderRunAttempt:   apiEv.Job.RunAttempt,
		TrustClass:           trust,
		RunnerClass:          class,
		WorkspaceScopeID:     w.resolveWorkspaceScope(ctx, ev.RepositoryFullName, class, trust, run, apiEv.Job.Name, prBaseRef),
		LastDeliveryID:       deliveryID,
	}); err != nil {
		return fmt.Errorf("ensure demand: %w", err)
	}
	a := attrs
	a.Result = "succeeded"
	emitEvent(ctx, evDemandRecorded, a)
	markDirty()
	return nil
}

// refreshRunAndJobs is the in_progress/completed path: the payload's terminal
// claim is never trusted — run + ALL jobs of the exact attempt are re-read
// from the API and persisted as truth (observed_from_api_at set); only
// API-read completions produce terminal evidence.
func (w *worker) refreshRunAndJobs(ctx context.Context, ev jobEvent, deliveryID string) error {
	attrs := eventAttrs{
		DeliveryID: deliveryID,
		Repo:       ev.RepositoryFullName,
		RunID:      ev.Job.RunID,
		RunAttempt: ev.Job.RunAttempt,
	}
	a := attrs
	a.Result = "started"
	emitEvent(ctx, evRefreshStarted, a)

	run, err := w.gh.workflowRun(ctx, ev.RepositoryFullName, ev.Job.RunID)
	if err != nil {
		return fmt.Errorf("fetch workflow run: %w", err)
	}
	jobs, err := w.gh.workflowRunAttemptJobs(ctx, ev.RepositoryFullName, ev.Job.RunID, ev.Job.RunAttempt)
	if err != nil {
		return fmt.Errorf("fetch run attempt jobs: %w", err)
	}

	now := time.Now()
	ourClassSeen := false
	for _, aj := range jobs {
		p := payloadFromAPIJob(aj)
		class := runnerClassForLabels(p.Labels, w.cfg.runnerClassPrefix)
		if class != "" {
			ourClassSeen = true
		}
		row := jobRowFrom(jobEvent{
			Action:             ev.Action,
			InstallationID:     ev.InstallationID,
			RepositoryID:       ev.RepositoryID,
			RepositoryFullName: ev.RepositoryFullName,
			Job:                p,
		}, class, &now)
		if err := w.st.UpsertWorkflowJob(ctx, row); err != nil {
			return fmt.Errorf("persist API job %d: %w", p.ID, err)
		}
		ja := attrs
		ja.JobID, ja.RunnerClass = p.ID, class
		if p.RunnerName != "" {
			if err := w.st.UpsertJobAssignment(ctx, jobAssignment{
				ProviderJobID: p.ID,
				RunnerName:    p.RunnerName,
				RunnerID:      p.RunnerID,
				DeliveryID:    deliveryID,
			}); err != nil {
				return fmt.Errorf("record assignment: %w", err)
			}
			ja.Result, ja.Reason = "succeeded", "runner:"+p.RunnerName
			emitEvent(ctx, evAssignmentObserved, ja)
		}
		if p.Status == "completed" {
			ja.Result, ja.Reason = "succeeded", p.Conclusion
			emitEvent(ctx, evJobTerminalObserved, ja)
		}
	}

	prNumber, _, prResolved := w.resolveAndStampPullRequest(ctx, ev.RepositoryFullName, ev.Job.RunID, run, nil)
	if ourClassSeen && prResolved && prNumber > 0 {
		if err := w.st.MarkPRCommentDirty(ctx, ev.RepositoryID, ev.RepositoryFullName, prNumber); err != nil {
			slog.Error("worker: mark comment dirty", "repo", ev.RepositoryFullName, "pr", prNumber, "err", err)
		}
	}

	a = attrs
	a.Result = "succeeded"
	emitEvent(ctx, evRefreshCompleted, a)
	return nil
}

// resolveAndStampPullRequest maps a run to its PR (run.pull_requests[0] when
// GitHub includes it; for fork PRs that array is empty, so fall back to the
// commit→PRs listing, first open PR) and stamps the answer onto the run's job
// rows, also reporting the PR's base (target) branch for scope resolution.
// The fallback is gated on pull_request events: a push-to-main commit
// usually has a MERGED PR in that listing and must resolve as "no PR" (0),
// not resurrect the merged one. Best-effort: a resolution failure is logged
// and reported unresolved, never an error — comment plumbing must not block
// or retry deliveries.
func (w *worker) resolveAndStampPullRequest(ctx context.Context, repo string, runID int64, run apiWorkflowRun, obs *runObservation) (int64, string, bool) {
	prNumber := int64(0)
	baseRef := ""
	switch {
	case len(run.PullRequests) > 0:
		prNumber = run.PullRequests[0].Number
		baseRef = run.PullRequests[0].Base.Ref
	case (run.Event == "pull_request" || run.Event == "pull_request_target") && run.HeadSHA != "":
		pulls, err := w.gh.pullRequestsForCommit(ctx, repo, run.HeadSHA)
		if err != nil {
			slog.Warn("worker: pull request resolution failed", "repo", repo, "run_id", runID, "err", err)
			return 0, "", false
		}
		for _, p := range pulls {
			if p.State == "open" {
				prNumber = p.Number
				baseRef = p.Base.Ref
				break
			}
		}
	}
	if obs != nil {
		obs.PullRequestNumber = prNumber
	}
	if err := w.st.SetRunPullRequest(ctx, runID, prNumber); err != nil {
		slog.Warn("worker: stamp pull request", "repo", repo, "run_id", runID, "err", err)
		return prNumber, baseRef, false
	}
	return prNumber, baseRef, true
}

// resolveWorkspaceScope upserts the job-shape scope this demand reads (and,
// on branch trust, writes). Best-effort: the workspace cache is
// acceleration, so a resolution failure records no scope and the job simply
// runs cold.
func (w *worker) resolveWorkspaceScope(ctx context.Context, repoFullName, class, trust string, run apiWorkflowRun, jobName, prBaseRef string) string {
	org, repo, ok := strings.Cut(repoFullName, "/")
	if !ok {
		return ""
	}
	name, matrixKey := splitMatrixJobName(jobName)
	scopeID, err := w.st.EnsureWorkspaceScope(ctx, workspaceScopeKey{
		Org:          org,
		Repo:         repo,
		ScopeRef:     scopeRefFor(trust, run, prBaseRef),
		WorkflowPath: run.Path,
		JobName:      name,
		MatrixKey:    matrixKey,
		RunnerClass:  class,
	})
	if err != nil {
		slog.Warn("worker: ensure workspace scope", "repo", repoFullName, "err", err)
		return ""
	}
	return scopeID
}

// scopeRefFor picks the branch whose lineage a job shares: PR jobs read the
// TARGET branch's generations (their writes are never promoted), everything
// else lives on its own head branch.
func scopeRefFor(trust string, run apiWorkflowRun, prBaseRef string) string {
	if (trust == trustClassPR || trust == trustClassPRFork) && prBaseRef != "" {
		return prBaseRef
	}
	return run.HeadBranch
}

// splitMatrixJobName separates GitHub's rendered matrix suffix — "build
// (ubuntu, 3.12)" — into the job's own name and the matrix key, so matrix
// legs get distinct lineages.
func splitMatrixJobName(name string) (string, string) {
	if i := strings.Index(name, " ("); i > 0 && strings.HasSuffix(name, ")") {
		return name[:i], name[i+2 : len(name)-1]
	}
	return name, ""
}

func payloadFromAPIJob(j apiWorkflowJob) workflowJobPayload {
	attempt := j.RunAttempt
	if attempt == 0 {
		attempt = 1
	}
	return workflowJobPayload{
		ID:           j.ID,
		RunID:        j.RunID,
		RunAttempt:   attempt,
		Name:         j.Name,
		Status:       j.Status,
		Conclusion:   j.Conclusion,
		Labels:       j.Labels,
		RunnerID:     j.RunnerID,
		RunnerName:   j.RunnerName,
		HeadSHA:      j.HeadSHA,
		HeadBranch:   j.HeadBranch,
		WorkflowName: j.WorkflowName,
		StartedAt:    j.StartedAt,
		CompletedAt:  j.CompletedAt,
	}
}

func jobRowFrom(ev jobEvent, runnerClass string, observedFromAPIAt *time.Time) workflowJobRow {
	labels := ev.Job.Labels
	if labels == nil {
		labels = []string{}
	}
	return workflowJobRow{
		ProviderJobID:        ev.Job.ID,
		ProviderRunID:        ev.Job.RunID,
		ProviderRunAttempt:   ev.Job.RunAttempt,
		ProviderRepositoryID: ev.RepositoryID,
		RepositoryFullName:   ev.RepositoryFullName,
		Name:                 ev.Job.Name,
		Status:               firstNonEmpty(ev.Job.Status, ev.Action),
		Conclusion:           ev.Job.Conclusion,
		Labels:               labels,
		RunnerClass:          runnerClass,
		RunnerID:             ev.Job.RunnerID,
		RunnerName:           ev.Job.RunnerName,
		HeadSHA:              ev.Job.HeadSHA,
		HeadBranch:           ev.Job.HeadBranch,
		WorkflowName:         ev.Job.WorkflowName,
		StartedAt:            timePtr(ev.Job.StartedAt),
		CompletedAt:          timePtr(ev.Job.CompletedAt),
		ObservedFromAPIAt:    observedFromAPIAt,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
