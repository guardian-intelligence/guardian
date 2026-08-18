package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/guardian-intelligence/guardian/src/services/telemetry"
)

// ticket is the admission artifact (docs/netcode.md): identity, park, and
// role in one HMAC-signed blob the QUIC hello presents. Minted only by
// POST /session behind OIDC; checked statelessly by every hello.
type ticket struct {
	Sub  string `json:"sub"`
	Park string `json:"park"`
	Role string `json:"role"`
	Exp  int64  `json:"exp"`
}

type ticketMint struct {
	key []byte
}

func newTicketMint(path string) (*ticketMint, error) {
	key := make([]byte, 32)
	if path == "" {
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		return &ticketMint{key: key}, nil
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key = []byte(strings.TrimSpace(string(key)))
	if len(key) < 32 {
		return nil, errors.New("ticket key must contain at least 32 bytes")
	}
	return &ticketMint{key: key}, nil
}

func (m *ticketMint) mint(t ticket) string {
	body, _ := json.Marshal(t)
	mac := hmac.New(sha256.New, m.key)
	mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (m *ticketMint) check(raw string) (ticket, error) {
	var t ticket
	body64, sig64, ok := strings.Cut(raw, ".")
	if !ok {
		return t, errors.New("malformed ticket")
	}
	body, err := base64.RawURLEncoding.DecodeString(body64)
	if err != nil {
		return t, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(sig64)
	if err != nil {
		return t, err
	}
	mac := hmac.New(sha256.New, m.key)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return t, errors.New("bad ticket signature")
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return t, err
	}
	if time.Now().Unix() > t.Exp {
		return t, errors.New("expired ticket")
	}
	return t, nil
}

// oidcGate verifies bearer tokens from the customer realm. The verifier
// initializes lazily with retry so an unreachable issuer degrades /session
// to 503 instead of crashlooping the game service.
type oidcGate struct {
	issuer         string // the public iss tokens carry and are validated against
	jwksURL        string // where keys are actually fetched
	allowedClients map[string]bool
	requireEmail   bool

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
}

// newOIDCGate validates tokens carrying the public issuer as their `iss`
// but fetches signing keys from jwksURL. In the cluster that is the
// internal Keycloak certs endpoint over plain HTTP: the gateway runs
// hostNetwork on a distroless image with no CA bundle, so reaching the
// public issuer would both hairpin through the edge and fail certificate
// verification. An empty jwksURL falls back to discovery against the
// public issuer (local dev).
func newOIDCGate(issuer, jwksURL, clientIDs string, requireEmail bool) *oidcGate {
	g := &oidcGate{issuer: issuer, jwksURL: jwksURL, allowedClients: map[string]bool{}, requireEmail: requireEmail}
	for _, c := range strings.Split(clientIDs, ",") {
		if c = strings.TrimSpace(c); c != "" {
			g.allowedClients[c] = true
		}
	}
	return g
}

func (g *oidcGate) getVerifier(ctx context.Context) (*oidc.IDTokenVerifier, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.verifier != nil {
		return g.verifier, nil
	}
	// Audience is checked manually against the allowed client list in
	// verify (the web page and the load driver share this gate).
	cfg := &oidc.Config{SkipClientIDCheck: true}
	if g.jwksURL != "" {
		// A remote key set never touches the public issuer: keys come from
		// the internal endpoint, tokens are still validated against the
		// public iss. Key fetches lazily on first verify and cache.
		g.verifier = oidc.NewVerifier(g.issuer, oidc.NewRemoteKeySet(context.Background(), g.jwksURL), cfg)
		return g.verifier, nil
	}
	provider, err := oidc.NewProvider(ctx, g.issuer)
	if err != nil {
		return nil, err
	}
	g.verifier = provider.Verifier(cfg)
	return g.verifier, nil
}

type identity struct {
	Sub           string
	EmailVerified bool
	Azp           string
}

func (g *oidcGate) verify(ctx context.Context, bearer string) (identity, error) {
	v, err := g.getVerifier(ctx)
	if err != nil {
		return identity{}, fmt.Errorf("issuer unavailable: %w", err)
	}
	tok, err := v.Verify(ctx, bearer)
	if err != nil {
		return identity{}, err
	}
	var claims struct {
		Sub           string `json:"sub"`
		Azp           string `json:"azp"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := tok.Claims(&claims); err != nil {
		return identity{}, err
	}
	if claims.Sub == "" {
		return identity{}, errors.New("token has no subject")
	}
	if !g.allowedClients[claims.Azp] {
		return identity{}, fmt.Errorf("client %q not allowed", claims.Azp)
	}
	return identity{Sub: claims.Sub, EmailVerified: claims.EmailVerified, Azp: claims.Azp}, nil
}

// anonDeviceRe accepts the client-minted device UUID that names an
// anonymous spectator. The id is only a stable pseudonym for feature
// flags, traces, and the sign-in funnel join; it carries no write
// authority, so forging one gains nothing.
var anonDeviceRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// anonLimiter rate-limits anonymous session mints per client address:
// they are the only unauthenticated mint path, so a token bucket bounds
// what one source can spend.
type anonLimiter struct {
	mu      sync.Mutex
	buckets map[string]*anonBucket
}

type anonBucket struct {
	tokens float64
	last   time.Time
}

func newAnonLimiter() *anonLimiter { return &anonLimiter{buckets: map[string]*anonBucket{}} }

func (l *anonLimiter) allow(ip string) bool {
	const burst, perMinute = 6.0, 6.0
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buckets) > 65536 {
		for k, b := range l.buckets {
			if now.Sub(b.last) > time.Hour {
				delete(l.buckets, k)
			}
		}
	}
	b := l.buckets[ip]
	if b == nil {
		b = &anonBucket{tokens: burst, last: now}
		l.buckets[ip] = b
	}
	b.tokens = min(burst, b.tokens+perMinute*now.Sub(b.last).Minutes())
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func clientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// handleSessionMint is POST /session: identity in, admission ticket out.
// Two doors: an OIDC bearer mints a player (or spectator) for the WUM
// account it names, and a bare ?spectate&device=<uuid> mints an anonymous
// spectator — the acquisition funnel. Parks are a fixed registry, not a
// request-path side effect: an unknown name never opens an authority. The
// email_verified requirement is config-gated until every broker asserts it.
func (h *gameHandlers) handleSessionMint(gate *oidcGate, publicAddr string, certHash func() (string, bool)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		park := r.URL.Query().Get("park")
		if park == "" {
			park = "park-mythra"
		}
		if !h.directory.allowed(h.game, park) {
			mMints.WithLabelValues("unknown_park").Inc()
			http.Error(w, "unknown park", http.StatusNotFound)
			return
		}
		role := "player"
		if r.URL.Query().Has("spectate") {
			role = "spectator"
		}

		var sub string
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case bearer == "" && role == "spectator":
			device := r.URL.Query().Get("device")
			if !anonDeviceRe.MatchString(device) {
				http.Error(w, "anonymous spectating needs a device id", http.StatusBadRequest)
				return
			}
			if !h.anonMints.allow(clientIP(r)) {
				mMints.WithLabelValues("anon_limited").Inc()
				http.Error(w, "too many anonymous sessions", http.StatusTooManyRequests)
				return
			}
			sub = "anon:" + device
			mMints.WithLabelValues("anon").Inc()
		case bearer == "":
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		default:
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			id, err := gate.verify(ctx, bearer)
			if err != nil {
				mMints.WithLabelValues("denied").Inc()
				log.Printf("session mint denied: %v", err)
				status := http.StatusUnauthorized
				if strings.Contains(err.Error(), "issuer unavailable") {
					status = http.StatusServiceUnavailable
				}
				http.Error(w, "unauthorized", status)
				return
			}
			if gate.requireEmail && !id.EmailVerified {
				mMints.WithLabelValues("unverified").Inc()
				http.Error(w, "email verification required", http.StatusForbidden)
				return
			}
			sub = id.Sub
			mMints.WithLabelValues("ok").Inc()
		}

		tk := h.tickets.mint(ticket{
			Sub: sub, Park: park, Role: role,
			Exp: time.Now().Add(60 * time.Second).Unix(),
		})
		// The mint is where a browser's quoted trace id meets a game
		// identity: this line and the span attributes are the join between
		// a client rpc trace and everything the session does afterwards.
		telemetry.SpanAttrs(r.Context(), map[string]string{
			"wum.sub": sub, "wum.park": park, "wum.role": role,
		})
		if tid := telemetry.TraceIDFrom(r.Context()); tid != "" {
			log.Printf("session minted: sub=%s park=%s role=%s trace_id=%s", sub, park, role, tid)
		}
		resp := map[string]any{
			"ticket": tk, "endpoint": publicAddr, "park": park, "role": role,
		}
		if hash, selfSigned := certHash(); selfSigned {
			resp["certHashB64"] = hash
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(resp)
	}
}
