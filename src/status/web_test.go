package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, srv *server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func publishedServer(t *testing.T) *server {
	t.Helper()
	srv := newServer()
	snap, err := newSnapshot(sampleDocument())
	if err != nil {
		t.Fatal(err)
	}
	srv.publish(snap)
	return srv
}

func TestHealthzGatesOnFirstSnapshot(t *testing.T) {
	srv := newServer()
	for _, path := range []string{"/healthz", "/", "/status.toml", "/status.json", "/status.yaml"} {
		if rec := get(t, srv, path); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s before first snapshot: got %d, want 503", path, rec.Code)
		}
	}
	snap, err := newSnapshot(sampleDocument())
	if err != nil {
		t.Fatal(err)
	}
	srv.publish(snap)
	if rec := get(t, srv, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("GET /healthz after snapshot: got %d, want 200", rec.Code)
	}
}

func TestEncodingRoutes(t *testing.T) {
	srv := publishedServer(t)
	cases := []struct{ path, contentType string }{
		{"/", "text/html; charset=utf-8"},
		{"/status.toml", "text/plain; charset=utf-8"},
		{"/status.json", "application/json"},
		{"/status.yaml", "application/yaml"},
	}
	for _, c := range cases {
		rec := get(t, srv, c.path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: got %d, want 200", c.path, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != c.contentType {
			t.Errorf("GET %s: Content-Type %q, want %q", c.path, got, c.contentType)
		}
	}
	var parsed map[string]any
	if err := json.Unmarshal(get(t, srv, "/status.json").Body.Bytes(), &parsed); err != nil {
		t.Errorf("/status.json does not parse: %v", err)
	}
}

// TestPageHasNoScript pins the page contract: zero JavaScript, one header
// line, the TOML in one <pre>, exactly three links.
func TestPageHasNoScript(t *testing.T) {
	srv := publishedServer(t)
	page := get(t, srv, "/").Body.String()
	lower := strings.ToLower(page)
	for _, banned := range []string{"<script", "javascript:", " onclick", "<button", "<form", "<input"} {
		if strings.Contains(lower, banned) {
			t.Errorf("page contains %q", banned)
		}
	}
	if got := strings.Count(page, "<a "); got != 3 {
		t.Errorf("page has %d links, want exactly 3", got)
	}
	for _, want := range []string{
		"GUARDIAN INTELLIGENCE — fleet status",
		`href="/status.toml"`,
		`href="/status.json"`,
		`href="/status.yaml"`,
		`name="viewport"`,
		"<pre>",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestUnknownPathIs404(t *testing.T) {
	srv := publishedServer(t)
	if rec := get(t, srv, "/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /nope: got %d, want 404", rec.Code)
	}
}
