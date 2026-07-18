package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

type config struct {
	PublicPageURL    string
	CanaryToken      string
	ClickHouseURL    string
	ClickHouseUser   string
	ClickHousePass   string
	ChromeExecutable string
	Timeout          time.Duration
}

type checkoutResponse struct {
	RunID        string `json:"run_id"`
	OrderID      string `json:"order_id"`
	Status       string `json:"status"`
	FailureClass string `json:"failure_class"`
	TraceID      string `json:"trace_id"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("configuration", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	if err := run(ctx, cfg); err != nil {
		slog.Error("checkout canary failed", "error_class", classify(err), "error", err)
		os.Exit(1)
	}
	slog.Info("checkout canary passed")
}

func loadConfig() (config, error) {
	timeout, err := time.ParseDuration(envOr("CANARY_TIMEOUT", "2m30s"))
	if err != nil || timeout < time.Minute || timeout > 5*time.Minute {
		return config{}, errors.New("CANARY_TIMEOUT must be between 1m and 5m")
	}
	cfg := config{
		PublicPageURL:    envOr("PAYMENTS_PUBLIC_CANARY_URL", "https://guardianintelligence.org/api/payments/v1/canary"),
		CanaryToken:      os.Getenv("PAYMENTS_CANARY_TOKEN"),
		ClickHouseURL:    envOr("CLICKHOUSE_HTTP_URL", "http://chendpoint-clickhouse-analytics.tenant-root.svc.cozy.local:8123"),
		ClickHouseUser:   envOr("CLICKHOUSE_USER", "ingest"),
		ClickHousePass:   os.Getenv("CLICKHOUSE_PASSWORD"),
		ChromeExecutable: os.Getenv("CHROME_EXECUTABLE"),
		Timeout:          timeout,
	}
	for label, rawURL := range map[string]string{
		"PAYMENTS_PUBLIC_CANARY_URL": cfg.PublicPageURL,
		"CLICKHOUSE_HTTP_URL":        cfg.ClickHouseURL,
	} {
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return config{}, fmt.Errorf("%s must be an absolute URL", label)
		}
	}
	if cfg.CanaryToken == "" || cfg.ClickHousePass == "" {
		return config{}, errors.New("PAYMENTS_CANARY_TOKEN and CLICKHOUSE_PASSWORD are required")
	}
	if cfg.ChromeExecutable == "" {
		cfg.ChromeExecutable, err = discoverChrome()
		if err != nil {
			return config{}, err
		}
	}
	return cfg, nil
}

func run(ctx context.Context, cfg config) error {
	traceID, parentID, err := newTraceContext()
	if err != nil {
		return err
	}
	traceparent := "00-" + traceID + "-" + parentID + "-01"
	result, err := browserPayment(ctx, cfg, traceparent)
	if err != nil {
		return fmt.Errorf("browser: %w", err)
	}
	if result.TraceID != traceID {
		return errors.New("trace_context")
	}
	if result.Status != "passed" {
		return fmt.Errorf("stripe_rail: %s", result.FailureClass)
	}
	if err := waitForTrace(ctx, cfg, traceID); err != nil {
		return fmt.Errorf("clickhouse_trace: %w", err)
	}
	slog.Info(
		"checkout canary evidence",
		"run_id", result.RunID,
		"order_id", result.OrderID,
		"trace_id", traceID,
	)
	return nil
}

func browserPayment(
	ctx context.Context,
	cfg config,
	traceparent string,
) (checkoutResponse, error) {
	allocatorOptions := append(
		chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(cfg.ChromeExecutable),
		chromedp.NoSandbox,
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-background-networking", true),
	)
	allocatorCtx, cancelAllocator := chromedp.NewExecAllocator(ctx, allocatorOptions...)
	defer cancelAllocator()
	browserCtx, cancelBrowser := chromedp.NewContext(allocatorCtx)
	defer cancelBrowser()

	if err := chromedp.Run(
		browserCtx,
		chromedp.Navigate(cfg.PublicPageURL),
		chromedp.WaitVisible("#canary-ready", chromedp.ByQuery),
	); err != nil {
		return checkoutResponse{}, fmt.Errorf("open canary page: %w", err)
	}
	createURL := strings.TrimSuffix(cfg.PublicPageURL, "/") + "/checkout"
	script := fmt.Sprintf(
		`(async () => {
			const response = await fetch(%s, {
				method: "POST",
				headers: {
					"Authorization": "Bearer " + %s,
					"traceparent": %s
				}
			});
			const body = await response.text();
			if (!response.ok) throw new Error("checkout canary HTTP " + response.status);
			return JSON.parse(body);
		})()`,
		jsString(createURL),
		jsString(cfg.CanaryToken),
		jsString(traceparent),
	)
	var result checkoutResponse
	if err := chromedp.Run(
		browserCtx,
		chromedp.Evaluate(
			script,
			&result,
			func(params *runtime.EvaluateParams) *runtime.EvaluateParams {
				return params.WithAwaitPromise(true)
			},
		),
		); err != nil {
		return checkoutResponse{}, fmt.Errorf("execute checkout canary in browser: %w", err)
	}
	if result.RunID == "" || result.OrderID == "" || result.TraceID == "" || result.Status == "" {
		return checkoutResponse{}, errors.New("checkout canary response was incomplete")
	}
	return result, nil
}

func waitForTrace(ctx context.Context, cfg config, traceID string) error {
	if len(traceID) != 32 {
		return errors.New("invalid trace ID")
	}
	if _, err := hex.DecodeString(traceID); err != nil {
		return errors.New("invalid trace ID")
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		complete, err := traceComplete(ctx, cfg, traceID)
		if err == nil && complete {
			return nil
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return err
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func traceComplete(ctx context.Context, cfg config, traceID string) (bool, error) {
	query := fmt.Sprintf(
		`SELECT
countIf(SpanName = 'POST /api/payments/v1/canary/checkout'),
countIf(SpanName = 'canary.browser_to_tigerbeetle'),
countIf(SpanName = 'stripe.payment_intent.succeeded'),
countIf(SpanName = 'tigerbeetle.project_payment')
FROM guardian_analytics.otel_traces
WHERE TraceId = '%s' AND ServiceName = 'guardian-payments'
FORMAT TSV`,
		traceID,
	)
	endpoint, err := url.Parse(cfg.ClickHouseURL)
	if err != nil {
		return false, err
	}
	parameters := endpoint.Query()
	parameters.Set("query", query)
	endpoint.RawQuery = parameters.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return false, err
	}
	request.SetBasicAuth(cfg.ClickHouseUser, cfg.ClickHousePass)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return false, err
	}
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("ClickHouse HTTP %d", response.StatusCode)
	}
	fields := strings.Fields(string(body))
	if len(fields) != 4 {
		return false, fmt.Errorf("unexpected ClickHouse response")
	}
	for _, field := range fields {
		count, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			return false, errors.New("invalid ClickHouse count")
		}
		if count < 1 {
			return false, nil
		}
	}
	return true, nil
}

func newTraceContext() (string, string, error) {
	var traceBytes [16]byte
	var parentBytes [8]byte
	if _, err := rand.Read(traceBytes[:]); err != nil {
		return "", "", err
	}
	if _, err := rand.Read(parentBytes[:]); err != nil {
		return "", "", err
	}
	return hex.EncodeToString(traceBytes[:]), hex.EncodeToString(parentBytes[:]), nil
}

func discoverChrome() (string, error) {
	patterns := []string{
		"/ms-playwright/chromium-*/chrome-linux*/chrome",
		"/ms-playwright/chromium_headless_shell-*/chrome-headless-shell-linux*/chrome-headless-shell",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium",
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err == nil && len(matches) > 0 {
			return matches[0], nil
		}
	}
	return "", errors.New("Chrome executable not found")
}

func classify(err error) string {
	message := err.Error()
	for _, class := range []string{
		"browser",
		"clickhouse_trace",
		"trace_context",
		"stripe_rail",
	} {
		if strings.Contains(message, class) {
			return class
		}
	}
	return "dependency_or_invariant"
}

func jsString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
