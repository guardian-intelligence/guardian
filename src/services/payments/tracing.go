package main

import (
	"context"
	"crypto/sha256"
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

const paymentsServiceName = "guardian-payments"

func initTracing(ctx context.Context, endpoint string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if endpoint == "" {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(context.Context) error { return nil }, nil
	}
	options := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	}
	if strings.Contains(endpoint, "://") {
		options = []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(endpoint)}
	}
	exporter, err := otlptracegrpc.New(ctx, options...)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(paymentsServiceName)))
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(time.Second)),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

func traceIDFromContext(ctx context.Context) string {
	id := trace.SpanContextFromContext(ctx).TraceID()
	if !id.IsValid() {
		return ""
	}
	return id.String()
}

func contextForPersistedTrace(ctx context.Context, traceID string) context.Context {
	id, err := trace.TraceIDFromHex(traceID)
	if err != nil || !id.IsValid() {
		return ctx
	}
	parentDigest := sha256.Sum256([]byte("guardian-persisted-parent:" + traceID))
	var parentID trace.SpanID
	copy(parentID[:], parentDigest[:len(parentID)])
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    id,
		SpanID:     parentID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	return trace.ContextWithRemoteSpanContext(ctx, spanContext)
}
