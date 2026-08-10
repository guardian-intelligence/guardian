package main

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
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
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

// newTicketMint keys the mint per boot: tickets are 60-second artifacts
// between /session and the hello, so a restart invalidating them only
// costs in-flight dials one extra /session round trip.
func newTicketMint() *ticketMint {
	key := make([]byte, 32)
	rand.Read(key)
	return &ticketMint{key: key}
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
	issuer         string
	allowedClients map[string]bool
	requireEmail   bool

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
}

func newOIDCGate(issuer, clientIDs string, requireEmail bool) *oidcGate {
	g := &oidcGate{issuer: issuer, allowedClients: map[string]bool{}, requireEmail: requireEmail}
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
	provider, err := oidc.NewProvider(ctx, g.issuer)
	if err != nil {
		return nil, err
	}
	// Audience is checked manually against the allowed client list below
	// (two clients share this gate: the web page and the load driver).
	g.verifier = provider.Verifier(&oidc.Config{SkipClientIDCheck: true})
	return g.verifier, nil
}

type identity struct {
	Sub           string
	EmailVerified bool
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
	return identity{Sub: claims.Sub, EmailVerified: claims.EmailVerified}, nil
}

// handleSessionMint is POST /session: OIDC in, admission ticket out. The
// email_verified requirement is config-gated until the realm can issue it
// (no SMTP today — docs/wake-up-mythra-development.md gaps).
func (h *gameHandlers) handleSessionMint(gate *oidcGate, publicAddr string, certHash func() (string, bool)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if bearer == "" {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
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
		park := r.URL.Query().Get("park")
		if park == "" {
			park = "park-mythra"
		}
		if len(park) > 64 {
			http.Error(w, "park name too long", http.StatusBadRequest)
			return
		}
		role := "player"
		if r.URL.Query().Has("spectate") {
			role = "spectator"
		}
		tk := h.tickets.mint(ticket{
			Sub: id.Sub, Park: park, Role: role,
			Exp: time.Now().Add(60 * time.Second).Unix(),
		})
		mMints.WithLabelValues("ok").Inc()
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
