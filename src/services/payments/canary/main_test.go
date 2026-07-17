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
