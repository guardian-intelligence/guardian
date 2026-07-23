package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
)

var errObservedPlanPending = errors.New("observed assignment is not yet visible from the provider")

type observedPlanRejected struct{ reason string }

func (e observedPlanRejected) Error() string { return e.reason }

type observedPlanTiming struct {
	memberLookup time.Duration
	runFetch     time.Duration
	jobsFetch    time.Duration
	persist      time.Duration
}

func (s *syncServer) handleResolveJobPlan(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	ctx, span := s.tracer.Start(r.Context(), "hostd.job_plan.resolve")
	defer span.End()
	r = r.WithContext(ctx)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeProblems(w, []problem{problemMethodNotAllowed()})
		return
	}
	if !s.authorized(r) {
		writeProblems(w, []problem{problemSyncUnauthorized()})
		return
	}
	var request syncproto.JobPlanResolveRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxSyncRequestBytes)).Decode(&request); err != nil {
		writeProblems(w, []problem{problemSyncPayloadInvalid("job-plan resolution request does not parse: " + err.Error())})
		return
	}
	if err := validateJobPlanResolveRequest(request); err != nil {
		writeProblems(w, []problem{problemSyncPayloadInvalid(err.Error())})
		return
	}
	span.SetAttributes(
		attribute.String("host_id", request.HostID),
		attribute.String("member_id", request.MemberID),
		attribute.String("vm_id", request.VMID),
		attribute.Int64("check_run_id", request.Assignment.CheckRunID),
		attribute.String("run_id", request.Assignment.Identity.RunID),
	)

	lookupStarted := time.Now()
	member, err := s.st.ResolvePoolMember(r.Context(), request.HostID, request.BootID, request.MemberID, request.VMID)
	timing := observedPlanTiming{memberLookup: time.Since(lookupStarted)}
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblems(w, []problem{problemJobPlanRejected()})
		return
	}
	if err != nil {
		s.syncError(w, request.HostID, "resolve pool member", err)
		return
	}
	if request.Assignment.RunnerName != member.RunnerName ||
		request.Assignment.Identity.RunnerName != member.RunnerName {
		writeProblems(w, []problem{problemJobPlanRejected()})
		return
	}

	providerJobID, resolutionTiming, err := s.resolver.resolveObservedPlan(r.Context(), member, request.Assignment)
	timing.runFetch = resolutionTiming.runFetch
	timing.jobsFetch = resolutionTiming.jobsFetch
	timing.persist = resolutionTiming.persist
	if err != nil {
		var rejected observedPlanRejected
		switch {
		case errors.Is(err, errObservedPlanPending):
			writeProblems(w, []problem{problemJobPlanPending()})
		case errors.As(err, &rejected):
			slog.Error("postflight.controlplane.job_plan.resolve_rejected",
				"host_id", request.HostID, "member_id", request.MemberID,
				"check_run_id", request.Assignment.CheckRunID, "reason", rejected.reason)
			writeProblems(w, []problem{problemJobPlanRejected()})
		default:
			s.syncError(w, request.HostID, "resolve observed job plan", err)
		}
		return
	}
	if err := s.st.BindObservedAssignment(r.Context(), request.HostID, request.MemberID, request.Assignment); err != nil {
		s.syncError(w, request.HostID, "bind resolved assignment", err)
		return
	}

	snapshotStarted := time.Now()
	snapshot, err := s.jobPlanSnapshot(r.Context(), request.HostID)
	if err != nil {
		s.syncError(w, request.HostID, "read resolved job plan", err)
		return
	}
	var plan syncproto.JobPlan
	for _, candidate := range snapshot.Plans {
		if candidate.ExecutionID == strconv.FormatInt(providerJobID, 10) &&
			candidate.CheckRunID == request.Assignment.CheckRunID {
			if plan.PlanID != "" {
				writeProblems(w, []problem{problemJobPlanRejected()})
				return
			}
			plan = candidate
		}
	}
	if plan.PlanID == "" {
		writeProblems(w, []problem{problemJobPlanPending()})
		return
	}
	planReadElapsed := time.Since(snapshotStarted)

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(syncproto.JobPlanResolveResponse{Plan: plan})
	span.SetAttributes(
		attribute.String("postflight.result", "succeeded"),
		attribute.Int64("postflight.provider_job_id", providerJobID),
		attribute.Int64("postflight.member_lookup_ns", timing.memberLookup.Nanoseconds()),
		attribute.Int64("postflight.run_fetch_ns", timing.runFetch.Nanoseconds()),
		attribute.Int64("postflight.jobs_fetch_ns", timing.jobsFetch.Nanoseconds()),
		attribute.Int64("postflight.persist_ns", timing.persist.Nanoseconds()),
		attribute.Int64("postflight.plan_read_ns", planReadElapsed.Nanoseconds()),
	)
	slog.Info("postflight.controlplane.job_plan.resolve_completed",
		"host_id", request.HostID, "member_id", request.MemberID,
		"check_run_id", request.Assignment.CheckRunID, "provider_job_id", providerJobID,
		"duration_ns", time.Since(started).Nanoseconds(),
		"member_lookup_ns", timing.memberLookup.Nanoseconds(),
		"run_fetch_ns", timing.runFetch.Nanoseconds(),
		"jobs_fetch_ns", timing.jobsFetch.Nanoseconds(),
		"persist_ns", timing.persist.Nanoseconds(),
		"plan_read_ns", planReadElapsed.Nanoseconds())
}

func validateJobPlanResolveRequest(request syncproto.JobPlanResolveRequest) error {
	assignment := request.Assignment
	if request.HostID == "" || request.BootID == "" || request.MemberID == "" || request.VMID == "" {
		return errors.New("host, boot, member, and VM identity are required")
	}
	if len(request.HostID) > 128 || len(request.BootID) > 128 || len(request.MemberID) > 128 || len(request.VMID) > 128 {
		return errors.New("host, boot, member, or VM identity is too long")
	}
	if assignment.RequestID == "" || assignment.JobID == "" || assignment.CheckRunID <= 0 ||
		assignment.RunnerName == "" || assignment.JobDisplayName == "" ||
		assignment.Identity.RunID == "" || assignment.Identity.RunAttempt <= 0 ||
		assignment.Identity.RunnerName != assignment.RunnerName ||
		!validRepoFullName(assignment.Identity.Repository) || assignment.Identity.WorkflowJob == "" {
		return errors.New("observed assignment identity is incomplete")
	}
	return nil
}

func (w *worker) resolveObservedPlan(ctx context.Context, member resolvablePoolMember, observed syncproto.ObservedAssignment) (int64, observedPlanTiming, error) {
	var timing observedPlanTiming
	runID, err := strconv.ParseInt(observed.Identity.RunID, 10, 64)
	if err != nil || runID <= 0 {
		return 0, timing, observedPlanRejected{reason: "local run id is invalid"}
	}
	org, _, _ := strings.Cut(observed.Identity.Repository, "/")
	if org != member.OrgID {
		return 0, timing, observedPlanRejected{reason: "repository organization does not own the selected runner"}
	}

	type runResult struct {
		run      apiWorkflowRun
		duration time.Duration
		err      error
	}
	type attemptJobsResult struct {
		jobs     []apiWorkflowJob
		duration time.Duration
		err      error
	}
	runResults := make(chan runResult, 1)
	jobResults := make(chan attemptJobsResult, 1)
	go func() {
		started := time.Now()
		run, err := w.gh.workflowRun(ctx, member.InstallationID, observed.Identity.Repository, runID)
		runResults <- runResult{run: run, duration: time.Since(started), err: err}
	}()
	go func() {
		started := time.Now()
		jobs, err := w.gh.workflowRunAttemptJobs(ctx, member.InstallationID, observed.Identity.Repository, runID, int64(observed.Identity.RunAttempt))
		jobResults <- attemptJobsResult{jobs: jobs, duration: time.Since(started), err: err}
	}()
	resolvedRun := <-runResults
	attemptJobs := <-jobResults
	timing.runFetch = resolvedRun.duration
	timing.jobsFetch = attemptJobs.duration
	if resolvedRun.err != nil {
		return 0, timing, fmt.Errorf("fetch observed workflow run: %w", resolvedRun.err)
	}
	if attemptJobs.err != nil {
		return 0, timing, fmt.Errorf("fetch observed workflow jobs: %w", attemptJobs.err)
	}
	run := resolvedRun.run
	if run.ID != runID || run.RunAttempt != int64(observed.Identity.RunAttempt) ||
		run.Repository.ID <= 0 || run.Repository.FullName != observed.Identity.Repository {
		return 0, timing, observedPlanRejected{reason: "provider workflow run identity differs from the local assignment"}
	}

	var matched *apiWorkflowJob
	for i := range attemptJobs.jobs {
		if parseCheckRunID(attemptJobs.jobs[i].CheckRunURL) != observed.CheckRunID {
			continue
		}
		if matched != nil {
			return 0, timing, observedPlanRejected{reason: "provider returned more than one job for the check run"}
		}
		matched = &attemptJobs.jobs[i]
	}
	if matched == nil || matched.RunnerName == "" {
		return 0, timing, errObservedPlanPending
	}
	job := payloadFromAPIJob(*matched)
	class := runnerClassForLabels(job.Labels, w.cfg.runnerClassPrefix)
	if job.RunID != runID || job.RunAttempt != int64(observed.Identity.RunAttempt) ||
		job.Name != observed.JobDisplayName || job.RunnerName != member.RunnerName ||
		class != member.RunnerClass {
		return 0, timing, observedPlanRejected{reason: "provider job identity differs from the selected local runner"}
	}
	if !jobCanStillRequireRendezvous(job.Status) {
		return 0, timing, observedPlanRejected{reason: "provider job is no longer runnable"}
	}

	persistStarted := time.Now()
	deliveryID := fmt.Sprintf("hostd:%d:%s", observed.CheckRunID, member.RunnerName)
	ev := jobEvent{
		Action:             job.Status,
		InstallationID:     member.InstallationID,
		RepositoryID:       run.Repository.ID,
		RepositoryFullName: observed.Identity.Repository,
		Job:                job,
	}
	now := time.Now()
	if err := w.st.UpsertWorkflowJob(ctx, jobRowFrom(ev, class, &now)); err != nil {
		return 0, timing, fmt.Errorf("persist observed API job: %w", err)
	}
	if err := w.st.UpsertJobAssignment(ctx, jobAssignment{
		ProviderJobID: job.ID, RunnerName: job.RunnerName, RunnerID: job.RunnerID, DeliveryID: deliveryID,
	}); err != nil {
		return 0, timing, fmt.Errorf("persist observed runner assignment: %w", err)
	}
	obs := runObservation{
		Event:                  run.Event,
		RepositoryFullName:     observed.Identity.Repository,
		HeadRepositoryFullName: run.HeadRepository.FullName,
		HeadBranch:             run.HeadBranch,
		HeadSHA:                run.HeadSHA,
	}
	_, prBaseRef, _ := w.resolveAndStampPullRequest(ctx, member.InstallationID, observed.Identity.Repository, runID, run, &obs)
	if err := w.ensureDemandForAPIJob(ctx, ev, run, trustClassForRun(obs), prBaseRef, deliveryID); err != nil {
		return 0, timing, fmt.Errorf("ensure observed provider demand: %w", err)
	}
	if _, err := w.st.EnsureJobIntents(ctx); err != nil {
		return 0, timing, fmt.Errorf("ensure observed job intent: %w", err)
	}
	if err := w.st.NotifyJobPlans(ctx); err != nil {
		return 0, timing, fmt.Errorf("publish observed job plan: %w", err)
	}
	timing.persist = time.Since(persistStarted)
	return job.ID, timing, nil
}
