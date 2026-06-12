package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The metrics vecs live on the process-global default registry, so these
// tests assert DELTAS around the action under test and never t.Parallel():
// absolute values accumulate across every test in the package.

// fixtureTransport serves one canned response for every request, the swap
// point being httpClient.Transport (see the comment on httpClient in
// ingest.go), so adapter fetches run against testdata instead of the net.
type fixtureTransport struct {
	status int
	body   []byte
}

func (f fixtureTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: f.status,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(bytes.NewReader(f.body)),
	}, nil
}

// errTransport fails every request at the transport, the "network" branch.
type errTransport struct{}

func (errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

func swapTransport(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	orig := httpClient.Transport
	httpClient.Transport = rt
	t.Cleanup(func() { httpClient.Transport = orig })
}

// In production the pool collectors register inside openStore; tests have
// no database, so register once against an unconnected pool (pgxpool.New is
// lazy) to get the D3 names linted, pinned, and value-checked.
var poolMetricsOnce sync.Once

func registerTestPoolMetrics(t *testing.T) {
	t.Helper()
	poolMetricsOnce.Do(func() {
		pool, err := pgxpool.New(context.Background(), "postgres://aisucks@127.0.0.1:5432/aisucks")
		if err != nil {
			t.Fatalf("pgxpool.New: %v", err)
		}
		registerPoolMetrics(pool)
	})
}

// gatherValue reads one unlabeled gauge/counter from the default registry.
func gatherValue(t *testing.T, name string) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		m := mf.GetMetric()[0]
		if g := m.GetGauge(); g != nil {
			return g.GetValue()
		}
		if c := m.GetCounter(); c != nil {
			return c.GetValue()
		}
	}
	t.Fatalf("metric %s not found", name)
	return 0
}

// fetchDurationCount reads the chatgpt fetch histogram's sample count
// (testutil.ToFloat64 covers only counters and gauges).
func fetchDurationCount(t *testing.T) uint64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "aisucks_fetch_duration_seconds" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "source" && lp.GetValue() == sourceChatGPT {
					return m.GetHistogram().GetSampleCount()
				}
			}
		}
	}
	t.Fatal("aisucks_fetch_duration_seconds{source=chatgpt} not found")
	return 0
}

// D1.v1: promlint-clean names for every aisucks_* metric on the default
// registry. One recorded waiver: aisucks_pgxpool_acquire_count is pinned by
// the spec catalog to mirror pgxpool.Stat().AcquireCount(), and promlint
// wants counters to end in _total.
func TestMetricsLint(t *testing.T) {
	registerTestPoolMetrics(t)
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, mf := range mfs {
		if strings.HasPrefix(mf.GetName(), "aisucks_") {
			names = append(names, mf.GetName())
		}
	}
	problems, err := testutil.GatherAndLint(prometheus.DefaultGatherer, names...)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		if p.Metric == "aisucks_pgxpool_acquire_count" {
			continue // waiver, see above
		}
		t.Errorf("promlint %s: %s", p.Metric, p.Text)
	}
}

// fetchSource stubs ShareSource at the handler boundary so each /report
// outcome is reachable without a transport.
type fetchSource struct {
	conv *Conversation
	err  error
}

func (fetchSource) Match(*url.URL) bool { return true }
func (f fetchSource) Fetch(context.Context, *url.URL) (*Conversation, error) {
	return f.conv, f.err
}

// D1.v2: each /report outcome moves exactly its own reportsTotal series and
// the matching status-code series, including the 413/422/429/500/502 paths.
func TestReportMetricDeltas(t *testing.T) {
	okConv := &Conversation{Provider: "stub", Turns: []Turn{{Role: "user", Content: "x"}}}
	link := "link=https%3A%2F%2Fchatgpt.com%2Fshare%2F11111111-2222-3333-4444-555555555555"
	outcomes := []string{"accepted", "duplicate", "bounced", "parse_failed", "rejected", "ratelimited"}

	cases := []struct {
		name    string
		source  ShareSource // nil keeps the real list (these paths stop before Fetch)
		store   *stubStore
		body    string
		pre     int    // requests burned before the measured one
		code    string // expected aisucks_http_requests_total code label
		outcome string // expected reportsTotal outcome; "" = none (the 5xx paths)
		status  int
	}{
		{"accepted", fetchSource{conv: okConv}, &stubStore{}, link, 0, "200", "accepted", 200},
		{"duplicate", fetchSource{conv: okConv}, &stubStore{duplicate: true}, link, 0, "200", "duplicate", 200},
		{"parse_failed", fetchSource{err: ErrParse}, &stubStore{}, link, 0, "200", "parse_failed", 200},
		{"bounced", fetchSource{err: ErrGone}, &stubStore{}, link, 0, "422", "bounced", 422},
		{"rejected_garbage", nil, &stubStore{}, "link=https%3A%2F%2Fevil.example%2Fshare%2Fabc", 0, "422", "rejected", 422},
		{"rejected_oversize", nil, &stubStore{}, "link=" + strings.Repeat("A", 8<<10), 0, "413", "rejected", 413},
		{"fetch_fault", fetchSource{err: errors.New("upstream timeout")}, &stubStore{}, link, 0, "502", "", 502},
		{"insert_fault", fetchSource{conv: okConv}, &stubStore{insertErr: errors.New("db down")}, link, 0, "500", "", 500},
		{"ratelimited", fetchSource{conv: okConv}, &stubStore{}, link, bucketBurst, "429", "ratelimited", 429},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.source != nil {
				orig := sources
				sources = []ShareSource{c.source}
				t.Cleanup(func() { sources = orig })
			}
			srv := newServer(c.store)
			post := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest("POST", "/report", strings.NewReader(c.body))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.RemoteAddr = "203.0.113.7:55555"
				rec := httptest.NewRecorder()
				srv.ServeHTTP(rec, req)
				return rec
			}
			for i := 0; i < c.pre; i++ {
				post()
			}
			before := make(map[string]float64, len(outcomes))
			for _, o := range outcomes {
				before[o] = testutil.ToFloat64(reportsTotal.WithLabelValues(o))
			}
			codeBefore := testutil.ToFloat64(
				httpRequestsTotal.WithLabelValues(listenerSite, "POST /report", "post", c.code))

			rec := post()
			if rec.Code != c.status {
				t.Fatalf("POST /report = %d, want %d", rec.Code, c.status)
			}
			for _, o := range outcomes {
				want := 0.0
				if o == c.outcome {
					want = 1
				}
				got := testutil.ToFloat64(reportsTotal.WithLabelValues(o)) - before[o]
				if got != want {
					t.Errorf("reportsTotal{outcome=%q} delta = %v, want %v", o, got, want)
				}
			}
			codeGot := testutil.ToFloat64(
				httpRequestsTotal.WithLabelValues(listenerSite, "POST /report", "post", c.code)) - codeBefore
			if codeGot != 1 {
				t.Errorf("requestsTotal{handler=\"POST /report\",code=%q} delta = %v, want 1", c.code, codeGot)
			}
		})
	}
}

// D2.v: the reason/strategy mapping is the drift detector — each fixture
// must land on its designated series, or the next upstream format change
// goes unnoticed the way the v6 soft-404 incident did.
func TestFetchDependencyMetrics(t *testing.T) {
	u, src, err := canonicalShareURL("https://chatgpt.com/share/11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	fixture := func(t *testing.T, name string) []byte {
		t.Helper()
		body, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	fetchDelta := func(t *testing.T, reason string, f func()) float64 {
		t.Helper()
		before := testutil.ToFloat64(fetchTotal.WithLabelValues(sourceChatGPT, reason))
		f()
		return testutil.ToFloat64(fetchTotal.WithLabelValues(sourceChatGPT, reason)) - before
	}

	t.Run("soft404_is_no_conversation", func(t *testing.T) {
		swapTransport(t, fixtureTransport{status: 200, body: fixture(t, "chatgpt_soft404.html")})
		d := fetchDelta(t, "no_conversation", func() {
			if _, err := src.Fetch(context.Background(), u); !errors.Is(err, ErrGone) {
				t.Errorf("Fetch(soft-404) = %v, want ErrGone", err)
			}
		})
		if d != 1 {
			t.Errorf("fetchTotal{reason=no_conversation} delta = %v, want 1", d)
		}
	})

	t.Run("flight_framing_wins_as_flight", func(t *testing.T) {
		swapTransport(t, fixtureTransport{status: 200, body: fixture(t, "chatgpt_share_flight.html")})
		parseBefore := testutil.ToFloat64(parseTotal.WithLabelValues(sourceChatGPT, "flight", "ok"))
		durBefore := fetchDurationCount(t)
		d := fetchDelta(t, "ok", func() {
			if _, err := src.Fetch(context.Background(), u); err != nil {
				t.Errorf("Fetch(flight fixture) = %v", err)
			}
		})
		if d != 1 {
			t.Errorf("fetchTotal{reason=ok} delta = %v, want 1", d)
		}
		if d := testutil.ToFloat64(parseTotal.WithLabelValues(sourceChatGPT, "flight", "ok")) - parseBefore; d != 1 {
			t.Errorf("parseTotal{strategy=flight,result=ok} delta = %v, want 1", d)
		}
		if got := fetchDurationCount(t) - durBefore; got != 1 {
			t.Errorf("fetchDuration sample count delta = %d, want 1", got)
		}
	})

	t.Run("mapping_framing_wins_as_mapping", func(t *testing.T) {
		before := testutil.ToFloat64(parseTotal.WithLabelValues(sourceChatGPT, "mapping", "ok"))
		if _, err := parseChatGPT(string(fixture(t, "chatgpt_share.html"))); err != nil {
			t.Fatal(err)
		}
		if d := testutil.ToFloat64(parseTotal.WithLabelValues(sourceChatGPT, "mapping", "ok")) - before; d != 1 {
			t.Errorf("parseTotal{strategy=mapping,result=ok} delta = %v, want 1", d)
		}
	})

	t.Run("non_200_is_http_status", func(t *testing.T) {
		swapTransport(t, fixtureTransport{status: 403, body: []byte("challenge")})
		d := fetchDelta(t, "http_status", func() {
			if _, err := src.Fetch(context.Background(), u); !errors.Is(err, ErrGone) {
				t.Errorf("Fetch(403) = %v, want ErrGone", err)
			}
		})
		if d != 1 {
			t.Errorf("fetchTotal{reason=http_status} delta = %v, want 1", d)
		}
	})

	t.Run("transport_error_is_network", func(t *testing.T) {
		swapTransport(t, errTransport{})
		d := fetchDelta(t, "network", func() {
			if _, err := src.Fetch(context.Background(), u); !errors.Is(err, ErrGone) {
				t.Errorf("Fetch(refused) = %v, want ErrGone", err)
			}
		})
		if d != 1 {
			t.Errorf("fetchTotal{reason=network} delta = %v, want 1", d)
		}
	})

	// The claude adapter is out of the launch source list but stays compiled;
	// its ok/parse accounting must already mirror chatgpt's, or the day it
	// ships its successes are invisible to UpstreamFetchDegraded's
	// denominator and the failure share reads high.
	t.Run("claude_ok_and_parse_counted", func(t *testing.T) {
		cu, err := url.Parse("https://claude.ai/share/0f5a1c2d-3b4e-4f5a-8b9c-0d1e2f3a4b5c")
		if err != nil {
			t.Fatal(err)
		}
		claudeDelta := func(t *testing.T, reason string, f func()) float64 {
			t.Helper()
			before := testutil.ToFloat64(fetchTotal.WithLabelValues(sourceClaude, reason))
			f()
			return testutil.ToFloat64(fetchTotal.WithLabelValues(sourceClaude, reason)) - before
		}

		swapTransport(t, fixtureTransport{status: 200, body: fixture(t, "claude_share.html")})
		d := claudeDelta(t, "ok", func() {
			if _, err := (claudeSource{}).Fetch(context.Background(), cu); err != nil {
				t.Errorf("Fetch(claude fixture) = %v", err)
			}
		})
		if d != 1 {
			t.Errorf("fetchTotal{source=claude,reason=ok} delta = %v, want 1", d)
		}

		swapTransport(t, fixtureTransport{status: 200, body: []byte("<html>nothing here</html>")})
		d = claudeDelta(t, "parse", func() {
			if _, err := (claudeSource{}).Fetch(context.Background(), cu); !errors.Is(err, ErrParse) {
				t.Errorf("Fetch(claude junk) = %v, want ErrParse", err)
			}
		})
		if d != 1 {
			t.Errorf("fetchTotal{source=claude,reason=parse} delta = %v, want 1", d)
		}
	})
}

// D1.v3 leak lock, charter value 2: a submitted link must never surface in
// any label value. The canary rides a full handler round trip — canonical
// URL, fetch, parse, store — and must then be absent from the entire
// rendered registry.
func TestMetricsLabelLeakCanary(t *testing.T) {
	const canary = "c4n4ry-d15t1nct1v3-0000-4000-8000-feedfacecafe"
	body, err := os.ReadFile("testdata/chatgpt_share.html")
	if err != nil {
		t.Fatal(err)
	}
	swapTransport(t, fixtureTransport{status: 200, body: body})
	srv := newServer(&stubStore{})
	req := httptest.NewRequest("POST", "/report", strings.NewReader("link=https%3A%2F%2Fchatgpt.com%2Fshare%2F"+canary))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "203.0.113.8:44444"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /report = %d, want 200", rec.Code)
	}
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if strings.Contains(mf.String(), canary) {
			t.Errorf("canary %q leaked into metric family %s", canary, mf.GetName())
		}
	}
}

// D3 names report sane values even before any connection exists: max_conns
// is the pool ceiling, never zero (the live end-to-end check is D3.v,
// against the dev VM).
func TestPoolMetricValues(t *testing.T) {
	registerTestPoolMetrics(t)
	if v := gatherValue(t, "aisucks_pgxpool_max_conns"); v <= 0 {
		t.Errorf("aisucks_pgxpool_max_conns = %v, want > 0", v)
	}
	if v := gatherValue(t, "aisucks_pgxpool_acquire_count"); v != 0 {
		t.Errorf("aisucks_pgxpool_acquire_count = %v, want 0 (no DB in tests)", v)
	}
}

// D1.v3: the metrics surface is pinned the way TestMethodAndPathHygiene
// pins the routing surface — every aisucks_* name in the allowlist, every
// label value in its closed set, bounded series count. Adding a metric or a
// label value is a deliberate edit here, never an accident (and never a
// request-derived string: charter value 2).
func TestMetricsSurfacePinned(t *testing.T) {
	registerTestPoolMetrics(t)
	allowedNames := map[string]bool{
		"aisucks_reports_total":                          true,
		"aisucks_http_requests_total":                    true,
		"aisucks_http_request_duration_seconds":          true,
		"aisucks_http_inflight_requests":                 true,
		"aisucks_fetch_duration_seconds":                 true,
		"aisucks_fetch_total":                            true,
		"aisucks_parse_total":                            true,
		"aisucks_pgxpool_acquired_conns":                 true,
		"aisucks_pgxpool_idle_conns":                     true,
		"aisucks_pgxpool_total_conns":                    true,
		"aisucks_pgxpool_max_conns":                      true,
		"aisucks_pgxpool_acquire_duration_seconds_total": true,
		"aisucks_pgxpool_acquire_count":                  true,
	}
	allowedValues := map[string]map[string]bool{
		"listener": {listenerSite: true, listenerHTTP80: true},
		"handler": {
			"GET /{$}": true, "POST /report": true, "GET /healthz": true,
			"GET /livez": true, "/": true, "acme": true, "other": true,
		},
		// promhttp's bounded method set (sanitizeMethod); anything outside
		// it is "unknown", so attacker-chosen methods cannot mint series.
		"method": {
			"get": true, "put": true, "head": true, "post": true,
			"delete": true, "connect": true, "options": true, "notify": true,
			"trace": true, "patch": true, "unknown": true,
		},
		"outcome": {
			"accepted": true, "duplicate": true, "bounced": true,
			"parse_failed": true, "rejected": true, "ratelimited": true,
		},
		"source": {sourceChatGPT: true, sourceClaude: true},
		"reason": {
			"ok": true, "http_status": true, "network": true,
			"body_too_large": true, "no_conversation": true, "parse": true,
		},
		"strategy": {"mapping": true, "flight": true, "none": true},
		"result":   {"ok": true, "miss": true},
	}
	codeRE := regexp.MustCompile(`^[1-5][0-9][0-9]$`)

	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	series := 0
	for _, mf := range mfs {
		name := mf.GetName()
		if !strings.HasPrefix(name, "aisucks_") {
			continue
		}
		if !allowedNames[name] {
			t.Errorf("metric %s is not in the pinned allowlist", name)
		}
		for _, m := range mf.GetMetric() {
			if h := m.GetHistogram(); h != nil {
				series += len(h.GetBucket()) + 3 // +Inf bucket, _sum, _count
			} else {
				series++
			}
			for _, lp := range m.GetLabel() {
				ln, lv := lp.GetName(), lp.GetValue()
				if ln == "code" {
					if !codeRE.MatchString(lv) {
						t.Errorf("%s: code=%q is not a 3-digit status", name, lv)
					}
					continue
				}
				vals, ok := allowedValues[ln]
				if !ok {
					t.Errorf("%s: label %q is not pinned", name, ln)
					continue
				}
				if !vals[lv] {
					t.Errorf("%s: %s=%q is outside the closed set", name, ln, lv)
				}
			}
		}
	}
	if series > 400 {
		t.Errorf("aisucks_* series = %d, want <= 400", series)
	}
}
