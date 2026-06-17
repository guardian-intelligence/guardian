package main

import (
	"strings"
	"testing"
)

func TestStoragePlaneSiteManifests(t *testing.T) {
	wantVolumes := map[string]int{"dev": 4, "gamma": 3, "prod": 4}
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestSite(t, siteName)
			planes, err := storagePlanes(site)
			if err != nil {
				t.Fatal(err)
			}
			if len(planes) != 1 {
				t.Fatalf("storage plane count = %d, want 1", len(planes))
			}
			plane := planes[0]
			if plane.Metadata.Name != "local-zfs" {
				t.Fatalf("StoragePlane name = %q, want local-zfs", plane.Metadata.Name)
			}
			if plane.Spec.Site != siteName {
				t.Fatalf("StoragePlane site = %q, want %q", plane.Spec.Site, siteName)
			}
			if plane.Spec.NodeName != site.Node.Hostname {
				t.Fatalf("StoragePlane nodeName = %q, want %q", plane.Spec.NodeName, site.Node.Hostname)
			}
			if plane.Spec.StorageClassName != "guardian-local-retain" {
				t.Fatalf("StoragePlane storageClassName = %q", plane.Spec.StorageClassName)
			}
			if len(plane.Spec.Volumes) != wantVolumes[siteName] {
				t.Fatalf("StoragePlane volumes = %d, want %d", len(plane.Spec.Volumes), wantVolumes[siteName])
			}
			for _, volume := range plane.Spec.Volumes {
				if !strings.HasPrefix(volume.LocalPath, site.Storage.ProductPool.Mountpoint+"/") {
					t.Fatalf("StoragePlane volume %s localPath = %q, want under %q", volume.Name, volume.LocalPath, site.Storage.ProductPool.Mountpoint)
				}
			}
		})
	}
}

func TestStoragePlanePlatformRender(t *testing.T) {
	out := buildTestPlatformPackage(t)
	for _, want := range []string{
		"kind: CompositeResourceDefinition",
		"name: storageplanes.platform.guardian.dev",
		"kind: Composition",
		"name: storage-plane-local-static",
		"kind: StorageClass",
		"provisioner: kubernetes.io/no-provisioner",
		"kind: PersistentVolume",
		"claimRef:",
		"kubernetes.io/hostname",
		"name: function-environment-configs",
		"name: function-auto-ready",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("StoragePlane platform render missing %q", want)
		}
	}
}

func TestLocalStorageBootstrapRender(t *testing.T) {
	kubectl, err := kubectlPath()
	if err != nil {
		t.Fatalf("locate kubectl: %v", err)
	}
	site := loadTestSite(t, "dev")
	c := componentByName(t, "local-storage-bootstrap")
	rendered, err := buildComponentKustomization(kubectl, c, map[string]string{"postgres": postgresTestImage}, site)
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	for _, want := range []string{
		"kind: Job",
		"name: zfs-pool-init",
		"namespace: guardian-storage",
		"image: " + postgresTestImage,
		"name: zfs-pool-init-values",
		"pool: guardian",
		"serial: 362510FD7C47",
		"mountpoint: /var/mnt/guardian",
		"/var/mnt/guardian/victoria-metrics",
		`ensure_dataset "${local_path}"`,
		`zfs create -p -o mountpoint="${local_path}" "${dataset}"`,
		"mountPropagation: Bidirectional",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("local storage bootstrap render missing %q", want)
		}
	}
}
