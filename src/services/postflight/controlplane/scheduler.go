package main

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/trace"
)

const runnerGroupID = 1

// scheduler maintains registered listeners independently of queued jobs and
// classifies completed durable generations. Job-to-member binding happens in
// the host sync transaction that carries the local runner assignment.
type scheduler struct {
	st     *pgStore
	gh     *githubClient
	cfg    config
	tracer trace.Tracer
}

func (s *scheduler) run(ctx context.Context) {
	work := context.WithoutCancel(ctx)
	ticker := time.NewTicker(s.cfg.schedulerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.tick(work)
	}
}

func (s *scheduler) tick(ctx context.Context) {
	if _, err := s.st.EnsureRunnerPools(ctx, s.cfg.runnerPoolSize); err != nil {
		slog.Error("scheduler: ensure runner pools", "err", err)
	}
	if _, err := s.st.EnsureJobIntents(ctx); err != nil {
		slog.Error("scheduler: ensure job intents", "err", err)
	}
	s.recoverOfflineHosts(ctx)
	s.preparePoolMembers(ctx)
	s.expireSealingAssignments(ctx)
	s.promoteSealedGenerations(ctx)
	s.discardStaleCandidates(ctx)
	s.sweepReapableGenerations(ctx)
}

func (s *scheduler) recoverOfflineHosts(ctx context.Context) {
	count, err := s.st.RecoverOfflineHosts(ctx, time.Now().Add(-s.cfg.hostOfflineTimeout))
	if err != nil {
		slog.Error("scheduler: recover offline hosts", "err", err)
	} else if count > 0 {
		slog.Warn("scheduler: recovered assignments from offline hosts", "assignments", count)
	}
}

func (s *scheduler) preparePoolMembers(ctx context.Context) {
	members, err := s.st.ListPoolMembersNeedingJIT(ctx, s.cfg.workerBatchSize)
	if err != nil {
		slog.Error("scheduler: list pool members needing jit", "err", err)
		return
	}
	for _, member := range members {
		started := time.Now()
		jit, err := s.gh.generateJITConfig(ctx, member.InstallationID, member.OrgID,
			member.RunnerName, runnerGroupID, []string{member.RunnerClass})
		if err != nil {
			if markErr := s.st.FailPoolMemberJIT(ctx, member.MemberID, err.Error()); markErr != nil {
				slog.Error("scheduler: recycle member after jit failure", "member_id", member.MemberID, "err", markErr)
			}
			continue
		}
		stored, err := s.st.RecordPoolMemberJIT(ctx, member.MemberID, jit)
		if err != nil {
			slog.Error("scheduler: store member jit", "member_id", member.MemberID, "err", err)
			continue
		}
		if stored {
			slog.Info("postflight.controlplane.pool_member.jit_ready",
				"member_id", member.MemberID, "runner_name", member.RunnerName,
				"duration_ns", time.Since(started).Nanoseconds())
		}
	}
}

func (s *scheduler) expireSealingAssignments(ctx context.Context) {
	count, err := s.st.ExpireSealingAssignments(ctx, time.Now())
	if err != nil {
		slog.Error("scheduler: expire sealing assignments", "err", err)
		return
	}
	if count > 0 {
		slog.Warn("scheduler: discarded unconfirmed process snapshots", "assignments", count)
	}
}

func (s *scheduler) promoteSealedGenerations(ctx context.Context) {
	candidates, err := s.st.ListSealedCandidates(ctx, s.cfg.workerBatchSize)
	if err != nil {
		slog.Error("scheduler: list sealed candidates", "err", err)
		return
	}
	for _, candidate := range candidates {
		attempt, parseErr := strconv.ParseInt(candidate.AssignmentAttemptID, 10, 64)
		if parseErr != nil || attempt != candidate.JobRunAttempt || candidate.JobConclusion != "success" {
			if _, err := s.st.DiscardGeneration(ctx, candidate.Generation); err != nil {
				slog.Error("scheduler: discard generation", "generation", candidate.Generation, "err", err)
			}
			continue
		}
		promoted, retained, err := s.st.PromoteGeneration(ctx, candidate)
		if err != nil {
			slog.Error("scheduler: promote generation", "generation", candidate.Generation, "err", err)
			continue
		}
		if promoted || retained {
			slog.Info("postflight.controlplane.generation.classified",
				"assignment_id", candidate.AssignmentID, "generation", candidate.Generation,
				"promoted", promoted, "retained", retained)
		}
	}
}

func (s *scheduler) discardStaleCandidates(ctx context.Context) {
	count, err := s.st.DiscardStaleCandidates(ctx, time.Now().Add(-s.cfg.verdictTimeout))
	if err != nil {
		slog.Error("scheduler: discard stale generations", "err", err)
	} else if count > 0 {
		slog.Warn("scheduler: discarded stale generations", "count", count)
	}
}

func (s *scheduler) sweepReapableGenerations(ctx context.Context) {
	count, err := s.st.SweepReapableGenerations(ctx)
	if err != nil {
		slog.Error("scheduler: sweep reapable generations", "err", err)
	} else if count > 0 {
		slog.Info("scheduler: generations ready for host reap", "count", count)
	}
}
