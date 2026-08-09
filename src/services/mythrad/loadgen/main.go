// loadgen drives mythrad through scripted load phases and reports
// client-observed latencies. Each session is a webtransport client that
// joins a room, moves, pings, chats, and (in the storm phase) mass-reconnects.
//
// Phases, in order (durations via env):
//
//	ramp:      0 -> SESSIONS at RAMP_RATE/s; move MOVE_HZ, ping 1/s
//	hold:      steady state
//	intents:   every session moves at STORM_MOVE_HZ
//	chat:      every session chats at CHAT_HZ
//	reconnect: all sessions drop simultaneously and re-dial with tokens
//	drain:     close everything, print summary
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"math"
	mrand "math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"
	"github.com/quic-go/quic-go/http3"
	webtransport "github.com/quic-go/webtransport-go"
)

var (
	lgConnected = promauto.NewGauge(prometheus.GaugeOpts{Name: "loadgen_sessions_connected"})
	lgConnects  = promauto.NewCounterVec(prometheus.CounterOpts{Name: "loadgen_connects_total"}, []string{"result"})
	lgTicks     = promauto.NewCounter(prometheus.CounterOpts{Name: "loadgen_ticks_received_total"})
	lgTickBytes = promauto.NewCounter(prometheus.CounterOpts{Name: "loadgen_tick_bytes_total"})
	lgRTT       = promauto.NewHistogram(prometheus.HistogramOpts{
		Name: "loadgen_rtt_seconds", Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1}})
	lgResumeGap = promauto.NewHistogram(prometheus.HistogramOpts{
		Name: "loadgen_resume_gap_seconds", Help: "Disconnect-to-welcome during reconnect storm.",
		Buckets: []float64{.05, .1, .2, .3, .5, .75, 1, 2, 5, 10, 30}})
	lgPresence = promauto.NewCounter(prometheus.CounterOpts{Name: "loadgen_mythra_msgs_total"})
	lgChatRecv = promauto.NewCounter(prometheus.CounterOpts{Name: "loadgen_chat_received_total"})
	lgPhase    = promauto.NewGauge(prometheus.GaugeOpts{Name: "loadgen_phase", Help: "0 idle,1 ramp,2 hold,3 intents,4 chat,5 reconnect,6 drain"})
)

func envInt(k string, d int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return d
}
func envStr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type sessionBot struct {
	idx    int
	room   string
	token  atomic.Value // string
	moveHz atomic.Int64 // milli-Hz to allow fractional
	chatHz atomic.Int64
	drop   chan struct{} // signal: drop now and re-dial
}

type runner struct {
	url      string
	tlsConf  *tls.Config
	bots     []*sessionBot
	wg       sync.WaitGroup
	stopping atomic.Bool
}

func (r *runner) runBot(ctx context.Context, b *sessionBot) {
	defer r.wg.Done()
	for !r.stopping.Load() {
		start := time.Now()
		err := r.oneConnection(ctx, b, start)
		if err != nil && !r.stopping.Load() {
			lgConnects.WithLabelValues("error").Inc()
			time.Sleep(time.Duration(200+mrand.Intn(400)) * time.Millisecond)
		}
	}
}

func (r *runner) oneConnection(ctx context.Context, b *sessionBot, dialStart time.Time) error {
	d := webtransport.Transport{TLSClientConfig: r.tlsConf}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	_, sess, err := d.Dial(cctx, r.url, nil)
	if err != nil {
		return err
	}
	defer sess.CloseWithError(0, "bye")

	stream, err := sess.OpenStreamSync(cctx)
	if err != nil {
		return err
	}
	tok, _ := b.token.Load().(string)
	hello, _ := json.Marshal(map[string]string{
		"type": "hello", "name": fmt.Sprintf("bot-%d", b.idx), "device": "loadgen",
		"token": tok, "room": b.room})
	stream.Write(append(hello, '\n'))

	welcomed := make(chan struct{})
	// control-stream reader
	go func() {
		defer cancel()
		dec := json.NewDecoder(stream)
		for {
			var m map[string]any
			if dec.Decode(&m) != nil {
				return
			}
			switch m["type"] {
			case "welcome":
				if t, ok := m["token"].(string); ok {
					b.token.Store(t)
				}
				select {
				case <-welcomed:
				default:
					close(welcomed)
				}
			case "presence":
				lgPresence.Inc()
			case "chat":
				lgChatRecv.Inc()
			}
		}
	}()

	select {
	case <-welcomed:
		lgConnects.WithLabelValues("ok").Inc()
		if !dialStart.IsZero() {
			lgResumeGap.Observe(time.Since(dialStart).Seconds())
		}
		lgConnected.Inc()
		defer lgConnected.Dec()
	case <-time.After(10 * time.Second):
		return fmt.Errorf("welcome timeout")
	case <-cctx.Done():
		return cctx.Err()
	}

	// datagram reader
	go func() {
		for {
			data, err := sess.ReceiveDatagram(cctx)
			if err != nil {
				return
			}
			var m struct {
				Type string  `json:"type"`
				CT   float64 `json:"ct"`
			}
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			switch m.Type {
			case "tick":
				lgTicks.Inc()
				lgTickBytes.Add(float64(len(data)))
			case "pong":
				lgRTT.Observe(float64(time.Now().UnixMilli())/1000 - m.CT/1000)
			}
		}
	}()

	// actor loop: ping 1/s, move at moveHz, chat at chatHz
	ping := time.NewTicker(time.Second)
	defer ping.Stop()
	act := time.NewTicker(50 * time.Millisecond)
	defer act.Stop()
	var x, y float64
	lastMove, lastChat := time.Now(), time.Now()
	for {
		select {
		case <-cctx.Done():
			return nil
		case <-b.drop:
			return nil // triggers immediate re-dial in runBot
		case <-ping.C:
			p, _ := json.Marshal(map[string]any{"type": "ping", "ct": time.Now().UnixMilli()})
			sess.SendDatagram(p)
		case now := <-act.C:
			if hz := float64(b.moveHz.Load()) / 1000; hz > 0 && now.Sub(lastMove).Seconds() >= 1/hz {
				lastMove = now
				x += math.Sin(float64(now.UnixMilli())) * 3
				y += math.Cos(float64(now.UnixMilli())) * 3
				mv, _ := json.Marshal(map[string]any{"type": "move", "x": x, "y": y})
				sess.SendDatagram(mv)
			}
			if hz := float64(b.chatHz.Load()) / 1000; hz > 0 && now.Sub(lastChat).Seconds() >= 1/hz {
				lastChat = now
				c, _ := json.Marshal(map[string]string{"type": "chat", "body": "woof woof from the load test"})
				stream.Write(append(c, '\n'))
			}
		}
	}
}

func main() {
	var (
		url         = envStr("TARGET_URL", "https://mythrad:4433/wt")
		sessions    = envInt("SESSIONS", 500)
		rooms       = envInt("ROOMS", 20)
		rampRate    = envInt("RAMP_RATE", 25) // sessions/s
		moveHz      = envInt("MOVE_MILLIHZ", 500)
		holdSec     = envInt("HOLD_SEC", 120)
		stormMoveHz = envInt("STORM_MOVE_MILLIHZ", 10000)
		stormSec    = envInt("STORM_SEC", 90)
		chatMilliHz = envInt("CHAT_MILLIHZ", 1000)
		chatSec     = envInt("CHAT_SEC", 90)
		metricsPort = envInt("METRICS_PORT", 9091)
	)

	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
		log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", metricsPort), mux))
	}()

	// docs/loadtest.md: results are VictoriaMetrics series, never only pod
	// logs. Push the whole client registry to vminsert's text-import endpoint
	// every 15s so short phases survive scrape-interval gaps.
	if pushURL := os.Getenv("VM_IMPORT_URL"); pushURL != "" {
		go func() {
			for range time.Tick(15 * time.Second) {
				pushMetrics(pushURL)
			}
		}()
		defer pushMetrics(pushURL)
	}

	r := &runner{
		url: url,
		// In-cluster spike trust model: the server's cert is ephemeral
		// self-signed; the loadgen pins nothing and skips verification.
		tlsConf: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{http3.NextProtoH3}},
	}
	ctx := context.Background()

	log.Printf("loadgen: %d sessions over %d rooms -> %s", sessions, rooms, url)

	// Phase 1: ramp
	lgPhase.Set(1)
	interval := time.Second / time.Duration(rampRate)
	for i := 0; i < sessions; i++ {
		b := &sessionBot{idx: i, room: fmt.Sprintf("park-%d", i%rooms), drop: make(chan struct{})}
		b.moveHz.Store(int64(moveHz))
		r.bots = append(r.bots, b)
		r.wg.Add(1)
		go r.runBot(ctx, b)
		time.Sleep(interval)
	}
	log.Printf("phase ramp done: %d sessions launched", sessions)

	// Phase 2: hold
	lgPhase.Set(2)
	time.Sleep(time.Duration(holdSec) * time.Second)

	// Phase 3: intent storm
	lgPhase.Set(3)
	log.Printf("phase intents: move rate -> %.1f/s per session", float64(stormMoveHz)/1000)
	for _, b := range r.bots {
		b.moveHz.Store(int64(stormMoveHz))
	}
	time.Sleep(time.Duration(stormSec) * time.Second)
	for _, b := range r.bots {
		b.moveHz.Store(int64(moveHz))
	}

	// Phase 4: chat storm
	lgPhase.Set(4)
	log.Printf("phase chat: %.1f msg/s per session", float64(chatMilliHz)/1000)
	for _, b := range r.bots {
		b.chatHz.Store(int64(chatMilliHz))
	}
	time.Sleep(time.Duration(chatSec) * time.Second)
	for _, b := range r.bots {
		b.chatHz.Store(0)
	}

	// Phase 5: reconnect storm — everyone drops at once, re-dials with token
	lgPhase.Set(5)
	log.Print("phase reconnect: dropping all sessions simultaneously")
	for _, b := range r.bots {
		select {
		case b.drop <- struct{}{}:
		default:
		}
	}
	time.Sleep(60 * time.Second)

	// Phase 6: drain
	lgPhase.Set(6)
	log.Print("phase drain")
	r.stopping.Store(true)
	for _, b := range r.bots {
		select {
		case b.drop <- struct{}{}:
		default:
		}
	}
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
	}

	summary(sessions, rooms)
	// Keep serving /metrics briefly so the last scrape lands.
	time.Sleep(45 * time.Second)
}

// pushMetrics exports the default registry in Prometheus text format to a
// VictoriaMetrics import endpoint (vminsert .../api/v1/import/prometheus).
// Labels identifying the run ride as extra_label query params on the URL.
func pushMetrics(url string) {
	var buf bytes.Buffer
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return
	}
	enc := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfs {
		enc.Encode(mf)
	}
	resp, err := http.Post(url, "text/plain", &buf)
	if err != nil {
		log.Printf("metrics push failed: %v", err)
		return
	}
	resp.Body.Close()
}

func summary(sessions, rooms int) {
	type h struct {
		name string
		hist prometheus.Histogram
	}
	log.Printf("SUMMARY sessions=%d rooms=%d", sessions, rooms)
	mfs, _ := prometheus.DefaultGatherer.Gather()
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			switch {
			case m.GetCounter() != nil && mf.GetName() != "":
				log.Printf("SUMMARY %s%v = %.0f", mf.GetName(), m.GetLabel(), m.GetCounter().GetValue())
			case m.GetHistogram() != nil:
				hg := m.GetHistogram()
				log.Printf("SUMMARY %s count=%d sum=%.3f", mf.GetName(), hg.GetSampleCount(), hg.GetSampleSum())
				var lines []string
				for _, b := range hg.GetBucket() {
					lines = append(lines, fmt.Sprintf("le%.3g=%d", b.GetUpperBound(), b.GetCumulativeCount()))
				}
				sort.Strings(lines)
				log.Printf("SUMMARY %s buckets %v", mf.GetName(), lines)
			}
		}
	}
}
