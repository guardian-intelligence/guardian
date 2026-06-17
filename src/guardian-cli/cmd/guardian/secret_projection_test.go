package main

import (
	"strings"
	"testing"
)

func TestSecretProjectionPlatformRender(t *testing.T) {
	out := buildTestPlatformPackage(t)
	for _, want := range []string{
		"kind: CompositeResourceDefinition",
		"name: secretprojections.platform.guardian.dev",
		"kind: Composition",
		"name: secret-projection-openbao",
		"kind: SecretStore",
		"kind: ExternalSecret",
		"createNamespace",
		"deletionPolicy: Orphan",
		"providerConfigRef:",
		"name: platform",
		"server: http://openbao.openbao.svc:8200",
		"mountPath: kubernetes",
		"name: function-auto-ready",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("SecretProjection platform render missing %q", want)
		}
	}
}

func TestSecretProjectionSiteManifests(t *testing.T) {
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			sitePath, err := toolPath("_main/src/sites/" + siteName + "/bootstrap.yaml")
			if err != nil {
				t.Fatalf("locate bootstrap.yaml: %v", err)
			}
			site, err := loadSite(sitePath)
			if err != nil {
				t.Fatal(err)
			}
			projections, err := secretProjections(site)
			if err != nil {
				t.Fatal(err)
			}
			if len(projections) == 0 {
				t.Fatal("site declares no SecretProjection instances")
			}
			observability := secretProjectionByName(t, projections, "observability-admin-secrets")
			if observability.Spec.Target.Namespace != "observability" {
				t.Fatalf("observability namespace = %q", observability.Spec.Target.Namespace)
			}
			if observability.Spec.Target.CreateNamespace == nil || *observability.Spec.Target.CreateNamespace {
				t.Fatal("observability SecretProjection should not create the ObservabilityStack namespace")
			}
			if len(observability.Spec.Target.NamespaceLabels) != 0 {
				t.Fatalf("observability SecretProjection namespaceLabels = %#v, want none", observability.Spec.Target.NamespaceLabels)
			}
			if observability.Spec.OpenBao.Role != "observability-secrets" {
				t.Fatalf("observability role = %q", observability.Spec.OpenBao.Role)
			}
			for _, secretName := range []string{"clickhouse-admin", "grafana-admin"} {
				secret := projectedSecretByName(t, observability, secretName)
				wantPath := "guardian/" + site.Cluster.Name + "/observability/" + secretName
				if secret.RemotePath != wantPath {
					t.Fatalf("%s remotePath = %q, want %q", secretName, secret.RemotePath, wantPath)
				}
				if len(secret.Data) != 1 || secret.Data[0].SecretKey != "password" || secret.Data[0].Property != "password" {
					t.Fatalf("%s data = %#v, want password mapping", secretName, secret.Data)
				}
			}
			if site.OCI.Domain != "" {
				zot := secretProjectionByName(t, projections, "zot-publisher-secrets")
				if zot.Spec.Target.Namespace != "guardian-oci" {
					t.Fatalf("zot namespace = %q, want guardian-oci", zot.Spec.Target.Namespace)
				}
				if zot.Spec.Target.NamespaceLabels["pod-security.kubernetes.io/enforce"] != "privileged" {
					t.Fatalf("zot namespaceLabels = %#v, want privileged PSA", zot.Spec.Target.NamespaceLabels)
				}
				if !zot.waitForSecrets() {
					t.Fatal("zot publisher secrets should gate registry converge")
				}
				if zot.Spec.OpenBao.Role != "guardian-oci-secrets" {
					t.Fatalf("zot role = %q, want guardian-oci-secrets", zot.Spec.OpenBao.Role)
				}
				secret := projectedSecretByName(t, zot, "zot-publisher")
				wantPath := "guardian/" + site.Cluster.Name + "/oci/zot-publisher"
				if secret.RemotePath != wantPath {
					t.Fatalf("zot remotePath = %q, want %q", secret.RemotePath, wantPath)
				}
				if len(secret.Data) != 3 {
					t.Fatalf("zot data length = %d, want 3", len(secret.Data))
				}
			}
			directus := secretProjectionByName(t, projections, "directus-secrets")
			if directus.Spec.Target.Namespace != "directus" {
				t.Fatalf("directus namespace = %q", directus.Spec.Target.Namespace)
			}
			if directus.Spec.Target.CreateNamespace == nil || *directus.Spec.Target.CreateNamespace {
				t.Fatal("directus SecretProjection should not create the Directus namespace")
			}
			if len(directus.Spec.Target.NamespaceLabels) != 0 {
				t.Fatalf("directus SecretProjection namespaceLabels = %#v, want none", directus.Spec.Target.NamespaceLabels)
			}
			if directus.waitForSecrets() {
				t.Fatal("directus secrets should not gate public site converge")
			}
			for _, tc := range []struct {
				name string
				path string
				key  string
			}{
				{name: "directus-runtime", path: "runtime", key: "secret"},
				{name: "directus-admin", path: "admin", key: "password"},
				{name: "directus-postgres", path: "postgres", key: "password"},
			} {
				secret := projectedSecretByName(t, directus, tc.name)
				wantPath := "guardian/" + site.Cluster.Name + "/directus/" + tc.path
				if secret.RemotePath != wantPath {
					t.Fatalf("%s remotePath = %q, want %q", tc.name, secret.RemotePath, wantPath)
				}
				if len(secret.Data) != 1 || secret.Data[0].SecretKey != tc.key || secret.Data[0].Property != tc.key {
					t.Fatalf("%s data = %#v, want %s mapping", tc.name, secret.Data, tc.key)
				}
			}
		})
	}
}

func TestSecretProjectionCreatesNamespaceDefault(t *testing.T) {
	var projection secretProjectionManifest
	if !projection.createsNamespace() {
		t.Fatal("SecretProjection should create target namespaces by default")
	}
	disabled := false
	projection.Spec.Target.CreateNamespace = &disabled
	if projection.createsNamespace() {
		t.Fatal("SecretProjection createNamespace=false should disable namespace creation")
	}
}

func secretProjectionByName(t *testing.T, projections []secretProjectionManifest, name string) secretProjectionManifest {
	t.Helper()
	for _, projection := range projections {
		if projection.Metadata.Name == name {
			return projection
		}
	}
	t.Fatalf("SecretProjection %s not found", name)
	return secretProjectionManifest{}
}

func projectedSecretByName(t *testing.T, projection secretProjectionManifest, name string) secretProjectionSecret {
	t.Helper()
	for _, secret := range projection.Spec.Secrets {
		if secret.Name == name {
			return secret
		}
	}
	t.Fatalf("SecretProjection %s secret %s not found", projection.Metadata.Name, name)
	return secretProjectionSecret{}
}
