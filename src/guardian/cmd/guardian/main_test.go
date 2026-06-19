package main

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"
)

func TestParseUpArgsRequiresHostFileFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "short file flag", args: []string{"-f", "src/hosts/ash-bm-004/host.json", "--output=json", "--status=plain", "--execute"}},
		{name: "long file flag", args: []string{"--file=src/hosts/ash-bm-004/host.json", "--output=json", "--status=plain", "--execute"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseUpArgs(tc.args)
			if err != nil {
				t.Fatalf("parseUpArgs() error = %v", err)
			}
			if parsed.HostPath != "src/hosts/ash-bm-004/host.json" {
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
	if _, err := parseUpArgs([]string{"src/hosts/ash-bm-004/host.json"}); err == nil {
		t.Fatalf("parseUpArgs() error = nil, want rejection")
	}
}

func TestParseUpArgsRejectsMultipleHosts(t *testing.T) {
	if _, err := parseUpArgs([]string{"-f", "one.json", "two.json"}); err == nil {
		t.Fatalf("parseUpArgs() error = nil, want rejection")
	}
	if _, err := parseUpArgs([]string{"-f", "one.json", "--file", "two.json"}); err == nil {
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
	if _, err := parseUpArgs([]string{"-f", "src/hosts/ash-bm-004/host.json", "--status=verbose"}); err == nil {
		t.Fatalf("parseUpArgs() error = nil, want rejection")
	}
}

func TestAutoStatusIsOffForStructuredOutput(t *testing.T) {
	reporter, closeStatus, err := newStatusReporter(upArgs{
		HostPath: "src/hosts/ash-bm-004/host.json",
		Execute:  true,
		Format:   "toml",
		Status:   "auto",
	}, "guardian-nonprod")
	if err != nil {
		t.Fatal(err)
	}
	defer closeStatus()
	if reporter != nil {
		t.Fatalf("reporter = %#v, want nil for structured output with --status=auto", reporter)
	}
}

func TestConfigLoadCodeRecognizesMissingPath(t *testing.T) {
	if got := configLoadCode(fmt.Errorf("load host: %w", fs.ErrNotExist)); got != "config.path.notFound" {
		t.Fatalf("configLoadCode() = %q, want config.path.notFound", got)
	}
	if got := configLoadCode(errors.New("invalid json")); got != "config.load" {
		t.Fatalf("configLoadCode() = %q, want config.load", got)
	}
}
