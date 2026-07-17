package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	Listen                   string
	DatabaseURL              string
	PublicBaseURL            *url.URL
	OIDCIssuer               string
	OIDCClientID             string
	StripeAPIKey             string
	StripeAccountID          string
	StripeWebhookSecret      string
	StripeCanaryPriceID      string
	StripeAPIBase            string
	TigerBeetleAddresses     []string
	TigerBeetleClusterID     uint64
	JournalEndpoint          string
	JournalRegion            string
	JournalBucket            string
	JournalAccessKeyID       string
	JournalSecretAccessKey   string
	JournalPrefix            string
	OTLPEndpoint             string
	CanaryToken              string
	CanaryAmountCents        int64
	CustomerCheckoutEnabled  bool
	EventWorkerInterval      time.Duration
	ReconciliationInterval   time.Duration
	CanaryCompletionDeadline time.Duration
}

func loadConfig() (config, error) {
	publicBase, err := url.Parse(os.Getenv("PUBLIC_BASE_URL"))
	if err != nil || publicBase.Scheme != "https" || publicBase.Host == "" {
		return config{}, errors.New("PUBLIC_BASE_URL must be an absolute https URL")
	}
	clusterID, err := strconv.ParseUint(envOr("TIGERBEETLE_CLUSTER_ID", "0"), 10, 64)
	if err != nil {
		return config{}, fmt.Errorf("TIGERBEETLE_CLUSTER_ID: %w", err)
	}
	amount, err := strconv.ParseInt(envOr("CANARY_AMOUNT_CENTS", "50"), 10, 64)
	if err != nil || amount < 50 || amount > 1000 {
		return config{}, errors.New("CANARY_AMOUNT_CENTS must be between 50 and 1000")
	}
	customerCheckoutEnabled, err := strconv.ParseBool(envOr("CUSTOMER_CHECKOUT_ENABLED", "false"))
	if err != nil {
		return config{}, fmt.Errorf("CUSTOMER_CHECKOUT_ENABLED: %w", err)
	}
	if customerCheckoutEnabled {
		return config{}, errors.New("customer checkout is not admitted until ledger 1 hardening is complete")
	}
	cfg := config{
		Listen:                   envOr("PAYMENTS_LISTEN", ":8080"),
		DatabaseURL:              os.Getenv("DATABASE_URL"),
		PublicBaseURL:            publicBase,
		OIDCIssuer:               strings.TrimSuffix(os.Getenv("OIDC_ISSUER"), "/"),
		OIDCClientID:             os.Getenv("OIDC_CLIENT_ID"),
		StripeAPIKey:             os.Getenv("STRIPE_API_KEY"),
		StripeAccountID:          os.Getenv("STRIPE_ACCOUNT_ID"),
		StripeWebhookSecret:      os.Getenv("STRIPE_WEBHOOK_SECRET"),
		StripeCanaryPriceID:      os.Getenv("STRIPE_CANARY_PRICE_ID"),
		StripeAPIBase:            os.Getenv("STRIPE_API_BASE"),
		TigerBeetleAddresses:     splitNonEmpty(os.Getenv("TIGERBEETLE_ADDRESSES")),
		TigerBeetleClusterID:     clusterID,
		JournalEndpoint:          os.Getenv("JOURNAL_S3_ENDPOINT"),
		JournalRegion:            envOr("JOURNAL_S3_REGION", "auto"),
		JournalBucket:            os.Getenv("JOURNAL_S3_BUCKET"),
		JournalAccessKeyID:       os.Getenv("JOURNAL_S3_ACCESS_KEY_ID"),
		JournalSecretAccessKey:   os.Getenv("JOURNAL_S3_SECRET_ACCESS_KEY"),
		JournalPrefix:            strings.Trim(os.Getenv("JOURNAL_S3_PREFIX"), "/"),
		OTLPEndpoint:             os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"),
		CanaryToken:              os.Getenv("PAYMENTS_CANARY_TOKEN"),
		CanaryAmountCents:        amount,
		CustomerCheckoutEnabled:  customerCheckoutEnabled,
		EventWorkerInterval:      time.Second,
		ReconciliationInterval:   time.Minute,
		CanaryCompletionDeadline: 50 * time.Second,
	}
	for name, value := range map[string]string{
		"DATABASE_URL":                 cfg.DatabaseURL,
		"OIDC_ISSUER":                  cfg.OIDCIssuer,
		"OIDC_CLIENT_ID":               cfg.OIDCClientID,
		"STRIPE_API_KEY":               cfg.StripeAPIKey,
		"STRIPE_ACCOUNT_ID":            cfg.StripeAccountID,
		"STRIPE_WEBHOOK_SECRET":        cfg.StripeWebhookSecret,
		"STRIPE_CANARY_PRICE_ID":       cfg.StripeCanaryPriceID,
		"JOURNAL_S3_ENDPOINT":          cfg.JournalEndpoint,
		"JOURNAL_S3_BUCKET":            cfg.JournalBucket,
		"JOURNAL_S3_ACCESS_KEY_ID":     cfg.JournalAccessKeyID,
		"JOURNAL_S3_SECRET_ACCESS_KEY": cfg.JournalSecretAccessKey, // gitleaks:allow -- identifiers only
		"JOURNAL_S3_PREFIX":            cfg.JournalPrefix,
		"PAYMENTS_CANARY_TOKEN":        cfg.CanaryToken,
	} {
		if strings.TrimSpace(value) == "" {
			return config{}, fmt.Errorf("%s is required", name)
		}
	}
	if !strings.HasPrefix(cfg.StripeAccountID, "acct_") {
		return config{}, errors.New("STRIPE_ACCOUNT_ID must start with acct_")
	}
	if !strings.HasPrefix(cfg.StripeWebhookSecret, "whsec_") {
		return config{}, errors.New("STRIPE_WEBHOOK_SECRET must start with whsec_")
	}
	if !strings.HasPrefix(cfg.StripeCanaryPriceID, "price_") {
		return config{}, errors.New("STRIPE_CANARY_PRICE_ID must start with price_")
	}
	if !strings.HasPrefix(cfg.StripeAPIKey, "rk_test_") {
		return config{}, errors.New("STRIPE_API_KEY must be a restricted sandbox key")
	}
	if len(cfg.TigerBeetleAddresses) != 3 {
		return config{}, errors.New("TIGERBEETLE_ADDRESSES must contain exactly three addresses")
	}
	if cfg.TigerBeetleClusterID != 0 {
		return config{}, errors.New("TIGERBEETLE_CLUSTER_ID must remain 0 for the accepted cluster")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func splitNonEmpty(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if clean := strings.TrimSpace(part); clean != "" {
			out = append(out, clean)
		}
	}
	return out
}
