package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

type config struct {
	PageURL          string
	GitHubUsername   string
	GitHubPassword   string
	GitHubTOTPSeed   string
	ChromeExecutable string
	Timeout          time.Duration
}

type sessionResponse struct {
	Authenticated bool `json:"authenticated"`
	User          struct {
		Subject  string `json:"subject"`
		Username string `json:"username"`
	} `json:"user"`
}

type oauthPageState struct {
	Host            string `json:"host"`
	Path            string `json:"path"`
	HasTOTP         bool   `json:"hasTOTP"`
	CanGrant        bool   `json:"canGrant"`
	GrantBlocked    bool   `json:"grantBlocked"`
	HasKeycloakPage bool   `json:"hasKeycloakPage"`
	HasCollision    bool   `json:"hasCollision"`
	HasError        bool   `json:"hasError"`
}

type oauthPageAction int

const (
	oauthWait oauthPageAction = iota
	oauthComplete
	oauthSubmitTOTP
	oauthGrant
)

// A rendered Keycloak document. Redirect pass-throughs under /realms/ are
// legitimate and carry none of this DOM, so the probe never keys on the URL.
const keycloakRenderedPageJS = `Boolean(document.querySelector(` +
	`"#kc-page-title, #kc-header, .login-pf-page, #kc-error-message, form#kc-idp-review-profile-form"))`

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
		slog.Error("Guardian OAuth canary failed", "error", err)
		os.Exit(1)
	}
	slog.Info("Guardian OAuth canary passed")
}

func loadConfig() (config, error) {
	timeout, err := time.ParseDuration(envOr("CANARY_TIMEOUT", "2m30s"))
	if err != nil || timeout < time.Minute || timeout > 5*time.Minute {
		return config{}, errors.New("CANARY_TIMEOUT must be between 1m and 5m")
	}
	cfg := config{
		PageURL:          envOr("POSTFLIGHT_URL", "https://guardianintelligence.org/postflight"),
		GitHubUsername:   strings.TrimSpace(os.Getenv("GITHUB_CANARY_USERNAME")),
		GitHubPassword:   os.Getenv("GITHUB_CANARY_PASSWORD"),
		GitHubTOTPSeed:   strings.TrimSpace(os.Getenv("GITHUB_CANARY_TOTP_SECRET")),
		ChromeExecutable: strings.TrimSpace(os.Getenv("CHROME_EXECUTABLE")),
		Timeout:          timeout,
	}
	parsed, err := url.Parse(cfg.PageURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return config{}, errors.New("POSTFLIGHT_URL must be an absolute HTTPS URL")
	}
	if cfg.GitHubUsername == "" || cfg.GitHubPassword == "" || cfg.GitHubTOTPSeed == "" {
		return config{}, errors.New("GitHub canary username, password, and TOTP secret are required")
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
	allocatorOptions := append(
		chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(cfg.ChromeExecutable),
		chromedp.NoSandbox,
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
	)
	allocatorCtx, cancelAllocator := chromedp.NewExecAllocator(ctx, allocatorOptions...)
	defer cancelAllocator()
	browserCtx, cancelBrowser := chromedp.NewContext(allocatorCtx)
	defer cancelBrowser()

	if err := chromedp.Run(
		browserCtx,
		chromedp.Navigate(cfg.PageURL),
		chromedp.WaitVisible("#postflight-sign-in", chromedp.ByQuery),
		chromedp.Click("#postflight-sign-in", chromedp.ByQuery),
	); err != nil {
		return fmt.Errorf("open Guardian login: %w", err)
	}
	if err := awaitGitHubRedirect(browserCtx); err != nil {
		return err
	}
	if err := chromedp.Run(
		browserCtx,
		chromedp.WaitVisible("input[name=login]", chromedp.ByQuery),
		chromedp.SendKeys("input[name=login]", cfg.GitHubUsername, chromedp.ByQuery),
		chromedp.SendKeys("input[name=password]", cfg.GitHubPassword, chromedp.ByQuery),
		chromedp.Click("input[type=submit], button[type=submit]", chromedp.ByQuery),
	); err != nil {
		return fmt.Errorf("GitHub primary login: %w", err)
	}

	if err := finishGitHubAuthorization(browserCtx, cfg); err != nil {
		return err
	}
	if err := awaitPostflightLanding(
		browserCtx,
		"/postflight/console",
		"[data-postflight-console=ready]",
		"Postflight console landing",
	); err != nil {
		return err
	}

	var envelope struct {
		Status int             `json:"status"`
		Body   sessionResponse `json:"body"`
	}
	if err := chromedp.Run(browserCtx, chromedp.Evaluate(
		`fetch("/postflight/auth/session", {credentials:"same-origin"})
			.then(async response => ({status: response.status, body: await response.json()}))`,
		&envelope,
		awaitPromise,
	)); err != nil {
		return fmt.Errorf("read BFF session: %w", err)
	}
	if envelope.Status != 200 || !envelope.Body.Authenticated ||
		envelope.Body.User.Subject == "" || envelope.Body.User.Username == "" {
		return errors.New("BFF session was not authenticated")
	}

	if err := chromedp.Run(
		browserCtx,
		chromedp.Navigate(strings.TrimSuffix(cfg.PageURL, "/")+"/auth/logout"),
	); err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	if err := awaitPostflightLanding(
		browserCtx,
		"/postflight",
		"[data-postflight-oobe=ready]",
		"logout landing",
	); err != nil {
		return err
	}
	if err := chromedp.Run(browserCtx, chromedp.Evaluate(
		`fetch("/postflight/auth/session", {credentials:"same-origin"}).then(response => response.status)`,
		&envelope.Status,
		awaitPromise,
	)); err != nil {
		return fmt.Errorf("verify logout: %w", err)
	}
	if envelope.Status != 401 {
		return errors.New("BFF session survived logout")
	}
	return nil
}

func awaitPromise(params *runtime.EvaluateParams) *runtime.EvaluateParams {
	return params.WithAwaitPromise(true)
}

func awaitGitHubRedirect(ctx context.Context) error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var state struct {
			OnGitHub     bool `json:"onGitHub"`
			KeycloakPage bool `json:"keycloakPage"`
		}
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`(() => ({
				onGitHub: location.hostname === "github.com",
				keycloakPage: `+keycloakRenderedPageJS+`
			}))()`,
			&state,
		)); err != nil {
			return fmt.Errorf("inspect GitHub redirect: %w", err)
		}
		if state.OnGitHub {
			return nil
		}
		if state.KeycloakPage {
			return errors.New("Keycloak rendered a page instead of redirecting to GitHub")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return errors.New("Guardian login did not reach GitHub")
}

func awaitPostflightLanding(ctx context.Context, wantPath, marker, step string) error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var state struct {
			Path         string `json:"path"`
			Ready        bool   `json:"ready"`
			KeycloakPage bool   `json:"keycloakPage"`
		}
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			fmt.Sprintf(`(() => ({
				path: location.pathname,
				ready: Boolean(document.querySelector(%q)),
				keycloakPage: %s
			}))()`, marker, keycloakRenderedPageJS),
			&state,
		)); err != nil {
			return fmt.Errorf("%s: %w", step, err)
		}
		if state.KeycloakPage {
			return fmt.Errorf("%s: Keycloak rendered a page", step)
		}
		if state.Path == wantPath && state.Ready {
			return nil
		}
		if state.Path != wantPath && strings.HasPrefix(state.Path, "/postflight") &&
			!strings.HasPrefix(state.Path, "/postflight/auth/") {
			return fmt.Errorf("%s: landed on %q, want %q", step, state.Path, wantPath)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("%s: %q did not render", step, wantPath)
}

func finishGitHubAuthorization(ctx context.Context, cfg config) error {
	postflightURL, err := url.Parse(cfg.PageURL)
	if err != nil {
		return fmt.Errorf("parse Postflight URL: %w", err)
	}
	deadline := time.Now().Add(75 * time.Second)
	totpSent := false
	grantSent := false
	for time.Now().Before(deadline) {
		var state oauthPageState
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`(() => ({
				host: location.hostname,
				path: location.pathname,
				hasTOTP: Boolean(document.querySelector("input[name=otp], input[name=app_otp], input#app_totp")),
				canGrant: Boolean(document.querySelector("button[name=authorize][value='1']:not([disabled]), input[name=authorize][value='1']:not([disabled]), button[value=authorize]:not([disabled])")),
				grantBlocked: Boolean(document.querySelector("button[name=authorize][value='1'][disabled], input[name=authorize][value='1'][disabled], button[value=authorize][disabled]")),
				hasKeycloakPage: `+keycloakRenderedPageJS+`,
				hasCollision: Boolean(document.querySelector("#linkAccount, #instruction1")),
				hasError: Array.from(document.querySelectorAll(".flash-error, [data-test-selector=auth-error], #kc-error-message, .pf-m-danger"))
					.some(element => {
						const style = getComputedStyle(element);
						const rect = element.getBoundingClientRect();
						return style.display !== "none" && style.visibility !== "hidden" &&
							rect.width > 0 && rect.height > 0;
					})
			}))()`,
			&state,
		)); err != nil {
			return fmt.Errorf("inspect GitHub authorization: %w", err)
		}
		action, err := classifyOAuthPage(state, postflightURL.Hostname())
		if err != nil {
			return err
		}
		switch action {
		case oauthComplete:
			return nil
		case oauthSubmitTOTP:
			if totpSent {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(250 * time.Millisecond):
				}
				continue
			}
			if delay := totpBoundaryDelay(time.Now()); delay > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(delay):
				}
			}
			code, err := totp(cfg.GitHubTOTPSeed, time.Now())
			if err != nil {
				return err
			}
			if err := chromedp.Run(ctx,
				chromedp.SendKeys(
					"input[name=otp], input[name=app_otp], input#app_totp",
					code,
					chromedp.ByQuery,
				),
				chromedp.Click("button[type=submit], input[type=submit]", chromedp.ByQuery),
			); err != nil {
				return fmt.Errorf("GitHub TOTP: %w", err)
			}
			totpSent = true
		case oauthGrant:
			if grantSent {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(250 * time.Millisecond):
				}
				continue
			}
			if err := chromedp.Run(ctx, chromedp.Click(
				"button[name=authorize][value='1']:not([disabled]), input[name=authorize][value='1']:not([disabled]), button[value=authorize]:not([disabled])",
				chromedp.ByQuery,
			)); err != nil {
				return fmt.Errorf("GitHub OAuth grant: %w", err)
			}
			grantSent = true
		case oauthWait:
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	return errors.New("GitHub OAuth flow did not return to Postflight")
}

func classifyOAuthPage(state oauthPageState, postflightHost string) (oauthPageAction, error) {
	switch state.Host {
	case postflightHost:
		if strings.HasPrefix(state.Path, "/postflight") {
			return oauthComplete, nil
		}
		if state.HasCollision {
			return oauthWait, errors.New("Guardian refused automatic linking for an existing account")
		}
		if state.HasError {
			return oauthWait, errors.New("Guardian rejected the brokered login")
		}
		if state.HasKeycloakPage {
			return oauthWait, errors.New("Keycloak rendered a page during the brokered login")
		}
		return oauthWait, nil
	case "github.com":
		if state.HasError {
			return oauthWait, errors.New("GitHub rejected the canary login")
		}
		if state.GrantBlocked {
			return oauthWait, errors.New("GitHub OAuth authorization is disabled; verify account readiness, the persisted app grant, and OAuth token cadence")
		}
		if state.HasTOTP {
			return oauthSubmitTOTP, nil
		}
		if state.CanGrant {
			return oauthGrant, nil
		}
		return oauthWait, nil
	default:
		return oauthWait, fmt.Errorf("OAuth flow reached unexpected host %q", state.Host)
	}
}

func totpBoundaryDelay(now time.Time) time.Duration {
	const guard = 5 * time.Second
	remaining := 30*time.Second - time.Duration(now.UnixNano()%int64(30*time.Second))
	if remaining <= guard {
		return remaining + time.Second
	}
	return 0
}

func totp(seed string, now time.Time) (string, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(seed), " ", ""))
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(normalized)
	if err != nil || len(key) < 10 {
		return "", errors.New("GitHub canary TOTP secret is invalid")
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(now.Unix()/30))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000), nil
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

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
