package main

import (
	"fmt"
	"net/http"
)

type server struct {
	mux     *http.ServeMux
	page    []byte
	metrics *metrics
}

func newServer(page []byte, metrics *metrics, domain string) *server {
	s := &server{mux: http.NewServeMux(), page: page, metrics: metrics}
	s.mux.HandleFunc("GET /{$}", metrics.wrap("GET /{$}", s.handleIndex))
	s.mux.HandleFunc("GET /healthz", metrics.wrap("GET /healthz", s.handleHealthz))
	s.mux.HandleFunc("GET /livez", metrics.wrap("GET /livez", s.handleLivez))
	return s
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if r.TLS != nil {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	s.mux.ServeHTTP(w, r)
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(s.page)
}

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}

func (s *server) handleLivez(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}
