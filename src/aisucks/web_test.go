package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// stubStore lets handler tests run without Postgres.
type stubStore struct {
	insertErr error
	duplicate bool // Insert reports created=false, as ON CONFLICT does
	inserted  []Report
}

func (s *stubStore) Insert(_ context.Context, r Report) (bool, error) {
	if s.insertErr != nil {
		return false, s.insertErr
	}
	s.inserted = append(s.inserted, r)
	return !s.duplicate, nil
}
func (s *stubStore) Healthy(context.Context) error { return nil }

// TestAcceptancePageLeaksNoCorpusMembership pins the fix for the membership
// oracle: an accepted submission must render an identical page whether the
// link was new or already present, and must never say "already"/"duplicate"
// — otherwise anyone holding a share link could probe whether it is in the
// corpus (charter value 2).
func TestAcceptancePageLeaksNoCorpusMembership(t *testing.T) {
	srv := newServer(&stubStore{})
	var buf bytes.Buffer
	if err := srv.tmpl.ExecuteTemplate(&buf, "result.html.tmpl", resultData{Kind: "logged"}); err != nil {
		t.Fatal(err)
	}
	body := strings.ToLower(buf.String())
	for _, leak := range []string{"already", "duplicate", "beat you", "someone else"} {
		if strings.Contains(body, leak) {
			t.Errorf("acceptance page reveals corpus membership via %q", leak)
		}
	}
}

// promise is "The promise" from docs/aisucks/charter.md — canonical and
// verbatim home-page copy, changed only by charter amendment.
const promise = "Your chat and chat messages will never be sold to OpenAI, Anthropic, or anyone else. " +
	"Expert human annotators convert a PII-redacted version of your shared link into an exam question " +
	"for the next generation of AI. Learn more about how we protect your privacy and hold AI " +
	"companies accountable."

func TestIndexRenders(t *testing.T) {
	srv := newServer(&stubStore{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{promise, `action="/report"`, `name="link"`} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %q", want)
		}
	}
	if strings.Contains(strings.ToLower(body), "<script") {
		t.Error("index page contains JavaScript; the page must be JS-free")
	}
}

func TestHealthz(t *testing.T) {
	srv := newServer(&stubStore{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d", rec.Code)
	}
}

func TestReportRejectsGarbage(t *testing.T) {
	store := &stubStore{}
	srv := newServer(store)
	req := httptest.NewRequest("POST", "/report", strings.NewReader("link=https%3A%2F%2Fevil.example%2Fshare%2Fabc"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "203.0.113.7:55555"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST garbage = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "NOT A SHARE LINK") {
		t.Error("rejection page missing verdict")
	}
	if len(store.inserted) != 0 {
		t.Error("garbage submission reached the store")
	}
}

func TestReportClosedMode(t *testing.T) {
	t.Setenv("INGEST", "off")
	srv := newServer(&stubStore{})
	req := httptest.NewRequest("POST", "/report", strings.NewReader("link=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "203.0.113.7:55555"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "OPENS SOON") {
		t.Error("closed mode did not serve the closed page")
	}
}

func TestRateLimitBites(t *testing.T) {
	srv := newServer(&stubStore{})
	var last int
	for i := 0; i < bucketBurst+1; i++ {
		req := httptest.NewRequest("POST", "/report", strings.NewReader("link=junk"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "203.0.113.9:1234"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		last = rec.Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("request %d = %d, want 429", bucketBurst+1, last)
	}
}

// TestDuplicateResponseByteIdentical pins the membership-oracle fix at the
// handler level: the created=false path (ON CONFLICT no-op) must produce a
// response byte-identical to a fresh insert — status, headers, and body —
// or a share-link holder could probe corpus membership. The split exists
// ONLY in the loopback metrics counter.
func TestDuplicateResponseByteIdentical(t *testing.T) {
	// Swap the package-level source list for a stub: Match accepts the
	// canonical chatgpt URL shape, Fetch succeeds without touching the net.
	orig := sources
	sources = []ShareSource{stubSource{}}
	t.Cleanup(func() { sources = orig })
	post := func(dup bool) *httptest.ResponseRecorder {
		srv := newServer(&stubStore{duplicate: dup})
		req := httptest.NewRequest("POST", "/report", strings.NewReader("link=https%3A%2F%2Fchatgpt.com%2Fshare%2F11111111-2222-3333-4444-555555555555"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "203.0.113.5:1111"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	fresh, dup := post(false), post(true)
	if fresh.Code != dup.Code {
		t.Errorf("status differs: fresh %d, duplicate %d", fresh.Code, dup.Code)
	}
	if !bytes.Equal(fresh.Body.Bytes(), dup.Body.Bytes()) {
		t.Error("response body differs between fresh insert and duplicate")
	}
}

// stubSource matches everything and returns a minimal parsed conversation.
type stubSource struct{}

func (stubSource) Match(*url.URL) bool { return true }
func (stubSource) Fetch(context.Context, *url.URL) (*Conversation, error) {
	return &Conversation{Provider: "stub", Turns: []Turn{{Role: "user", Content: "x"}}}, nil
}
