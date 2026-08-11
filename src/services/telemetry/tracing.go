// Package telemetry is the one tracer bootstrap shared by every Go binary
// that exports spans: OTLP gRPC to the in-namespace collector, W3C
// trace-context propagation, 100% sampling (the ClickHouse-vendor consensus
// for owned trace storage — volume control is the table TTL). An empty
// endpoint installs a no-op provider: observability never gates a service.
package telemetry

import (
	"context"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Init registers the global tracer provider and W3C propagator. The
// endpoint is the OTEL_EXPORTER_OTLP_TRACES_ENDPOINT value: bare host:port
// (insecure) or a scheme-prefixed URL; empty means no-op. The returned
// shutdown flushes the batcher.
func Init(ctx context.Context, serviceName, endpoint string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if endpoint == "" {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(context.Context) error { return nil }, nil
	}
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
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(serviceName)))
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

// TraceIDFrom returns the active trace id as 32-hex, or "" when the
// context carries no sampled span — the form users quote and log lines
// carry.
func TraceIDFrom(ctx context.Context) string {
	id := trace.SpanContextFromContext(ctx).TraceID()
	if !id.IsValid() {
		return ""
	}
	return id.String()
}
