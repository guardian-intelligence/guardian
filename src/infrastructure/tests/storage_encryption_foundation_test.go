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
		"src/infrastructure/deployments/products/prod/postgres.yaml":              "storageClass: replicated-encrypted",
	}
	for workloadPath, want := range workloads {
		raw := readText(t, runfilePath(workloadPath))
		assertTextContains(t, raw, want, workloadPath)
	}
}

func TestTalosBootAndVolumeEncryptionConformance(t *testing.T) {
	const controlplaneInstaller = "factory.talos.dev/metal-installer-secureboot/be66fdc8a38c2f517f33cba0a6daa7ab97ff87d51e8ca7d2160e45911ba09cf5:v1.13.6@sha256:f6245aaf9c630fc40479e5798b616468c144c1601c29fc6e19de8d32cc7277e2"
	const workerInstaller = "factory.talos.dev/metal-installer/be66fdc8a38c2f517f33cba0a6daa7ab97ff87d51e8ca7d2160e45911ba09cf5:v1.13.6@sha256:89997784056d862a59c2a2d0f2fa78714a6c3ef76f34ef4f8d835dee829719d3"

	assetsPath := runfilePath("src/infrastructure/talm/secureboot-assets.yaml")
	assets := singleYAMLDoc(t, assetsPath)
	assertNestedString(t, assets, "TalosSecureBootAssets", "kind")
	assertNestedString(t, assets, "be66fdc8a38c2f517f33cba0a6daa7ab97ff87d51e8ca7d2160e45911ba09cf5", "spec", "schematic", "id")
	assertNestedString(t, assets, "sha256:f6245aaf9c630fc40479e5798b616468c144c1601c29fc6e19de8d32cc7277e2", "spec", "installer", "digest")
	assertNestedString(t, assets, "0eb58631ff9757a203dab2d761e258dc504f40a9c57df48119ed9b93c9bb51a8", "spec", "iso", "sha256")
	assertNestedString(t, assets, "2026-08-16", "spec", "iso", "observedAt")
	assertNestedString(t, assets, "siderolabs", "spec", "enrollment", "provider")
	assertNestedBool(t, assets, false, "spec", "enrollment", "includeWellKnownUefiCertificates")
	assertNestedString(t, assets, "7d7cf9ff27dea79cd039ddf1117ba1d860618bdc5cf52eb4dd2eaa458e169ddf", "spec", "enrollment", "pkAuthSha256")
	assertNestedString(t, assets, "6b9bfd2b026c4c2de6fd104f0f96e720f78b73f5910dfdff804b9d3d8c119796", "spec", "enrollment", "kekAuthSha256")
	assertNestedString(t, assets, "3048c1a98e19d912d1dd8146dae64a5d09c93d5e91c4fb41cac767c0c8f68286", "spec", "enrollment", "dbAuthSha256")
	assertNestedString(t, assets, "1ae5d7c8ac1032eaf0d2c1a2e6a952517342e8db6b5354d32791a9c960a9472e", "spec", "signatures", "secureBootCertificateSha256")
	assertNestedString(t, assets, "9c42059148e157a030f5edc51bd4967a2a3b1bc64cdd48941a3ece3c3fdc032f", "spec", "signatures", "pcrSigningPublicSpkiSha256")

	workerAssetsPath := runfilePath("src/infrastructure/talm/wum-worker-assets.yaml")
	workerAssets := singleYAMLDoc(t, workerAssetsPath)
	assertNestedString(t, workerAssets, "TalosWorkerAssets", "kind")
	assertNestedString(t, workerAssets, "ash-worker0", "metadata", "name")
	assertNestedString(t, workerAssets, "v1.13.6", "spec", "talosVersion")
	assertNestedString(t, workerAssets, "be66fdc8a38c2f517f33cba0a6daa7ab97ff87d51e8ca7d2160e45911ba09cf5", "spec", "schematic", "id")
	assertNestedString(t, workerAssets, "sha256:89997784056d862a59c2a2d0f2fa78714a6c3ef76f34ef4f8d835dee829719d3", "spec", "installer", "digest")
	assertNestedString(t, workerAssets, "https://factory.talos.dev/image/be66fdc8a38c2f517f33cba0a6daa7ab97ff87d51e8ca7d2160e45911ba09cf5/v1.13.6/metal-amd64.raw.xz", "spec", "diskImage", "url")
	assertNestedString(t, workerAssets, "86a0e2cd51351096a682394d201a72ae23be34a1c063ddeeeca8dc4127cc0cd3", "spec", "diskImage", "sha256")
	assertNestedString(t, workerAssets, "2026-08-16", "spec", "diskImage", "observedAt")

	valuesPath := runfilePath("src/infrastructure/talm/values.yaml")
	values := readText(t, valuesPath)
	assertTextContains(t, values, `image: "`+controlplaneInstaller+`"`, valuesPath)
	assertTextContains(t, values, "- factory.talos.dev", valuesPath)
	assertTextContains(t, values, "systemVolumeEncryption:\n  controlplane: true\n  worker: false", valuesPath)
	assertTextContains(t, values, "workerNodeLabels: {}\nworkerNodeTaints: {}", valuesPath)

	templatePath := runfilePath("src/infrastructure/talm/templates/_helpers.tpl")
	template := readText(t, templatePath)
	for _, want := range []string{
		`"a|^/dev/mapper/luks2-r-guardian-data$|"`,
		`hasKey $systemVolumeEncryption .MachineType`,
		`index $systemVolumeEncryption .MachineType`,
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
		assertTextContains(t, nodeConfig, "image: "+controlplaneInstaller, nodePath)
		assertTextContains(t, nodeConfig, "name: STATE", nodePath)
		assertTextContains(t, nodeConfig, "name: EPHEMERAL", nodePath)
	}

	workerPath := "src/infrastructure/talm/nodes/ash-worker0.yaml"
	worker := readText(t, runfilePath(workerPath))
	for _, want := range []string{
		"type: worker",
		"206.223.228.99",
		"serial: 362510FCEFF6",
		"node-labels: guardian.dev/dedicated=wum",
		"register-with-taints: guardian.dev/dedicated=wum:NoSchedule",
		"image: " + workerInstaller,
	} {
		assertTextContains(t, worker, want, workerPath)
	}
	for _, forbidden := range []string{
		"name: STATE",
		"name: EPHEMERAL",
		"guardian.dev/openbao-static-seal",
		"kind: Layer2VIPConfig",
		"\n  nodeLabels:\n    guardian.dev/dedicated:",
		"\n  nodeTaints:\n    guardian.dev/dedicated:",
	} {
		assertTextNotContains(t, worker, forbidden, workerPath)
	}

	workerOverlayPath := "src/infrastructure/talm/nodes/ash-worker0-overlay.yaml"
	workerOverlay := readText(t, runfilePath(workerOverlayPath))
	for _, want := range []string{
		`serial: "362510FCEFF6"`,
		"hostname: ash-worker0",
		"address: 10.8.0.14/24",
		"node: ash-worker0",
		"node-labels: guardian.dev/dedicated=wum",
		"register-with-taints: guardian.dev/dedicated=wum:NoSchedule",
		"image: " + workerInstaller,
	} {
		assertTextContains(t, workerOverlay, want, workerOverlayPath)
	}
	for _, forbidden := range []string{"kind: RawVolumeConfig", "r-guardian-data"} {
		assertTextNotContains(t, workerOverlay, forbidden, workerOverlayPath)
	}
}
