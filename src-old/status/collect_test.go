package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// vecJSON builds a /api/v1/query vector response.
func vecJSON(t *testing.T, samples []sample) []byte {
	t.Helper()
	type rs struct {
		Metric map[string]string `json:"metric"`
		Value  [2]any            `json:"value"`
	}
	result := make([]rs, 0, len(samples))
	for _, s := range samples {
		result = append(result, rs{Metric: s.metric, Value: [2]any{1765576200.0, strconv.FormatFloat(s.value, 'f', -1, 64)}})
	}
	raw, err := json.Marshal(map[string]any{
		"status": "success",
		"data":   map[string]any{"resultType": "vector", "result": result},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// workload makes one isDeploying sample for a kind's name label.
func workload(ns, kindLabel, name string, deploying float64) sample {
	return sample{metric: map[string]string{"namespace": ns, kindLabel: name}, value: deploying}
}

// stubVM serves canned vectors per query expression; unknown expressions get
// an empty vector.
func stubVM(t *testing.T, canned map[string][]sample) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expr := r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		w.Write(vecJSON(t, canned[expr]))
	}))
}

func findSection(t *testing.T, d Document, name string) Section {
	t.Helper()
	for _, s := range d.Sections {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("document has no section %q", name)
	return Section{}
}

func findEntry(t *testing.T, s Section, key string) Entry {
	t.Helper()
	for _, e := range s.Entries {
		if e.Key == key {
			return e
		}
	}
	t.Fatalf("section %q has no entry %q", s.Name, key)
	return Entry{}
}

// TestDocumentBooleans: every workload across the three kinds becomes one
// boolean under its namespace section; a deploying one reads true, the rest
// false; statefulsets fold into the same namespace section as deployments.
func TestDocumentBooleans(t *testing.T) {
	srv := stubVM(t, map[string][]sample{
		qDeployments: {
			workload("aisucks", "deployment", "aisucks", 1), // mid-rollout
			workload("aisucks", "deployment", "gatus", 0),
			workload("observability", "deployment", "grafana", 0),
		},
		qStatefulSets: {
			workload("aisucks", "statefulset", "postgres", 0),
			workload("openbao", "statefulset", "openbao", 0),
		},
		qDaemonSets: {
			workload("kube-system", "daemonset", "cilium", 0),
		},
	})
	defer srv.Close()

	doc := newCollector(srv.URL, "dev").document(context.Background())

	ais := findSection(t, doc, "aisucks")
	if got := findEntry(t, ais, "aisucks").Value; got != true {
		t.Errorf("aisucks/aisucks isDeploying = %v, want true", got)
	}
	if got := findEntry(t, ais, "gatus").Value; got != false {
		t.Errorf("aisucks/gatus isDeploying = %v, want false", got)
	}
	if got := findEntry(t, ais, "postgres").Value; got != false {
		t.Errorf("aisucks/postgres (statefulset) isDeploying = %v, want false", got)
	}
	if got := findEntry(t, findSection(t, doc, "openbao"), "openbao").Value; got != false {
		t.Errorf("openbao isDeploying = %v, want false", got)
	}
	if got := findEntry(t, findSection(t, doc, "kube-system"), "cilium").Value; got != false {
		t.Errorf("kube-system/cilium isDeploying = %v, want false", got)
	}

	// The header names the site and the isolation posture.
	joined := strings.Join(doc.Header, "\n")
	if !strings.Contains(joined, "site: dev") || !strings.Contains(joined, "isolation") {
		t.Errorf("header missing site/isolation disclosure:\n%s", joined)
	}

	// Booleans survive the TOML round-trip.
	tomlB, err := doc.TOML()
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := toml.Unmarshal(tomlB, &parsed); err != nil {
		t.Fatalf("document TOML does not parse: %v\n%s", err, tomlB)
	}
	ns := parsed["aisucks"].(map[string]any)
	if ns["aisucks"] != true || ns["gatus"] != false {
		t.Errorf("round-tripped booleans wrong: %#v", ns)
	}
}

// TestDocumentDeadVM: VictoriaMetrics down → an honest "unknown", never a
// fabricated all-false page; the document still renders and parses.
func TestDocumentDeadVM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	doc := newCollector(srv.URL, "gamma").document(context.Background())

	e := findEntry(t, findSection(t, doc, "status"), "deploy_state")
	if e.Value != "unknown" || e.Comment == "" {
		t.Errorf("dead VM deploy_state = %v (comment %q), want unknown with reason", e.Value, e.Comment)
	}
	if _, err := doc.TOML(); err != nil {
		t.Fatalf("degraded document TOML does not parse: %v", err)
	}
}
