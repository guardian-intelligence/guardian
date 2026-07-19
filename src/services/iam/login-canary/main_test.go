package main

import (
	"strings"
	"testing"
	"time"
)

func TestTOTPMatchesRFC6238SHA1Vector(t *testing.T) {
	t.Parallel()
	code, err := totp(
		"GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ",
		time.Unix(59, 0),
	)
	if err != nil {
		t.Fatal(err)
	}
	if code != "287082" {
		t.Fatalf("totp = %q, want 287082", code)
	}
}

func TestClassifyOAuthPage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		state   oauthPageState
		action  oauthPageAction
		errText string
	}{
		{
			name:   "already linked return",
			state:  oauthPageState{Host: "guardianintelligence.org", Path: "/postflight/auth/callback"},
			action: oauthComplete,
		},
		{
			name:   "first Guardian enrollment",
			state:  oauthPageState{Host: "guardianintelligence.org", Path: "/realms/guardianintelligence.org/broker/github/endpoint", HasReviewProfile: true},
			action: oauthReviewProfile,
		},
		{
			name:    "email collision never auto-links",
			state:   oauthPageState{Host: "guardianintelligence.org", Path: "/realms/guardianintelligence.org/login-actions/first-broker-login", HasCollision: true},
			errText: "refused automatic linking",
		},
		{
			name:   "GitHub TOTP",
			state:  oauthPageState{Host: "github.com", Path: "/sessions/two-factor/app", HasTOTP: true},
			action: oauthSubmitTOTP,
		},
		{
			name:   "GitHub consent",
			state:  oauthPageState{Host: "github.com", Path: "/login/oauth/authorize", CanGrant: true},
			action: oauthGrant,
		},
		{
			name:    "GitHub disabled consent",
			state:   oauthPageState{Host: "github.com", Path: "/login/oauth/authorize", GrantBlocked: true},
			errText: "verify the canary email",
		},
		{
			name:    "GitHub visible error",
			state:   oauthPageState{Host: "github.com", Path: "/login", HasError: true},
			errText: "rejected the canary login",
		},
		{
			name:   "GitHub redirect without consent",
			state:  oauthPageState{Host: "github.com", Path: "/login/oauth/authorize"},
			action: oauthWait,
		},
		{
			name:    "unknown origin",
			state:   oauthPageState{Host: "example.com", Path: "/login"},
			errText: "unexpected host",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			action, err := classifyOAuthPage(tt.state, "guardianintelligence.org")
			if tt.errText != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errText) {
					t.Fatalf("error = %v, want containing %q", err, tt.errText)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if action != tt.action {
				t.Fatalf("action = %v, want %v", action, tt.action)
			}
		})
	}
}

func TestTOTPBoundaryDelay(t *testing.T) {
	t.Parallel()
	if delay := totpBoundaryDelay(time.Unix(20, 0)); delay != 0 {
		t.Fatalf("delay away from boundary = %s, want zero", delay)
	}
	if delay := totpBoundaryDelay(time.Unix(29, 0)); delay != 2*time.Second {
		t.Fatalf("delay at boundary = %s, want 2s", delay)
	}
}
