package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// reportsTotal counts submissions to /report by final outcome, exported only
// on the loopback diagnostics listener (main.go). Aggregate counters carry no
// per-report detail — no URL, no submitter — so charter value 2 holds; in
// particular the duplicate count is invisible in any response, which stays
// identical for new and repeat links (the v5 membership-oracle fix).
// parse_failed counts SUBMISSIONS, not distinct URLs: resubmitting a parked
// link increments it again (the dup split only applies on the parsed path).
// ratelimited counts 429 bounces so the funnel sums against
// aisucks_http_requests_total{handler="POST /report"}; the 5xx paths stay
// out of the funnel by design — the 502/500 themselves are the signal,
// counted in the requests counter.
var reportsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "aisucks_reports_total",
	Help: "Report submissions by outcome.",
}, []string{"outcome"})

func init() {
	// Pre-create every label so each series exists from process start;
	// rate() over a counter that appears mid-flight misses its first change.
	for _, o := range []string{"accepted", "duplicate", "bounced", "parse_failed", "rejected", "ratelimited"} {
		reportsTotal.WithLabelValues(o)
	}
}

//go:embed templates/*.tmpl
var templateFS embed.FS

// indexHTML is the landing page, prerendered at development time from the
// TanStack Start app in src/viteplus-monorepo/apps/aisucks-web (pnpm
// generate) and committed. It is static, self-contained (inlined CSS), and
// script-free; every visible string is charter-approved copy.
//
//go:embed web/index.html
var indexHTML []byte

// reportStore is the slice of Store the handlers need; tests substitute a
// stub so handler behavior is testable without Postgres. Insert's created
// flag distinguishes a fresh row from an idempotent duplicate — it feeds the
// metrics counter only and must never influence the response.
type reportStore interface {
	Insert(ctx context.Context, r Report) (created bool, err error)
	Healthy(ctx context.Context) error
}

type server struct {
	store    reportStore
	tmpl     *template.Template
	limiter  *limiter
	mux      *http.ServeMux
	handler  http.Handler
	ingestOn bool
}

func newServer(store reportStore) *server {
	s := &server{
		store:   store,
		tmpl:    template.Must(template.ParseFS(templateFS, "templates/*.tmpl")),
		limiter: newLimiter(),
		mux:     http.NewServeMux(),
		// INGEST=off serves the page with the form marked "opening soon";
		// the skeleton release ships that way so the release loop is proven
		// before any user data can land.
		ingestOn: os.Getenv("INGEST") != "off",
	}
	// Each route is instrumented at registration with its pattern as the
	// handler label (metrics.go); the floor wrapper owns only the unmatched
	// 404/405 noise. The inflight gauge watches the site listener alone, for
	// saturation and drain visibility during rollouts.
	handle := func(pattern string, h http.HandlerFunc) {
		s.mux.Handle(pattern, instrument(listenerSite, pattern, h))
	}
	handle("GET /{$}", s.handleIndex)
	handle("POST /report", s.handleReport)
	handle("GET /healthz", s.handleHealthz)
	handle("GET /livez", s.handleLivez)
	s.handler = promhttp.InstrumentHandlerInFlight(httpInflightRequests,
		requestFloor(listenerSite, s.mux))
	return s
}

// ServeHTTP is deliberately log-free: no access log exists to scrub
// (charter value 2 — no IP is written anywhere, including stdout).
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	s.handler.ServeHTTP(w, r)
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// resultData drives result.html.tmpl. Kind selects the message block.
// Deliberately carries no per-report detail: the acceptance page is
// identical for every submission so it leaks nothing about the corpus.
type resultData struct {
	Kind string
}

func (s *server) handleReport(w http.ResponseWriter, r *http.Request) {
	if !s.ingestOn {
		s.render(w, "result.html.tmpl", resultData{Kind: "closed"})
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !s.limiter.allow(ip) {
		reportsTotal.WithLabelValues("ratelimited").Inc()
		w.WriteHeader(http.StatusTooManyRequests)
		s.render(w, "result.html.tmpl", resultData{Kind: "ratelimited"})
		return
	}

	// The whole form is one short URL; cap the body so a hostile client
	// can't stream megabytes into the parser. 4KB is generous for a share
	// link plus form framing.
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		reportsTotal.WithLabelValues("rejected").Inc()
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		s.render(w, "result.html.tmpl", resultData{Kind: "rejected"})
		return
	}
	u, src, err := canonicalShareURL(r.PostFormValue("link"))
	if err != nil {
		reportsTotal.WithLabelValues("rejected").Inc()
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, "result.html.tmpl", resultData{Kind: "rejected"})
		return
	}

	// The liveness fetch is synchronous and bounded: the submitter waits a
	// few seconds and gets a truthful verdict instead of a fire-and-forget.
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	conv, err := src.Fetch(ctx, u)

	report := Report{ShareURL: u.String(), ParserVersion: parserVersion}
	outcome := "accepted"
	switch {
	case err == nil:
		report.Provider, report.Model = conv.Provider, conv.Model
		report.Status, report.Turns = "stored", conv.Turns
	case errors.Is(err, ErrParse):
		// Live link, extraction miss: keep the URL so a fixed adapter can
		// re-fetch. Provider from the matched source's host.
		report.Provider = providerOf(u.Host)
		report.Status = "parse_failed"
		outcome = "parse_failed"
	case errors.Is(err, ErrGone):
		reportsTotal.WithLabelValues("bounced").Inc()
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, "result.html.tmpl", resultData{Kind: "gone"})
		return
	default:
		// Our own fetch failed (network, timeout): a server-side fault, not a
		// report outcome; the error_total signal for it is the 502 itself.
		w.WriteHeader(http.StatusBadGateway)
		s.render(w, "result.html.tmpl", resultData{Kind: "error"})
		return
	}

	created, ierr := s.store.Insert(r.Context(), report)
	if ierr != nil {
		w.WriteHeader(http.StatusInternalServerError)
		s.render(w, "result.html.tmpl", resultData{Kind: "error"})
		return
	}
	if err == nil && !created {
		// Counted internally only; the rendered page below stays identical
		// for accepted and duplicate (membership-oracle fix, v5).
		outcome = "duplicate"
	}
	reportsTotal.WithLabelValues(outcome).Inc()
	// One identical acceptance page for every outcome we keep — freshly
	// stored, parked for re-parse, or already present. The response must not
	// reveal whether this link was in the corpus or who put it there: anyone
	// holding a share link could otherwise probe its membership (charter
	// value 2). The liveness fetch above already ran for every path, so the
	// timing is dominated by it, not by the insert.
	s.render(w, "result.html.tmpl", resultData{Kind: "logged"})
}

// handleLivez is process-alive only — deliberately no database dependency.
// It backs the kubelet livenessProbe: if liveness coupled to Postgres, a DB
// outage would get a healthy app killed and restarted in a loop. Readiness
// (/healthz) is what gates traffic on DB health.
func (s *server) handleLivez(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "ok")
}

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Healthy(r.Context()); err != nil {
		http.Error(w, "db unreachable", http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, "ok")
}

func (s *server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		// Headers are already written; nothing useful left to send.
		fmt.Fprintf(os.Stderr, "render %s: %v\n", name, err)
	}
}

func providerOf(host string) string {
	switch host {
	case "claude.ai":
		return "anthropic"
	default:
		return "openai"
	}
}
