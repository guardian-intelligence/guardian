package tests

import "testing"

func TestVictoriaMetricsResolutionTiers(t *testing.T) {
	monitoringPath := runfilePath("src/infrastructure/base/apps/observability.yaml")
	monitoring := singleYAMLDoc(t, monitoringPath)
	spec := nestedMap(t, monitoring, "spec")
	if _, exists := spec["vmagent"]; exists {
		t.Fatal("root Monitoring must use the platform monitoring-agent instead of declaring an unused vmagent")
	}

	wantTiers := map[string]struct {
		interval string
		storage  string
	}{
		"shortterm": {interval: "1s", storage: "20Gi"},
		"longterm":  {interval: "1m", storage: "20Gi"},
	}
	for _, value := range sliceValue(spec["metricsStorages"]) {
		storage := mapValue(value)
		name := stringValue(storage["name"])
		want, ok := wantTiers[name]
		if !ok {
			continue
		}
		if got := stringValue(storage["deduplicationInterval"]); got != want.interval {
			t.Fatalf("metrics storage %s deduplicationInterval = %q, want %q", name, got, want.interval)
		}
		if got := stringValue(storage["storage"]); got != want.storage {
			t.Fatalf("metrics storage %s capacity = %q, want %q", name, got, want.storage)
		}
		delete(wantTiers, name)
	}
	if len(wantTiers) != 0 {
		t.Fatalf("missing metrics storage tiers: %v", wantTiers)
	}

	packagePath := runfilePath("src/infrastructure/base/platform-patches/cozystack-monitoring-agents.yaml")
	monitoringPackage := singleYAMLDoc(t, packagePath)
	values := nestedMap(t, monitoringPackage, "spec", "components", "monitoring-agents", "values")
	assertNestedString(t, values, "0s,1m", "vmagent", "extraArgs", "remoteWrite.streamAggr.dedupInterval")
}
