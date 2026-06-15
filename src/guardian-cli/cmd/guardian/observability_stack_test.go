package main

import (
	"strings"
	"testing"
)

func TestObservabilityStackSiteManifests(t *testing.T) {
	wantClickHouse := map[string]bool{"dev": true, "gamma": true, "prod": false}
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestSite(t, siteName)
			stacks, err := observabilityStacks(site)
			if err != nil {
				t.Fatal(err)
			}
			if len(stacks) != 1 {
				t.Fatalf("observability stack count = %d, want 1", len(stacks))
			}
			stack := stacks[0]
			if stack.Metadata.Name != "observability" {
				t.Fatalf("ObservabilityStack name = %q, want observability", stack.Metadata.Name)
			}
			if stack.Spec.Site != siteName {
				t.Fatalf("ObservabilityStack site = %q, want %q", stack.Spec.Site, siteName)
			}
			if stack.Spec.Namespace != "observability" {
				t.Fatalf("ObservabilityStack namespace = %q, want observability", stack.Spec.Namespace)
			}
			if stack.Spec.NamespaceLabels["pod-security.kubernetes.io/enforce"] != "privileged" {
				t.Fatalf("ObservabilityStack namespaceLabels = %#v, want privileged PSA", stack.Spec.NamespaceLabels)
			}
			if stack.Spec.Clickhouse.Enabled != wantClickHouse[siteName] {
				t.Fatalf("ObservabilityStack clickhouse.enabled = %v, want %v", stack.Spec.Clickhouse.Enabled, wantClickHouse[siteName])
			}
			if site.Clickhouse.Enabled != stack.Spec.Clickhouse.Enabled {
				t.Fatalf("site clickhouse.enabled = %v, want ObservabilityStack value %v", site.Clickhouse.Enabled, stack.Spec.Clickhouse.Enabled)
			}
		})
	}
}

func TestObservabilityStackPlatformRender(t *testing.T) {
	c := componentByName(t, "observability-stack-platform")
	rendered, err := renderComponentManifest(c, "", nil, &Site{})
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	for _, want := range []string{
		"name: observabilitystacks.platform.guardian.dev",
		"kind: ObservabilityStack",
		"name: observability-stack-status",
		"name: function-environment-configs",
		"name: function-auto-ready",
		"clickhouseEnabled",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ObservabilityStack platform render missing %q", want)
		}
	}
}
