package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"
)

const realmPath = "/realms/dev"

type codeRec struct {
	sub       string
	client    string
	challenge string
	redirect  string
	exp       time.Time
}

type refreshRec struct {
	sub    string
	client string
}

type issuer struct {
	base string
	key  *rsa.PrivateKey
	kid  string

	mu      sync.Mutex
	codes   map[string]codeRec
	refresh map[string]refreshRec
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func randToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return b64url(b)
}

// signJWT mints an RS256 JWT with the given claims — stdlib only, the
// exact shape go-oidc and the browser's subjectOf expect.
func (is *issuer) signJWT(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": is.kid})
	body, _ := json.Marshal(claims)
	signing := b64url(header) + "." + b64url(body)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, is.key, crypto.SHA256, digest[:])
	if err != nil {
		log.Fatalf("sign: %v", err)
	}
	return signing + "." + b64url(sig)
}

func (is *issuer) mintTokens(sub, client string, withRefresh bool) map[string]any {
	now := time.Now()
	claims := map[string]any{
		"iss":                is.base,
		"sub":                sub,
		"aud":                "account",
		"azp":                client,
		"iat":                now.Unix(),
		"exp":                now.Add(time.Hour).Unix(),
		"email_verified":     true,
		"preferred_username": sub,
	}
	resp := map[string]any{
		"access_token": is.signJWT(claims),
		"id_token":     is.signJWT(claims),
		"token_type":   "Bearer",
		"expires_in":   3600,
	}
	if withRefresh {
		rt := randToken()
		is.mu.Lock()
		is.refresh[rt] = refreshRec{sub: sub, client: client}
		is.mu.Unlock()
		resp["refresh_token"] = rt
	}
	return resp
}

func cors(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func writeJSON(w http.ResponseWriter, v any) {
	cors(w)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func tokenError(w http.ResponseWriter, code int, err string) {
	cors(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err})
}

func (is *issuer) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                is.base,
		"authorization_endpoint":                is.base + "/protocol/openid-connect/auth",
		"token_endpoint":                        is.base + "/protocol/openid-connect/token",
		"jwks_uri":                              is.base + "/protocol/openid-connect/certs",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (is *issuer) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	pub := &is.key.PublicKey
	writeJSON(w, map[string]any{
		"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": is.kid,
			"n": b64url(pub.N.Bytes()),
			"e": b64url(big.NewInt(int64(pub.E)).Bytes()),
		}},
	})
}

// handleAuthorize implements the code flow's front channel. With ?u= the
// redirect is immediate (Playwright, curl); without it a minimal form asks
// who you want to be today.
func (is *issuer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, state := q.Get("redirect_uri"), q.Get("state")
	ru, err := url.Parse(redirect)
	if err != nil || ru.Hostname() != "127.0.0.1" && ru.Hostname() != "localhost" {
		http.Error(w, "redirect_uri must be loopback", http.StatusBadRequest)
		return
	}
	if u := q.Get("u"); u != "" {
		code := randToken()
		is.mu.Lock()
		is.codes[code] = codeRec{
			sub: u, client: q.Get("client_id"), challenge: q.Get("code_challenge"),
			redirect: redirect, exp: time.Now().Add(2 * time.Minute),
		}
		is.mu.Unlock()
		sep := "?"
		if ru.RawQuery != "" {
			sep = "&"
		}
		http.Redirect(w, r, redirect+sep+"code="+code+"&state="+url.QueryEscape(state), http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<!doctype html><title>WUM dev sign-in</title>
<body style="font:16px system-ui;display:grid;place-items:center;height:100vh;margin:0;background:#161a21;color:#f4f1ea">
<form style="display:grid;gap:12px;text-align:center">
<div style="font-size:22px">Who are you today?</div>
<input name="u" placeholder="username" autofocus required
  style="font:inherit;padding:8px 12px;border-radius:6px;border:1px solid #555;background:#222733;color:inherit">
<button style="font:inherit;padding:8px 12px;border-radius:6px;border:0;background:#ffd166;cursor:pointer">Sign in</button>
%s
</form>`, hiddenInputs(q))
}

func hiddenInputs(q url.Values) string {
	s := ""
	for _, k := range []string{"client_id", "redirect_uri", "state", "code_challenge", "code_challenge_method", "response_type", "scope"} {
		if v := q.Get(k); v != "" {
			s += fmt.Sprintf(`<input type="hidden" name=%q value=%q>`, k, html.EscapeString(v))
		}
	}
	return s
}

func (is *issuer) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		cors(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	client := r.PostFormValue("client_id")
	if u, _, ok := r.BasicAuth(); ok && client == "" {
		client = u
	}
	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		code := r.PostFormValue("code")
		is.mu.Lock()
		rec, ok := is.codes[code]
		delete(is.codes, code)
		is.mu.Unlock()
		if !ok || time.Now().After(rec.exp) || rec.client != client {
			tokenError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		if rec.challenge != "" {
			digest := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
			if subtle.ConstantTimeCompare([]byte(b64url(digest[:])), []byte(rec.challenge)) != 1 {
				tokenError(w, http.StatusBadRequest, "invalid_grant")
				return
			}
		}
		writeJSON(w, is.mintTokens(rec.sub, rec.client, true))
	case "refresh_token":
		is.mu.Lock()
		rec, ok := is.refresh[r.PostFormValue("refresh_token")]
		is.mu.Unlock()
		if !ok || rec.client != client {
			tokenError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		writeJSON(w, is.mintTokens(rec.sub, rec.client, false))
	case "client_credentials":
		// Any secret is accepted: the binding is loopback-only and the
		// laptop's user is the trust boundary. azp is what the gateway gates on.
		if client == "" {
			tokenError(w, http.StatusBadRequest, "invalid_client")
			return
		}
		writeJSON(w, is.mintTokens("service-account-"+client, client, false))
	default:
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type")
	}
}

func main() {
	port := 9635
	if v, err := strconv.Atoi(os.Getenv("PORT")); err == nil {
		port = v
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatalf("keygen: %v", err)
	}
	sum := sha256.Sum256(key.PublicKey.N.Bytes())
	is := &issuer{
		base:    fmt.Sprintf("http://127.0.0.1:%d%s", port, realmPath),
		key:     key,
		kid:     b64url(sum[:8]),
		codes:   map[string]codeRec{},
		refresh: map[string]refreshRec{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc(realmPath+"/.well-known/openid-configuration", is.handleDiscovery)
	mux.HandleFunc(realmPath+"/protocol/openid-connect/certs", is.handleJWKS)
	mux.HandleFunc(realmPath+"/protocol/openid-connect/auth", is.handleAuthorize)
	mux.HandleFunc(realmPath+"/protocol/openid-connect/token", is.handleToken)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	log.Printf("devissuer: %s (issuer %s)", addr, is.base)
	log.Fatal(http.ListenAndServe(addr, mux))
}
