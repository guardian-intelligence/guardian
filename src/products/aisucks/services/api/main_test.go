package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicSurface(t *testing.T) {
	srv := newServer([]byte("<!doctype html><p>never be sold</p>"), newMetrics(), "aisucks.test")

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantBody   string
	}{
		{name: "page", method: http.MethodGet, path: "/", wantStatus: http.StatusOK, wantBody: "never be sold"},
		{name: "health", method: http.MethodGet, path: "/healthz", wantStatus: http.StatusOK, wantBody: "ok"},
		{name: "live", method: http.MethodGet, path: "/livez", wantStatus: http.StatusOK, wantBody: "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d; want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("body %q does not contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestHTTPRedirectKeepsHealthLocal(t *testing.T) {
	srv := newServer([]byte("page"), newMetrics(), "aisucks.test")
	h := redirectingHTTP(srv, "aisucks.test")

	health := httptest.NewRecorder()
	h.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d; want 200", health.Code)
	}

	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusMovedPermanently {
		t.Fatalf("page status = %d; want 301", page.Code)
	}
	if loc := page.Header().Get("Location"); loc != "https://aisucks.test/" {
		t.Fatalf("redirect location = %q", loc)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	m := newMetrics()
	srv := newServer([]byte("page"), m, "aisucks.test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	metrics := httptest.NewRecorder()
	m.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	out := metrics.Body.String()
	for _, want := range []string{
		"aisucks_build_info",
		`aisucks_http_requests_total{handler="GET /healthz",method="GET",code="200"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("metrics missing %q:\n%s", want, out)
		}
	}
}
