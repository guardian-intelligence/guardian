package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// config is the full environment surface of the control plane.
//
// Stage (a) is single-tenant: one GitHub App installation, pinned here.
// FIXME(multi-tenant): the pinned installation id is what replaces verself's
// installation/repository binding lookup; multi-tenancy reintroduces the
// mirror tables and drops this field.
type config struct {
	appID             int64
	installationID    int64
	webhookSecret     string
	privateKeyPEM     string
	apiBaseURL        string
	runnerClassPrefix string
	databaseURL       string
	listenAddr        string
	workerInterval    time.Duration
	workerBatchSize   int
	maxDeliveryTries  int32
	commentInterval   time.Duration
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadConfig() (config, error) {
	var errs []error
	required := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", key))
		}
		return v
	}
	requiredID := func(key string) int64 {
		v := required(key)
		if v == "" {
			return 0
		}
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			errs = append(errs, fmt.Errorf("%s: %q is not a positive integer", key, v))
			return 0
		}
		return id
	}
	duration := func(key, fallback string) time.Duration {
		v := envOr(key, fallback)
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s: %q is not a positive duration", key, v))
			return 0
		}
		return d
	}
	positiveInt := func(key, fallback string) int {
		v := envOr(key, fallback)
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			errs = append(errs, fmt.Errorf("%s: %q is not a positive integer", key, v))
			return 0
		}
		return n
	}

	cfg := config{
		appID:             requiredID("GITHUB_APP_ID"),
		installationID:    requiredID("GITHUB_APP_INSTALLATION_ID"),
		webhookSecret:     required("GITHUB_WEBHOOK_SECRET"),
		privateKeyPEM:     required("GITHUB_APP_PRIVATE_KEY_PEM"),
		databaseURL:       required("DATABASE_URL"),
		apiBaseURL:        envOr("GITHUB_API_BASE_URL", "https://api.github.com"),
		runnerClassPrefix: envOr("RUNNER_CLASS_PREFIX", "verself-"),
		listenAddr:        envOr("LISTEN_ADDR", ":8080"),
		workerInterval:    duration("WORKER_INTERVAL", "500ms"),
		workerBatchSize:   positiveInt("WORKER_BATCH_SIZE", "16"),
		maxDeliveryTries:  int32(positiveInt("MAX_DELIVERY_TRIES", "8")),
		commentInterval:   duration("COMMENT_INTERVAL", "5s"),
	}
	if len(errs) > 0 {
		return cfg, errors.Join(errs...)
	}
	return cfg, nil
}
