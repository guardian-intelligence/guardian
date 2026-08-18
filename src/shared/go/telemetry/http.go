package telemetry

import (
	"context"
	"fmt"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

type middlewareConfig struct {
	skip            map[string]bool
	traceparentOnly map[string]bool
}

// MiddlewareOption configures Middleware.
type MiddlewareOption func(*middlewareConfig)

// WithSkipPaths exempts exact paths from tracing entirely. Sampling is
// deliberately 100% (volume control is the table TTL), so paths hit by
// probes and scrapes — which outnumber real traffic by orders of magnitude
// and say nothing — have to be excluded before the span exists.
func WithSkipPaths(paths ...string) MiddlewareOption {
	return func(c *middlewareConfig) {
		for _, p := range paths {
			c.skip[p] = true
		}
	}
}

// WithTraceparentOnly traces the given exact paths only when the request
// carries a traceparent header. Instrumented browsers send one on every
// same-origin fetch; external monitors and crawlers do not, and on a hot
// polled path their spans drown the real ones.
func WithTraceparentOnly(paths ...string) MiddlewareOption {
	return func(c *middlewareConfig) {
		for _, p := range paths {
			c.traceparentOnly[p] = true
		}
	}
}

// Middleware continues the caller's W3C trace context (browsers mint the
// trace id per logical action, so the span lands under the id the user can
// quote) and records one server span per request. Handlers reach the
// active id via TraceIDFrom(r.Context()).
func Middleware(next http.Handler, opts ...MiddlewareOption) http.Handler {
	cfg := &middlewareConfig{skip: map[string]bool{}, traceparentOnly: map[string]bool{}}
	for _, o := range opts {
		o(cfg)
	}
	tracer := otel.Tracer("guardian/http")
	prop := propagation.TraceContext{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.skip[r.URL.Path] ||
			(cfg.traceparentOnly[r.URL.Path] && r.Header.Get("traceparent") == "") {
			next.ServeHTTP(w, r)
			return
		}
		ctx := prop.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := tracer.Start(ctx, r.Method+" "+r.URL.Path,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", r.Method),
				attribute.String("url.path", r.URL.Path),
			),
		)
		defer span.End()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r.WithContext(ctx))
		span.SetAttributes(attribute.Int("http.response.status_code", sw.status))
		if sw.status >= 500 {
			span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", sw.status))
		}
	})
}

// SpanAttrs adds string attributes to the active span, if any — the way a
// handler enriches its server span with identity the middleware cannot know
// (who authenticated, which park, what role).
func SpanAttrs(ctx context.Context, kv map[string]string) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	attrs := make([]attribute.KeyValue, 0, len(kv))
	for k, v := range kv {
		attrs = append(attrs, attribute.String(k, v))
	}
	span.SetAttributes(attrs...)
}
