package main

import (
	"strings"
	"testing"
)

func TestDirectusInstancesFromSiteBundles(t *testing.T) {
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestSite(t, siteName)
			instances, err := directusInstances(site)
			if err != nil {
				t.Fatal(err)
			}
			if len(instances) != 1 {
				t.Fatalf("directus instance count = %d, want 1", len(instances))
			}
			instance := instances[0]
			if instance.Metadata.Name != "directus" {
				t.Fatalf("directus instance name = %q, want directus", instance.Metadata.Name)
			}
			if instance.Spec.Site != siteName {
				t.Fatalf("directus instance site = %q, want %q", instance.Spec.Site, siteName)
			}
			if instance.Spec.Storage.S3 != nil && instance.Spec.Storage.S3.Enabled {
				t.Fatal("checked-in Directus instances should use local upload storage until object storage is admitted")
			}
		})
	}
}

func TestDirectusInstanceRejectsIncompleteS3Storage(t *testing.T) {
	site := siteWithEnvironment(directusEnvironment("dev", "directus", `
  storage:
    s3:
      enabled: true
      bucket: guardian-directus
`))
	_, err := directusInstances(site)
	if err == nil || !strings.Contains(err.Error(), "storage.s3.enabled requires") {
		t.Fatalf("directusInstances error = %v, want incomplete S3 storage error", err)
	}
}

func TestDirectusInstanceRejectsUnprojectedSecrets(t *testing.T) {
	site := siteWithEnvironment(strings.Replace(directusEnvironment("dev", "directus", ""), "runtimeSecretName: directus-runtime", "runtimeSecretName: manual-runtime", 1))
	_, err := directusInstances(site)
	if err == nil || !strings.Contains(err.Error(), "manual-runtime is not declared by a SecretProjection") {
		t.Fatalf("directusInstances error = %v, want unprojected secret error", err)
	}
}

func TestCompanySiteDirectusBindingMustMatchInstance(t *testing.T) {
	xr := &companySiteSpec{}
	xr.DirectusRef.Name = "missing"
	xr.ContentSnapshot.Digest = "workspace"
	site := &Site{Name: "dev"}
	site.Company.Domain = "dev.guardianintelligence.org"
	err := validateCompanySiteDirectusBinding(site, "environment.yaml", xr, []directusInstanceManifest{{}})
	if err == nil || !strings.Contains(err.Error(), "does not match any DirectusInstance") {
		t.Fatalf("validateCompanySiteDirectusBinding error = %v, want missing DirectusInstance error", err)
	}
}

func siteWithEnvironment(raw string) *Site {
	site := &Site{Name: "dev"}
	site.EnvironmentBundle.Path = "environment.yaml"
	site.EnvironmentBundle.Raw = []byte(raw)
	return site
}

func directusEnvironment(siteName, directusName, extra string) string {
	return `apiVersion: platform.guardian.dev/v1alpha1
kind: SecretProjection
metadata:
  name: directus-secrets
spec:
  target:
    namespace: directus
  openbao:
    role: directus-secrets
    serviceAccountName: external-secrets-directus
  secrets:
    - name: directus-runtime
      type: Opaque
      remotePath: guardian/guardian-dev/directus/runtime
      data:
        - secretKey: secret
          property: secret
    - name: directus-admin
      type: Opaque
      remotePath: guardian/guardian-dev/directus/admin
      data:
        - secretKey: password
          property: password
    - name: directus-postgres
      type: Opaque
      remotePath: guardian/guardian-dev/directus/postgres
      data:
        - secretKey: password
          property: password
---
apiVersion: platform.guardian.dev/v1alpha1
kind: DirectusInstance
metadata:
  name: ` + directusName + `
spec:
  site: ` + siteName + `
  namespace: directus
  image: registry.guardian.internal/directus@sha256:deadbeef
  postgresImage: registry.guardian.internal/postgres@sha256:deadbeef
  publicAdminRoute: false
  secrets:
    runtimeSecretName: directus-runtime
    runtimeSecretKey: secret
    adminSecretName: directus-admin
    adminPasswordKey: password
    databaseSecretName: directus-postgres
    databasePasswordKey: password
  admin:
    email: ops@example.com
  database:
    name: directus
    user: directus
    storagePath: /var/lib/guardian/directus/postgres
  uploadsPath: /var/lib/guardian/directus/uploads
  resources:
    requests:
      cpu: 100m
      memory: 512Mi
    limits:
      memory: 1Gi
  gateway:
    name: edge
    namespace: gateway
    httpSectionName: http
` + extra
}
