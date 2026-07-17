package tests

import "testing"

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

	for _, name := range []string{"local", "local-retain", "openbao-local", "replicated", "replicated-retain"} {
		if classes[name] == nil {
			t.Errorf("storage migration must retain StorageClass %s", name)
		}
	}
	legacyAnnotations := mapValue(mapValue(classes["replicated"]["metadata"])["annotations"])
	if got := stringValue(legacyAnnotations["storageclass.kubernetes.io/is-default-class"]); got != "false" {
		t.Errorf("replicated default annotation = %q, want false", got)
	}
	encryptedAnnotations := mapValue(mapValue(classes["replicated-encrypted"]["metadata"])["annotations"])
	if got := stringValue(encryptedAnnotations["storageclass.kubernetes.io/is-default-class"]); got != "true" {
		t.Errorf("replicated-encrypted default annotation = %q, want true", got)
	}

	for _, name := range []string{"synthetic-local", "synthetic-local-retain", "synthetic-replicated", "synthetic-replicated-retain"} {
		labels := mapValue(mapValue(classes[name]["metadata"])["labels"])
		if got := stringValue(labels["guardian.dev/data-classification"]); got != "synthetic" {
			t.Errorf("StorageClass %s classification label = %q, want synthetic", name, got)
		}
	}

	patchPath := runfilePath("src/infrastructure/base/storage/linstor-encryption.yaml")
	patch := readText(t, patchPath)
	assertTextContains(t, patch, "kind: LinstorCluster", patchPath)
	assertTextContains(t, patch, "kustomize.toolkit.fluxcd.io/ssa: Merge", patchPath)
	assertTextContains(t, patch, "kustomize.toolkit.fluxcd.io/prune: disabled", patchPath)
	assertTextContains(t, patch, "linstorPassphraseSecret:", patchPath)
	assertTextContains(t, patch, "guardian-linstor-master-passphrase", patchPath)

	canaryPath := runfilePath("src/infrastructure/deployments/guardian/system/storage-encryption-canary.yaml")
	canary := readText(t, canaryPath)
	assertTextContains(t, canary, "storageClassName: local-encrypted", canaryPath)
	assertTextContains(t, canary, "storageClassName: replicated-encrypted", canaryPath)

	policyPath := runfilePath("src/infrastructure/base/admission/synthetic-storage-classification.yaml")
	policy := readText(t, policyPath)
	assertTextContains(t, policy, "guardian.dev/data-classification", policyPath)

	workloads := map[string]string{
		"src/infrastructure/base/apps/postflight-controlplane-postgres.yaml":      "storageClass: replicated-encrypted",
		"src/infrastructure/base/apps/observability.yaml":                         "storageClassName: replicated-encrypted",
		"src/infrastructure/deployments/guardian/system/openbao-helmrelease.yaml": "storageClass: local-encrypted-retain",
		"src/infrastructure/deployments/guardian/system/zot-helmrelease.yaml":     "storageClassName: replicated-encrypted",
		"src/infrastructure/deployments/iam/prod/postgres.yaml":                   "storageClass: replicated-encrypted",
		"src/infrastructure/deployments/products/prod/electric.yaml":              "storageClassName: replicated-encrypted",
		"src/infrastructure/deployments/products/prod/postgres.yaml":              "storageClass: replicated-encrypted",
	}
	for workloadPath, want := range workloads {
		raw := readText(t, runfilePath(workloadPath))
		assertTextContains(t, raw, want, workloadPath)
	}
}
