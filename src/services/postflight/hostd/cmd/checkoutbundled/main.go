// checkoutbundled is the standalone checkout-bundle server: the
// checkoutbundle package behind a static lease fixture, for running on a
// runner host before hostd proper exists. hostd absorbs the package and
// replaces the fixture with its live lease table.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/checkoutbundle"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("checkoutbundled exiting", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	storeDir := os.Getenv("CHECKOUT_STORE_DIR")
	if storeDir == "" {
		return errors.New("CHECKOUT_STORE_DIR is required")
	}
	secretPath := os.Getenv("CHECKOUT_HOST_SECRET_FILE")
	if secretPath == "" {
		return errors.New("CHECKOUT_HOST_SECRET_FILE is required")
	}
	secret, err := os.ReadFile(secretPath)
	if err != nil {
		return fmt.Errorf("read host secret: %w", err)
	}
	if len(secret) < 32 {
		return errors.New("host secret must be at least 32 bytes")
	}

	fixturePath := os.Getenv("CHECKOUT_LEASE_FIXTURE")
	if fixturePath == "" {
		return errors.New("CHECKOUT_LEASE_FIXTURE is required (JSON array of lease identities)")
	}
	fixtureRaw, err := os.ReadFile(fixturePath)
	if err != nil {
		return fmt.Errorf("read lease fixture: %w", err)
	}
	var leases []checkoutbundle.LeaseIdentity
	if err := json.Unmarshal(fixtureRaw, &leases); err != nil {
		return fmt.Errorf("parse lease fixture: %w", err)
	}
	logger.Info("lease fixture loaded", "leases", len(leases))

	service := checkoutbundle.New(checkoutbundle.Config{
		StoreDir:          storeDir,
		HostSecret:        secret,
		GitHubWebBaseURL:  os.Getenv("CHECKOUT_GITHUB_WEB_BASE_URL"),
		MaxPackBytes:      envInt64("CHECKOUT_MAX_PACK_BYTES"),
		MaxConcurrent:     int(envInt64("CHECKOUT_MAX_CONCURRENT")),
		GitTimeout:        envDuration("CHECKOUT_GIT_TIMEOUT"),
		BundleTTL:         envDuration("CHECKOUT_BUNDLE_TTL"),
		BundleBudgetBytes: envInt64("CHECKOUT_BUNDLE_BUDGET_BYTES"),
		MirrorTTL:         envDuration("CHECKOUT_MIRROR_TTL"),
		Logger:            logger,
	}, &checkoutbundle.StaticResolver{Leases: leases})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go service.RunReaper(ctx, envDuration("CHECKOUT_REAP_INTERVAL"))

	listenAddr := os.Getenv("CHECKOUT_LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8480"
	}
	server := &http.Server{
		Addr:              listenAddr,
		Handler:           service.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	logger.Info("checkoutbundled listening", "addr", listenAddr, "store", storeDir)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func envInt64(name string) int64 {
	value, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if err != nil {
		return 0
	}
	return value
}

func envDuration(name string) time.Duration {
	value, err := time.ParseDuration(os.Getenv(name))
	if err != nil {
		return 0
	}
	return value
}
