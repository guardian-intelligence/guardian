package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// config is the full environment surface of the control plane.
type config struct {
	appID             int64
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

	// Stage (b): the hostd sync endpoint and the scheduler. Both default
	// inert — an unset sync secret leaves the endpoint unregistered, and the
	// scheduler only runs when explicitly enabled — so the deploy changes
	// nothing until each is switched on.
	hostdSyncSecret   string
	schedulerEnabled  bool
	schedulerInterval time.Duration
	runnerPoolSize    int
	// sealTimeout bounds how long an assignment may wait for its host to confirm
	// a requested workspace seal before the candidate is discarded.
	sealTimeout time.Duration
	// verdictTimeout bounds how long a sealed candidate may wait for its
	// GitHub verdict to be observed from the API before it is discarded —
	// a lost completed delivery would otherwise strand the candidate (and
	// its dataset on the host) forever.
	verdictTimeout time.Duration
	// hostOfflineTimeout is how long a host may go without syncing before
	// its active assignments are requeued (the host is presumed dead).
	hostOfflineTimeout time.Duration
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
		appID:              requiredID("GITHUB_APP_ID"),
		webhookSecret:      required("GITHUB_WEBHOOK_SECRET"),
		privateKeyPEM:      required("GITHUB_APP_PRIVATE_KEY_PEM"),
		databaseURL:        required("DATABASE_URL"),
		apiBaseURL:         envOr("GITHUB_API_BASE_URL", "https://api.github.com"),
		runnerClassPrefix:  envOr("RUNNER_CLASS_PREFIX", "postflight-"),
		listenAddr:         envOr("LISTEN_ADDR", ":8080"),
		workerInterval:     duration("WORKER_INTERVAL", "500ms"),
		workerBatchSize:    positiveInt("WORKER_BATCH_SIZE", "16"),
		maxDeliveryTries:   int32(positiveInt("MAX_DELIVERY_TRIES", "8")),
		commentInterval:    duration("COMMENT_INTERVAL", "5s"),
		hostdSyncSecret:    os.Getenv("HOSTD_SYNC_SECRET"),
		schedulerEnabled:   os.Getenv("SCHEDULER_ENABLED") == "true",
		schedulerInterval:  duration("SCHEDULER_INTERVAL", "500ms"),
		runnerPoolSize:     positiveInt("RUNNER_POOL_SIZE", "6"),
		sealTimeout:        duration("ASSIGNMENT_SEAL_TIMEOUT", "10m"),
		verdictTimeout:     duration("GENERATION_VERDICT_TIMEOUT", "1h"),
		hostOfflineTimeout: duration("HOST_OFFLINE_TIMEOUT", "5m"),
	}
	if len(errs) > 0 {
		return cfg, errors.Join(errs...)
	}
	return cfg, nil
}
