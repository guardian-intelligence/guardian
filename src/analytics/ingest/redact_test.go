package main

import (
	"strings"
	"testing"
)

func TestRedactCredentialShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in       string
		mustLose []string
		mustKeep []string
	}{
		{
			name:     "github token in props",
			in:       `{"note":"token ghp_0123456789abcdefghijABCDEFGHIJ leaked"}`,
			mustLose: []string{"ghp_0123456789abcdefghijABCDEFGHIJ"},
			mustKeep: []string{`{"note":"token `},
		},
		{
			name:     "fine grained pat",
			in:       `github_pat_11ABCDEFG0123456789_abcdefghij`,
			mustLose: []string{"github_pat_11ABCDEFG0123456789_abcdefghij"},
		},
		{
			name:     "bearer header value",
			in:       `{"auth":"Bearer abcDEF0123456789._~token"}`,
			mustLose: []string{"Bearer abcDEF0123456789._~token"},
		},
		{
			name:     "jwt shape",
			in:       `eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJndWFyZGlhbiJ9.sig`,
			mustLose: []string{"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJndWFyZGlhbiJ9"},
		},
		{
			name:     "credential assignment",
			in:       `client_secret=supersecretvalue1`,
			mustLose: []string{"supersecretvalue1"},
		},
		{
			name:     "oauth code in path query",
			in:       "/postflight/auth/callback?code=oauthcode123&next=/console",
			mustLose: []string{"oauthcode123"},
			mustKeep: []string{"/postflight/auth/callback?code=", "next=/console"},
		},
		{
			name:     "clean payload untouched",
			in:       `{"button":"sign-in","ms":420}`,
			mustKeep: []string{`{"button":"sign-in","ms":420}`},
		},
		{
			name: "empty string",
			in:   "",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := redactCredentialShapes(tc.in)
			for _, lost := range tc.mustLose {
				if strings.Contains(got, lost) {
					t.Fatalf("redact(%q) = %q still contains %q", tc.in, got, lost)
				}
			}
			for _, kept := range tc.mustKeep {
				if !strings.Contains(got, kept) {
					t.Fatalf("redact(%q) = %q lost %q", tc.in, got, kept)
				}
			}
		})
	}
}
