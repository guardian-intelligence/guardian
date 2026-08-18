// Package transfer serves sealed workspace-cache generations to peer hosts
// over the private transfer VLAN, so a job assigned off the sealing host can
// still run warm. A peer GETs one tree of a generation set and receives the
// compressed ZFS replication stream of its sealed snapshot; naming a base it
// already holds yields an incremental stream when the source can prove
// direct parentage. The listener must bind a specific VLAN address — wiring
// refuses wildcard binds — and callers authenticate with the host-fleet sync
// credential.
package transfer

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/guardian-intelligence/guardian/src/postflight/hostd/zvol"
)

const (
	// GenerationPathPrefix is the route peers target; the generation name is
	// the trailing path element.
	GenerationPathPrefix = "/internal/transfer/v1/generation/"

	// TreeParam selects the workspace, tool, or process tree of the set.
	TreeParam = "tree"
	// FromParam optionally names an incremental base generation.
	FromParam = "from"

	// IncrementalHeader reports whether the response body is an incremental
	// stream against the requested base.
	IncrementalHeader = "X-Postflight-Transfer-Incremental"

	streamContentType = "application/octet-stream"
)

// Config carries the tunables for a Server.
type Config struct {
	// Store serves the streams. Required.
	Store zvol.TransferStore

	// Secret is the fleet sync credential peers present as a bearer token.
	// Required.
	Secret []byte

	// MaxConcurrent bounds in-flight sends; excess gets 429.
	MaxConcurrent int

	Logger *slog.Logger
}

// Server implements the generation-transfer endpoint.
type Server struct {
	cfg   Config
	slots chan struct{}

	inflightMu sync.Mutex
	inflight   map[string]bool
}

// New wires a Server. Store and Secret are required; everything else
// defaults.
func New(cfg Config) (*Server, error) {
	if cfg.Store == nil {
		return nil, errors.New("transfer: store is required")
	}
	if len(cfg.Secret) == 0 {
		return nil, errors.New("transfer: secret is required")
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Server{
		cfg:      cfg,
		slots:    make(chan struct{}, cfg.MaxConcurrent),
		inflight: map[string]bool{},
	}, nil
}

// Handler serves the generation route plus a health probe.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(GenerationPathPrefix, s.handleGeneration)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func (s *Server) authorized(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	presented := sha256.Sum256([]byte(strings.TrimPrefix(header, "Bearer ")))
	expected := sha256.Sum256(s.cfg.Secret)
	return subtle.ConstantTimeCompare(presented[:], expected[:]) == 1
}

func (s *Server) handleGeneration(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "not authorized")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, GenerationPathPrefix)
	if name == "" || strings.ContainsAny(name, "/@") {
		writeError(w, http.StatusBadRequest, "generation name is invalid")
		return
	}
	if err := zvol.ValidateName("generation", name); err != nil {
		writeError(w, http.StatusBadRequest, "generation name is invalid")
		return
	}
	tree := zvol.TransferTree(r.URL.Query().Get(TreeParam))
	if tree == "" {
		tree = zvol.TreeWorkspace
	}
	if !zvol.ValidTransferTree(tree) {
		writeError(w, http.StatusBadRequest, "transfer tree is invalid")
		return
	}
	from := r.URL.Query().Get(FromParam)
	if from != "" {
		if err := zvol.ValidateName("generation", from); err != nil {
			writeError(w, http.StatusBadRequest, "incremental base is invalid")
			return
		}
	}

	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "transfer concurrency limit reached")
		return
	}
	key := name + "/" + string(tree)
	if !s.begin(key) {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "generation transfer already in flight")
		return
	}
	defer s.finish(key)

	plan, err := s.cfg.Store.ResolveSend(r.Context(), zvol.GenerationID(name), tree, zvol.GenerationID(from))
	if err != nil {
		status, message := http.StatusInternalServerError, "transfer resolution failed"
		if errors.Is(err, zvol.ErrNotFound) {
			status, message = http.StatusNotFound, "generation is not resident"
		}
		s.cfg.Logger.Info("transfer request rejected",
			"status", status, "generation", name, "tree", string(tree), "err", err.Error())
		writeError(w, status, message)
		return
	}

	w.Header().Set("Content-Type", streamContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(IncrementalHeader, strconv.FormatBool(plan.Incremental))
	written, err := s.cfg.Store.Send(r.Context(), plan, w)
	s.cfg.Logger.Info("transfer stream served",
		"generation", name, "tree", string(tree),
		"incremental", plan.Incremental, "bytes", written,
		"stream_error", err != nil,
		"duration_ms", time.Since(started).Milliseconds())
}

func (s *Server) begin(key string) bool {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	if s.inflight[key] {
		return false
	}
	s.inflight[key] = true
	return true
}

func (s *Server) finish(key string) {
	s.inflightMu.Lock()
	delete(s.inflight, key)
	s.inflightMu.Unlock()
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
