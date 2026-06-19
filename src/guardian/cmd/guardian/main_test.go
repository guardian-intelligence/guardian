package main

import "testing"

func TestParseUpArgsAcceptsFlagsBeforeAndAfterConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "flags before config", args: []string{"--execute", "--output", "json", "cluster.cue"}},
		{name: "flags after config", args: []string{"cluster.cue", "--output=json", "--execute"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, execute, format, err := parseUpArgs(tc.args)
			if err != nil {
				t.Fatalf("parseUpArgs() error = %v", err)
			}
			if path != "cluster.cue" {
				t.Fatalf("config path = %q, want cluster.cue", path)
			}
			if !execute {
				t.Fatalf("execute = false, want true")
			}
			if format != "json" {
				t.Fatalf("format = %q, want json", format)
			}
		})
	}
}

func TestParseUpArgsRejectsMultipleConfigs(t *testing.T) {
	if _, _, _, err := parseUpArgs([]string{"one.cue", "two.cue"}); err == nil {
		t.Fatalf("parseUpArgs() error = nil, want rejection")
	}
}
