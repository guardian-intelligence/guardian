package main

import (
	"context"
	"crypto/sha256"

	"go.opentelemetry.io/otel/trace"
)

const paymentsServiceName = "guardian-payments"

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
