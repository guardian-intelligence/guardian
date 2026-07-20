package tests

import (
	"strings"
	"testing"
)

func TestAlertmanagerUsesHeaderAuthentication(t *testing.T) {
	configPath := runfilePath("src/infrastructure/deployments/alerting/alertmanager-config.yaml")
	secret := singleYAMLDoc(t, configPath)
	assertNestedString(t, secret, "Secret", "kind")
	assertNestedString(t, secret, "guardian-alertmanager", "metadata", "name")
	assertNestedString(t, secret, "tenant-root", "metadata", "namespace")

	config, ok := nestedValue(t, secret, "stringData", "alertmanager.yaml").(string)
	if !ok {
		t.Fatal("alertmanager.yaml is not a string")
	}
	if strings.Contains(config, "api-key=") {
		t.Fatal("Alerta credential must not appear in a webhook URL")
	}
	for _, want := range []string{
		"url: http://alerta/api/webhooks/prometheus",
		"type: Key",
		"credentials_file: /etc/vm/secrets/alerta/alerta-api-key",
		"send_resolved: true",
		"send_resolved: false",
		"repeat_interval: 2m",
		"inhibit_rules:",
		`alertname=~"VMAgentDown|VMAgentCrashLooping"`,
		`prometheus="cozy-monitoring/vmagent"`,
		"GuardianLoginCanaryStale",
		"KubeAggregatedAPIErrors",
		"OpenBaoAuditLogSilent",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("alertmanager config is missing %q", want)
		}
	}
	if got := strings.Count(config, "credentials_file: /etc/vm/secrets/alerta/alerta-api-key"); got != 2 {
		t.Fatalf("credential file is referenced %d times, want both Alerta receivers", got)
	}

	managerPath := runfilePath("src/infrastructure/base/app-patches/monitoring-alertmanager-auth.yaml")
	manager := singleYAMLDoc(t, managerPath)
	assertNestedString(t, manager, "VMAlertmanager", "kind")
	assertNestedString(t, manager, "alertmanager", "metadata", "name")
	assertNestedString(t, manager, "tenant-root", "metadata", "namespace")
	assertNestedString(t, manager, "Override", "metadata", "annotations", "kustomize.toolkit.fluxcd.io/ssa")
	assertNestedString(t, manager, "guardian-alertmanager", "spec", "configSecret")
	secrets := sliceValue(nestedValue(t, manager, "spec", "secrets"))
	if len(secrets) != 1 || secrets[0] != "alerta" {
		t.Fatalf("spec.secrets = %#v, want the Alerta credential Secret", secrets)
	}
}
