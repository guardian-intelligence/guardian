package main

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

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
