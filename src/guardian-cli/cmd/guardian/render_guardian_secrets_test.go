package main

import (
	"strings"
	"testing"
)

func TestGuardianSecretsRenderProjectsObservabilitySecrets(t *testing.T) {
	sitePath, err := toolPath("_main/src/sites/dev/site.yaml")
	if err != nil {
		t.Fatalf("locate site.yaml: %v", err)
	}
	site, err := loadSite(sitePath)
	if err != nil {
		t.Fatal(err)
	}
	c := componentByName(t, "guardian-secrets")
	tmpl, err := toolPath("_main/src/infrastructure-components/guardian-secrets/k8s/guardian-secrets.yaml.tmpl")
	if err != nil {
		t.Fatalf("locate guardian-secrets manifest: %v", err)
	}
	c.manifest = tmpl
	rendered, err := renderComponentManifest(c, "", nil, site)
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	decodeKinds(t, rendered)
	for _, want := range []string{
		"kind: ServiceAccount",
		"name: external-secrets-observability",
		"kind: SecretStore",
		"name: openbao",
		"server: http://openbao.openbao.svc:8200",
		"path: kv",
		"version: v2",
		"mountPath: kubernetes",
		"role: observability-secrets",
		"kind: ExternalSecret",
		"name: clickhouse-admin",
		"name: grafana-admin",
		"namespace: observability",
		"key: guardian/" + site.Cluster.Name + "/observability/clickhouse-admin",
		"key: guardian/" + site.Cluster.Name + "/observability/grafana-admin",
		"property: password",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("guardian-secrets render missing %q", want)
		}
	}
	if strings.Contains(out, "tokenSecretRef") {
		t.Error("guardian-secrets must use Kubernetes auth, not a static OpenBao token Secret")
	}
	if strings.Contains(out, "ClusterSecretStore") {
		t.Error("observability projection should stay namespace-scoped through SecretStore")
	}
}
