package main

import (
	"context"
	"strconv"
	"strings"
	"time"

	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// runnerGroupID is GitHub's default runner group; per-tenant groups arrive
// with multi-tenancy.
const runnerGroupID = 1

// scheduler turns recorded demand into host leases: create a lease for the
// demanded class, claim a slot on a host offering it, mint the JIT runner
// config, and hand the lease to the host's next sync. Level-triggered like
// the delivery worker: every tick sweeps deadline expiry first, then
// allocation, then placement, all against current database state.
type scheduler struct {
	st     *pgStore
	gh     *githubClient
	cfg    config
	tracer trace.Tracer
}

func (s *scheduler) run(ctx context.Context) {
	// Same drain contract as the delivery worker: an in-flight tick finishes
	// on a non-cancelable child so a deploy never abandons a claimed slot
	// between claim and assignment.
	work := context.WithoutCancel(ctx)
	ticker := time.NewTicker(s.cfg.schedulerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.reconcileReservations(work)
		s.expireOverdue(work)
		s.rejectUnknownClasses(work)
		s.allocateDemands(work)
		s.placeLeases(work)
		s.promoteSealedGenerations(work)
		s.discardStaleCandidates(work)
		s.sweepReapableGenerations(work)
	}
}

// reconcileReservations converges the slot counters to the leases that
// actually hold them, healing any reservation orphaned by a crash between
// claim and assignment.
func (s *scheduler) reconcileReservations(ctx context.Context) {
	fixed, err := s.st.ReconcileSlotReservations(ctx)
	if err != nil {
		slog.Error("scheduler: reconcile slot reservations", "err", err)
		return
	}
	if fixed > 0 {
		slog.Warn("scheduler: slot reservations drifted from lease truth", "slots_corrected", fixed)
	}
}

// expireOverdue is the deadline reconciler: any lease sitting past its
// per-state deadline — or ready on a host that stopped syncing — is
// terminalized and its resources released. The next sync omits it, which is
// the host-side cancel.
func (s *scheduler) expireOverdue(ctx context.Context) {
	overdue, err := s.st.ListOverdueLeases(ctx, s.cfg.workerBatchSize, time.Now().Add(-s.cfg.hostOfflineTimeout))
	if err != nil {
		slog.Error("scheduler: list overdue leases", "err", err)
		return
	}
	for _, l := range overdue {
		reason := "allocate deadline exceeded"
		problems := []problem{problemCapacityTimeout(l.RunnerClass)}
		switch l.State {
		case leaseAssigned:
			reason = "assignment deadline exceeded"
			problems = []problem{problemAssignmentTimeout()}
		case leaseReady:
			reason = "host stopped syncing"
			problems = []problem{problemHostLost(l.HostID)}
		case leaseSealing:
			// The demand already completed at the exited report; an
			// unconfirmed seal discards its candidate and nothing else.
			reason = "seal not confirmed"
			problems = nil
		}
		expired, err := s.st.ExpireLease(ctx, l, reason, problems)
		if err != nil {
			slog.Error("scheduler: expire lease", "lease_id", l.LeaseID, "err", err)
			continue
		}
		if expired {
			emitEvent(ctx, evLeaseExpired, eventAttrs{
				LeaseID: l.LeaseID, HostID: l.HostID, JobID: l.ProviderJobID,
				RunnerClass: l.RunnerClass, Result: "failed", Reason: reason,
			})
		}
	}
}

// rejectUnknownClasses terminalizes recorded demands whose runner class the
// control plane does not serve, so the job's owner gets a problem doc
// instead of a silent hang.
func (s *scheduler) rejectUnknownClasses(ctx context.Context) {
	demands, err := s.st.ListUnknownClassDemands(ctx, s.cfg.workerBatchSize)
	if err != nil {
		slog.Error("scheduler: list unknown-class demands", "err", err)
		return
	}
	for _, d := range demands {
		if err := s.st.MarkProviderDemandFailed(ctx, d.ProviderJobID,
			[]problem{problemRunnerClassUnknown(d.RunnerClass)}); err != nil {
			slog.Error("scheduler: fail unknown-class demand", "job_id", d.ProviderJobID, "err", err)
			continue
		}
		emitEvent(ctx, evDemandFailed, eventAttrs{
			JobID: d.ProviderJobID, Repo: d.RepositoryFullName, RunnerClass: d.RunnerClass,
			Result: "failed", Reason: "demand.runner_class_unknown",
		})
	}
}

// allocateDemands creates an allocating lease for every recorded demand
// whose class this control plane serves.
func (s *scheduler) allocateDemands(ctx context.Context) {
	demands, err := s.st.ListSchedulableDemands(ctx, s.cfg.workerBatchSize)
	if err != nil {
		slog.Error("scheduler: list demands", "err", err)
		return
	}
	for _, d := range demands {
		org, _, ok := strings.Cut(d.RepositoryFullName, "/")
		if !ok || org == "" {
			slog.Error("scheduler: invalid repository name", "job_id", d.ProviderJobID, "repo", d.RepositoryFullName)
			continue
		}
		leaseID, created, err := s.st.CreateLeaseForDemand(ctx, d,
			strconv.FormatInt(d.ProviderJobID, 10), strconv.FormatInt(d.ProviderRunAttempt, 10),
			org, d.ProviderInstallationID, time.Now().Add(s.cfg.allocateTimeout))
		if err != nil {
			slog.Error("scheduler: create lease", "job_id", d.ProviderJobID, "err", err)
			continue
		}
		if created {
			emitEvent(ctx, evLeaseAllocated, eventAttrs{
				LeaseID: leaseID, JobID: d.ProviderJobID, Repo: d.RepositoryFullName,
				RunnerClass: d.RunnerClass, Result: "succeeded",
			})
		}
	}
}

// placeLeases binds allocating leases to hosts: CAS slot claim, JIT mint,
// assignment. A lease that finds no free slot simply stays allocating until
// a later tick places it or its allocate deadline expires.
func (s *scheduler) placeLeases(ctx context.Context) {
	allocating, err := s.st.ListAllocatingLeases(ctx, s.cfg.workerBatchSize)
	if err != nil {
		slog.Error("scheduler: list allocating leases", "err", err)
		return
	}
	for _, l := range allocating {
		s.placeLease(ctx, l)
	}
}

func (s *scheduler) placeLease(ctx context.Context, l allocatingLease) {
	ctx, span := s.tracer.Start(ctx, "lease.place", trace.WithAttributes(
		attribute.String("lease_id", l.LeaseID),
		attribute.Int64("job_id", l.ProviderJobID),
		attribute.String("runner_class", l.RunnerClass),
	))
	defer span.End()

	hostID, claimed, err := s.st.ClaimHostSlot(ctx, l.LeaseID, l.RunnerClass)
	if err != nil {
		slog.Error("scheduler: claim slot", "lease_id", l.LeaseID, "err", err)
		return
	}
	if !claimed {
		return // no capacity right now; the allocate deadline bounds the wait
	}

	attrs := eventAttrs{LeaseID: l.LeaseID, HostID: hostID, JobID: l.ProviderJobID, RunnerClass: l.RunnerClass}
	jitConfig, err := s.gh.generateJITConfig(ctx, l.InstallationID, l.OrgID, l.LeaseID, runnerGroupID, []string{l.RunnerClass})
	if err != nil {
		// The lease is terminalized (which returns the claimed slot): a mint
		// failure is a GitHub-side verdict on this job, not a placement retry.
		if _, ferr := s.st.FailAllocatingLease(ctx, l.LeaseID, "jit mint: "+err.Error(),
			[]problem{problemJITMintFailed(err)}); ferr != nil {
			slog.Error("scheduler: fail lease after mint failure", "lease_id", l.LeaseID, "err", ferr)
			return
		}
		attrs.Result, attrs.Reason = "failed", "lease.jit_mint_failed"
		emitEvent(ctx, evLeaseFailed, attrs)
		return
	}

	assigned, err := s.st.AssignLease(ctx, l.LeaseID, hostID, jitConfig, time.Now().Add(s.cfg.assignmentTimeout))
	if err != nil {
		slog.Error("scheduler: assign lease", "lease_id", l.LeaseID, "err", err)
		return
	}
	if !assigned {
		// The lease left 'allocating' concurrently (an expiry raced the
		// placement); the terminalizing transition released the claim.
		return
	}
	attrs.Result = "succeeded"
	emitEvent(ctx, evLeaseAssigned, attrs)
}

// promoteSealedGenerations classifies every host-confirmed seal whose GitHub
// verdict has been observed. Only an attempt-matching success promotes;
// everything else — failure, cancellation, a superseded attempt, an
// unparseable attempt — discards the candidate and leaves the previous
// current authoritative. Candidates whose verdict has not been observed yet
// are not listed at all, so ambiguity never advances anything.
func (s *scheduler) promoteSealedGenerations(ctx context.Context) {
	candidates, err := s.st.ListSealedCandidates(ctx, s.cfg.workerBatchSize)
	if err != nil {
		slog.Error("scheduler: list sealed candidates", "err", err)
		return
	}
	for _, c := range candidates {
		attrs := eventAttrs{
			LeaseID: c.LeaseID, HostID: c.HostID, JobID: c.ProviderJobID, Generation: c.Generation,
		}
		attempt, parseErr := strconv.ParseInt(c.LeaseAttemptID, 10, 64)
		if parseErr != nil || attempt != c.JobRunAttempt || c.JobConclusion != "success" {
			discarded, err := s.st.DiscardGeneration(ctx, c.Generation)
			if err != nil {
				slog.Error("scheduler: discard generation", "generation", c.Generation, "err", err)
				continue
			}
			if discarded {
				attrs.Result = "failed"
				attrs.Reason = "conclusion:" + c.JobConclusion
				if parseErr == nil && attempt != c.JobRunAttempt {
					attrs.Reason = "stale_attempt"
				}
				emitEvent(ctx, evGenerationDiscarded, attrs)
			}
			continue
		}
		promoted, retained, err := s.st.PromoteGeneration(ctx, c)
		if err != nil {
			slog.Error("scheduler: promote generation", "generation", c.Generation, "err", err)
			continue
		}
		switch {
		case promoted:
			attrs.Result = "succeeded"
			emitEvent(ctx, evGenerationPromoted, attrs)
		case retained:
			attrs.Result, attrs.Reason = "failed", "lost promotion race"
			emitEvent(ctx, evGenerationRetained, attrs)
		}
	}
}

// discardStaleCandidates ages out candidates whose GitHub verdict was never
// observed from the API — a lost delivery leaves nothing else to chase them.
// The discard costs only the cache write and releases the dataset.
func (s *scheduler) discardStaleCandidates(ctx context.Context) {
	discarded, err := s.st.DiscardStaleCandidates(ctx, time.Now().Add(-s.cfg.verdictTimeout))
	if err != nil {
		slog.Error("scheduler: discard stale candidates", "err", err)
		return
	}
	if discarded > 0 {
		slog.Warn("scheduler: candidates discarded with no observed verdict", "generations", discarded)
	}
}

// sweepReapableGenerations releases unreferenced retained/discarded
// generations to the sync response's reap verb.
func (s *scheduler) sweepReapableGenerations(ctx context.Context) {
	swept, err := s.st.SweepReapableGenerations(ctx)
	if err != nil {
		slog.Error("scheduler: sweep reapable generations", "err", err)
		return
	}
	if swept > 0 {
		slog.Info("scheduler: generations released for reap", "generations", swept)
	}
}
