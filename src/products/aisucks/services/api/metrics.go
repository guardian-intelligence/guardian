package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
)

type metricKey struct {
	handler string
	method  string
	code    int
}

type metrics struct {
	mu       sync.Mutex
	requests map[metricKey]uint64
	inflight atomic.Int64
}

func newMetrics() *metrics {
	return &metrics{requests: make(map[metricKey]uint64)}
}

func (m *metrics) wrap(handler string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m.inflight.Add(1)
		defer m.inflight.Add(-1)

		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(rw, r)

		method := r.Method
		if r.Method == http.MethodHead {
			method = http.MethodGet
		}
		m.mu.Lock()
		m.requests[metricKey{handler: handler, method: method, code: rw.status}]++
		m.mu.Unlock()
	}
}

func (m *metrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintln(w, "# HELP aisucks_build_info Build metadata for the aisucks API.")
	fmt.Fprintln(w, "# TYPE aisucks_build_info gauge")
	fmt.Fprintln(w, `aisucks_build_info{version="0.1.0"} 1`)
	fmt.Fprintln(w, "# HELP aisucks_http_inflight_requests In-flight public HTTP requests.")
	fmt.Fprintln(w, "# TYPE aisucks_http_inflight_requests gauge")
	fmt.Fprintf(w, "aisucks_http_inflight_requests %d\n", m.inflight.Load())
	fmt.Fprintln(w, "# HELP aisucks_http_requests_total Public HTTP requests by handler, method, and status code.")
	fmt.Fprintln(w, "# TYPE aisucks_http_requests_total counter")

	m.mu.Lock()
	keys := make([]metricKey, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].handler != keys[j].handler {
			return keys[i].handler < keys[j].handler
		}
		if keys[i].method != keys[j].method {
			return keys[i].method < keys[j].method
		}
		return keys[i].code < keys[j].code
	})
	for _, k := range keys {
		fmt.Fprintf(w, "aisucks_http_requests_total{handler=%q,method=%q,code=%q} %d\n",
			k.handler, k.method, strconv.Itoa(k.code), m.requests[k])
	}
	m.mu.Unlock()
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func newDiagServer(metrics *metrics) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	return mux
}
