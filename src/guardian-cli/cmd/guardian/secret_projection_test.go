package main

import (
	"strings"
	"testing"
)

func TestSecretProjectionPlatformRender(t *testing.T) {
	c := componentByName(t, "secret-projection-platform")
	tmpl, err := toolPath("_main/" + c.manifest)
	if err != nil {
		t.Fatalf("locate SecretProjection platform manifest: %v", err)
	}
	c.manifest = tmpl
	rendered, err := renderComponentManifest(c, "", nil, &Site{})
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	for _, want := range []string{
		"kind: CompositeResourceDefinition",
		"name: secretprojections.platform.guardian.dev",
		"kind: Composition",
		"name: secret-projection-openbao",
		"kind: SecretStore",
		"kind: ExternalSecret",
		"providerConfigRef:\n                name: platform",
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
