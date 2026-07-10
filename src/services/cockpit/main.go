// cockpit — the telemetry pipeline behind the live CPU/memory widget on the
// guardianintelligence.org homepage. One binary, four modes:
//
//	sampler:   reads host /proc/stat and /proc/meminfo at 10 Hz and serves
//	           the raw tick stream (SamplerService) to the hub. Stateless.
//	hub:       merges N sampler streams into a 60-minute in-memory ring and
//	           serves the public CockpitStreamService: burst-on-subscribe
//	           (keyframe + the last second of ticks), then one delta-coded
//	           frame per second on a shared flush ticker. Fan-out is O(1)
//	           in sampling cost; slow subscribers are dropped, never waited
//	           on.
//	synthetic: a sampler that emits deterministic fake ticks (seeded) — the
//	           CI test substrate and the frontend design-time stub.
//	rollup:    subscribes to a hub like any client and persists 1 s
//	           min/max/avg rows into Postgres — the Electric-served warm
//	           tier — pruned to the stream's horizon.
//	events:    polls the repo's main branch (GitHub commits API, ETag
//	           conditional requests) into the cockpit_events timeline —
//	           landed PRs, Kargo promotions, pushes.
//
// Wire-format rationale lives in src/proto/guardian/cockpit/v1/cockpit.proto.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"log/slog"

	// The @ubuntu_noble_base image ships no ca-certificates bundle, so the
	// system cert pool is empty and every public-TLS dial (the events
	// mode's GitHub API polls) fails x509 verification. This blank import
	// embeds the Go team's Mozilla root bundle, used only when the system
	// pool is empty — the alert-relay precedent.
	_ "golang.org/x/crypto/x509roots/fallback"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/cockpit/v1/cockpitv1connect"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cockpit <sampler|hub|synthetic|rollup|events> [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "sampler":
		err = runSampler(os.Args[2:])
	case "hub":
		err = runHub(os.Args[2:])
	case "synthetic":
		err = runSynthetic(os.Args[2:])
	case "rollup":
		err = runRollup(os.Args[2:])
	case "events":
		err = runEvents(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q; want sampler, hub, synthetic, rollup, or events\n", os.Args[1])
		os.Exit(2)
	}
	if err != nil {
		slog.Error("exit", "mode", os.Args[1], "err", err)
		os.Exit(1)
	}
}

func runSampler(args []string) error {
	fs := flag.NewFlagSet("sampler", flag.ExitOnError)
	listen := fs.String("listen", ":9101", "tick stream listen address")
	node := fs.String("node", "", "node name reported in ticks (default: hostname)")
	_ = fs.Parse(args)
	name := *node
	if name == "" {
		var err error
		if name, err = os.Hostname(); err != nil {
			return fmt.Errorf("hostname: %w", err)
		}
	}
	return serveTicks(*listen, name, procfsTicker("/proc/stat", "/proc/meminfo"))
}

func runSynthetic(args []string) error {
	fs := flag.NewFlagSet("synthetic", flag.ExitOnError)
	listen := fs.String("listen", ":9101", "tick stream listen address")
	node := fs.String("node", "synthetic-0", "node name reported in ticks")
	seed := fs.Uint64("seed", 1, "deterministic generator seed")
	_ = fs.Parse(args)
	return serveTicks(*listen, *node, newSynthGen(*seed).next)
}

func serveTicks(listen, node string, next tickFunc) error {
	svc := newSamplerService(node)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.run(ctx, next)

	mux := http.NewServeMux()
	path, handler := cockpitv1connect.NewSamplerServiceHandler(svc)
	mux.Handle(path, handler)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	slog.Info("sampler listening", "addr", listen, "node", node)
	return serveUntilSignal(ctx, cancel, listen, mux)
}

// repeatedFlag collects a repeatable string flag.
type repeatedFlag []string

func (f *repeatedFlag) String() string { return strings.Join(*f, ",") }

func (f *repeatedFlag) Set(v string) error {
	*f = append(*f, v)
	return nil
}

func runHub(args []string) error {
	fs := flag.NewFlagSet("hub", flag.ExitOnError)
	listen := fs.String("listen", ":8080", "public stream listen address")
	var samplers repeatedFlag
	fs.Var(&samplers, "sampler", "sampler address (host:port or URL); repeat per node")
	_ = fs.Parse(args)
	if len(samplers) == 0 {
		return errors.New("at least one --sampler is required")
	}

	m := &hubMetrics{}
	h := newHub(len(samplers), m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.run(ctx)
	for i, addr := range samplers {
		go h.runSamplerClient(ctx, i, addr)
	}

	mux := http.NewServeMux()
	path, handler := cockpitv1connect.NewCockpitStreamServiceHandler(&hubService{hub: h})
	mux.Handle(path, handler)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// In-cluster only: the public Ingress routes the Connect path, never
	// this one.
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m.render(w)
	})
	slog.Info("hub listening", "addr", *listen, "samplers", samplers.String())
	return serveUntilSignal(ctx, cancel, *listen, mux)
}

// serveUntilSignal runs an h2c server whose request contexts descend from
// ctx: canceling it (SIGTERM) ends every long-lived stream handler, which is
// what lets Shutdown's drain actually finish. Streaming responses also mean
// no Read/WriteTimeout — only the header read is deadline-bounded.
func serveUntilSignal(ctx context.Context, cancel context.CancelFunc, addr string, handler http.Handler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h2c.NewHandler(handler, &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
		case <-stop:
		}
		slog.Info("shutting down")
		cancel()
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-done
	return nil
}
