package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestChatgptHasConversation(t *testing.T) {
	real, err := os.ReadFile("testdata/chatgpt_share.html")
	if err != nil {
		t.Fatal(err)
	}
	if !chatgptHasConversation(string(real)) {
		t.Error("real share page reported as having no conversation")
	}
	shell, err := os.ReadFile("testdata/chatgpt_soft404.html")
	if err != nil {
		t.Fatal(err)
	}
	if chatgptHasConversation(string(shell)) {
		t.Error("soft-404 shell reported as having a conversation")
	}
	// The shell must also fail extraction outright (it carries no payload).
	if _, err := parseChatGPT(string(shell)); !errors.Is(err, ErrParse) {
		t.Errorf("parseChatGPT(soft-404) = %v, want ErrParse", err)
	}
}

// Method/path hygiene is enforced by the ServeMux pattern set, so malformed
// requests never reach handler logic. Pinned here so a routing change can't
// silently widen the surface.
func TestMethodAndPathHygiene(t *testing.T) {
	srv := newServer(&stubStore{})
	cases := []struct {
		method, path string
		want         int
	}{
		{"GET", "/", http.StatusOK},
		{"GET", "/healthz", http.StatusOK},
		{"GET", "/livez", http.StatusOK},
		{"POST", "/livez", http.StatusMethodNotAllowed},
		{"GET", "/report", http.StatusMethodNotAllowed}, // submit is POST-only
		{"PUT", "/report", http.StatusMethodNotAllowed},
		{"GET", "/wp-login.php", http.StatusNotFound},
		// Diagnostics live ONLY on the loopback listener (main.go): a future
		// route or DefaultServeMux slip exposing them publicly fails here.
		{"GET", "/metrics", http.StatusNotFound},
		{"GET", "/debug/pprof/", http.StatusNotFound},
		{"POST", "/", http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
}

func TestReportBodyCapped(t *testing.T) {
	store := &stubStore{}
	srv := newServer(store)
	huge := "link=" + strings.Repeat("A", 8<<10)
	req := httptest.NewRequest("POST", "/report", strings.NewReader(huge))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "203.0.113.20:9999"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body = %d, want 413", rec.Code)
	}
	if len(store.inserted) != 0 {
		t.Error("oversized submission reached the store")
	}
}
