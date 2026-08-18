package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTraceCompleteRequiresEverySpan(t *testing.T) {
	counts := "1\t1\t1\t1\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "ingest" || password != "secret" {
			t.Fatal("ClickHouse request did not use the configured identity")
		}
		if !strings.Contains(r.URL.Query().Get("query"), "0123456789abcdef0123456789abcdef") {
			t.Fatal("trace query did not bind the expected trace ID")
		}
		for _, span := range []string{
			"POST /api/payments/v1/canary/checkout",
			"canary.browser_to_tigerbeetle",
			"stripe.payment_intent.succeeded",
			"tigerbeetle.project_payment",
		} {
			if !strings.Contains(r.URL.Query().Get("query"), span) {
				t.Fatalf("trace query does not require %q", span)
			}
		}
		_, _ = w.Write([]byte(counts))
	}))
	defer server.Close()
	cfg := config{
		ClickHouseURL:  server.URL,
		ClickHouseUser: "ingest",
		ClickHousePass: "secret",
	}
	complete, err := traceComplete(
		context.Background(),
		cfg,
		"0123456789abcdef0123456789abcdef",
	)
	if err != nil || !complete {
		t.Fatalf("complete=%v err=%v", complete, err)
	}
	counts = "1\t1\t0\t1\n"
	complete, err = traceComplete(
		context.Background(),
		cfg,
		"0123456789abcdef0123456789abcdef",
	)
	if err != nil || complete {
		t.Fatalf("complete=%v err=%v", complete, err)
	}
}

func TestNewTraceContextIsW3CWidth(t *testing.T) {
	traceID, parentID, err := newTraceContext()
	if err != nil {
		t.Fatal(err)
	}
	if len(traceID) != 32 || len(parentID) != 16 {
		t.Fatalf("trace=%q parent=%q", traceID, parentID)
	}
}

func TestBrowserResolutionIsConfinedToTheCanaryOrigin(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pageURL string
		host    string
	}{
		{"apex with path", "https://guardianintelligence.org/api/payments/v1/canary", "guardianintelligence.org"},
		{"explicit port", "https://canary.example.com:8443/x", "canary.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules, err := browserResolverRules(tc.pageURL)
			if err != nil {
				t.Fatalf("browserResolverRules(%q): %v", tc.pageURL, err)
			}
			if !strings.HasPrefix(rules, "MAP * ~NOTFOUND") {
				t.Fatalf("resolution is not deny-by-default: %q", rules)
			}
			// The port must not survive into the rule: Chrome matches the
			// exclusion on hostname, so "host:8443" would exclude nothing
			// and the canary would fail to resolve its own origin.
			if !strings.HasSuffix(rules, "EXCLUDE "+tc.host) {
				t.Fatalf("rules do not admit exactly %q: %q", tc.host, rules)
			}
			// chromedp drives the DevTools endpoint over loopback.
			if !strings.Contains(rules, "EXCLUDE localhost") {
				t.Fatalf("rules do not admit loopback: %q", rules)
			}
		})
	}

	if _, err := browserResolverRules("://not-a-url"); err == nil {
		t.Fatal("a URL with no host must not yield resolver rules; Chrome would resolve nothing and every run would fail identically")
	}
}
