package main

import "testing"

func TestParseUpArgsRequiresHostFileFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "short file flag", args: []string{"-f", "src/hosts/ash-bm-001/host.cue", "--output=json", "--status=plain", "--execute"}},
		{name: "long file flag", args: []string{"--file=src/hosts/ash-bm-001/host.cue", "--output=json", "--status=plain", "--execute"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseUpArgs(tc.args)
			if err != nil {
				t.Fatalf("parseUpArgs() error = %v", err)
			}
			if parsed.HostPath != "src/hosts/ash-bm-001/host.cue" {
				t.Fatalf("host path = %q", parsed.HostPath)
			}
			if !parsed.Execute {
				t.Fatalf("execute = false, want true")
			}
			if parsed.Format != "json" {
				t.Fatalf("format = %q, want json", parsed.Format)
			}
			if parsed.Status != "plain" {
				t.Fatalf("status = %q, want plain", parsed.Status)
			}
		})
	}
}

func TestParseUpArgsRejectsPositionalHost(t *testing.T) {
	if _, err := parseUpArgs([]string{"src/hosts/ash-bm-001/host.cue"}); err == nil {
		t.Fatalf("parseUpArgs() error = nil, want rejection")
	}
}

func TestParseUpArgsRejectsMultipleHosts(t *testing.T) {
	if _, err := parseUpArgs([]string{"-f", "one.cue", "two.cue"}); err == nil {
		t.Fatalf("parseUpArgs() error = nil, want rejection")
	}
	if _, err := parseUpArgs([]string{"-f", "one.cue", "--file", "two.cue"}); err == nil {
		t.Fatalf("parseUpArgs() error = nil, want rejection")
	}
}

func TestParseUpArgsRejectsMissingFileFlagValue(t *testing.T) {
	if _, err := parseUpArgs([]string{"-f"}); err == nil {
		t.Fatalf("parseUpArgs() error = nil, want rejection")
	}
	if _, err := parseUpArgs([]string{"--file="}); err == nil {
		t.Fatalf("parseUpArgs() error = nil, want rejection")
	}
}

func TestParseUpArgsRejectsUnsupportedStatus(t *testing.T) {
	if _, err := parseUpArgs([]string{"-f", "src/hosts/ash-bm-001/host.cue", "--status=verbose"}); err == nil {
		t.Fatalf("parseUpArgs() error = nil, want rejection")
	}
}
