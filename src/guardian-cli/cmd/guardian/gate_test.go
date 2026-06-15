package main

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPublicHTTPGatePasses(t *testing.T) {
	site := gateTestSite()
	deps := publicHTTPGateDeps{
		queryVM: func(query string) (float64, bool, error) {
			switch {
			case strings.HasPrefix(query, `count(up{job="public-http",app="aisucks"}`):
				return 2, true, nil
			case strings.HasPrefix(query, `count(up{job="public-http",app="company-site"}`):
				return 2, true, nil
			case strings.HasPrefix(query, `probe_success{job="blackbox"`):
				return 1, true, nil
			case strings.Contains(query, "increase("), strings.Contains(query, "ALERTS"):
				return 0, true, nil
			default:
				return 1, true, nil
			}
		},
		deployment: func(_, _ string) (gateDeploymentStatus, error) {
			return gateDeploymentStatus{Desired: 2, Available: 2, Updated: 2, ObservedGeneration: 3, Generation: 3}, nil
		},
		rollout: func(_, _ string) error { return nil },
		getURL:  func(string) (int, error) { return 200, nil },
	}
	result := evaluatePublicHTTPGate(site, 15*time.Minute, deps)
	if !result.Passed {
		t.Fatalf("gate failed: %+v", result.Checks)
	}
	if result.Kind != publicHTTPGateKind || result.Window != "900s" {
		t.Fatalf("gate identity = %q/%q, want %q/900s", result.Kind, result.Window, publicHTTPGateKind)
	}
}

func TestPublicHTTPGateFailsOnSignals(t *testing.T) {
	site := gateTestSite()
	deps := publicHTTPGateDeps{
		queryVM: func(query string) (float64, bool, error) {
			switch {
			case strings.HasPrefix(query, `probe_success{job="blackbox"`):
				return 0, true, nil
			case strings.Contains(query, "kube_pod_container_status_restarts_total"):
				return 1, true, nil
			case strings.HasPrefix(query, `count(up{job="public-http",app="company-site"}`):
				return 1, true, nil
			case strings.HasPrefix(query, `count(up{job="public-http",app="aisucks"}`):
				return 2, true, nil
			default:
				return 0, true, nil
			}
		},
		deployment: func(namespace, _ string) (gateDeploymentStatus, error) {
			if namespace == "company" {
				return gateDeploymentStatus{Desired: 2, Available: 1, Updated: 1, ObservedGeneration: 2, Generation: 3}, nil
			}
			return gateDeploymentStatus{Desired: 2, Available: 2, Updated: 2, ObservedGeneration: 3, Generation: 3}, nil
		},
		rollout: func(_, resource string) error {
			if resource == "deployment/vmalert" {
				return errors.New("rollout not complete")
			}
			return nil
		},
		getURL: func(target string) (int, error) {
			if strings.Contains(target, "/news") {
				return 503, nil
			}
			return 200, nil
		},
	}
	result := evaluatePublicHTTPGate(site, 15*time.Minute, deps)
	if result.Passed {
		t.Fatal("gate passed despite failed signals")
	}
	if result.failedCount() == 0 {
		t.Fatal("failedCount = 0, want failures")
	}
}

func TestVictoriaMetricsQueryHelpers(t *testing.T) {
	query := `probe_success{job="blackbox",instance="https://gamma.guardianintelligence.org/news"}`
	path := victoriaMetricsQueryPath(query)
	if !strings.Contains(path, urlEscaped(query)) {
		t.Fatalf("query path %q does not contain escaped query", path)
	}
	value, present, err := parseVictoriaMetricsValue([]byte(`{"status":"success","data":{"result":[{"value":[1781527263,"4"]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !present || value != 4 {
		t.Fatalf("value=%v present=%v, want 4/true", value, present)
	}
	_, present, err = parseVictoriaMetricsValue([]byte(`{"status":"success","data":{"result":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("empty result present = true, want false")
	}
}

func TestParseDeploymentStatus(t *testing.T) {
	status, err := parseDeploymentStatus([]byte(`{
		"metadata":{"generation":7},
		"spec":{"replicas":2},
		"status":{"availableReplicas":2,"updatedReplicas":2,"observedGeneration":7}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !status.ready() {
		t.Fatalf("status not ready: %+v", status)
	}
}

func gateTestSite() *Site {
	site := &Site{Name: "gamma"}
	site.Cluster.Name = "guardian-gamma"
	site.Aisucks.Domain = "gamma.aisucks.app"
	site.Aisucks.Watch = []string{"https://dev.aisucks.app/healthz"}
	site.Aisucks.WatchPages = []string{"https://dev.aisucks.app/"}
	site.Company.Domain = "gamma.guardianintelligence.org"
	site.Company.WatchDomains = []string{"dev.guardianintelligence.org"}
	site.Company.Routes = []string{"/", "/letters", "/news"}
	site.Company.ProbeURLs = companyProbeURLs(site.Company.WatchDomains, site.Company.Routes)
	return site
}

func urlEscaped(value string) string {
	return url.QueryEscape(value)
}
