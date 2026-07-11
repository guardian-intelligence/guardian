package tests

import "testing"

func TestVictoriaMetricsResolutionTiers(t *testing.T) {
	monitoringPath := runfilePath("src/infrastructure/base/apps/observability.yaml")
	monitoring := singleYAMLDoc(t, monitoringPath)
	spec := nestedMap(t, monitoring, "spec")
	if _, exists := spec["vmagent"]; exists {
		t.Fatal("root Monitoring must use the platform monitoring-agent instead of declaring an unused vmagent")
	}

	wantIntervals := map[string]string{
		"shortterm": "1s",
		"longterm":  "1m",
	}
	for _, value := range sliceValue(spec["metricsStorages"]) {
		storage := mapValue(value)
		name := stringValue(storage["name"])
		want, ok := wantIntervals[name]
		if !ok {
			continue
		}
		if got := stringValue(storage["deduplicationInterval"]); got != want {
			t.Fatalf("metrics storage %s deduplicationInterval = %q, want %q", name, got, want)
		}
		delete(wantIntervals, name)
	}
	if len(wantIntervals) != 0 {
		t.Fatalf("missing metrics storage intervals: %v", wantIntervals)
	}

	packagePath := runfilePath("src/infrastructure/base/platform-patches/cozystack-monitoring-agents.yaml")
	monitoringPackage := singleYAMLDoc(t, packagePath)
	values := nestedMap(t, monitoringPackage, "spec", "components", "monitoring-agents", "values")
	assertNestedString(t, values, "0s,1m", "vmagent", "extraArgs", "remoteWrite.streamAggr.dedupInterval")
}
