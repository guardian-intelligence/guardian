package main

import (
	"strings"
	"testing"
)

func TestObservabilityStackSiteManifests(t *testing.T) {
	wantClickHouse := map[string]bool{"dev": true, "gamma": true, "prod": false}
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestHost(t, siteName)
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
			if stack.Spec.VictoriaMetrics.Image == "" {
				t.Fatal("ObservabilityStack victoriaMetrics.image is required")
			}
			if stack.Spec.VictoriaMetrics.Persistence.ClaimName != "victoria-metrics-storage" {
				t.Fatalf("ObservabilityStack victoriaMetrics.persistence.claimName = %q, want victoria-metrics-storage", stack.Spec.VictoriaMetrics.Persistence.ClaimName)
			}
			if stack.Spec.VictoriaMetrics.Persistence.VolumeName != "guardian-"+siteName+"-victoria-metrics" {
				t.Fatalf("ObservabilityStack victoriaMetrics.persistence.volumeName = %q, want guardian-%s-victoria-metrics", stack.Spec.VictoriaMetrics.Persistence.VolumeName, siteName)
			}
			if stack.Spec.VictoriaMetrics.RetentionPeriod != "13" {
				t.Fatalf("ObservabilityStack victoriaMetrics.retentionPeriod = %q, want 13", stack.Spec.VictoriaMetrics.RetentionPeriod)
			}
			if stack.Spec.VictoriaMetrics.Ports.HTTP != 8428 {
				t.Fatalf("ObservabilityStack victoriaMetrics.ports.http = %d, want 8428", stack.Spec.VictoriaMetrics.Ports.HTTP)
			}
			if site.Clickhouse.Enabled != stack.Spec.Clickhouse.Enabled {
				t.Fatalf("site clickhouse.enabled = %v, want ObservabilityStack value %v", site.Clickhouse.Enabled, stack.Spec.Clickhouse.Enabled)
			}
		})
	}
}

func TestObservabilityStackPlatformRender(t *testing.T) {
	out := buildTestPlatformPackage(t)
	for _, want := range []string{
		"name: observabilitystacks.platform.guardian.dev",
		"kind: ObservabilityStack",
		"name: observability-stack-status",
		"kind: Object",
		"name: observability-stack-{{ $spec.namespace }}-victoria-metrics",
		"name: observability-stack-{{ $spec.namespace }}-victoria-metrics-pvc",
		"kind: PersistentVolumeClaim",
		"image: {{ $spec.victoriaMetrics.image }}",
		"- -retentionPeriod={{ $spec.victoriaMetrics.retentionPeriod }}",
		"persistentVolumeClaim:",
		"claimName: {{ $spec.victoriaMetrics.persistence.claimName }}",
		"name: observability-stack-{{ $spec.namespace }}-victoria-metrics-service",
		"victoriaMetricsURL",
		"name: function-environment-configs",
		"name: function-auto-ready",
		"clickhouseEnabled",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ObservabilityStack platform render missing %q", want)
		}
	}
}

func TestObservabilityStackEnvironmentBundleInstances(t *testing.T) {
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestHost(t, siteName)
			rendered, err := buildTestEnvironmentBundle(site, testProductImages())
			if err != nil {
				t.Fatal(err)
			}
			out := string(rendered)
			for _, want := range []string{
				"kind: ObservabilityStack",
				"name: observability",
				"site: " + siteName,
				"namespace: observability",
				victoriaMetricsTestImage,
				"claimName: victoria-metrics-storage",
				"storageClassName: guardian-local-retain",
				"volumeName: guardian-" + siteName + "-victoria-metrics",
				"retentionPeriod: \"13\"",
				"memoryAllowedBytes: 256MiB",
				"http: 8428",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("ObservabilityStack environment render missing %q", want)
				}
			}
			if strings.Contains(out, "{{ index .Images") {
				t.Error("environment bundle left bootstrap image placeholders unresolved")
			}
		})
	}
}
