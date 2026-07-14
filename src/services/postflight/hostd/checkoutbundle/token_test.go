package checkoutbundle

import (
	"context"
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
	// The id separator must not be forgeable by shifting bytes between the
	// two identifiers.
	if DeriveCheckoutToken(secret, "ab", "c") == DeriveCheckoutToken(secret, "a", "bc") {
		t.Fatal("identifier boundary is ambiguous")
	}
}

func TestAuthenticate(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	lease := LeaseIdentity{
		ExecutionID:        "exec-1",
		AttemptID:          "attempt-1",
		RepositoryFullName: "acme/widget",
	}
	service := New(Config{StoreDir: t.TempDir(), HostSecret: secret},
		&StaticResolver{Leases: []LeaseIdentity{lease}})
	valid := DeriveCheckoutToken(secret, lease.ExecutionID, lease.AttemptID)

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
		{"token for another lease", "exec-1", "attempt-1", DeriveCheckoutToken(secret, "exec-2", "attempt-1"), false},
		{"valid token but no active lease", "exec-9", "attempt-9", DeriveCheckoutToken(secret, "exec-9", "attempt-9"), false},
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
			if tc.wantOK && identity.RepositoryFullName != lease.RepositoryFullName {
				t.Fatalf("resolved wrong lease: %+v", identity)
			}
		})
	}
}
