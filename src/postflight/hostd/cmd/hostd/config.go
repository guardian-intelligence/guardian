package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// config is the full environment surface of hostd. Everything dynamic
// (members, assignments, pool targets, reap verbs) arrives over sync; the environment only
// describes what this host is.
type config struct {
	hostID                       string
	syncURL                      string
	syncSecret                   string
	hostSecretFile               string
	stateDir                     string
	pool                         string
	class                        string
	imageID                      string
	slots                        int
	cpus                         int
	memoryMiB                    int
	storageMinimumAvailableBytes int64
	qemuPath                     string
	firmwarePath                 string
	criuVersion                  string
	syncInterval                 time.Duration
	guestNetwork                 string
	guestBridge                  string
	tapLifecyclePath             string

	checkoutListenAddr  string
	checkoutGuestOrigin string

	transferListenAddr string
	transferOrigin     string
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
	positiveInt64 := func(key, fallback string) int64 {
		v := envOr(key, fallback)
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			errs = append(errs, fmt.Errorf("%s: %q is not a positive integer", key, v))
			return 0
		}
		return n
	}

	cfg := config{
		hostID:                       required("HOSTD_HOST_ID"),
		syncURL:                      required("HOSTD_SYNC_URL"),
		syncSecret:                   required("HOSTD_SYNC_SECRET"),
		hostSecretFile:               required("HOSTD_HOST_SECRET_FILE"),
		stateDir:                     required("HOSTD_STATE_DIR"),
		pool:                         required("HOSTD_POOL"),
		class:                        required("HOSTD_CLASS"),
		imageID:                      required("HOSTD_IMAGE_ID"),
		slots:                        positiveInt("HOSTD_SLOTS", "4"),
		cpus:                         positiveInt("HOSTD_CPUS", "4"),
		memoryMiB:                    positiveInt("HOSTD_MEMORY_MIB", "16384"),
		storageMinimumAvailableBytes: positiveInt64("HOSTD_STORAGE_MIN_AVAILABLE_BYTES", "68719476736"),
		qemuPath:                     envOr("HOSTD_QEMU_PATH", "/usr/bin/qemu-system-x86_64"),
		firmwarePath:                 required("HOSTD_FIRMWARE_PATH"),
		criuVersion:                  required("HOSTD_CRIU_VERSION"),
		syncInterval:                 duration("HOSTD_SYNC_INTERVAL", "2s"),
		guestNetwork:                 envOr("HOSTD_GUEST_NETWORK", "none"),
		guestBridge:                  os.Getenv("HOSTD_GUEST_BRIDGE"),
		tapLifecyclePath:             envOr("HOSTD_TAP_LIFECYCLE_PATH", "/usr/local/libexec/postflight-tap"),
		checkoutListenAddr:           envOr("HOSTD_CHECKOUT_LISTEN_ADDR", "127.0.0.1:8480"),
		checkoutGuestOrigin:          required("HOSTD_CHECKOUT_GUEST_ORIGIN"),
		transferListenAddr:           os.Getenv("HOSTD_TRANSFER_LISTEN_ADDR"),
		transferOrigin:               os.Getenv("HOSTD_TRANSFER_ORIGIN"),
	}
	switch cfg.guestNetwork {
	case "none", "user":
	case "tap":
		if cfg.guestBridge == "" {
			errs = append(errs, errors.New("HOSTD_GUEST_BRIDGE is required for tap networking"))
		}
	default:
		errs = append(errs, fmt.Errorf("HOSTD_GUEST_NETWORK: %q is not none, user, or tap", cfg.guestNetwork))
	}
	if !filepath.IsAbs(cfg.tapLifecyclePath) {
		errs = append(errs, errors.New("HOSTD_TAP_LIFECYCLE_PATH must be absolute"))
	}
	if err := validateTransferConfig(cfg.transferListenAddr, cfg.transferOrigin); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return cfg, errors.Join(errs...)
	}
	return cfg, nil
}

// validateTransferConfig admits the generation-transfer lane only as a
// coherent pair bound to one specific address. The listener serves sealed
// tenant workspaces to anyone holding the fleet credential, so a wildcard
// bind — which would expose it beyond the transfer VLAN — is refused
// outright rather than warned about.
func validateTransferConfig(listenAddr, origin string) error {
	if listenAddr == "" && origin == "" {
		return nil
	}
	if listenAddr == "" || origin == "" {
		return errors.New("HOSTD_TRANSFER_LISTEN_ADDR and HOSTD_TRANSFER_ORIGIN must be set together")
	}
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return fmt.Errorf("HOSTD_TRANSFER_LISTEN_ADDR: %q is not host:port", listenAddr)
	}
	if host == "" {
		return errors.New("HOSTD_TRANSFER_LISTEN_ADDR must name the transfer VLAN address, not a wildcard bind")
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return errors.New("HOSTD_TRANSFER_LISTEN_ADDR must name the transfer VLAN address, not a wildcard bind")
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("HOSTD_TRANSFER_ORIGIN: %q is not an http(s) origin", origin)
	}
	return nil
}
