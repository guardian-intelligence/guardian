package main

import (
	"context"
	"strings"
	"time"

	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const serviceName = "postflight-runner-controlplane"

// Event names — postflight's github-integration taxonomy for the ingest slice,
// plus the new comment-engine pair. Emitted as span events and structured
// logs; postflight wrote these straight to ClickHouse, here the OTel collector
// owns transport.
const (
	evWebhookReceived       = "github.webhook.received"
	evWebhookRejected       = "github.webhook.rejected"
	evWebhookVerified       = "github.webhook.verified"
	evDeliveryEnqueued      = "github.delivery.enqueued"
	evDeliveryProcessed     = "github.delivery.processed"
	evDeliveryIgnored       = "github.delivery.ignored"
	evDeliveryRetryable     = "github.delivery.retryable"
	evDeliveryFailed        = "github.delivery.failed"
	evDemandRecorded        = "github.job.demand.recorded"
	evDemandIgnored         = "github.job.demand.ignored"
	evDemandReconciled      = "github.job.demand.reconciled"
	evDemandReconcileFailed = "github.job.demand.reconcile_failed"
	evDemandFailed          = "github.job.demand.failed"
	evRefreshStarted        = "github.provider.refresh.started"
	evRefreshCompleted      = "github.provider.refresh.completed"
	evAssignmentObserved    = "github.runner.assignment.observed"
	evJobTerminalObserved   = "github.job.terminal.observed"
	evCommentPosted         = "postflight.comment.posted"
	evCommentFailed         = "postflight.comment.failed"
	evLeaseAllocated        = "postflight.lease.allocated"
	evLeaseAssigned         = "postflight.lease.assigned"
	evLeaseReady            = "postflight.lease.ready"
	evLeaseCompleted        = "postflight.lease.completed"
	evLeaseFailed           = "postflight.lease.failed"
	evLeaseExpired          = "postflight.lease.expired"
)

// initTracing registers a global tracer exporting to the OTLP gRPC collector
// named by OTEL_EXPORTER_OTLP_TRACES_ENDPOINT; unset means a no-op provider —
// observability must never gate ingest.
func initTracing(ctx context.Context, endpoint string) (func(context.Context) error, error) {
	if endpoint == "" {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(context.Context) error { return nil }, nil
	}
	// WithEndpoint wants bare host:port; the OTEL_EXPORTER_OTLP_* spec form is
	// a full URL. Accept either — a scheme-prefixed value goes through
	// WithEndpointURL (http:// implies insecure), a bare one stays insecure.
	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	}
	if strings.Contains(endpoint, "://") {
		opts = []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(endpoint)}
	}
	exp, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

type eventAttrs struct {
	DeliveryID  string
	Repo        string
	RunID       int64
	RunAttempt  int64
	JobID       int64
	RunnerClass string
	LeaseID     string
	HostID      string
	Result      string
	Reason      string
}

// emitEvent records one pipeline-edge event as a span event plus a structured
// log line. Strictly fire-and-forget: telemetry never blocks, errors, or
// alters control flow.
func emitEvent(ctx context.Context, name string, a eventAttrs) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		attrs := make([]attribute.KeyValue, 0, 8)
		attrs = append(attrs, attribute.String("result", a.Result))
		if a.DeliveryID != "" {
			attrs = append(attrs, attribute.String("delivery_id", a.DeliveryID))
		}
		if a.Repo != "" {
			attrs = append(attrs, attribute.String("repo", a.Repo))
		}
		if a.RunID != 0 {
			attrs = append(attrs, attribute.Int64("run_id", a.RunID))
		}
		if a.RunAttempt != 0 {
			attrs = append(attrs, attribute.Int64("run_attempt", a.RunAttempt))
		}
		if a.JobID != 0 {
			attrs = append(attrs, attribute.Int64("job_id", a.JobID))
		}
		if a.RunnerClass != "" {
			attrs = append(attrs, attribute.String("runner_class", a.RunnerClass))
		}
		if a.LeaseID != "" {
			attrs = append(attrs, attribute.String("lease_id", a.LeaseID))
		}
		if a.HostID != "" {
			attrs = append(attrs, attribute.String("host_id", a.HostID))
		}
		if a.Reason != "" {
			attrs = append(attrs, attribute.String("reason", a.Reason))
		}
		span.AddEvent(name, trace.WithAttributes(attrs...))
	}
	args := make([]any, 0, 16)
	args = append(args, "result", a.Result)
	if a.DeliveryID != "" {
		args = append(args, "delivery_id", a.DeliveryID)
	}
	if a.Repo != "" {
		args = append(args, "repo", a.Repo)
	}
	if a.RunID != 0 {
		args = append(args, "run_id", a.RunID)
	}
	if a.RunAttempt != 0 {
		args = append(args, "run_attempt", a.RunAttempt)
	}
	if a.JobID != 0 {
		args = append(args, "job_id", a.JobID)
	}
	if a.RunnerClass != "" {
		args = append(args, "runner_class", a.RunnerClass)
	}
	if a.LeaseID != "" {
		args = append(args, "lease_id", a.LeaseID)
	}
	if a.HostID != "" {
		args = append(args, "host_id", a.HostID)
	}
	if a.Reason != "" {
		args = append(args, "reason", a.Reason)
	}
	slog.Info(name, args...)
}
