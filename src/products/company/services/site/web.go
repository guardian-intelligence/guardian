package main

import (
	"fmt"
	"mime"
	"net/http"
	"path"
	"strings"
)

type server struct {
	mux     *http.ServeMux
	assets  map[string][]byte
	metrics *metrics
}

func newServer(assets map[string][]byte, metrics *metrics) *server {
	s := &server{mux: http.NewServeMux(), assets: assets, metrics: metrics}
	s.mux.HandleFunc("GET /healthz", metrics.wrap("GET /healthz", s.handleHealthz))
	s.mux.HandleFunc("GET /livez", metrics.wrap("GET /livez", s.handleLivez))
	s.mux.HandleFunc("GET /", metrics.wrap("GET /", s.handleAsset))
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

func (s *server) handleAsset(w http.ResponseWriter, r *http.Request) {
	key, redirect := assetKey(r.URL.Path)
	if redirect != "" {
		http.Redirect(w, r, redirect, http.StatusMovedPermanently)
		return
	}
	body, ok := s.assets[key]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", cacheControl(key))
	w.Header().Set("Content-Type", contentType(key))
	if r.Method == http.MethodHead {
		return
	}
	w.Write(body)
}

func assetKey(rawPath string) (key string, redirect string) {
	clean := path.Clean("/" + rawPath)
	switch clean {
	case "/":
		return "index.html", ""
	case "/letters":
		return "letters/index.html", ""
	case "/news":
		return "news/index.html", ""
	}
	if strings.HasSuffix(rawPath, "/") {
		return "", strings.TrimRight(clean, "/")
	}
	if strings.HasPrefix(clean, "/letters/") || strings.HasPrefix(clean, "/news/") {
		return strings.TrimPrefix(clean, "/") + "/index.html", ""
	}
	return strings.TrimPrefix(clean, "/"), ""
}

func contentType(key string) string {
	switch path.Ext(key) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	default:
		if typ := mime.TypeByExtension(path.Ext(key)); typ != "" {
			return typ
		}
		return "application/octet-stream"
	}
}

func cacheControl(key string) string {
	if strings.HasPrefix(key, "og/") {
		return "public, max-age=300"
	}
	return "public, max-age=60"
}

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}

func (s *server) handleLivez(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}
