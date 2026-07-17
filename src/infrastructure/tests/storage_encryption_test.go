package tests

import (
	"strings"
	"testing"
)

const (
	talosSecureBootSchematic = "be66fdc8a38c2f517f33cba0a6daa7ab97ff87d51e8ca7d2160e45911ba09cf5"
	talosSecureBootInstaller = "factory.talos.dev/metal-installer-secureboot/" + talosSecureBootSchematic + ":v1.13.6@sha256:c3df0484a3f5f3bb68c77d04998fb977a9df6a5268b93bafdb23f668e6f4ed84"
)

func TestTalosSecureBootInstallerAndSchematicConformance(t *testing.T) {
	valuesPath := runfilePath("src/infrastructure/talm/values.yaml")
	values := readText(t, valuesPath)
	assertTextContains(t, values, `image: "`+talosSecureBootInstaller+`"`, valuesPath)
	assertTextNotContains(t, values, "ghcr.io/cozystack/cozystack/talos", valuesPath)

	lockPath := runfilePath("src/infrastructure/bootstrap/bundle/images.declared.lock")
	lock := readText(t, lockPath)
	assertTextContains(t, lock, talosSecureBootInstaller, lockPath)
	assertTextNotContains(t, lock, "ghcr.io/cozystack/cozystack/talos", lockPath)

	schematicPath := runfilePath("src/infrastructure/talm/image-factory-schematic.yaml")
	schematic := singleYAMLDoc(t, schematicPath)
	extensions := sliceValue(nestedValue(t, schematic, "customization", "systemExtensions", "officialExtensions"))
	wantExtensions := []string{
		"siderolabs/amd-ucode",
		"siderolabs/amdgpu",
		"siderolabs/bnx2-bnx2x",
		"siderolabs/intel-ice-firmware",
		"siderolabs/i915",
		"siderolabs/intel-ucode",
		"siderolabs/qlogic-firmware",
		"siderolabs/drbd",
		"siderolabs/zfs",
	}
	if len(extensions) != len(wantExtensions) {
		t.Fatalf("%s has %d extensions, want %d", schematicPath, len(extensions), len(wantExtensions))
	}
	for i, want := range wantExtensions {
		if got := stringValue(extensions[i]); got != want {
			t.Fatalf("%s extension[%d] = %q, want %q", schematicPath, i, got, want)
		}
	}
	assertNestedBool(t, schematic, false, "customization", "secureboot", "includeWellKnownCertificates")

	for _, node := range []string{"ash-earth", "ash-wind", "ash-water"} {
		path := runfilePath("src/infrastructure/talm/nodes/" + node + ".yaml")
		raw := readText(t, path)
		assertTextContains(t, raw, "image: "+talosSecureBootInstaller, path)
		assertTextNotContains(t, raw, "ghcr.io/cozystack/cozystack/talos", path)
	}
}

func TestTalosTPMSystemVolumeEncryptionConformance(t *testing.T) {
	templatePath := runfilePath("src/infrastructure/talm/templates/_helpers.tpl")
	template := readText(t, templatePath)
	for _, want := range []string{
		"kind: VolumeConfig\nname: STATE",
		"kind: VolumeConfig\nname: EPHEMERAL",
		"provider: luks2",
		"checkSecurebootStatusOnEnroll: true",
		"pcrs:\n            - 7",
		"lockToState: true",
	} {
		assertTextContains(t, template, want, templatePath)
	}
	if got := strings.Count(template, "checkSecurebootStatusOnEnroll: true"); got != 2 {
		t.Fatalf("%s has %d system-volume Secure Boot enrollment checks, want 2", templatePath, got)
	}
	if got := strings.Count(template, "lockToState: true"); got != 1 {
		t.Fatalf("%s has %d lockToState entries, want exactly EPHEMERAL", templatePath, got)
	}

	for _, node := range []string{"ash-earth", "ash-wind", "ash-water"} {
		path := runfilePath("src/infrastructure/talm/nodes/" + node + "-overlay.yaml")
		for _, doc := range yamlDocs(t, path) {
			if stringValue(doc["kind"]) == "RawVolumeConfig" {
				t.Fatalf("%s must not put a Talos raw-volume encryption layer underneath LINSTOR", path)
			}
		}
	}
}

func TestCozystackNativeLinstorEncryptionConformance(t *testing.T) {
	path := runfilePath("src/infrastructure/base/storage/storageclasses.yaml")
	classes := map[string]map[string]interface{}{}
	for _, doc := range yamlDocs(t, path) {
		if stringValue(doc["kind"]) != "StorageClass" {
			continue
		}
		classes[stringValue(mapValue(doc["metadata"])["name"])] = doc
	}

	wants := []struct {
		name       string
		layers     string
		remote     string
		reclaim    string
		replicated bool
	}{
		{name: "local-encrypted", layers: "luks storage", remote: "false", reclaim: "Delete"},
		{name: "local-encrypted-retain", layers: "luks storage", remote: "false", reclaim: "Retain"},
		{name: "replicated-encrypted", layers: "drbd luks storage", remote: "true", reclaim: "Delete", replicated: true},
		{name: "replicated-encrypted-retain", layers: "drbd luks storage", remote: "true", reclaim: "Retain", replicated: true},
	}
	for _, want := range wants {
		sc := classes[want.name]
		if sc == nil {
			t.Fatalf("%s missing StorageClass %s", path, want.name)
		}
		parameters := mapValue(sc["parameters"])
		for key, value := range map[string]string{
			"linstor.csi.linbit.com/storagePool":             "data",
			"linstor.csi.linbit.com/layerList":               want.layers,
			"linstor.csi.linbit.com/encryption":              "true",
			"linstor.csi.linbit.com/allowRemoteVolumeAccess": want.remote,
		} {
			if got := stringValue(parameters[key]); got != value {
				t.Errorf("StorageClass %s parameter %s = %q, want %q", want.name, key, got, value)
			}
		}
		if got := stringValue(sc["reclaimPolicy"]); got != want.reclaim {
			t.Errorf("StorageClass %s reclaimPolicy = %q, want %q", want.name, got, want.reclaim)
		}
		if want.replicated && stringValue(parameters["linstor.csi.linbit.com/autoPlace"]) != "3" {
			t.Errorf("StorageClass %s must place three DRBD replicas", want.name)
		}
		labels := mapValue(mapValue(sc["metadata"])["labels"])
		if got := stringValue(labels["guardian.dev/encryption-at-rest"]); got != "linstor-luks" {
			t.Errorf("StorageClass %s encryption label = %q, want linstor-luks", want.name, got)
		}
	}
	annotations := mapValue(mapValue(classes["replicated-encrypted"]["metadata"])["annotations"])
	if got := stringValue(annotations["storageclass.kubernetes.io/is-default-class"]); got != "true" {
		t.Errorf("replicated-encrypted must be the default StorageClass, got %q", got)
	}

	for _, name := range []string{"synthetic-local", "synthetic-local-retain", "synthetic-replicated", "synthetic-replicated-retain"} {
		sc := classes[name]
		if sc == nil {
			t.Fatalf("%s missing StorageClass %s", path, name)
		}
		labels := mapValue(mapValue(sc["metadata"])["labels"])
		if got := stringValue(labels["guardian.dev/data-classification"]); got != "synthetic" {
			t.Errorf("StorageClass %s classification label = %q, want synthetic", name, got)
		}
	}
	patchPath := runfilePath("src/infrastructure/base/app-patches/cozy-linstor-encryption.yaml")
	patch := readText(t, patchPath)
	assertTextContains(t, patch, "kind: LinstorCluster", patchPath)
	assertTextContains(t, patch, "/spec/linstorPassphraseSecret", patchPath)
	assertTextContains(t, patch, "guardian-linstor-master-passphrase", patchPath)

	policyPath := runfilePath("src/infrastructure/base/admission/synthetic-storage-classification.yaml")
	policy := readText(t, policyPath)
	assertTextContains(t, policy, "guardian.dev/data-classification", policyPath)
}

func TestPersistentWorkloadsSelectEncryptedStorageClasses(t *testing.T) {
	wants := map[string]string{
		"src/infrastructure/base/apps/postflight-controlplane-postgres.yaml":      "storageClass: replicated-encrypted",
		"src/infrastructure/base/apps/observability.yaml":                         "storageClassName: replicated-encrypted",
		"src/infrastructure/deployments/guardian/system/openbao-helmrelease.yaml": "storageClass: local-encrypted-retain",
		"src/infrastructure/deployments/guardian/system/zot-helmrelease.yaml":     "storageClassName: replicated-encrypted",
		"src/infrastructure/deployments/iam/prod/postgres.yaml":                   "storageClass: replicated-encrypted",
		"src/infrastructure/deployments/products/prod/electric.yaml":              "storageClassName: replicated-encrypted",
		"src/infrastructure/deployments/products/prod/postgres.yaml":              "storageClass: replicated-encrypted",
	}
	for path, want := range wants {
		runfile := runfilePath(path)
		raw := readText(t, runfile)
		assertTextContains(t, raw, want, runfile)
	}
}

func TestPiraeusStoragePoolsUseStableRawDevices(t *testing.T) {
	path := runfilePath("src/infrastructure/base/storage/linstor-data-pools.yaml")
	raw := readText(t, path)
	docs := yamlDocs(t, path)
	if len(docs) != 3 {
		t.Fatalf("%s has %d storage-pool documents, want 3", path, len(docs))
	}
	wantDevices := map[string]string{
		"guardian-data-pool-ash-earth": "/dev/disk/by-id/nvme-MTFDKCC960TGP-1BK1JABYY_362510FD7C47",
		"guardian-data-pool-ash-wind":  "/dev/disk/by-id/nvme-MTFDKCC960TGP-1BK1JABYY_352410A4E0A6",
		"guardian-data-pool-ash-water": "/dev/disk/by-id/nvme-MTFDKCC960TGP-1BK1JABYY_362510FE3204",
	}
	for _, doc := range docs {
		name := stringValue(mapValue(doc["metadata"])["name"])
		pools := sliceValue(nestedValue(t, doc, "spec", "storagePools"))
		if len(pools) != 1 {
			t.Fatalf("%s/%s has %d storage pools, want 1", stringValue(doc["kind"]), name, len(pools))
		}
		devices := sliceValue(nestedValue(t, mapValue(pools[0]), "source", "hostDevices"))
		if len(devices) != 1 || stringValue(devices[0]) != wantDevices[name] {
			t.Fatalf("%s/%s hostDevices = %v, want only %s", stringValue(doc["kind"]), name, devices, wantDevices[name])
		}
	}
	assertTextNotContains(t, raw, "/dev/mapper/luks2-r-guardian-data", path)
}
