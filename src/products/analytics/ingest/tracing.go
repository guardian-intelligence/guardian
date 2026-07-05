package main

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const serviceName = "guardian-analytics-ingest"

// initTracing registers a global tracer that exports to the in-namespace OTel
// collector (OTLP gRPC). Spans carry service.name so they land under one
// ServiceName in guardian_analytics.otel_traces; per-request correlation_id
// is stamped in the Publish handler so a trace joins the visitor's event
// rows. Returns a shutdown func; if the collector env is unset, tracing is a
// no-op provider (server-side observability must never gate on the collector).
func initTracing(ctx context.Context, endpoint string) (func(context.Context) error, error) {
	if endpoint == "" {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
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
		// 100% sampling: no client-side span dropping (the ClickHouse-vendor
		// consensus for owned trace storage). Volume control is the TTL.
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// stampCorrelation records the visitor correlation id on the active span so
// analytics event rows and this trace share a join key.
func stampCorrelation(ctx context.Context, corr [16]byte, tier uint8, site string) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.SetAttributes(
		attribute.String("guardian.correlation_id", hexID(corr)),
		attribute.Int("guardian.trust_tier", int(tier)),
		attribute.String("guardian.site", site),
	)
}

func hexID(b [16]byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 32)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0x0f]
	}
	return string(out)
}
