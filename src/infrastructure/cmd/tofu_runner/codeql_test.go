package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func codeQLTestDesired() codeQLDesired {
	return codeQLDesired{Repository: codeQLRepository,
		DefaultSetup:      codeQLRouting{State: "configured", RunnerType: "labeled", RunnerLabel: codeQLRunner},
		RequiredLanguages: []string{"actions", "python", "rust"}}
}

func codeQLTestSetup(routed bool) map[string]any {
	setup := map[string]any{
		"state": "configured", "runner_type": "standard", "runner_label": nil,
		"languages":   []string{"actions", "python", "rust", "go"},
		"query_suite": "default", "threat_model": "remote", "schedule": "weekly",
		"future_unowned_setting": map[string]any{"enabled": true}, "updated_at": "before",
	}
	if routed {
		setup["runner_type"] = "labeled"
		setup["runner_label"] = codeQLRunner
		setup["updated_at"] = "after"
	}
	return setup
}

func TestCodeQLNoOpAndPlanNeverWrite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		routed bool
		mode   mode
		want   string
	}{{"matching apply", true, modeApply, "no-op"}, {"matching plan", true, modePlan, "no-op"}, {"drift plan", false, modePlan, "drift"}} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/default-setup") {
					t.Errorf("unexpected mutation/request: %s %s", r.Method, r.URL.Path)
				}
				json.NewEncoder(w).Encode(codeQLTestSetup(tc.routed))
			}))
			defer server.Close()
			client := codeQLClient{baseURL: server.URL, client: server.Client(), token: "test"}
			got, err := client.reconcile(context.Background(), codeQLTestDesired(), tc.mode)
			if err != nil || got.Status != tc.want || requests != 1 {
				t.Fatalf("got=%+v err=%v requests=%d", got, err, requests)
			}
		})
	}
}

func TestCodeQLApplyWaitsAndPreservesAllUnownedSettings(t *testing.T) {
	var calls []string
	getSetup, getRun, writes, waits := 0, 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Error("GitHub request lacks configured authentication or pinned API version")
		}
		switch {
		case r.Method == http.MethodPatch:
			writes++
			var patch map[string]any
			json.NewDecoder(r.Body).Decode(&patch)
			want := map[string]any{"state": "configured", "runner_type": "labeled", "runner_label": codeQLRunner}
			if !reflect.DeepEqual(patch, want) {
				t.Fatalf("PATCH changed unowned settings: %#v", patch)
			}
			w.WriteHeader(http.StatusAccepted)
			// Untrusted response URLs are ignored; the numeric ID is resolved
			// under the fixed Guardian repository endpoint.
			json.NewEncoder(w).Encode(map[string]any{"run_id": 42, "run_url": "https://attacker.invalid/token"})
		case strings.HasSuffix(r.URL.Path, "/actions/runs/42"):
			getRun++
			if getRun == 1 {
				json.NewEncoder(w).Encode(map[string]any{"id": 42, "status": "in_progress", "conclusion": nil})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"id": 42, "status": "completed", "conclusion": "success"})
			}
		case strings.HasSuffix(r.URL.Path, "/default-setup"):
			getSetup++
			setup := codeQLTestSetup(getSetup > 1)
			if getSetup > 1 {
				setup["languages"] = []string{"rust", "go", "python", "actions"}
			}
			json.NewEncoder(w).Encode(setup)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := codeQLClient{baseURL: server.URL, client: server.Client(), token: "test-token",
		wait: func(context.Context) error { waits++; return nil }}
	got, err := client.reconcile(context.Background(), codeQLTestDesired(), modeApply)
	if err != nil || got.Status != "converged" || got.RunID != 42 || writes != 1 || waits != 1 || getSetup != 2 || getRun != 2 {
		t.Fatalf("got=%+v err=%v writes=%d waits=%d calls=%v", got, err, writes, waits, calls)
	}
}

func TestCodeQLAsyncFailuresNeverReportConvergence(t *testing.T) {
	for _, name := range []string{"accepted without run", "conflict", "validation pending", "validation failed", "readback drift", "coverage lost", "query changed", "threat changed", "schedule changed", "future setting changed"} {
		t.Run(name, func(t *testing.T) {
			writes, reads, runReads := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPatch {
					writes++
					if name == "conflict" {
						w.WriteHeader(http.StatusConflict)
						return
					}
					w.WriteHeader(http.StatusAccepted)
					if name == "accepted without run" {
						w.Write([]byte(`{}`))
						return
					}
					w.Write([]byte(`{"run_id":42}`))
					return
				}
				if strings.Contains(r.URL.Path, "/actions/runs/") {
					runReads++
					if name == "validation pending" {
						w.Write([]byte(`{"id":42,"status":"queued"}`))
						return
					}
					conclusion := "success"
					if name == "validation failed" {
						conclusion = "failure"
					}
					json.NewEncoder(w).Encode(map[string]any{"id": 42, "status": "completed", "conclusion": conclusion})
					return
				}
				reads++
				setup := codeQLTestSetup(reads > 1 && name != "readback drift")
				if reads > 1 {
					switch name {
					case "coverage lost":
						setup["languages"] = []string{"actions", "python"}
					case "query changed":
						setup["query_suite"] = "extended"
					case "threat changed":
						setup["threat_model"] = "remote_and_local"
					case "schedule changed":
						setup["schedule"] = "daily"
					case "future setting changed":
						setup["future_unowned_setting"] = false
					}
				}
				json.NewEncoder(w).Encode(setup)
			}))
			defer server.Close()
			client := codeQLClient{baseURL: server.URL, client: server.Client(), wait: func(context.Context) error { return context.DeadlineExceeded }}
			got, err := client.reconcile(context.Background(), codeQLTestDesired(), modeApply)
			if err == nil || got.Status == "converged" || writes != 1 {
				t.Fatalf("got=%+v err=%v writes=%d", got, err, writes)
			}
			if name == "conflict" && (reads != 2 || runReads != 0 || !errors.Is(err, errCodeQLPending)) {
				t.Fatalf("conflict was not observed and held: reads=%d runReads=%d err=%v", reads, runReads, err)
			}
		})
	}
}

func TestCodeQLMissingBaselineCoverageRefusesEvenPlan(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes++
		}
		setup := codeQLTestSetup(false)
		setup["languages"] = []string{"actions", "python"}
		json.NewEncoder(w).Encode(setup)
	}))
	defer server.Close()
	client := codeQLClient{baseURL: server.URL, client: server.Client()}
	for _, mode := range []mode{modePlan, modeApply} {
		if _, err := client.reconcile(context.Background(), codeQLTestDesired(), mode); err == nil {
			t.Fatal("missing rust coverage must fail closed")
		}
	}
	if writes != 0 {
		t.Fatal("attempted to overwrite existing coverage")
	}
}

func TestCodeQLDeclarationHasNarrowOwnership(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codeql-runner.json")
	desired := codeQLTestDesired()
	body, _ := json.Marshal(desired)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCodeQLDesired(path); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"repository":"guardian-intelligence/guardian","default_setup":{"state":"configured","runner_type":"labeled","runner_label":"postflight-4vcpu-ubuntu24-turbo","languages":["python"]},"required_languages":["actions","python","rust"]}`,
		strings.Replace(string(body), codeQLRepository, "other/repository", 1),
		strings.Replace(string(body), codeQLRunner, "ubuntu-latest", 1),
		strings.Replace(string(body), `"rust"`, `"go"`, 1),
		string(body) + ` {}`,
	} {
		os.WriteFile(path, []byte(body), 0o600)
		if _, err := loadCodeQLDesired(path); err == nil {
			t.Fatalf("accepted unsafe declaration: %s", body)
		}
	}
}
