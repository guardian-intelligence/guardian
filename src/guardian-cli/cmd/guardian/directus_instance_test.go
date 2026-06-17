package main

import (
	"strings"
	"testing"
)

func TestDirectusInstancesFromSiteBundles(t *testing.T) {
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestHost(t, siteName)
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
			if instance.Spec.Database.Persistence.ClaimName != "directus-postgres-data" {
				t.Fatalf("directus database claim = %q, want directus-postgres-data", instance.Spec.Database.Persistence.ClaimName)
			}
			if instance.Spec.Uploads.Persistence.ClaimName != "directus-uploads" {
				t.Fatalf("directus uploads claim = %q, want directus-uploads", instance.Spec.Uploads.Persistence.ClaimName)
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

func TestDirectusInstanceProjectsS3StorageSecrets(t *testing.T) {
	site := siteWithEnvironment(directusEnvironment("dev", "directus", `
  storage:
    s3:
      enabled: true
      bucket: guardian-directus
      region: auto
      endpoint: https://example.r2.cloudflarestorage.com
      remotePath: guardian/guardian-dev/directus/uploads
      secretName: directus-uploads
      accessKeyIDKey: access-key-id
      secretAccessKeyKey: secret-access-key
`))
	instances, err := directusInstances(site)
	if err != nil {
		t.Fatal(err)
	}
	projection := directusSecretProjection(instances[0])
	secret := projectedSecretByName(t, projection, "directus-uploads")
	if secret.RemotePath != "guardian/guardian-dev/directus/uploads" {
		t.Fatalf("s3 remotePath = %q", secret.RemotePath)
	}
	if len(secret.Data) != 2 {
		t.Fatalf("s3 data length = %d, want 2", len(secret.Data))
	}
	for _, want := range []string{"access-key-id", "secret-access-key"} {
		var found bool
		for _, item := range secret.Data {
			if item.SecretKey == want && item.Property == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("s3 data missing %s mapping: %#v", want, secret.Data)
		}
	}
}

func TestDirectusInstanceRequiresProjectionPaths(t *testing.T) {
	site := siteWithEnvironment(strings.Replace(directusEnvironment("dev", "directus", ""), "runtime: guardian/guardian-dev/directus/runtime", "runtime: ''", 1))
	_, err := directusInstances(site)
	if err == nil || !strings.Contains(err.Error(), "secrets.projection.remotePaths.runtime is required") {
		t.Fatalf("directusInstances error = %v, want missing projection path error", err)
	}
}

func TestCompanySiteDirectusBindingMustMatchInstance(t *testing.T) {
	xr := &companySiteSpec{}
	xr.DirectusRef.Name = "missing"
	xr.ContentSnapshot.Digest = "workspace"
	site := &Host{Name: "dev"}
	site.Company.Domain = "dev.guardianintelligence.org"
	err := validateCompanySiteDirectusBinding(site, "environment.yaml", xr, []directusInstanceManifest{{}})
	if err == nil || !strings.Contains(err.Error(), "does not match any DirectusInstance") {
		t.Fatalf("validateCompanySiteDirectusBinding error = %v, want missing DirectusInstance error", err)
	}
}

func siteWithEnvironment(raw string) *Host {
	site := &Host{Name: "dev"}
	site.Storage.ProductPool.Mountpoint = "/var/mnt/guardian"
	plane := storagePlaneManifest{Kind: "StoragePlane"}
	plane.Metadata.Name = "local-zfs"
	plane.Spec.Site = "dev"
	plane.Spec.NodeName = "dev-w0"
	plane.Spec.StorageClassName = "guardian-local-retain"
	plane.Spec.ReclaimPolicy = "Retain"
	plane.Spec.VolumeBindingMode = "WaitForFirstConsumer"
	plane.Spec.Volumes = []storagePlaneVolume{
		{
			Name:                 "directus-postgres",
			PersistentVolumeName: "guardian-dev-directus-postgres",
			Namespace:            "directus",
			ClaimName:            "directus-postgres-data",
			Capacity:             "20Gi",
			LocalPath:            "/var/mnt/guardian/directus/postgres",
		},
		{
			Name:                 "directus-uploads",
			PersistentVolumeName: "guardian-dev-directus-uploads",
			Namespace:            "directus",
			ClaimName:            "directus-uploads",
			Capacity:             "10Gi",
			LocalPath:            "/var/mnt/guardian/directus/uploads",
		},
	}
	site.StoragePlane = &plane
	site.EnvironmentBundle.Path = "environment.yaml"
	site.EnvironmentBundle.Raw = []byte(raw)
	return site
}

func directusEnvironment(siteName, directusName, extra string) string {
	return `apiVersion: platform.guardian.dev/v1alpha1
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
    projection:
      openbao:
        role: directus-secrets
        serviceAccountName: external-secrets-directus
      remotePaths:
        runtime: guardian/guardian-dev/directus/runtime
        admin: guardian/guardian-dev/directus/admin
        database: guardian/guardian-dev/directus/postgres
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
    persistence:
      claimName: directus-postgres-data
      storageClassName: guardian-local-retain
      size: 20Gi
      volumeName: guardian-dev-directus-postgres
      accessModes:
        - ReadWriteOnce
  uploads:
    persistence:
      claimName: directus-uploads
      storageClassName: guardian-local-retain
      size: 10Gi
      volumeName: guardian-dev-directus-uploads
      accessModes:
        - ReadWriteOnce
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
