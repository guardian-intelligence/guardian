package tests

import "testing"

func TestCozystackNativeLinstorEncryptionFoundationConformance(t *testing.T) {
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

	// Existing names and the old default remain available until every bound
	// volume has been copied and verified; this foundation change must not
	// trigger workload recreation or prune a StorageClass during convergence.
	for _, name := range []string{"local", "local-retain", "openbao-local", "replicated", "replicated-retain"} {
		if classes[name] == nil {
			t.Errorf("migration foundation must retain StorageClass %s", name)
		}
	}
	metadata := mapValue(classes["replicated"]["metadata"])
	annotations := mapValue(metadata["annotations"])
	if got := stringValue(annotations["storageclass.kubernetes.io/is-default-class"]); got != "true" {
		t.Errorf("legacy replicated class must remain default during the foundation phase, got %q", got)
	}

	patchPath := runfilePath("src/infrastructure/base/app-patches/cozy-linstor-encryption.yaml")
	patch := readText(t, patchPath)
	assertTextContains(t, patch, "kind: LinstorCluster", patchPath)
	assertTextContains(t, patch, "/spec/linstorPassphraseSecret", patchPath)
	assertTextContains(t, patch, "guardian-linstor-master-passphrase", patchPath)

	canaryPath := runfilePath("src/infrastructure/deployments/guardian/system/storage-encryption-canary.yaml")
	canary := readText(t, canaryPath)
	assertTextContains(t, canary, "storageClassName: local-encrypted", canaryPath)
	assertTextContains(t, canary, "storageClassName: replicated-encrypted", canaryPath)
}
