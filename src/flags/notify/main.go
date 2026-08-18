// Package main is feature-flags-notify: the change-notification half of the
// client feature-flag contract. Clients evaluate flags over OFREP (flagd,
// mounted same-origin at /features) and hold a read-only SSE subscription to
// /features/events, which streams the flag-set epoch — a content hash of the
// committed flags.json. The stream carries no flag values and no per-user
// state: one identical event fans out to every subscriber and each client
// re-evaluates through OFREP with its own context, so targeting rules stay
// server-side and a GA flip lands on every open client within about a
// second of the ConfigMap refresh.
//
// The epoch source is the same guardian-flags ConfigMap flagd syncs from:
// git merge -> Flux -> ConfigMap -> kubelet mount refresh -> new epoch. The
// OFREP streaming ADR specifies SSE for exactly this job; when official
// providers ship it, the client-side glue is deleted and this endpoint's
// contract is already the right shape.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	mSubscribers = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "featureflags_notify_subscribers", Help: "Open SSE subscriptions."})
	mEpochChanges = promauto.NewCounter(prometheus.CounterOpts{
		Name: "featureflags_notify_epoch_changes_total", Help: "Flag-set epoch transitions observed."})
	mReadErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "featureflags_notify_read_errors_total", Help: "Failed reads of the flag file."})
)

func envStr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envDur(k string, d time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return v
	}
	return d
}

// hub fans one epoch string out to every subscriber. Sends coalesce: a slow
// receiver only ever sees the latest epoch, never a backlog — intermediate
// values are worthless because the client's reaction is a full re-evaluate.
type hub struct {
	mu    sync.Mutex
	subs  map[chan string]struct{}
	epoch string
}

func newHub() *hub { return &hub{subs: make(map[chan string]struct{})} }

func (h *hub) subscribe() (ch chan string, current string, cancel func()) {
	ch = make(chan string, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	current = h.epoch
	h.mu.Unlock()
	mSubscribers.Inc()
	return ch, current, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
		mSubscribers.Dec()
	}
}

func (h *hub) set(epoch string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if epoch == h.epoch {
		return
	}
	changed := h.epoch != ""
	h.epoch = epoch
	if changed {
		mEpochChanges.Inc()
	}
	for ch := range h.subs {
		select {
		case ch <- epoch:
		default:
			select {
			case <-ch:
			default:
			}
			ch <- epoch
		}
	}
}

func (h *hub) current() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.epoch
}

// watch hashes the flag file on a short poll. The file is a few KB behind a
// kubelet symlink swap; hashing it every second is cheaper than carrying an
// inotify dependency and immune to the swap's rename-vs-write event shape.
func (h *hub) watch(path string, every time.Duration) {
	for ; ; time.Sleep(every) {
		b, err := os.ReadFile(path)
		if err != nil {
			mReadErrors.Inc()
			log.Printf("read %s: %v", path, err)
			continue
		}
		sum := sha256.Sum256(b)
		h.set(hex.EncodeToString(sum[:6]))
	}
}

func (h *hub) serveSSE(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ch, current, cancel := h.subscribe()
	defer cancel()

	send := func(epoch string) {
		fmt.Fprintf(w, "id: %s\nevent: epoch\ndata: %s\n\n", epoch, epoch)
		fl.Flush()
	}
	fmt.Fprint(w, "retry: 3000\n\n")
	// The current epoch always opens the stream: a reconnecting client may
	// have missed a flip, and comparing against its held epoch is its job.
	send(current)

	// Heartbeats keep intermediaries from idling the connection out
	// (Cloudflare drops streams silent for ~100s).
	heartbeat := time.NewTicker(envDur("NOTIFY_HEARTBEAT", 20*time.Second))
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case epoch := <-ch:
			send(epoch)
		case <-heartbeat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			fl.Flush()
		}
	}
}

func main() {
	listen := envStr("NOTIFY_LISTEN", ":8080")
	path := envStr("NOTIFY_FLAGS_PATH", "/etc/flags/flags.json")

	h := newHub()
	go h.watch(path, envDur("NOTIFY_POLL", time.Second))

	mux := http.NewServeMux()
	// Served un-rewritten: the Ingress forwards /features/events verbatim.
	mux.HandleFunc("GET /features/events", h.serveSSE)
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		// Not ready until the first successful hash: a pod that cannot see
		// the flag file must not hold client subscriptions.
		if h.current() == "" {
			http.Error(w, "no epoch yet", http.StatusPreconditionFailed)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("feature-flags-notify listening on %s, watching %s", listen, path)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	srv.Close()
}
