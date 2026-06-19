package main

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

// snapshot is one immutable rendering of the document in every encoding the
// page serves. Handlers only ever copy these bytes out — sub-millisecond,
// zero per-request queries.
type snapshot struct {
	toml, json, yaml, html []byte
}

func newSnapshot(doc Document) (*snapshot, error) {
	t, err := doc.TOML()
	if err != nil {
		return nil, err
	}
	j, err := doc.JSON()
	if err != nil {
		return nil, err
	}
	y, err := doc.YAML()
	if err != nil {
		return nil, err
	}
	return &snapshot{toml: t, json: j, yaml: y, html: renderHTML(t)}, nil
}

type server struct {
	mux  *http.ServeMux
	snap atomic.Pointer[snapshot]
}

func newServer() *server {
	s := &server{mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /{$}", s.serveSnapshot("text/html; charset=utf-8", func(sn *snapshot) []byte { return sn.html }))
	s.mux.HandleFunc("GET /status.toml", s.serveSnapshot("text/plain; charset=utf-8", func(sn *snapshot) []byte { return sn.toml }))
	s.mux.HandleFunc("GET /status.json", s.serveSnapshot("application/json", func(sn *snapshot) []byte { return sn.json }))
	s.mux.HandleFunc("GET /status.yaml", s.serveSnapshot("application/yaml", func(sn *snapshot) []byte { return sn.yaml }))
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	return s
}

// publish swaps in a freshly rendered snapshot; readers never block.
func (s *server) publish(sn *snapshot) {
	s.snap.Store(sn)
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	s.mux.ServeHTTP(w, r)
}

func (s *server) serveSnapshot(contentType string, pick func(*snapshot) []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sn := s.snap.Load()
		if sn == nil {
			http.Error(w, "first snapshot pending", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Write(pick(sn))
	}
}

// handleHealthz backs the kubelet readiness probe: 200 once the first
// snapshot exists. The collector publishes even when every query fails (the
// page renders the failure honestly), so this means "the page exists", and a
// pod never serves a 503 page body to the Gateway.
func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if s.snap.Load() == nil {
		http.Error(w, "first snapshot pending", http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, "ok")
}
