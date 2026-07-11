package tests

import "testing"

func TestVictoriaLogsStreamFields(t *testing.T) {
	packagePath := runfilePath("src/infrastructure/base/platform-patches/cozystack-monitoring-agents.yaml")
	monitoringPackage := singleYAMLDoc(t, packagePath)
	fluentBit := nestedMap(t, monitoringPackage, "spec", "components", "monitoring-agents", "values", "fluent-bit", "config")
	outputs, ok := fluentBit["outputs"].(string)
	if !ok {
		t.Fatal("monitoring-agents fluent-bit config.outputs is not a string")
	}

	assertTextContains(t, outputs, "_stream_fields=log_source,reason,metadata_namespace", packagePath)
	assertTextContains(t, outputs, "_stream_fields=log_source,stream,kubernetes_pod_name,kubernetes_container_name,kubernetes_namespace_name", packagePath)
	for _, forbidden := range []string{"meatdata_namespace", "metadata_name&_msg_field", "requestUri"} {
		assertTextNotContains(t, outputs, forbidden, packagePath)
	}
}
