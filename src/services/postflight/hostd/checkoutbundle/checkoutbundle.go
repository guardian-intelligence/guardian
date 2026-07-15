// Package checkoutbundle serves single-commit git packfiles to the Postflight
// checkout action running inside a job.
//
// The action POSTs {repository, ref, sha, github_token} to
// /internal/sandbox/v1/github-checkout/bundle with a lease-scoped bearer token
// and receives the exact pack closure of one commit, materialized from a
// host-local bare mirror. The mirror and a per-SHA pack cache make repeat
// checkouts (matrix jobs, retries, warm workspaces) free of GitHub traffic.
//
// The package is transport-complete but host-agnostic: lease identity comes
// through the IdentityResolver seam, which hostd backs with its live lease
// table.
package checkoutbundle

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// BundlePath is the route the checkout action targets. The action builds
	// it as POSTFLIGHT_CHECKOUT_PATH + "/bundle"; both halves pin the default.
	BundlePath = "/internal/sandbox/v1/github-checkout/bundle"

	// maxRequestBodyBytes bounds the JSON request body.
	maxRequestBodyBytes = 16 << 10

	// packContentType is what the action requires; anything else is a
	// protocol mismatch on the client side.
	packContentType = "application/x-git-packed-objects"

	shaHeader      = "X-Postflight-Checkout-Sha"
	sizeHeader     = "X-Postflight-Checkout-Size-Bytes"
	cacheHitHeader = "X-Postflight-Checkout-Bundle-Cache-Hit"

	executionIDHeader = "X-Postflight-Execution-Id"
	attemptIDHeader   = "X-Postflight-Attempt-Id"
)

// Config carries the tunables for a Service. Zero values fall back to the
// defaults applied in New.
type Config struct {
	// StoreDir roots the mirror and bundle stores. Required.
	StoreDir string

	// HostSecret keys the lease-scoped bearer tokens. Per host, never shared
	// across hosts. Required.
	HostSecret []byte

	// GitHubWebBaseURL prefixes clone URLs ("https://github.com" in
	// production; a file:// root in tests).
	GitHubWebBaseURL string

	// MaxPackBytes rejects packs larger than this with 413. Matches the
	// action's own 512 MiB ceiling by default.
	MaxPackBytes int64

	// MaxConcurrent bounds in-flight bundle requests; excess gets 429.
	MaxConcurrent int

	// GitTimeout bounds each child git command.
	GitTimeout time.Duration

	// BundleTTL ages packs out of the cache.
	BundleTTL time.Duration

	// BundleBudgetBytes caps the bundle store; oldest packs are evicted
	// first once the budget is exceeded.
	BundleBudgetBytes int64

	// MirrorTTL ages out mirrors that no lease has used.
	MirrorTTL time.Duration

	Logger *slog.Logger
}

// Metrics are cumulative counters, readable at any time.
type Metrics struct {
	Requests      atomic.Int64
	CacheHits     atomic.Int64
	MirrorFetches atomic.Int64
	BytesServed   atomic.Int64
	Rejected      atomic.Int64
}

// Service implements the checkout-bundle endpoint.
type Service struct {
	cfg      Config
	resolver IdentityResolver
	slots    chan struct{}

	repoLocksMu sync.Mutex
	repoLocks   map[string]*sync.Mutex

	Metrics Metrics
}

// New wires a Service. StoreDir, HostSecret, and resolver are required;
// everything else defaults.
func New(cfg Config, resolver IdentityResolver) *Service {
	if cfg.GitHubWebBaseURL == "" {
		cfg.GitHubWebBaseURL = "https://github.com"
	}
	if cfg.MaxPackBytes <= 0 {
		cfg.MaxPackBytes = 512 << 20
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 8
	}
	if cfg.GitTimeout <= 0 {
		cfg.GitTimeout = 5 * time.Minute
	}
	if cfg.BundleTTL <= 0 {
		cfg.BundleTTL = 7 * 24 * time.Hour
	}
	if cfg.BundleBudgetBytes <= 0 {
		cfg.BundleBudgetBytes = 20 << 30
	}
	if cfg.MirrorTTL <= 0 {
		cfg.MirrorTTL = 30 * 24 * time.Hour
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Service{
		cfg:       cfg,
		resolver:  resolver,
		slots:     make(chan struct{}, cfg.MaxConcurrent),
		repoLocks: map[string]*sync.Mutex{},
	}
}

// lockRepo serializes mirror and cache mutation per repository. The lock map
// is never pruned; its size is bounded by the number of distinct repositories
// a host has ever served, which is small by construction (a host serves the
// leases the control plane routes to it).
func (s *Service) lockRepo(repoKey string) func() {
	s.repoLocksMu.Lock()
	lock, ok := s.repoLocks[repoKey]
	if !ok {
		lock = &sync.Mutex{}
		s.repoLocks[repoKey] = lock
	}
	s.repoLocksMu.Unlock()
	lock.Lock()
	return lock.Unlock
}
