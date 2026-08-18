package main

import (
	"context"
	"crypto/subtle"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	authorizationv1 "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/authorization/v1"
	"github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/authorization/v1/authorizationv1connect"
)

type config struct {
	listen     string
	spiceURL   string
	spiceToken string
	spiceCA    string
	checkToken string
	writeToken string
}

func loadConfig() (config, error) {
	cfg := config{
		listen:     envOr("LISTEN_ADDR", ":8080"),
		spiceURL:   strings.TrimRight(envOr("SPICEDB_HTTP_URL", "https://spicedb:8443"), "/"),
		spiceToken: strings.TrimSpace(os.Getenv("SPICEDB_TOKEN")),
		spiceCA:    envOr("SPICEDB_CA_FILE", "/tls/ca.crt"),
		checkToken: strings.TrimSpace(os.Getenv("AUTHORIZATION_CHECK_TOKEN")),
		writeToken: strings.TrimSpace(os.Getenv("AUTHORIZATION_WRITE_TOKEN")),
	}
	if cfg.spiceToken == "" || cfg.checkToken == "" || cfg.writeToken == "" {
		return config{}, errors.New("SPICEDB_TOKEN, AUTHORIZATION_CHECK_TOKEN, and AUTHORIZATION_WRITE_TOKEN are required")
	}
	if subtle.ConstantTimeCompare([]byte(cfg.checkToken), []byte(cfg.writeToken)) == 1 {
		return config{}, errors.New("authorization check and write tokens must differ")
	}
	if !strings.HasPrefix(cfg.spiceURL, "https://") {
		return config{}, errors.New("SPICEDB_HTTP_URL must use https")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("configuration", "error", err)
		os.Exit(1)
	}
	caPEM, err := os.ReadFile(cfg.spiceCA)
	if err != nil {
		slog.Error("read SpiceDB CA", "error", err)
		os.Exit(1)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		slog.Error("parse SpiceDB CA")
		os.Exit(1)
	}
	spice := &spiceDBClient{
		baseURL: cfg.spiceURL,
		token:   cfg.spiceToken,
		http: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: tlsConfig(roots),
			},
		},
	}
	registry := prometheus.NewRegistry()
	service := &authorizationService{
		spice:   spice,
		metrics: newAuthorizationMetrics(registry),
	}
	mux := http.NewServeMux()
	path, handler := authorizationv1connect.NewAuthorizationServiceHandler(service)
	mux.Handle(path, authenticate(cfg.checkToken, cfg.writeToken, handler))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		_, err := spice.check(ctx, checkInput{
			Resource:   objectRef{Type: "organization", ID: "__readiness__"},
			Permission: "view",
			Subject:    subjectRef{Object: objectRef{Type: "guardian_account", ID: "__readiness__"}},
		})
		if err != nil {
			http.Error(w, "authorization datastore unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	slog.Info("authorization API listening", "address", cfg.listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server", "error", err)
		os.Exit(1)
	}
}

func authenticate(checkToken, writeToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented := r.Header.Get("Authorization")
		allowed := tokenEqual(presented, checkToken)
		if r.URL.Path == authorizationv1connect.AuthorizationServiceWriteRelationshipsProcedure {
			allowed = tokenEqual(presented, writeToken)
		} else if r.URL.Path == authorizationv1connect.AuthorizationServiceCheckProcedure {
			allowed = allowed || tokenEqual(presented, writeToken)
		}
		if !allowed {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func tokenEqual(presented, token string) bool {
	expected := []byte("Bearer " + token)
	actual := []byte(presented)
	return len(actual) == len(expected) && subtle.ConstantTimeCompare(actual, expected) == 1
}

type authorizationService struct {
	authorizationv1connect.UnimplementedAuthorizationServiceHandler
	spice   *spiceDBClient
	metrics *authorizationMetrics
}

func (s *authorizationService) Check(
	ctx context.Context,
	request *connect.Request[authorizationv1.CheckRequest],
) (*connect.Response[authorizationv1.CheckResponse], error) {
	started := time.Now()
	input, err := checkedCheckInput(request.Msg)
	if err != nil {
		s.metrics.observeCheck(started, "error")
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	result, err := s.spice.check(ctx, input)
	if err != nil {
		s.metrics.observeCheck(started, "error")
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	decision := "deny"
	if result.Allowed {
		decision = "allow"
	}
	s.metrics.observeCheck(started, decision)
	return connect.NewResponse(&authorizationv1.CheckResponse{
		Allowed:        result.Allowed,
		CheckedAtToken: result.Token,
	}), nil
}

func (s *authorizationService) WriteRelationships(
	ctx context.Context,
	request *connect.Request[authorizationv1.WriteRelationshipsRequest],
) (*connect.Response[authorizationv1.WriteRelationshipsResponse], error) {
	started := time.Now()
	if len(request.Msg.GetUpdates()) == 0 || len(request.Msg.GetUpdates()) > 100 {
		s.metrics.observeWrite(started, "error")
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("updates must contain between 1 and 100 relationships"))
	}
	updates := make([]relationshipUpdate, 0, len(request.Msg.GetUpdates()))
	for _, update := range request.Msg.GetUpdates() {
		checked, err := checkedRelationship(update)
		if err != nil {
			s.metrics.observeWrite(started, "error")
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		updates = append(updates, checked)
	}
	token, err := s.spice.write(ctx, updates)
	if err != nil {
		s.metrics.observeWrite(started, "error")
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	s.metrics.observeWrite(started, "success")
	return connect.NewResponse(&authorizationv1.WriteRelationshipsResponse{WrittenAtToken: token}), nil
}

func checkedCheckInput(request *authorizationv1.CheckRequest) (checkInput, error) {
	if request.GetResource() == nil || request.GetSubject() == nil || request.GetSubject().GetObject() == nil {
		return checkInput{}, errors.New("resource and subject are required")
	}
	resource := objectRef{Type: request.GetResource().GetType(), ID: request.GetResource().GetId()}
	subject := subjectRef{
		Object: objectRef{
			Type: request.GetSubject().GetObject().GetType(),
			ID:   request.GetSubject().GetObject().GetId(),
		},
		OptionalRelation: request.GetSubject().GetOptionalRelation(),
	}
	if err := validateObject(resource); err != nil {
		return checkInput{}, fmt.Errorf("resource: %w", err)
	}
	if err := validateSubject(subject); err != nil {
		return checkInput{}, fmt.Errorf("subject: %w", err)
	}
	if !allowedPermission(resource.Type, request.GetPermission()) {
		return checkInput{}, fmt.Errorf("permission %q is not valid for %s", request.GetPermission(), resource.Type)
	}
	return checkInput{
		Resource:            resource,
		Permission:          request.GetPermission(),
		Subject:             subject,
		AtLeastAsFreshToken: request.GetAtLeastAsFreshToken(),
	}, nil
}

func checkedRelationship(update *authorizationv1.RelationshipUpdate) (relationshipUpdate, error) {
	if update.GetResource() == nil || update.GetSubject() == nil || update.GetSubject().GetObject() == nil {
		return relationshipUpdate{}, errors.New("relationship resource and subject are required")
	}
	resource := objectRef{Type: update.GetResource().GetType(), ID: update.GetResource().GetId()}
	subject := subjectRef{
		Object: objectRef{
			Type: update.GetSubject().GetObject().GetType(),
			ID:   update.GetSubject().GetObject().GetId(),
		},
		OptionalRelation: update.GetSubject().GetOptionalRelation(),
	}
	if err := validateObject(resource); err != nil {
		return relationshipUpdate{}, fmt.Errorf("resource: %w", err)
	}
	if err := validateSubject(subject); err != nil {
		return relationshipUpdate{}, fmt.Errorf("subject: %w", err)
	}
	if subject.OptionalRelation != "" ||
		!allowedRelation(resource.Type, update.GetRelation(), subject.Object.Type) {
		return relationshipUpdate{}, fmt.Errorf("relation %s#%s@%s is not part of the Guardian graph contract", resource.Type, update.GetRelation(), subject.Object.Type)
	}
	operation := ""
	switch update.GetOperation() {
	case authorizationv1.RelationshipOperation_RELATIONSHIP_OPERATION_TOUCH:
		operation = "OPERATION_TOUCH"
	case authorizationv1.RelationshipOperation_RELATIONSHIP_OPERATION_DELETE:
		operation = "OPERATION_DELETE"
	default:
		return relationshipUpdate{}, errors.New("relationship operation must be touch or delete")
	}
	return relationshipUpdate{
		Operation: operation,
		Resource:  resource,
		Relation:  update.GetRelation(),
		Subject:   subject,
	}, nil
}
