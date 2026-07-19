package tests

import (
	"strings"
	"testing"
)

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
		if classes[name] != nil {
			t.Errorf("unencrypted StorageClass %s must not remain after migration", name)
		}
	}
	encryptedAnnotations := mapValue(mapValue(classes["replicated-encrypted"]["metadata"])["annotations"])
	if got := stringValue(encryptedAnnotations["storageclass.kubernetes.io/is-default-class"]); got != "true" {
		t.Errorf("replicated-encrypted default annotation = %q, want true", got)
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
		if got := stringValue(labels["guardian.dev/encryption-at-rest"]); got != "talos-luks2-raw-volume" {
			t.Errorf("StorageClass %s encryption label = %q, want talos-luks2-raw-volume", name, got)
		}
		if got := stringValue(labels["guardian.dev/linstor-encryption-at-rest"]); got != "disabled" {
			t.Errorf("StorageClass %s LINSTOR encryption label = %q, want disabled", name, got)
		}
		parameters := mapValue(sc["parameters"])
		if got := stringValue(parameters["linstor.csi.linbit.com/encryption"]); got != "" {
			t.Errorf("StorageClass %s must omit immutable LINSTOR encryption parameter, got %q", name, got)
		}
	}

	patchPath := runfilePath("src/infrastructure/base/storage/linstor-encryption.yaml")
	patch := readText(t, patchPath)
	assertTextContains(t, patch, "kind: LinstorCluster", patchPath)
	assertTextContains(t, patch, "kustomize.toolkit.fluxcd.io/ssa: Merge", patchPath)
	assertTextContains(t, patch, "kustomize.toolkit.fluxcd.io/prune: disabled", patchPath)
	assertTextContains(t, patch, "linstorPassphraseSecret:", patchPath)
	assertTextContains(t, patch, "guardian-linstor-master-passphrase", patchPath)
	for _, setting := range []string{
		"DrbdOptions/Net/max-buffers",
		"DrbdOptions/Net/rcvbuf-size",
		"DrbdOptions/Net/sndbuf-size",
		"DrbdOptions/PeerDevice/c-fill-target",
		"DrbdOptions/PeerDevice/c-max-rate",
		"DrbdOptions/PeerDevice/c-min-rate",
		"DrbdOptions/PeerDevice/resync-rate",
		"DrbdOptions/PeerDevice/c-plan-ahead",
	} {
		assertTextContains(t, patch, setting, patchPath)
	}

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
		"src/infrastructure/deployments/authorization/data/postgres.yaml":         "storageClass: replicated-encrypted",
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
	electricPath := "src/infrastructure/deployments/products/prod/electric.yaml"
	electric := readText(t, runfilePath(electricPath))
	if got := strings.Count(electric, "electric-shape-log-encrypted"); got != 2 {
		t.Fatalf("%s must declare and mount electric-shape-log-encrypted exactly twice, got %d", electricPath, got)
	}
}

func TestTalosSecureBootVolumeEncryptionConformance(t *testing.T) {
	const installer = "factory.talos.dev/metal-installer-secureboot/be66fdc8a38c2f517f33cba0a6daa7ab97ff87d51e8ca7d2160e45911ba09cf5:v1.13.6@sha256:c3df0484a3f5f3bb68c77d04998fb977a9df6a5268b93bafdb23f668e6f4ed84"

	assetsPath := runfilePath("src/infrastructure/talm/secureboot-assets.yaml")
	assets := singleYAMLDoc(t, assetsPath)
	assertNestedString(t, assets, "TalosSecureBootAssets", "kind")
	assertNestedString(t, assets, "be66fdc8a38c2f517f33cba0a6daa7ab97ff87d51e8ca7d2160e45911ba09cf5", "spec", "schematic", "id")
	assertNestedString(t, assets, "sha256:c3df0484a3f5f3bb68c77d04998fb977a9df6a5268b93bafdb23f668e6f4ed84", "spec", "installer", "digest")
	assertNestedString(t, assets, "f62fd4d79492b3a95bc9e99a71adcfe33353ebb9175ee93785393e537dfb6574", "spec", "iso", "sha256")
	assertNestedString(t, assets, "siderolabs", "spec", "enrollment", "provider")
	assertNestedBool(t, assets, false, "spec", "enrollment", "includeWellKnownUefiCertificates")
	assertNestedString(t, assets, "376357e93a6ec32db748c2eb45656f13c9ee6951af7ab83ee1a8153ae5052f7b", "spec", "enrollment", "pkAuthSha256")
	assertNestedString(t, assets, "d3475be84bbdc6adfe98b5abd26b8ac90fbe4ee227a537713f8c964ae393922e", "spec", "enrollment", "kekAuthSha256")
	assertNestedString(t, assets, "8fa031d0ecebdab3e4469c7fe95b9b1ed1a390af966e0159093659ecb4d6dff1", "spec", "enrollment", "dbAuthSha256")
	assertNestedString(t, assets, "1ae5d7c8ac1032eaf0d2c1a2e6a952517342e8db6b5354d32791a9c960a9472e", "spec", "signatures", "secureBootCertificateSha256")
	assertNestedString(t, assets, "9c42059148e157a030f5edc51bd4967a2a3b1bc64cdd48941a3ece3c3fdc032f", "spec", "signatures", "pcrSigningPublicSpkiSha256")

	valuesPath := runfilePath("src/infrastructure/talm/values.yaml")
	values := readText(t, valuesPath)
	assertTextContains(t, values, `image: "`+installer+`"`, valuesPath)
	assertTextContains(t, values, "- factory.talos.dev", valuesPath)

	templatePath := runfilePath("src/infrastructure/talm/templates/_helpers.tpl")
	template := readText(t, templatePath)
	for _, want := range []string{
		`"a|^/dev/mapper/luks2-r-guardian-data$|"`,
		"name: STATE",
		"name: EPHEMERAL",
		"provider: luks2",
		"checkSecurebootStatusOnEnroll: true",
		"lockToState: true",
	} {
		assertTextContains(t, template, want, templatePath)
	}

	poolsPath := runfilePath("src/infrastructure/base/storage/linstor-data-pools.yaml")
	pools := readText(t, poolsPath)
	if got := strings.Count(pools, "/dev/mapper/luks2-r-guardian-data"); got != 3 {
		t.Fatalf("%s must source all three LINSTOR pools from the Talos encrypted mapper, got %d", poolsPath, got)
	}

	nodes := map[string]string{
		"ash-earth": "362510FD7C47",
		"ash-wind":  "352410A4E0A6",
		"ash-water": "362510FE3204",
	}
	for node, serial := range nodes {
		overlayPath := "src/infrastructure/talm/nodes/" + node + "-overlay.yaml"
		overlay := readText(t, runfilePath(overlayPath))
		for _, want := range []string{
			"name: guardian-data",
			`match: 'disk.serial == "` + serial + `"'`,
			"provider: luks2",
			"checkSecurebootStatusOnEnroll: true",
			"lockToState: true",
		} {
			assertTextContains(t, overlay, want, overlayPath)
		}

		nodePath := "src/infrastructure/talm/nodes/" + node + ".yaml"
		nodeConfig := readText(t, runfilePath(nodePath))
		assertTextContains(t, nodeConfig, "image: "+installer, nodePath)
		assertTextContains(t, nodeConfig, "name: STATE", nodePath)
		assertTextContains(t, nodeConfig, "name: EPHEMERAL", nodePath)
	}
}
