package checkoutbundle

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeriveCheckoutTokenIsDeterministicAndScoped(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	token := DeriveCheckoutToken(secret, "exec-1", "attempt-1")
	if token != DeriveCheckoutToken(secret, "exec-1", "attempt-1") {
		t.Fatal("derivation is not deterministic")
	}
	for name, other := range map[string]string{
		"different execution": DeriveCheckoutToken(secret, "exec-2", "attempt-1"),
		"different attempt":   DeriveCheckoutToken(secret, "exec-1", "attempt-2"),
		"different secret":    DeriveCheckoutToken([]byte("fedcba9876543210fedcba9876543210"), "exec-1", "attempt-1"),
	} {
		if token == other {
			t.Fatalf("token collided under %s", name)
		}
	}
	// Length-prefixing makes the identifier boundary unforgeable: shifting
	// bytes across it — including across a literal colon — cannot alias.
	for _, pair := range [][4]string{
		{"ab", "c", "a", "bc"},
		{"a:b", "c", "a", "b:c"},
	} {
		if DeriveCheckoutToken(secret, pair[0], pair[1]) ==
			DeriveCheckoutToken(secret, pair[2], pair[3]) {
			t.Fatalf("identifier boundary is ambiguous for (%q,%q) vs (%q,%q)", pair[0], pair[1], pair[2], pair[3])
		}
	}
}

type erroringResolver struct{}

func (erroringResolver) ResolveActiveAssignment(context.Context, string, string) (AssignmentIdentity, bool, error) {
	return AssignmentIdentity{}, false, errors.New("assignment store is down")
}

func TestAuthenticateResolverErrorIsRetryable(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	service := New(Config{StoreDir: t.TempDir(), HostSecret: secret}, erroringResolver{})
	r := httptest.NewRequest("POST", BundlePath, nil)
	r.Header.Set(executionIDHeader, "exec-1")
	r.Header.Set(attemptIDHeader, "attempt-1")
	r.Header.Set("Authorization", "Bearer "+DeriveCheckoutToken(secret, "exec-1", "attempt-1"))
	_, err := service.authenticate(context.Background(), r)
	if !errors.Is(err, errResolverUnavailable) {
		t.Fatalf("resolver error must surface as retryable, got %v", err)
	}
}

func TestAuthenticate(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	assignment := AssignmentIdentity{
		ExecutionID:        "exec-1",
		AttemptID:          "attempt-1",
		RepositoryFullName: "acme/widget",
	}
	service := New(Config{StoreDir: t.TempDir(), HostSecret: secret},
		&StaticResolver{Assignments: []AssignmentIdentity{assignment}})
	valid := DeriveCheckoutToken(secret, assignment.ExecutionID, assignment.AttemptID)

	cases := []struct {
		name      string
		execution string
		attempt   string
		bearer    string
		wantOK    bool
	}{
		{"valid", "exec-1", "attempt-1", valid, true},
		{"missing execution header", "", "attempt-1", valid, false},
		{"missing attempt header", "exec-1", "", valid, false},
		{"missing bearer", "exec-1", "attempt-1", "", false},
		{"tampered bearer", "exec-1", "attempt-1", valid[:len(valid)-2] + "xx", false},
		{"token for another assignment", "exec-1", "attempt-1", DeriveCheckoutToken(secret, "exec-2", "attempt-1"), false},
		{"valid token but no active assignment", "exec-9", "attempt-9", DeriveCheckoutToken(secret, "exec-9", "attempt-9"), false},
		{"oversized identifier", strings.Repeat("x", maxIdentifierLength+1), "attempt-1", valid, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", BundlePath, nil)
			if tc.execution != "" {
				r.Header.Set(executionIDHeader, tc.execution)
			}
			if tc.attempt != "" {
				r.Header.Set(attemptIDHeader, tc.attempt)
			}
			if tc.bearer != "" {
				r.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			identity, err := service.authenticate(context.Background(), r)
			if tc.wantOK && err != nil {
				t.Fatalf("expected success, got %v", err)
			}
			if !tc.wantOK && err == nil {
				t.Fatal("expected rejection")
			}
			if tc.wantOK && identity.RepositoryFullName != assignment.RepositoryFullName {
				t.Fatalf("resolved wrong assignment: %+v", identity)
			}
		})
	}
}
