package main

import (
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Serving-, dependency-, and store-plane metrics
// (docs/architecture/metrics.md), all on the default registry and exported
// only by the loopback diagnostics listener (main.go). Every label value is
// drawn from a closed set enumerated here or by promhttp — never from a
// path, URL, or other request-derived string (charter value 2, pinned by
// TestMetricsSurfacePinned).

// listener label values. The loopback diagnostics mux carries neither: the
// collector's own scrapes must not become traffic. In plain-HTTP dev mode
// only "site" exists.
const (
	listenerSite   = "site"   // the public site server (:443, or LISTEN)
	listenerHTTP80 = "http80" // the :80 redirect+ACME server (domain mode)
)

var httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "aisucks_http_requests_total",
	Help: "HTTP requests by listener, mux pattern, method, and status code.",
}, []string{"listener", "handler", "method", "code"})

// The 30s top bucket exists because POST /report holds the synchronous ≤25s
// liveness fetch. Classic buckets, not native histograms: native-histogram
// support across prometheus-receiver → remote-write → VictoriaMetrics is the
// bleeding edge of three projects, and boring wins.
var httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "aisucks_http_request_duration_seconds",
	Help:    "HTTP request duration by listener and mux pattern.",
	Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
}, []string{"listener", "handler"})

var httpInflightRequests = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "aisucks_http_inflight_requests",
	Help: "Requests currently in flight on the site listener.",
})

// instrument wraps one route with the RED pair, curried to a fixed
// listener+handler at registration. handler is the registered mux pattern
// (or "other"/"acme" for the floors), never the request path. Currying at
// registration is the binding shape (metrics.md): an outer wrapper reading
// r.Pattern would mislabel 404/405, which leave it empty — requestFloor
// owns that floor instead. promhttp fills method (its bounded set; methods
// are attacker-controlled input) and code (an implicit 200 is normalized by
// its delegator). The code="500" series is pre-created for the route's own
// method because a counter that has never incremented does not exist in
// PromQL, and an error-rate query over a missing series reads empty —
// which masquerades as health.
func instrument(listener, handler string, h http.Handler) http.Handler {
	labels := prometheus.Labels{"listener": listener, "handler": handler}
	requests := httpRequestsTotal.MustCurryWith(labels)
	method := "get"
	if strings.HasPrefix(handler, http.MethodPost+" ") {
		method = "post"
	}
	requests.WithLabelValues(method, "500")
	return promhttp.InstrumentHandlerCounter(requests,
		promhttp.InstrumentHandlerDuration(httpRequestDuration.MustCurryWith(labels), h))
}

// requestFloor gives requests no mux pattern claims — 404s and
// method-mismatch 405s, both served by mux internals — the bounded identity
// handler="other", so scanner noise is one series instead of one per probed
// path. Matched requests pass straight through to the route wrappers
// installed at registration and are not double-counted.
func requestFloor(listener string, mux *http.ServeMux) http.Handler {
	other := instrument(listener, "other", mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern == "" {
			other.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// Dependency plane: the share-page fetch is the one hard external
// dependency. source names the adapter.
const (
	sourceChatGPT = "chatgpt"
	sourceClaude  = "claude"
)

// fetchReasons is the closed reason set for fetchTotal. The transport
// reasons (http_status, network, body_too_large) are counted at the branch
// points inside fetchPage — ErrGone collapses them in the returned error,
// so the classification cannot be recovered at the handler. ok,
// no_conversation, and parse are decided after extraction, in the adapter's
// Fetch.
var fetchReasons = []string{"ok", "http_status", "network", "body_too_large", "no_conversation", "parse"}

// Buckets end at 20s, the upstream client's timeout.
var fetchDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "aisucks_fetch_duration_seconds",
	Help:    "Upstream share-page fetch duration: request plus body read, not parsing.",
	Buckets: []float64{.1, .25, .5, 1, 2.5, 5, 10, 20},
}, []string{"source"})

var fetchTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "aisucks_fetch_total",
	Help: "Share-page fetch attempts by source and outcome reason.",
}, []string{"source", "reason"})

var parseTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "aisucks_parse_total",
	Help: "Transcript extractions by source, winning strategy, and result.",
}, []string{"source", "strategy", "result"})

func init() {
	// Pre-create the chatgpt fetch/parse sets (the one live source) so the
	// UpstreamFetchDegraded failure-share query reads 0, not empty, on a
	// healthy process.
	for _, reason := range fetchReasons {
		fetchTotal.WithLabelValues(sourceChatGPT, reason)
	}
	fetchDuration.WithLabelValues(sourceChatGPT)
	for _, sr := range [][2]string{{"mapping", "ok"}, {"flight", "ok"}, {"none", "miss"}} {
		parseTotal.WithLabelValues(sourceChatGPT, sr[0], sr[1])
	}
}

// registerPoolMetrics exports pgxpool's own statistics, registered at store
// construction — the pool is non-nil there and the process opens exactly one
// store, so the funcs close over it safely. With go_goroutines these are the
// leak-detection pair the original crash-loop investigation lacked.
// AcquireDuration and AcquireCount are monotonic over the pool's lifetime,
// which is what CounterFunc requires.
func registerPoolMetrics(pool *pgxpool.Pool) {
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "aisucks_pgxpool_acquired_conns",
		Help: "Connections currently checked out of the pool.",
	}, func() float64 { return float64(pool.Stat().AcquiredConns()) })
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "aisucks_pgxpool_idle_conns",
		Help: "Idle connections held by the pool.",
	}, func() float64 { return float64(pool.Stat().IdleConns()) })
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "aisucks_pgxpool_total_conns",
		Help: "Total connections held by the pool.",
	}, func() float64 { return float64(pool.Stat().TotalConns()) })
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "aisucks_pgxpool_max_conns",
		Help: "The pool's connection ceiling.",
	}, func() float64 { return float64(pool.Stat().MaxConns()) })
	promauto.NewCounterFunc(prometheus.CounterOpts{
		Name: "aisucks_pgxpool_acquire_duration_seconds_total",
		Help: "Cumulative time spent acquiring connections from the pool.",
	}, func() float64 { return pool.Stat().AcquireDuration().Seconds() })
	// The name mirrors pgxpool.Stat().AcquireCount() per the spec catalog
	// rather than promlint's _total convention; TestMetricsLint carries the
	// waiver.
	promauto.NewCounterFunc(prometheus.CounterOpts{
		Name: "aisucks_pgxpool_acquire_count",
		Help: "Cumulative connection acquires from the pool.",
	}, func() float64 { return float64(pool.Stat().AcquireCount()) })
}
