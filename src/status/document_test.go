package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// sampleDocument exercises every value type the document model allows, plus
// keys that need TOML quoting (tags with '/') and comment lines.
func sampleDocument() Document {
	return Document{
		Header: []string{"GUARDIAN INTELLIGENCE — fleet status"},
		Sections: []Section{
			{Name: "meta", Entries: []Entry{
				{Key: "generated_at", Value: "2026-06-12T22:10:00Z"},
				{Key: "vantage", Value: "dev"},
				{Key: "refresh_seconds", Value: int64(30)},
			}},
			{Name: "fleet.gamma", Comments: []string{"probed from dev"}, Entries: []Entry{
				{Key: "healthz", Value: "up"},
				{Key: "availability_30d_pct", Value: 97.636},
				{Key: "tls_cert_days_remaining", Value: 89.0},
				{Key: "hello_last_pass_seconds", Value: "n/a", Comment: "app metrics never cross sites"},
			}},
			{Name: "fleet.dev", Entries: []Entry{
				{Key: "healthz", Value: "up", Comment: "self-reported"},
				{Key: "deploy_in_progress", Value: false},
				{Key: "app_restarts_24h", Value: int64(4)},
			}},
			{Name: "releases.record", Entries: []Entry{
				{Key: "aisucks/v10", Value: "aisucks@sha256:8686ee67 — note with “unicode”"},
				{Key: "quoted \"note\"", Value: "back\\slash and\nnewline"},
			}},
			{Name: "incidents", Entries: []Entry{
				{Key: "count", Value: int64(0), Comment: "none recorded"},
			}},
		},
	}
}

// normalize collapses every numeric type the three decoders produce to
// float64 so the value trees compare; key order is presentation, not data.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = normalize(vv)
		}
		return out
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case float64:
		return x
	default:
		return v
	}
}

// TestEncodingsAgree is the golden contract: the TOML (the page), the JSON,
// and the YAML all parse and decode to the same value tree.
func TestEncodingsAgree(t *testing.T) {
	doc := sampleDocument()

	tomlB, err := doc.TOML()
	if err != nil {
		t.Fatalf("render TOML: %v", err)
	}
	jsonB, err := doc.JSON()
	if err != nil {
		t.Fatalf("render JSON: %v", err)
	}
	yamlB, err := doc.YAML()
	if err != nil {
		t.Fatalf("render YAML: %v", err)
	}

	var fromTOML map[string]any
	if err := toml.Unmarshal(tomlB, &fromTOML); err != nil {
		t.Fatalf("rendered TOML does not parse: %v\n%s", err, tomlB)
	}
	var fromJSON map[string]any
	if err := json.Unmarshal(jsonB, &fromJSON); err != nil {
		t.Fatalf("rendered JSON does not parse: %v", err)
	}
	var fromYAML map[string]any
	if err := yaml.Unmarshal(yamlB, &fromYAML); err != nil {
		t.Fatalf("rendered YAML does not parse: %v", err)
	}

	tn, jn, yn := normalize(fromTOML), normalize(fromJSON), normalize(fromYAML)
	if !reflect.DeepEqual(tn, jn) {
		t.Errorf("TOML and JSON disagree:\ntoml: %#v\njson: %#v", tn, jn)
	}
	if !reflect.DeepEqual(tn, yn) {
		t.Errorf("TOML and YAML disagree:\ntoml: %#v\nyaml: %#v", tn, yn)
	}
}

func TestTOMLCarriesComments(t *testing.T) {
	tomlB, err := sampleDocument().TOML()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# GUARDIAN INTELLIGENCE — fleet status",
		"# probed from dev",
		"# self-reported",
		"# none recorded",
		"[fleet.gamma]",
		"[releases.record]",
	} {
		if !strings.Contains(string(tomlB), want) {
			t.Errorf("TOML missing %q:\n%s", want, tomlB)
		}
	}
}

func TestRenderHTMLEscapes(t *testing.T) {
	page := string(renderHTML([]byte(`note = "<script>alert(1)</script>"`)))
	if strings.Contains(page, "<script>alert") {
		t.Fatal("TOML content not escaped into the page")
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Fatal("expected escaped TOML in <pre>")
	}
}
