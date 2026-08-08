// Package main is presenced: a room-scoped presence and tick service over
// WebTransport, instrumented for load testing.
//
// Protocol (all JSON for the spike-grade version; binary comes with the real
// delta encoder):
//
//	client -> server stream:   {"type":"hello","name","device","token","room"}
//	                           {"type":"join","room":"park-17"}
//	                           {"type":"chat","body":"..."}
//	server -> client stream:   {"type":"welcome","you":{...},"token":"..."}
//	                           {"type":"presence","room":"...","players":[...]}
//	                           {"type":"chat","from":"...","body":"...","st":...}
//	client -> server datagram: {"type":"ping","ct":...}
//	                           {"type":"move","x":...,"y":...}
//	server -> client datagram: {"type":"tick","n":...,"st":...,"pos":[[id,x,y],...]}
//	                           {"type":"pong","ct":...,"st":...,"addr":"..."}
package main

import (
	"math"
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/quic-go/quic-go/http3"
	webtransport "github.com/quic-go/webtransport-go"
)

var (
	mTickDur = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "presence_tick_duration_seconds",
		Help:    "Wall time of one full tick (all rooms): budget is 1/tickrate.",
		Buckets: []float64{.0005, .001, .0025, .005, .01, .02, .03, .0417, .06, .1, .25},
	})
	mSessions = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "presence_sessions", Help: "Connected sessions."})
	mRooms = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "presence_rooms_occupied", Help: "Rooms with at least one occupant."})
	mHandshakes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "presence_handshakes_total", Help: "Session handshakes."}, []string{"result"})
	mResumes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "presence_resumes_total", Help: "Token-resumed sessions."}, []string{"result"})
	mIntents = promauto.NewCounter(prometheus.CounterOpts{
		Name: "presence_intents_total", Help: "State-change intents applied (join+move)."})
	mChat = promauto.NewCounter(prometheus.CounterOpts{
		Name: "presence_chat_total", Help: "Chat messages accepted."})
	mChatFanout = promauto.NewCounter(prometheus.CounterOpts{
		Name: "presence_chat_fanout_total", Help: "Chat messages delivered (accepted x occupancy)."})
	mDgSent = promauto.NewCounter(prometheus.CounterOpts{
		Name: "presence_datagrams_sent_total", Help: "Datagrams sent (ticks+pongs)."})
	mDgBytes = promauto.NewCounter(prometheus.CounterOpts{
		Name: "presence_datagram_bytes_total", Help: "Datagram payload bytes sent."})
	mDrops = promauto.NewCounter(prometheus.CounterOpts{
		Name: "presence_fanout_dropped_total", Help: "Stream messages dropped on stalled clients."})
	mDgErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "presence_datagram_errors_total", Help: "SendDatagram failures (oversize/blocked)."})
	mStreamSent = promauto.NewCounter(prometheus.CounterOpts{
		Name: "presence_stream_msgs_sent_total", Help: "Reliable-stream messages sent."})
)

func envInt(k string, d int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return d
}

type Player struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Device   string  `json:"device"`
	Room     string  `json:"room"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	LastSeen int64   `json:"lastSeen"`

	token string
	sess  *webtransport.Session
	out   chan []byte
}

type Hub struct {
	mu      sync.Mutex
	players map[string]*Player          // by token
	rooms   map[string]map[*Player]bool // room -> occupants
	tick    uint64
	tickHz  int
}

func nowMs() int64 { return time.Now().UnixMilli() }

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func newHub(tickHz int) *Hub {
	return &Hub{players: map[string]*Player{}, rooms: map[string]map[*Player]bool{}, tickHz: tickHz}
}

func (h *Hub) moveLocked(p *Player, room string) {
	if cur, ok := h.rooms[p.Room]; ok {
		delete(cur, p)
		if len(cur) == 0 {
			delete(h.rooms, p.Room)
		}
	}
	p.Room = room
	if room != "" {
		if h.rooms[room] == nil {
			h.rooms[room] = map[*Player]bool{}
		}
		h.rooms[room][p] = true
	}
	mRooms.Set(float64(len(h.rooms)))
}

// broadcastRoom sends a room-scoped presence snapshot to that room's occupants.
func (h *Hub) broadcastRoom(room string) {
	h.mu.Lock()
	occ := h.rooms[room]
	list := make([]*Player, 0, len(occ))
	for p := range occ {
		list = append(list, p)
	}
	msg, _ := json.Marshal(map[string]any{"type": "presence", "room": room, "players": list, "st": nowMs()})
	msg = append(msg, '\n')
	for p := range occ {
		h.sendLocked(p, msg)
	}
	h.mu.Unlock()
}

func (h *Hub) sendLocked(p *Player, msg []byte) {
	if p.out == nil {
		return
	}
	select {
	case p.out <- msg:
		mStreamSent.Inc()
	default:
		mDrops.Inc()
	}
}

// tickLoop is the hot path: per room, marshal the position blob once, then
// send it to each occupant. Fanout cost = sum over rooms of occupancy².
func (h *Hub) tickLoop() {
	interval := time.Second / time.Duration(h.tickHz)
	for range time.Tick(interval) {
		start := time.Now()
		h.mu.Lock()
		h.tick++
		st := nowMs()
		for _, occ := range h.rooms {
			// Cap the interest set so the datagram stays under one MTU
			// (~1200B): 30 entries of quantized coords is ~700B of JSON.
			pos := make([][3]any, 0, min(len(occ), 30))
			for p := range occ {
				if len(pos) == cap(pos) {
					break
				}
				pos = append(pos, [3]any{p.ID, math.Round(p.X*10) / 10, math.Round(p.Y*10) / 10})
			}
			msg, _ := json.Marshal(map[string]any{"type": "tick", "n": h.tick, "st": st, "occ": len(occ), "pos": pos})
			for p := range occ {
				if p.sess == nil {
					continue
				}
				if err := p.sess.SendDatagram(msg); err != nil {
					mDgErrors.Inc()
				} else {
					mDgSent.Inc()
					mDgBytes.Add(float64(len(msg)))
				}
			}
		}
		h.mu.Unlock()
		mTickDur.Observe(time.Since(start).Seconds())
	}
}

func (h *Hub) handleSession(sess *webtransport.Session, remote string) {
	ctx := sess.Context()
	stream, err := sess.AcceptStream(ctx)
	if err != nil {
		mHandshakes.WithLabelValues("no_stream").Inc()
		return
	}
	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	if !sc.Scan() {
		mHandshakes.WithLabelValues("no_hello").Inc()
		return
	}
	var hello struct{ Type, Name, Device, Token, Room string }
	if json.Unmarshal(sc.Bytes(), &hello) != nil || hello.Type != "hello" {
		mHandshakes.WithLabelValues("bad_hello").Inc()
		return
	}

	h.mu.Lock()
	p, resumed := h.players[hello.Token]
	if resumed {
		mResumes.WithLabelValues("ok").Inc()
	} else {
		p = &Player{ID: randHex(4), token: randHex(16)}
		h.players[p.token] = p
		if hello.Token != "" {
			mResumes.WithLabelValues("expired").Inc()
		}
	}
	if hello.Name != "" {
		p.Name = hello.Name
	}
	if hello.Device != "" {
		p.Device = hello.Device
	}
	room := p.Room
	if room == "" {
		room = "lobby"
	}
	if hello.Room != "" && !resumed {
		room = hello.Room
	}
	h.moveLocked(p, room)
	p.LastSeen = nowMs()
	p.sess = sess
	p.out = make(chan []byte, 64)
	out := p.out
	nSessions := 0
	for _, q := range h.players {
		if q.sess != nil {
			nSessions++
		}
	}
	mSessions.Set(float64(nSessions))
	h.mu.Unlock()
	mHandshakes.WithLabelValues("ok").Inc()

	welcome, _ := json.Marshal(map[string]any{"type": "welcome", "you": p, "token": p.token, "resumed": resumed})
	stream.Write(append(welcome, '\n'))

	go func() {
		for {
			select {
			case msg := <-out:
				stream.SetWriteDeadline(time.Now().Add(3 * time.Second))
				if _, err := stream.Write(msg); err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	go h.datagramLoop(sess, p, remote)
	h.broadcastRoom(p.Room)

	for sc.Scan() {
		var m struct {
			Type, Room, Body string
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Type {
		case "join":
			h.mu.Lock()
			old := p.Room
			h.moveLocked(p, m.Room)
			p.LastSeen = nowMs()
			h.mu.Unlock()
			mIntents.Inc()
			h.broadcastRoom(old)
			h.broadcastRoom(m.Room)
		case "chat":
			if len(m.Body) > 500 {
				continue
			}
			mChat.Inc()
			msg, _ := json.Marshal(map[string]any{"type": "chat", "from": p.Name, "room": p.Room, "body": m.Body, "st": nowMs()})
			msg = append(msg, '\n')
			h.mu.Lock()
			for q := range h.rooms[p.Room] {
				h.sendLocked(q, msg)
				mChatFanout.Inc()
			}
			h.mu.Unlock()
		}
	}

	h.mu.Lock()
	if p.sess == sess {
		h.moveLocked(p, "") // leaves rooms but keeps the token for resume
		p.sess = nil
		p.out = nil
		p.LastSeen = nowMs()
	}
	n := 0
	for _, q := range h.players {
		if q.sess != nil {
			n++
		}
	}
	mSessions.Set(float64(n))
	room = p.Room
	h.mu.Unlock()
	h.broadcastRoom(room)
}

func (h *Hub) datagramLoop(sess *webtransport.Session, p *Player, remote string) {
	for {
		data, err := sess.ReceiveDatagram(sess.Context())
		if err != nil {
			return
		}
		var m struct {
			Type string  `json:"type"`
			CT   float64 `json:"ct"`
			X    float64 `json:"x"`
			Y    float64 `json:"y"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "ping":
			pong, _ := json.Marshal(map[string]any{"type": "pong", "ct": m.CT, "st": nowMs(), "addr": remote})
			if sess.SendDatagram(pong) == nil {
				mDgSent.Inc()
				mDgBytes.Add(float64(len(pong)))
			}
		case "move":
			h.mu.Lock()
			p.X, p.Y = m.X, m.Y
			p.LastSeen = nowMs()
			h.mu.Unlock()
			mIntents.Inc()
		}
	}
}

// purgeLoop drops tokens that have been offline for an hour.
func (h *Hub) purgeLoop() {
	for range time.Tick(time.Minute) {
		h.mu.Lock()
		cut := nowMs() - time.Hour.Milliseconds()
		for tok, p := range h.players {
			if p.sess == nil && p.LastSeen < cut {
				delete(h.players, tok)
			}
		}
		h.mu.Unlock()
	}
}

func selfSignedCert() (tls.Certificate, [32]byte) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "presenced"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 24 * time.Hour),
		DNSNames:     []string{"presenced", "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, sha256.Sum256(der)
}

func main() {
	tickHz := envInt("TICK_HZ", 24)
	wtPort := envInt("WT_PORT", 4433)
	metricsPort := envInt("METRICS_PORT", 9090)

	cert, certHash := selfSignedCert()
	hub := newHub(tickHz)
	go hub.tickLoop()
	go hub.purgeLoop()

	wtMux := http.NewServeMux()
	wt := webtransport.Server{
		H3: &http3.Server{
			Addr:            fmt.Sprintf(":%d", wtPort),
			TLSConfig:       &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{http3.NextProtoH3}},
			Handler:         wtMux,
			EnableDatagrams: true,
		},
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	wtMux.HandleFunc("/wt", func(w http.ResponseWriter, r *http.Request) {
		sess, err := wt.Upgrade(w, r)
		if err != nil {
			mHandshakes.WithLabelValues("upgrade_failed").Inc()
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		go hub.handleSession(sess, r.RemoteAddr)
	})

	obsMux := http.NewServeMux()
	obsMux.Handle("/metrics", promhttp.Handler())
	obsMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	obsMux.HandleFunc("/cert-hash", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"certHashB64": base64.StdEncoding.EncodeToString(certHash[:])})
	})

	log.Printf("presenced: wt=:%d metrics=:%d tick=%dHz cert=%s", wtPort, metricsPort, tickHz, hex.EncodeToString(certHash[:8]))
	go func() { log.Fatal(wt.ListenAndServe()) }()
	go func() { log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", metricsPort), obsMux)) }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	log.Print("SIGTERM: closing sessions (clients will resume against replacement)")
	wt.Close()
}
