package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

const (
	defaultListenAddress      = ":8080"
	defaultAudience           = "https://agent-auth.guardianintelligence.org"
	defaultKubernetesAPI      = "https://kubernetes.default.svc"
	defaultKubernetesAudience = "https://10.8.0.250:6443"
	defaultNamespace          = "tenant-root"
	serviceAccountTokenPath   = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	serviceAccountCAPath      = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"

	defaultCursorIssuer      = "https://api.cursor.com"
	defaultCursorJWKSURL     = "https://api.cursor.com/keys"
	defaultCursorSubject     = "user:344209842"
	defaultCursorOwnerUserID = "344209842"
	defaultCursorRepository  = "github.com/guardian-intelligence/guardian"

	defaultDevinIssuer  = "https://app.devin.ai"
	defaultDevinJWKSURL = "https://app.devin.ai/.well-known/jwks.json"
)

type config struct {
	ListenAddress      string
	Audience           string
	KubernetesAPI      string
	KubernetesAudience string
	Namespace          string
	Cursor             cursorPolicy
	Devin              devinPolicy
}

type cursorPolicy struct {
	Issuer      string
	JWKSURL     string
	Subject     string
	OwnerUserID string
	Repository  string
}

type devinPolicy struct {
	Issuer                string
	JWKSURL               string
	OrganizationID        string
	AccountID             string
	Repository            string
	RequestingUserIDs     []string
	RequestingUserEmails  []string
	AllowedServiceUserIDs []string
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envList(name string) []string {
	var values []string
	for _, value := range strings.Split(os.Getenv(name), ",") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	slices.Sort(values)
	return slices.Compact(values)
}

func loadConfig() config {
	return config{
		ListenAddress:      env("LISTEN_ADDRESS", defaultListenAddress),
		Audience:           env("OIDC_AUDIENCE", defaultAudience),
		KubernetesAPI:      strings.TrimRight(env("KUBERNETES_API", defaultKubernetesAPI), "/"),
		KubernetesAudience: env("KUBERNETES_AUDIENCE", defaultKubernetesAudience),
		Namespace:          env("KUBERNETES_NAMESPACE", defaultNamespace),
		Cursor: cursorPolicy{
			Issuer:      env("CURSOR_OIDC_ISSUER", defaultCursorIssuer),
			JWKSURL:     env("CURSOR_OIDC_JWKS_URL", defaultCursorJWKSURL),
			Subject:     env("CURSOR_SUBJECT", defaultCursorSubject),
			OwnerUserID: env("CURSOR_OWNER_USER_ID", defaultCursorOwnerUserID),
			Repository:  env("CURSOR_REPOSITORY", defaultCursorRepository),
		},
		Devin: devinPolicy{
			Issuer:                env("DEVIN_OIDC_ISSUER", defaultDevinIssuer),
			JWKSURL:               env("DEVIN_OIDC_JWKS_URL", defaultDevinJWKSURL),
			OrganizationID:        strings.TrimSpace(os.Getenv("DEVIN_ORG_ID")),
			AccountID:             strings.TrimSpace(os.Getenv("DEVIN_ACCOUNT_ID")),
			Repository:            strings.TrimSpace(os.Getenv("DEVIN_REPOSITORY")),
			RequestingUserIDs:     envList("DEVIN_REQUESTING_USER_IDS"),
			RequestingUserEmails:  envList("DEVIN_REQUESTING_USER_EMAILS"),
			AllowedServiceUserIDs: envList("DEVIN_SERVICE_USER_IDS"),
		},
	}
}

func (cfg config) validate() error {
	if cfg.ListenAddress == "" || cfg.Audience == "" || cfg.KubernetesAPI == "" || cfg.KubernetesAudience == "" || cfg.Namespace == "" {
		return errors.New("common federation configuration is incomplete")
	}
	if cfg.Cursor.Issuer == "" || cfg.Cursor.JWKSURL == "" || cfg.Cursor.Subject == "" || cfg.Cursor.OwnerUserID == "" || cfg.Cursor.Repository == "" {
		return errors.New("Cursor federation policy is incomplete")
	}
	if cfg.Devin.Issuer == "" || cfg.Devin.JWKSURL == "" || cfg.Devin.OrganizationID == "" {
		return errors.New("Devin federation policy is incomplete")
	}
	if len(cfg.Devin.RequestingUserIDs) == 0 && len(cfg.Devin.RequestingUserEmails) == 0 && len(cfg.Devin.AllowedServiceUserIDs) == 0 {
		return errors.New("Devin federation policy has no allowed principal")
	}
	if cfg.Cursor.Issuer == cfg.Devin.Issuer {
		return errors.New("federation providers must use distinct issuers")
	}
	return nil
}

type workloadIdentity struct {
	Provider  string
	RunID     string
	Principal string
}

type identityResolver interface {
	Verify(context.Context, string) (workloadIdentity, error)
}

type oidcClaimsVerifier struct {
	verifier *oidc.IDTokenVerifier
}

func newOIDCClaimsVerifier(issuer, jwksURL, audience string) *oidcClaimsVerifier {
	keys := oidc.NewRemoteKeySet(context.Background(), jwksURL)
	return &oidcClaimsVerifier{verifier: oidc.NewVerifier(issuer, keys, &oidc.Config{ClientID: audience})}
}

func (v *oidcClaimsVerifier) Verify(ctx context.Context, bearer string, claims any) error {
	token, err := v.verifier.Verify(ctx, bearer)
	if err != nil {
		return err
	}
	return token.Claims(claims)
}

type cursorClaims struct {
	Subject         string   `json:"sub"`
	AgentRuntime    string   `json:"agent_runtime"`
	OwnerUserID     string   `json:"owner_user_id"`
	RepositoryURL   string   `json:"repo_url"`
	RepositoryURLs  []string `json:"repo_urls"`
	RepositoryCount int      `json:"repo_count"`
	CloudAgentID    string   `json:"cloud_agent_id"`
}

type cursorVerifier struct {
	policy   cursorPolicy
	verifier *oidcClaimsVerifier
}

func (v *cursorVerifier) Verify(ctx context.Context, bearer string) (workloadIdentity, error) {
	var claims cursorClaims
	if err := v.verifier.Verify(ctx, bearer, &claims); err != nil {
		return workloadIdentity{}, err
	}
	return normalizeCursorIdentity(v.policy, claims)
}

func normalizeCursorIdentity(policy cursorPolicy, claims cursorClaims) (workloadIdentity, error) {
	if claims.Subject != policy.Subject || claims.OwnerUserID != policy.OwnerUserID {
		return workloadIdentity{}, errors.New("Cursor owner mismatch")
	}
	if claims.AgentRuntime != "managed" {
		return workloadIdentity{}, errors.New("Cursor runtime is not managed")
	}
	if claims.RepositoryURL != policy.Repository || claims.RepositoryCount != 1 || len(claims.RepositoryURLs) != 1 || claims.RepositoryURLs[0] != policy.Repository {
		return workloadIdentity{}, errors.New("Cursor repository set mismatch")
	}
	if claims.CloudAgentID == "" {
		return workloadIdentity{}, errors.New("Cursor cloud agent id is missing")
	}
	return workloadIdentity{Provider: "cursor", RunID: claims.CloudAgentID, Principal: claims.Subject}, nil
}

type devinClaims struct {
	Subject             string `json:"sub"`
	OrganizationID      string `json:"org_id"`
	AccountID           string `json:"account_id"`
	DevinID             string `json:"devin_id"`
	RequestingUserID    string `json:"requesting_user_id"`
	RequestingUserEmail string `json:"requesting_user_email"`
	ServiceUserID       string `json:"service_user_id"`
	RepositoryName      string `json:"repository_name"`
}

type devinVerifier struct {
	policy   devinPolicy
	verifier *oidcClaimsVerifier
}

func (v *devinVerifier) Verify(ctx context.Context, bearer string) (workloadIdentity, error) {
	var claims devinClaims
	if err := v.verifier.Verify(ctx, bearer, &claims); err != nil {
		return workloadIdentity{}, err
	}
	return normalizeDevinIdentity(v.policy, claims)
}

func normalizeDevinIdentity(policy devinPolicy, claims devinClaims) (workloadIdentity, error) {
	if claims.OrganizationID != policy.OrganizationID {
		return workloadIdentity{}, errors.New("Devin organization mismatch")
	}
	if policy.AccountID != "" && claims.AccountID != policy.AccountID {
		return workloadIdentity{}, errors.New("Devin account mismatch")
	}
	if claims.DevinID == "" || !strings.HasPrefix(claims.DevinID, "devin-") {
		return workloadIdentity{}, errors.New("Devin session id is missing or malformed")
	}
	if policy.Repository != "" && claims.RepositoryName != policy.Repository {
		return workloadIdentity{}, errors.New("Devin repository mismatch")
	}

	principal := ""
	if claims.ServiceUserID != "" {
		if !slices.Contains(policy.AllowedServiceUserIDs, claims.ServiceUserID) {
			return workloadIdentity{}, errors.New("Devin service user is not allowed")
		}
		principal = "service_user:" + claims.ServiceUserID
	} else if claims.RequestingUserID != "" && slices.Contains(policy.RequestingUserIDs, claims.RequestingUserID) {
		principal = "user:" + claims.RequestingUserID
	} else if email := strings.ToLower(claims.RequestingUserEmail); email != "" && slices.Contains(policy.RequestingUserEmails, email) {
		principal = "email:" + email
	} else {
		return workloadIdentity{}, errors.New("Devin requesting user is not allowed")
	}
	if claims.Subject == "" {
		return workloadIdentity{}, errors.New("Devin subject is missing")
	}
	return workloadIdentity{Provider: "devin", RunID: claims.DevinID, Principal: principal}, nil
}

type providerRegistry struct {
	providers map[string]identityResolver
}

func newProviderRegistry(cfg config) *providerRegistry {
	return &providerRegistry{providers: map[string]identityResolver{
		cfg.Cursor.Issuer: &cursorVerifier{
			policy:   cfg.Cursor,
			verifier: newOIDCClaimsVerifier(cfg.Cursor.Issuer, cfg.Cursor.JWKSURL, cfg.Audience),
		},
		cfg.Devin.Issuer: &devinVerifier{
			policy:   cfg.Devin,
			verifier: newOIDCClaimsVerifier(cfg.Devin.Issuer, cfg.Devin.JWKSURL, cfg.Audience),
		},
	}}
}

func (r *providerRegistry) Verify(ctx context.Context, bearer string) (workloadIdentity, error) {
	issuer, err := jwtIssuer(bearer)
	if err != nil {
		return workloadIdentity{}, err
	}
	provider, ok := r.providers[issuer]
	if !ok {
		return workloadIdentity{}, errors.New("untrusted workload issuer")
	}
	return provider.Verify(ctx, bearer)
}

func jwtIssuer(token string) (string, error) {
	if token == "" || len(token) > 16*1024 {
		return "", errors.New("invalid JWT size")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return "", errors.New("malformed JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) > 8*1024 {
		return "", errors.New("malformed JWT payload")
	}
	var envelope struct {
		Issuer string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Issuer == "" {
		return "", errors.New("JWT issuer is missing")
	}
	return envelope.Issuer, nil
}

type mintedToken struct {
	Token      string
	Expiration time.Time
}

type tokenMinter interface {
	Mint(context.Context, string, string, string, int64) (mintedToken, error)
}

type kubernetesMinter struct {
	apiURL         string
	credentialPath string
	client         *http.Client
}

func newKubernetesMinter(cfg config) (*kubernetesMinter, error) {
	caPEM, err := os.ReadFile(serviceAccountCAPath)
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("Kubernetes CA contained no certificates")
	}
	return &kubernetesMinter{
		apiURL:         cfg.KubernetesAPI,
		credentialPath: serviceAccountTokenPath,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS13,
				RootCAs:    roots,
			}},
		},
	}, nil
}

func (m *kubernetesMinter) Mint(ctx context.Context, namespace, serviceAccount, audience string, seconds int64) (mintedToken, error) {
	credential, err := os.ReadFile(m.credentialPath)
	if err != nil {
		return mintedToken{}, fmt.Errorf("read federation credential: %w", err)
	}
	body, err := json.Marshal(map[string]any{
		"apiVersion": "authentication.k8s.io/v1",
		"kind":       "TokenRequest",
		"spec": map[string]any{
			"audiences":         []string{audience},
			"expirationSeconds": seconds,
		},
	})
	if err != nil {
		return mintedToken{}, err
	}
	endpoint := fmt.Sprintf("%s/api/v1/namespaces/%s/serviceaccounts/%s/token", m.apiURL, url.PathEscape(namespace), url.PathEscape(serviceAccount))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return mintedToken{}, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(credential)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	response, err := m.client.Do(req)
	if err != nil {
		return mintedToken{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return mintedToken{}, fmt.Errorf("TokenRequest returned %s: %s", response.Status, strings.TrimSpace(string(detail)))
	}
	var result struct {
		Status struct {
			Token               string    `json:"token"`
			ExpirationTimestamp time.Time `json:"expirationTimestamp"`
		} `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return mintedToken{}, fmt.Errorf("decode TokenRequest: %w", err)
	}
	if result.Status.Token == "" || result.Status.ExpirationTimestamp.IsZero() {
		return mintedToken{}, errors.New("TokenRequest response omitted token or expiration")
	}
	return mintedToken{Token: result.Status.Token, Expiration: result.Status.ExpirationTimestamp}, nil
}

type rateWindow struct {
	Started time.Time
	Count   int
}

type runLimiter struct {
	mu      sync.Mutex
	windows map[string]rateWindow
	now     func() time.Time
}

func newRunLimiter() *runLimiter {
	return &runLimiter{windows: make(map[string]rateWindow), now: time.Now}
}

func (l *runLimiter) Allow(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for key, window := range l.windows {
		if now.Sub(window.Started) >= 2*time.Minute {
			delete(l.windows, key)
		}
	}
	window := l.windows[id]
	if window.Started.IsZero() || now.Sub(window.Started) >= time.Minute {
		l.windows[id] = rateWindow{Started: now, Count: 1}
		return true
	}
	if window.Count >= 12 {
		return false
	}
	window.Count++
	l.windows[id] = window
	return true
}

type broker struct {
	cfg      config
	resolver identityResolver
	minter   tokenMinter
	limiter  *runLimiter
}

type mintRequest struct {
	Persona string `json:"persona"`
}

type execCredential struct {
	APIVersion string               `json:"apiVersion"`
	Kind       string               `json:"kind"`
	Status     execCredentialStatus `json:"status"`
}

type execCredentialStatus struct {
	ExpirationTimestamp time.Time `json:"expirationTimestamp"`
	Token               string    `json:"token"`
}

func bearerToken(header string) (string, error) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("missing bearer token")
	}
	return token, nil
}

func personaTarget(provider, persona string) (string, int64, error) {
	if provider != "cursor" && provider != "devin" {
		return "", 0, errors.New("unknown provider")
	}
	switch persona {
	case "read":
		return "guardian-cloud-agent-" + provider, 900, nil
	case "write-basic":
		return "guardian-cloud-agent-" + provider + "-write-basic", 600, nil
	default:
		return "", 0, errors.New("unknown persona")
	}
}

func (b *broker) handleMint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); mediaType != "application/json" {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	bearer, err := bearerToken(r.Header.Get("Authorization"))
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	var request mintRequest
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if request.Persona == "" {
		request.Persona = "read"
	}
	if request.Persona != "read" && request.Persona != "write-basic" {
		http.Error(w, "unknown persona", http.StatusBadRequest)
		return
	}

	identity, err := b.resolver.Verify(r.Context(), bearer)
	if err != nil {
		log.Printf("workload assertion rejected: %v", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	serviceAccount, duration, err := personaTarget(identity.Provider, request.Persona)
	if err != nil {
		log.Printf("normalized provider rejected for run=%q: %v", identity.RunID, err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	limitKey := identity.Provider + ":" + identity.RunID
	if !b.limiter.Allow(limitKey) {
		log.Printf("credential exchange rate limited provider=%s run=%q", identity.Provider, identity.RunID)
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}

	minted, err := b.minter.Mint(r.Context(), b.cfg.Namespace, serviceAccount, b.cfg.KubernetesAudience, duration)
	if err != nil {
		log.Printf("credential mint failed provider=%s run=%q persona=%s: %v", identity.Provider, identity.RunID, request.Persona, err)
		http.Error(w, "credential mint failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	response := execCredential{
		APIVersion: "client.authentication.k8s.io/v1",
		Kind:       "ExecCredential",
		Status: execCredentialStatus{
			ExpirationTimestamp: minted.Expiration,
			Token:               minted.Token,
		},
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("credential response failed provider=%s run=%q persona=%s: %v", identity.Provider, identity.RunID, request.Persona, err)
		return
	}
	log.Printf("credential minted provider=%s run=%q persona=%s expires=%s", identity.Provider, identity.RunID, request.Persona, minted.Expiration.UTC().Format(time.RFC3339))
}

func (b *broker) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/credentials/kubernetes", b.handleMint)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	return mux
}

func main() {
	cfg := loadConfig()
	if err := cfg.validate(); err != nil {
		log.Fatal(err)
	}
	minter, err := newKubernetesMinter(cfg)
	if err != nil {
		log.Fatal(err)
	}
	app := &broker{
		cfg:      cfg,
		resolver: newProviderRegistry(cfg),
		minter:   minter,
		limiter:  newRunLimiter(),
	}
	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           app.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	log.Printf("cloud-agent federation listening on %s", cfg.ListenAddress)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
