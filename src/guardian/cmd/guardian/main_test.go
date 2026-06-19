package main

import (
	"reflect"
	"testing"
)

func TestParseUpArgsAcceptsFileFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "short file flag", args: []string{"-f", "cluster.cue", "--output=json", "--status=plain", "--execute"}},
		{name: "long file flag", args: []string{"--file=cluster.cue", "--output=json", "--status=plain", "--execute"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseUpArgs(tc.args)
			if err != nil {
				t.Fatalf("parseUpArgs() error = %v", err)
			}
			if parsed.ConfigPath != "cluster.cue" {
				t.Fatalf("config path = %q, want cluster.cue", parsed.ConfigPath)
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

func TestParseUpArgsAcceptsGenesisRecipients(t *testing.T) {
	t.Setenv("GUARDIAN_GENESIS_AGE_RECIPIENTS", "age1env")
	parsed, err := parseUpArgs([]string{
		"--genesis-age-recipient", "age1first",
		"--genesis-age-recipient=age1second",
		"-f", "cluster.cue",
	})
	if err != nil {
		t.Fatalf("parseUpArgs() error = %v", err)
	}
	want := []string{"age1first", "age1second"}
	if !reflect.DeepEqual(parsed.GenesisAgeRecipients, want) {
		t.Fatalf("recipients = %#v, want %#v", parsed.GenesisAgeRecipients, want)
	}
}

func TestParseUpArgsReadsGenesisRecipientsFromEnv(t *testing.T) {
	t.Setenv("GUARDIAN_GENESIS_AGE_RECIPIENTS", "age1one, age1two\nage1three")
	parsed, err := parseUpArgs([]string{"-f", "cluster.cue"})
	if err != nil {
		t.Fatalf("parseUpArgs() error = %v", err)
	}
	want := []string{"age1one", "age1two", "age1three"}
	if !reflect.DeepEqual(parsed.GenesisAgeRecipients, want) {
		t.Fatalf("recipients = %#v, want %#v", parsed.GenesisAgeRecipients, want)
	}
}

func TestParseUpArgsRejectsMultipleConfigs(t *testing.T) {
	if _, err := parseUpArgs([]string{"-f", "one.cue", "two.cue"}); err == nil {
		t.Fatalf("parseUpArgs() error = nil, want rejection")
	}
	if _, err := parseUpArgs([]string{"-f", "one.cue", "--file", "two.cue"}); err == nil {
		t.Fatalf("parseUpArgs() error = nil, want rejection")
	}
}

func TestParseUpArgsRejectsPositionalConfig(t *testing.T) {
	if _, err := parseUpArgs([]string{"cluster.cue"}); err == nil {
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
	if _, err := parseUpArgs([]string{"-f", "cluster.cue", "--status=verbose"}); err == nil {
		t.Fatalf("parseUpArgs() error = nil, want rejection")
	}
}
