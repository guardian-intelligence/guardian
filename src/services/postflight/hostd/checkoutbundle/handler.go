package checkoutbundle

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Handler serves the bundle route plus a health probe.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(BundlePath, s.handleBundle)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func (s *Service) handleBundle(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	s.Metrics.Requests.Add(1)
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	identity, err := s.authenticate(r.Context(), r)
	if err != nil {
		s.Metrics.Rejected.Add(1)
		status, message := http.StatusUnauthorized, "not authorized"
		if errors.Is(err, errResolverUnavailable) {
			status, message = http.StatusServiceUnavailable, "lease lookup is unavailable"
			w.Header().Set("Retry-After", "2")
		}
		s.cfg.Logger.Info("checkout request rejected",
			"status", status,
			"execution_id", r.Header.Get(executionIDHeader),
			"attempt_id", r.Header.Get(attemptIDHeader))
		writeError(w, status, message)
		return
	}

	var req bundleRequest
	body := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		s.rejectSpec(w, identity, errInvalid)
		return
	}
	spec, err := validateRequest(req, identity)
	if err != nil {
		s.rejectSpec(w, identity, err)
		return
	}

	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		s.Metrics.Rejected.Add(1)
		w.Header().Set("Retry-After", "2")
		s.cfg.Logger.Warn("checkout request throttled",
			"execution_id", identity.ExecutionID, "repository", spec.Repository)
		writeError(w, http.StatusTooManyRequests, "checkout concurrency limit reached")
		return
	}

	bundle, err := s.prepareBundle(r.Context(), identity, spec)
	if err != nil {
		status, message := classifyBundleError(err)
		s.Metrics.Rejected.Add(1)
		s.cfg.Logger.Error("checkout bundle failed",
			"status", status,
			"execution_id", identity.ExecutionID,
			"attempt_id", identity.AttemptID,
			"repository", spec.Repository,
			"sha", spec.SHA,
			"error", boundedGitError(err))
		writeError(w, status, message)
		return
	}
	file := bundle.File
	defer func() { _ = file.Close() }()

	w.Header().Set("Content-Type", packContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.FormatInt(bundle.SizeBytes, 10))
	w.Header().Set(shaHeader, spec.SHA)
	w.Header().Set(sizeHeader, strconv.FormatInt(bundle.SizeBytes, 10))
	w.Header().Set(cacheHitHeader, strconv.FormatBool(bundle.CacheHit))
	written, copyErr := io.Copy(w, file)
	s.Metrics.BytesServed.Add(written)
	s.cfg.Logger.Info("checkout bundle served",
		"execution_id", identity.ExecutionID,
		"attempt_id", identity.AttemptID,
		"repository", spec.Repository,
		"sha", spec.SHA,
		"cache_hit", bundle.CacheHit,
		"pack_bytes", bundle.SizeBytes,
		"streamed_bytes", written,
		"stream_error", copyErr != nil,
		"duration_ms", time.Since(start).Milliseconds())
}

func (s *Service) rejectSpec(w http.ResponseWriter, identity LeaseIdentity, err error) {
	status, message := classifyBundleError(err)
	s.Metrics.Rejected.Add(1)
	s.cfg.Logger.Info("checkout request rejected",
		"status", status,
		"execution_id", identity.ExecutionID,
		"attempt_id", identity.AttemptID,
		"error", boundedGitError(err))
	writeError(w, status, message)
}

// classifyBundleError maps the pipeline's typed failures onto the status
// vocabulary the action classifies: 400/404 terminal rejections, 403
// lease-boundary violations, 413 size, 429 elsewhere, 502 retryable
// transport, 500 everything unexpected.
func classifyBundleError(err error) (int, string) {
	switch {
	case errors.Is(err, errInvalid):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, errForbidden):
		return http.StatusForbidden, err.Error()
	case errors.Is(err, errNotFound):
		return http.StatusNotFound, "commit is not available from origin"
	case errors.Is(err, errTooLarge):
		return http.StatusRequestEntityTooLarge, "checkout pack exceeds the size limit"
	case errors.Is(err, errUpstream):
		return http.StatusBadGateway, "origin fetch failed"
	default:
		return http.StatusInternalServerError, "checkout bundle failed"
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
