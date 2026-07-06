package main

import (
	"context"
	"fmt"

	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// reconcileQueued is the missed-webhook / stuck-demand sweeper: still-queued
// jobs of our class with no assignment and an absent-or-just-recorded demand
// are re-run through the same submitQueuedJob path, which re-reads GitHub
// run+job — so a missed 'completed' webhook is caught here because the API
// read shows non-queued status and records truth instead.
func (w *worker) reconcileQueued(ctx context.Context) {
	jobs, err := w.st.ListQueuedJobsForReconcile(ctx, w.cfg.workerBatchSize, reconcileQuietPeriod)
	if err != nil {
		slog.Error("reconcile: list queued jobs", "err", err)
		return
	}
	for _, j := range jobs {
		release, acquired, err := w.st.TryJobLock(ctx, j.ProviderJobID)
		if err != nil {
			slog.Error("reconcile: job lock", "job_id", j.ProviderJobID, "err", err)
			continue
		}
		if !acquired {
			continue // another worker holds the job; not an error
		}
		w.reconcileJob(ctx, j)
		release()
	}
}

func (w *worker) reconcileJob(ctx context.Context, j queuedJob) {
	deliveryID := fmt.Sprintf("reconcile:%d:%d", j.ProviderRunID, j.ProviderJobID)
	ctx, span := w.tracer.Start(ctx, "job.reconcile", trace.WithAttributes(
		attribute.String("delivery_id", deliveryID),
		attribute.Int64("job_id", j.ProviderJobID),
	))
	defer span.End()

	attrs := eventAttrs{
		DeliveryID:  deliveryID,
		Repo:        j.RepositoryFullName,
		RunID:       j.ProviderRunID,
		RunAttempt:  j.ProviderRunAttempt,
		JobID:       j.ProviderJobID,
		RunnerClass: j.RunnerClass,
	}
	a := attrs
	a.Result = "started"
	emitEvent(ctx, evDemandReconciled, a)

	ev := jobEvent{
		Action:             "queued",
		InstallationID:     w.cfg.installationID,
		RepositoryID:       j.ProviderRepositoryID,
		RepositoryFullName: j.RepositoryFullName,
		Job: workflowJobPayload{
			ID:         j.ProviderJobID,
			RunID:      j.ProviderRunID,
			RunAttempt: j.ProviderRunAttempt,
			Name:       j.Name,
			Status:     "queued",
			Labels:     j.Labels,
		},
	}
	if err := w.submitQueuedJob(ctx, ev, deliveryID); err != nil {
		a = attrs
		a.Result, a.Reason = "failed", err.Error()
		emitEvent(ctx, evDemandReconcileFailed, a)
		return
	}
	a = attrs
	a.Result = "succeeded"
	emitEvent(ctx, evDemandReconciled, a)
}
