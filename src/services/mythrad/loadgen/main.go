// The Wake Up Mythra load driver, journal protocol (docs/netcode.md):
// bots authenticate through the real admission path (Keycloak client
// credentials -> POST /session -> ticketed hello), receive the event
// stream, and act at human rate, exercising fan-out, admission, and
// intent flow. The driver asserts liveness (intent -> visible latency),
// not correctness: correctness is certified offline by replaying the
// journal against stored snapshots (docs/netcode.md).
//
// The connect path sits behind a circuit breaker: consecutive dial
// failures open it and pause the fleet, so a struggling server sees a
// back-off, never a retry storm.
//
// Results land in VictoriaMetrics via VM_IMPORT_URL pushes, never only in
// pod logs (docs/loadtest.md).
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go/http3"
	webtransport "github.com/quic-go/webtransport-go"
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

// ---------- metrics (pushed as Prometheus text) ----------

var (
	lgSessions atomic.Int64
	lgBytes    atomic.Int64
	lgEvents   atomic.Int64
	lgIntents  atomic.Int64
	lgRejects  atomic.Int64
	lgBreaker  atomic.Int64
	lgConnects sync.Map // result -> *atomic.Int64
	latMu      sync.Mutex
	latSamples []float64 // intent -> visible seconds
)

func counter(m *sync.Map, key string) *atomic.Int64 {
	v, _ := m.LoadOrStore(key, &atomic.Int64{})
	return v.(*atomic.Int64)
}

func metricsText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "lg_sessions %d\n", lgSessions.Load())
	fmt.Fprintf(&b, "lg_bytes_total %d\n", lgBytes.Load())
	fmt.Fprintf(&b, "lg_events_total %d\n", lgEvents.Load())
	fmt.Fprintf(&b, "lg_intents_total %d\n", lgIntents.Load())
	fmt.Fprintf(&b, "lg_rejects_total %d\n", lgRejects.Load())
	fmt.Fprintf(&b, "lg_breaker_open %d\n", lgBreaker.Load())
	lgConnects.Range(func(k, v any) bool {
		fmt.Fprintf(&b, "lg_connects_total{result=%q} %d\n", k, v.(*atomic.Int64).Load())
		return true
	})
	latMu.Lock()
	samples := append([]float64(nil), latSamples...)
	latMu.Unlock()
	if len(samples) > 0 {
		sort.Float64s(samples)
		q := func(p float64) float64 { return samples[min(len(samples)-1, int(p*float64(len(samples))))] }
		fmt.Fprintf(&b, "lg_intent_visible_seconds{q=\"0.5\"} %.4f\n", q(0.5))
		fmt.Fprintf(&b, "lg_intent_visible_seconds{q=\"0.99\"} %.4f\n", q(0.99))
		fmt.Fprintf(&b, "lg_intent_visible_seconds{q=\"1.0\"} %.4f\n", samples[len(samples)-1])
		fmt.Fprintf(&b, "lg_intent_visible_count %d\n", len(samples))
	}
	return b.String()
}

func pushMetrics(importURL string) {
	if importURL == "" {
		return
	}
	for range time.Tick(15 * time.Second) {
		req, _ := http.NewRequest("POST", importURL, strings.NewReader(metricsText()))
		if resp, err := http.DefaultClient.Do(req); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
}

// ---------- token + tickets ----------

type tokenSource struct {
	tokenURL, clientID, secret string
	mu                         sync.Mutex
	token                      string
	sub                        string
	exp                        time.Time
}

func (t *tokenSource) get(ctx context.Context) (string, string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Now().Before(t.exp) {
		return t.token, t.sub, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {t.clientID}, "client_secret": {t.secret}}
	req, _ := http.NewRequestWithContext(ctx, "POST", t.tokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", "", fmt.Errorf("token endpoint %d: %s", resp.StatusCode, body)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", "", err
	}
	t.token = tok.AccessToken
	t.exp = time.Now().Add(time.Duration(tok.ExpiresIn/2) * time.Second)
	if parts := strings.Split(tok.AccessToken, "."); len(parts) == 3 {
		if raw, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var claims struct {
				Sub string `json:"sub"`
			}
			json.Unmarshal(raw, &claims)
			t.sub = claims.Sub
		}
	}
	return t.token, t.sub, nil
}

// ---------- circuit breaker ----------

var (
	consecFails atomic.Int64
	openUntil   atomic.Int64 // unix nanos
)

func breakerAllow() bool {
	if time.Now().UnixNano() < openUntil.Load() {
		return false
	}
	lgBreaker.Store(0)
	return true
}

func breakerReport(ok bool) {
	if ok {
		consecFails.Store(0)
		return
	}
	if consecFails.Add(1) >= 25 {
		openUntil.Store(time.Now().Add(30 * time.Second).UnixNano())
		consecFails.Store(0)
		lgBreaker.Store(1)
		log.Print("circuit breaker OPEN: pausing dials for 30s")
	}
}

// ---------- the bot ----------

func dogID(sub string) uint64 {
	f := fnv.New64a()
	f.Write([]byte(sub))
	return f.Sum64()
}

type bot struct {
	idx         int
	park        string
	sessionURL  string
	targetURL   string
	terrainBase string // "<http base>/terrain/"
	toks        *tokenSource
	actSec      int

	seq       int64
	sinceTick uint64
	dims      atomic.Uint32 // w<<16 | h, decoded from the terrain blob
	intentID  atomic.Uint64
	pending   sync.Map // id -> time.Time
	sub       string
}

// terrainDims caches w<<16|h per terrain hex process-wide, so the fleet
// decodes each artifact's header once instead of per bot.
var terrainDims sync.Map

func (b *bot) loadDims(hex string) {
	if hex == "" {
		return
	}
	if v, ok := terrainDims.Load(hex); ok {
		b.dims.Store(v.(uint32))
		return
	}
	blob, err := b.fetchTerrain(hex)
	if err != nil || len(blob) < 12 {
		return
	}
	d := uint32(binary.LittleEndian.Uint16(blob[8:10]))<<16 | uint32(binary.LittleEndian.Uint16(blob[10:12]))
	terrainDims.Store(hex, d)
	b.dims.Store(d)
}

func (b *bot) fetchTerrain(hex string) ([]byte, error) {
	return fetchBytes(b.terrainBase + hex)
}

func fetchBytes(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil || len(body) < 8 {
		return nil, fmt.Errorf("%s: %v (%d bytes)", url, err, len(body))
	}
	return body, nil
}

// errLog rate-limits connect-error logging to one line per few seconds
// across the fleet: enough to diagnose, not enough to flood.
var lastErrLog atomic.Int64

func (b *bot) runForever(ctx context.Context) {
	backoff := 300 * time.Millisecond
	for ctx.Err() == nil {
		if !breakerAllow() {
			time.Sleep(time.Second + time.Duration(rand.Intn(500))*time.Millisecond)
			continue
		}
		err := b.runOnce(ctx)
		// Report the outcome exactly once: a clean return (session held then
		// closed) is a success, only an error trips the breaker.
		breakerReport(err == nil)
		if err != nil {
			counter(&lgConnects, "error").Add(1)
			now := time.Now().Unix()
			if prev := lastErrLog.Load(); now-prev >= 3 && lastErrLog.CompareAndSwap(prev, now) {
				log.Printf("bot %d connect error: %v", b.idx, err)
			}
		}
		time.Sleep(backoff + time.Duration(rand.Int63n(int64(backoff))))
		backoff = min(backoff*2, 10*time.Second)
	}
}

func (b *bot) runOnce(ctx context.Context) error {
	bearer, sub, err := b.toks.get(ctx)
	if err != nil {
		counter(&lgConnects, "err_token").Add(1)
		return fmt.Errorf("token: %w", err)
	}
	b.sub = sub + "/bot-" + strconv.Itoa(b.idx)
	q := url.Values{"park": {b.park}, "bot": {strconv.Itoa(b.idx)}}
	req, _ := http.NewRequestWithContext(ctx, "POST", b.sessionURL+"?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		counter(&lgConnects, "err_mint_net").Add(1)
		return fmt.Errorf("mint: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		counter(&lgConnects, "err_mint_"+strconv.Itoa(resp.StatusCode)).Add(1)
		return fmt.Errorf("mint %d: %s", resp.StatusCode, body)
	}
	var sess struct {
		Ticket   string `json:"ticket"`
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		return err
	}

	d := webtransport.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{http3.NextProtoH3}},
	}
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	_, wtSess, err := d.Dial(dialCtx, b.targetURL, nil)
	cancel()
	if err != nil {
		counter(&lgConnects, "err_dial").Add(1)
		return fmt.Errorf("dial: %w", err)
	}
	defer wtSess.CloseWithError(0, "bye")

	stream, err := wtSess.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	hello, _ := json.Marshal(map[string]any{
		"type": "hello", "proto": 3, "ticket": sess.Ticket,
		"since_seq": b.seq, "since_tick": b.sinceTick,
	})
	if _, err := stream.Write(append(hello, '\n')); err != nil {
		return err
	}
	breakerReport(true)
	counter(&lgConnects, "ok").Add(1)
	lgSessions.Add(1)
	defer lgSessions.Add(-1)

	send := func(o map[string]any) {
		msg, _ := json.Marshal(o)
		stream.Write(append(msg, '\n'))
	}
	sendIntent := func(kind uint16, payload []byte) {
		id := b.intentID.Add(1)
		b.pending.Store(id, time.Now())
		lgIntents.Add(1)
		send(map[string]any{"type": "intent", "id": id, "kind": kind, "p": payload})
	}
	myDogPayload := func() []byte {
		var p [8]byte
		binary.LittleEndian.PutUint64(p[:], dogID(b.sub))
		return p[:]
	}

	done := make(chan struct{})
	defer close(done)
	var present atomic.Bool
	boosting := false // owned by the act goroutine

	// act at human rate; checks pull from the shared replica
	go func() {
		act := time.NewTicker(time.Duration(b.actSec)*time.Second + time.Duration(rand.Intn(1000*b.actSec))*time.Millisecond)
		defer act.Stop()
		for {
			select {
			case <-done:
				return
			case <-act.C:
				// Human-rate intents: mostly movement orders, with enough
				// leave/join churn to exercise the roster and spawn paths,
				// and boost toggles so held-button state rides replay,
				// snapshots, and hash checks under load.
				switch {
				case !present.Load():
					present.Store(true)
					boosting = false
					sendIntent(1, myDogPayload())
				case rand.Intn(10) < 3:
					present.Store(false)
					sendIntent(2, myDogPayload())
				case rand.Intn(10) < 2:
					boosting = !boosting
					p := append(myDogPayload(), 0)
					if boosting {
						p[8] = 1
					}
					sendIntent(8, p)
				default:
					dims := b.dims.Load()
					w, h := dims>>16, dims&0xFFFF
					if w == 0 || h == 0 {
						continue
					}
					p := append(myDogPayload(), 0, 0)
					binary.LittleEndian.PutUint16(p[8:], uint16(rand.Intn(int(w*h))))
					sendIntent(4, p)
				}
			}
		}
	}()
	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 512*1024), 512*1024)
	joined := false
	for sc.Scan() {
		lgBytes.Add(int64(len(sc.Bytes()) + 1))
		var m struct {
			Type    string `json:"type"`
			Seq     int64  `json:"seq"`
			Tick    uint64 `json:"tick"`
			Actor   string `json:"actor"`
			Intent  uint64 `json:"intent"`
			Terrain string `json:"terrain"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Type {
		case "welcome":
			b.loadDims(m.Terrain)
			if !joined {
				joined = true
				present.Store(true)
				sendIntent(1, myDogPayload())
			}
		case "snapshot":
			b.seq = m.Seq
			b.sinceTick = m.Tick
		case "event":
			lgEvents.Add(1)
			b.seq = m.Seq
			b.sinceTick = m.Tick
			if m.Actor == b.sub {
				if v, ok := b.pending.LoadAndDelete(m.Intent); ok {
					latMu.Lock()
					latSamples = append(latSamples, time.Since(v.(time.Time)).Seconds())
					latMu.Unlock()
				}
			}
		case "reject":
			lgRejects.Add(1)
			b.pending.Delete(m.Intent)
		}
	}
	return sc.Err()
}

func main() {
	sessions := envInt("SESSIONS", 100)
	parksN := envInt("PARKS", 1)
	rampRate := envInt("RAMP_RATE", 50)
	holdSec := envInt("HOLD_SEC", 300)
	actSec := envInt("ACT_SEC", 30)
	targetURL := envStr("TARGET_URL", "https://mythrad:4433/wt")
	sessionURL := envStr("SESSION_URL", "http://mythrad.tenant-guardian-prod.svc/session")
	importURL := os.Getenv("VM_IMPORT_URL")
	secretFile := envStr("CLIENT_SECRET_FILE", "/etc/loadgen/client-secret")

	secret, err := os.ReadFile(secretFile)
	if err != nil {
		log.Fatalf("client secret: %v", err)
	}
	toks := &tokenSource{
		tokenURL: envStr("TOKEN_URL", "https://auth.wakeupmythra.com/realms/wakeupmythra.com/protocol/openid-connect/token"),
		clientID: envStr("CLIENT_ID", "mythra-loadgen"),
		secret:   strings.TrimSpace(string(secret)),
	}

	go pushMetrics(importURL)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(holdSec)*time.Second+10*time.Minute)
	defer cancel()

	log.Printf("loadgen: %d sessions across %d park(s), ramp %d/s, hold %ds, act %ds",
		sessions, parksN, rampRate, holdSec, actSec)

	var wg sync.WaitGroup
	interval := time.Second / time.Duration(max(1, rampRate))
	for i := 0; i < sessions; i++ {
		// Single-park runs may target a named park (e.g. the public
		// park-mythra) instead of the numbered load-park convention.
		park := fmt.Sprintf("park-%d", i%parksN)
		if name := envStr("PARK_NAME", ""); name != "" && parksN == 1 {
			park = name
		}
		b := &bot{
			idx: i, park: park,
			sessionURL: sessionURL, targetURL: targetURL,
			terrainBase: strings.TrimSuffix(sessionURL, "/session") + "/terrain/",
			toks:        toks,
			actSec:      actSec,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.runForever(ctx)
		}()
		time.Sleep(interval)
	}

	holdEnd := time.After(time.Duration(holdSec) * time.Second)
	<-holdEnd
	log.Printf("hold complete; final metrics:\n%s", metricsText())
	cancel()
	wgWait(&wg, 30*time.Second)
}

func wgWait(wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}
