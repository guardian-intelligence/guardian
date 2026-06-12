// aisucks.app: paste a ChatGPT/Claude share link where the model said
// something factually wrong; we verify the link is real and store the raw
// transcript. Collect-only v0 — no verification pipeline, no dataset API.
//
// The privacy promise (docs/aisucks/charter.md) is load-bearing in this
// process: no access logs, no IP ever written to storage or stdout, abuse
// signals live and die in process memory.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sys/unix"
)

// logger is the process-wide structured logger: JSON to stderr. The charter
// forbids logging anything request-scoped (no IPs, no URLs), so every call
// site is process-lifecycle only.
var logger = slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("component", "aisucks")

func main() {
	if err := run(); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// listen binds with SO_REUSEPORT so a rolling deploy's old and new pods
// (hostNetwork, one node) hold :80/:443 simultaneously — the kernel
// balances between them and the old one drains away. This is the entire
// zero-downtime-deploy mechanism; there is no proxy in front.
func listen(ctx context.Context, addr string) (net.Listener, error) {
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
		}); err != nil {
			return err
		}
		return serr
	}}
	return lc.Listen(ctx, "tcp", addr)
}

// drainOnShutdown finishes in-flight requests when the pod is terminated
// mid-rollout, bounded under the pod's terminationGracePeriodSeconds.
func drainOnShutdown(ctx context.Context, servers ...*http.Server) {
	<-ctx.Done()
	shCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	for _, s := range servers {
		s.Shutdown(shCtx)
	}
}

// serveDiagnostics serves Prometheus metrics and pprof on loopback ONLY.
// 127.0.0.1:9090 is the host's loopback (the pod is hostNetwork): invisible
// publicly and unpoliced by the node ingress firewall, so an operator scrapes
// or profiles via `kubectl exec`/SSH without opening anything. pprof is
// mounted here explicitly and must never touch the public mux. listen()'s
// SO_REUSEPORT lets the surge pod bind alongside the old one during a
// rolling deploy; scrapes interleave two counter sets for that window
// (accepted — and the reason replicas stays 1, see the deployment manifest).
func serveDiagnostics(ctx context.Context) error {
	mux := http.NewServeMux()
	// Default registry: Go runtime + process collectors come built in, plus
	// the custom counters registered in web.go.
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	ln, err := listen(ctx, "127.0.0.1:9090")
	if err != nil {
		return err
	}
	hs := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go drainOnShutdown(ctx, hs)
	go func() {
		// Diagnostics are not load-bearing for serving; losing them is loud
		// but not fatal.
		if err := hs.Serve(ln); err != http.ErrServerClosed {
			logger.Error("diagnostics listener", "error", err)
		}
	}()
	return nil
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}

	// Postgres and this process converge independently (same `up`, no
	// ordering guarantee), so connecting retries until the database answers
	// rather than crash-looping through kubelet backoff.
	store, err := openStore(ctx, dsn, 5*time.Minute)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		return err
	}

	srv := newServer(store)

	// The stdlib's default Server.ErrorLog writes lines like "TLS handshake
	// error from <client-ip>:..." to stderr. Charter value 2 forbids client
	// IPs in any log stream — and stderr becomes shipped telemetry once the
	// log pipeline lands — so the public-facing servers discard it. The
	// app's own slog lines carry no request-scoped data.
	silenced := log.New(io.Discard, "", 0)

	if err := serveDiagnostics(ctx); err != nil {
		return err
	}

	// DOMAIN selects the serving mode: with a domain, certmagic owns :80
	// (ACME HTTP-01 + redirect) and :443; without one (dev, pre-DNS sites)
	// we serve plain HTTP on LISTEN so the page is reachable by IP.
	domain := os.Getenv("DOMAIN")
	if domain == "" {
		addr := os.Getenv("LISTEN")
		if addr == "" {
			addr = ":80"
		}
		logger.Info("serving plain HTTP", "addr", addr)
		ln, err := listen(ctx, addr)
		if err != nil {
			return err
		}
		hs := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second, ErrorLog: silenced}
		go drainOnShutdown(ctx, hs)
		if err := hs.Serve(ln); err != http.ErrServerClosed {
			return err
		}
		return nil
	}

	certmagic.DefaultACME.Agreed = true
	certmagic.DefaultACME.Email = os.Getenv("ACME_EMAIL")
	if dir := os.Getenv("CERT_DIR"); dir != "" {
		certmagic.Default.Storage = &certmagic.FileStorage{Path: dir}
	}
	logger.Info("serving HTTPS", "domain", domain)

	// Not certmagic.HTTPS: its :80 handler only redirects, and a redirect
	// is something kubelet's readiness probe follows into TLS it can't
	// verify — pods served fine but sat NotReady forever. Serve /healthz
	// plainly on :80 (with the ACME challenge handler), redirect the rest.
	cfg := certmagic.NewDefault()
	issuer := certmagic.NewACMEIssuer(cfg, certmagic.DefaultACME)
	cfg.Issuers = []certmagic.Issuer{issuer}

	mux80 := http.NewServeMux()
	mux80.Handle("GET /healthz", instrument(listenerHTTP80, "GET /healthz", http.HandlerFunc(srv.handleHealthz)))
	mux80.Handle("GET /livez", instrument(listenerHTTP80, "GET /livez", http.HandlerFunc(srv.handleLivez)))
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := "https://" + domain + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	})
	mux80.Handle("/", instrument(listenerHTTP80, "/", redirect))
	// ACME challenge requests answer ahead of the mux and never match a
	// pattern, so they get their own handler label — otherwise every cert
	// renewal looks like 404 noise. A challenge path with a stale token
	// falls through to the same redirect the mux would serve, still counted
	// once, as acme. Everything else takes the mux and its 404 floor (which
	// "/" makes unreachable here; the floor exists for symmetry with the
	// site mux).
	floor80 := requestFloor(listenerHTTP80, mux80)
	acme := instrument(listenerHTTP80, "acme", issuer.HTTPChallengeHandler(redirect))
	h80 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
			acme.ServeHTTP(w, r)
			return
		}
		floor80.ServeHTTP(w, r)
	})
	ln80, err := listen(ctx, ":80")
	if err != nil {
		return err
	}
	hs80 := &http.Server{
		Handler:           h80,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          silenced,
	}
	go func() {
		if err := hs80.Serve(ln80); err != http.ErrServerClosed {
			logger.Error("http listener", "error", err)
			os.Exit(1) // :80 is load-bearing (probes + ACME); dying loudly beats a half-up pod
		}
	}()

	if err := cfg.ManageSync(ctx, []string{domain}); err != nil {
		return fmt.Errorf("certificate for %s: %w", domain, err)
	}
	tlsCfg := cfg.TLSConfig()
	tlsCfg.NextProtos = append([]string{"h2", "http/1.1"}, tlsCfg.NextProtos...)
	ln443, err := listen(ctx, ":443")
	if err != nil {
		return err
	}
	hs443 := &http.Server{
		Handler:           srv,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          silenced,
	}
	go drainOnShutdown(ctx, hs443, hs80)
	if err := hs443.ServeTLS(ln443, "", ""); err != http.ErrServerClosed {
		return err
	}
	return nil
}
