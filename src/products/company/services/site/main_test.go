package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicSurface(t *testing.T) {
	srv := newServer(siteAssets, newMetrics())
	tests := []struct {
		name        string
		path        string
		wantStatus  int
		wantBody    string
		contentType string
	}{
		{name: "home", path: "/", wantStatus: http.StatusOK, wantBody: "Guardian Intelligence", contentType: "text/html"},
		{name: "letters", path: "/letters", wantStatus: http.StatusOK, wantBody: "The Coding Agent is the Next Smartphone", contentType: "text/html"},
		{name: "letter", path: "/letters/the-coding-agent-is-the-next-smartphone", wantStatus: http.StatusOK, wantBody: "call them guardians", contentType: "text/html"},
		{name: "og", path: "/og/letters/the-coding-agent-is-the-next-smartphone.svg", wantStatus: http.StatusOK, wantBody: "<svg", contentType: "image/svg+xml"},
		{name: "health", path: "/healthz", wantStatus: http.StatusOK, wantBody: "ok", contentType: "text/plain"},
		{name: "missing", path: "/missing", wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d; want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("body does not contain %q:\n%s", tt.wantBody, rec.Body.String())
			}
			if tt.contentType != "" && !strings.HasPrefix(rec.Header().Get("Content-Type"), tt.contentType) {
				t.Fatalf("content-type = %q; want prefix %q", rec.Header().Get("Content-Type"), tt.contentType)
			}
		})
	}
}

func TestNoRequiredJavaScript(t *testing.T) {
	for path, body := range siteAssets {
		if !strings.HasSuffix(path, ".html") {
			continue
		}
		if strings.Contains(strings.ToLower(string(body)), "<script") {
			t.Fatalf("%s contains a script tag", path)
		}
	}
}

func TestHTTPRedirectKeepsHealthLocal(t *testing.T) {
	srv := newServer(siteAssets, newMetrics())
	h := redirectingHTTP(srv, "guardianintelligence.org")

	health := httptest.NewRecorder()
	h.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d; want 200", health.Code)
	}

	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/letters", nil))
	if page.Code != http.StatusMovedPermanently {
		t.Fatalf("page status = %d; want 301", page.Code)
	}
	if loc := page.Header().Get("Location"); loc != "https://guardianintelligence.org/letters" {
		t.Fatalf("redirect location = %q", loc)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	m := newMetrics()
	srv := newServer(siteAssets, m)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/letters", nil))

	metrics := httptest.NewRecorder()
	m.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	out := metrics.Body.String()
	for _, want := range []string{
		"company_site_build_info",
		`company_site_http_requests_total{handler="GET /",method="GET",code="200"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("metrics missing %q:\n%s", want, out)
		}
	}
}
