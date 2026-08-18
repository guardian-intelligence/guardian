package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

type impairment struct {
	latencyMs int
	jitterMs  int
	lossPct   float64
}

type shaper struct {
	mu           sync.Mutex
	imp          impairment
	rng          *rand.Rand
	silenceUntil time.Time
	dropped      int64
	relayed      int64
}

func (s *shaper) set(imp impairment, seed int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.imp = imp
	s.rng = rand.New(rand.NewSource(seed))
}

func (s *shaper) silence(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.silenceUntil = time.Now().Add(d)
}

// plan decides a packet's fate: dropped, or delivered after a delay.
func (s *shaper) plan() (drop bool, delay time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Now().Before(s.silenceUntil) {
		s.dropped++
		return true, 0
	}
	if s.imp.lossPct > 0 && s.rng.Float64()*100 < s.imp.lossPct {
		s.dropped++
		return true, 0
	}
	s.relayed++
	d := s.imp.latencyMs
	if s.imp.jitterMs > 0 {
		d += s.rng.Intn(2*s.imp.jitterMs) - s.imp.jitterMs
	}
	if d < 0 {
		d = 0
	}
	return false, time.Duration(d) * time.Millisecond
}

type flow struct {
	client   *net.UDPAddr
	mu       sync.Mutex
	upstream *net.UDPConn
}

func (f *flow) conn() *net.UDPConn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.upstream
}

func main() {
	listenAddr := envStr("LISTEN_ADDR", "127.0.0.1:14433")
	upstreamAddr := envStr("UPSTREAM_ADDR", "127.0.0.1:4433")
	controlAddr := envStr("CONTROL_ADDR", "127.0.0.1:14434")

	up, err := net.ResolveUDPAddr("udp", upstreamAddr)
	if err != nil {
		log.Fatal(err)
	}
	la, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		log.Fatal(err)
	}
	clientSock, err := net.ListenUDP("udp", la)
	if err != nil {
		log.Fatal(err)
	}

	sh := &shaper{rng: rand.New(rand.NewSource(1))}
	var (
		mu    sync.Mutex
		flows = map[string]*flow{}
	)

	dialUpstream := func(f *flow) error {
		conn, err := net.DialUDP("udp", nil, up)
		if err != nil {
			return err
		}
		f.mu.Lock()
		old := f.upstream
		f.upstream = conn
		f.mu.Unlock()
		if old != nil {
			old.Close()
		}
		// downstream pump for this upstream socket; dies with it
		go func() {
			buf := make([]byte, 65535)
			for {
				n, err := conn.Read(buf)
				if err != nil {
					return
				}
				pkt := append([]byte(nil), buf[:n]...)
				if drop, delay := sh.plan(); !drop {
					time.AfterFunc(delay, func() { clientSock.WriteToUDP(pkt, f.client) })
				}
			}
		}()
		return nil
	}

	// control API
	ctl := http.NewServeMux()
	ctl.HandleFunc("/impair", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		atoi := func(k string) int { v, _ := strconv.Atoi(q.Get(k)); return v }
		loss, _ := strconv.ParseFloat(q.Get("loss_pct"), 64)
		seed := int64(atoi("seed"))
		if seed == 0 {
			seed = 1
		}
		sh.set(impairment{latencyMs: atoi("latency_ms"), jitterMs: atoi("jitter_ms"), lossPct: loss}, seed)
		log.Printf("impair: %s", r.URL.RawQuery)
		fmt.Fprintln(w, "ok")
	})
	ctl.HandleFunc("/silence", func(w http.ResponseWriter, r *http.Request) {
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		sh.silence(time.Duration(ms) * time.Millisecond)
		log.Printf("silence: %dms", ms)
		fmt.Fprintln(w, "ok")
	})
	ctl.HandleFunc("/migrate", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		for _, f := range flows {
			if err := dialUpstream(f); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		log.Printf("migrate: %d flow(s) rebound", len(flows))
		fmt.Fprintln(w, "ok")
	})
	ctl.HandleFunc("/sever", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		for k, f := range flows {
			if c := f.conn(); c != nil {
				c.Close()
			}
			delete(flows, k)
		}
		log.Printf("sever: all flows dropped")
		fmt.Fprintln(w, "ok")
	})
	ctl.HandleFunc("/state", func(w http.ResponseWriter, _ *http.Request) {
		sh.mu.Lock()
		mu.Lock()
		state := map[string]any{
			"flows": len(flows), "latency_ms": sh.imp.latencyMs, "jitter_ms": sh.imp.jitterMs,
			"loss_pct": sh.imp.lossPct, "relayed": sh.relayed, "dropped": sh.dropped,
			"silent": time.Now().Before(sh.silenceUntil),
		}
		mu.Unlock()
		sh.mu.Unlock()
		json.NewEncoder(w).Encode(state)
	})
	go func() { log.Fatal(http.ListenAndServe(controlAddr, ctl)) }()

	log.Printf("netsim: relaying %s -> %s (control %s)", listenAddr, upstreamAddr, controlAddr)
	buf := make([]byte, 65535)
	for {
		n, from, err := clientSock.ReadFromUDP(buf)
		if err != nil {
			log.Fatal(err)
		}
		key := from.String()
		mu.Lock()
		f, ok := flows[key]
		if !ok {
			f = &flow{client: from}
			if err := dialUpstream(f); err != nil {
				mu.Unlock()
				log.Printf("upstream dial: %v", err)
				continue
			}
			flows[key] = f
			log.Printf("flow: %s", key)
		}
		mu.Unlock()
		pkt := append([]byte(nil), buf[:n]...)
		if drop, delay := sh.plan(); !drop {
			if c := f.conn(); c != nil {
				time.AfterFunc(delay, func() { c.Write(pkt) })
			}
		}
	}
}

func envStr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
