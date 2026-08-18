package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	authorizationv1 "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/authorization/v1"
)

type staticTokenVerifier struct {
	identity customerIdentity
	err      error
}

func (v staticTokenVerifier) Verify(context.Context, string) (customerIdentity, error) {
	return v.identity, v.err
}

type staticAuthorizationChecker struct {
	allowed bool
	err     error
}

func (c staticAuthorizationChecker) CheckOrganization(
	context.Context,
	string,
	string,
	string,
) (bool, error) {
	return c.allowed, c.err
}

func TestAuthorizationCheckerUsesGuardianSubject(t *testing.T) {
	t.Parallel()
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		body = string(raw)
		response, err := proto.Marshal(&authorizationv1.CheckResponse{
			Allowed:        true,
			CheckedAtToken: "revision",
		})
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/proto")
		_, _ = w.Write(response)
	}))
	defer server.Close()

	allowed, err := newAuthorizationChecker(server.URL, "test-token").CheckOrganization(
		t.Context(),
		"guardian-subject",
		"guardian",
		"manage_billing",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("authorization decision was not allowed")
	}
	for _, expected := range []string{"guardian-subject", "guardian", "manage_billing"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("request body %q lacks %q", body, expected)
		}
	}
}

func TestCustomerCheckoutFailsClosedOnAuthorizationDecision(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		checker    staticAuthorizationChecker
		wantStatus int
	}{
		{
			name:       "denied",
			checker:    staticAuthorizationChecker{},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "authorization unavailable",
			checker: staticAuthorizationChecker{
				err: errors.New("authorization unavailable"),
			},
			wantStatus: http.StatusServiceUnavailable,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := &paymentServer{
				cfg: config{CustomerCheckoutEnabled: true},
				verifier: staticTokenVerifier{
					identity: customerIdentity{Subject: "guardian-subject"},
				},
				authorizer: test.checker,
			}
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/payments/v1/checkout-sessions",
				strings.NewReader(
					`{"organization_id":"guardian","amount_cents":50,"currency":"usd"}`,
				),
			)
			request.Header.Set("Authorization", "Bearer customer-token")
			response := httptest.NewRecorder()

			server.handleCustomerCheckout(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d; body=%q",
					response.Code,
					test.wantStatus,
					response.Body.String(),
				)
			}
		})
	}
}
