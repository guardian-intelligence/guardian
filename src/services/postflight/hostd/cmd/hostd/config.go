package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// config is the full environment surface of hostd. Everything dynamic
// (members, assignments, pool targets, reap verbs) arrives over sync; the environment only
// describes what this host is.
type config struct {
	hostID         string
	syncURL        string
	syncSecret     string
	hostSecretFile string
	stateDir       string
	pool           string
	class          string
	imageID        string
	slots          int
	cpus           int
	memoryMiB      int
	qemuPath       string
	firmwarePath   string
	criuVersion    string
	syncInterval   time.Duration
	guestNetwork   string

	checkoutListenAddr  string
	checkoutGuestOrigin string
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
		hostID:              required("HOSTD_HOST_ID"),
		syncURL:             required("HOSTD_SYNC_URL"),
		syncSecret:          required("HOSTD_SYNC_SECRET"),
		hostSecretFile:      required("HOSTD_HOST_SECRET_FILE"),
		stateDir:            required("HOSTD_STATE_DIR"),
		pool:                required("HOSTD_POOL"),
		class:               required("HOSTD_CLASS"),
		imageID:             required("HOSTD_IMAGE_ID"),
		slots:               positiveInt("HOSTD_SLOTS", "4"),
		cpus:                positiveInt("HOSTD_CPUS", "4"),
		memoryMiB:           positiveInt("HOSTD_MEMORY_MIB", "16384"),
		qemuPath:            envOr("HOSTD_QEMU_PATH", "/usr/bin/qemu-system-x86_64"),
		firmwarePath:        required("HOSTD_FIRMWARE_PATH"),
		criuVersion:         required("HOSTD_CRIU_VERSION"),
		syncInterval:        duration("HOSTD_SYNC_INTERVAL", "2s"),
		guestNetwork:        envOr("HOSTD_GUEST_NETWORK", "none"),
		checkoutListenAddr:  envOr("HOSTD_CHECKOUT_LISTEN_ADDR", "127.0.0.1:8480"),
		checkoutGuestOrigin: required("HOSTD_CHECKOUT_GUEST_ORIGIN"),
	}
	switch cfg.guestNetwork {
	case "none", "user":
	default:
		errs = append(errs, fmt.Errorf("HOSTD_GUEST_NETWORK: %q is not none or user", cfg.guestNetwork))
	}
	if len(errs) > 0 {
		return cfg, errors.Join(errs...)
	}
	return cfg, nil
}
