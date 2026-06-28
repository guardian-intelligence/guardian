package tests

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const openBaoStaticSealKeyID = "4dd033bda750c5e2e9718a6b571ba787a46443a412c2713727b7ab592001380d"

func TestOpenBaoStaticSealTLSAndStorageConformance(t *testing.T) {
	path := runfilePath("src/infrastructure/deployments/guardian/system/openbao-helmrelease.yaml")
	raw := readText(t, path)
	doc := singleYAMLDoc(t, path)
	values := nestedMap(t, doc, "spec", "values", "openbao")
	server := nestedMap(t, values, "server")

	assertNestedBool(t, values, false, "global", "tlsDisable")
	assertNestedString(t, server, "OrderedReady", "podManagementPolicy")
	assertNestedString(t, server, "OnDelete", "updateStrategyType")
	assertNestedBool(t, server, true, "shareProcessNamespace")
	assertNestedString(t, server, "openbao-local", "dataStorage", "storageClass")
	assertNestedString(t, server, "openbao-local", "auditStorage", "storageClass")
	assertNestedString(t, server, "true", "nodeSelector", "guardian.dev/openbao-static-seal")
	assertNestedString(t, server, "https://guardian-openbao-active.tenant-guardian.svc:8200", "ha", "apiAddr")
	assertNestedBool(t, doc, true, "spec", "upgrade", "disableWait")
	assertNestedInt(t, doc, 0, "spec", "upgrade", "remediation", "retries")

	wantFile := "file:///openbao/secrets/unseal-" + openBaoStaticSealKeyID + ".key"
	for _, want := range []string{
		`seal "static"`,
		`current_key_id = "` + openBaoStaticSealKeyID + `"`,
		`current_key = "` + wantFile + `"`,
		`test "$(sha256sum "$key" | awk '{print $1}')" = "` + openBaoStaticSealKeyID + `"`,
			`initialize "guardian_self_init"`,
			`request "write_ops_controller_role"`,
			`path \"sys/auth\"`,
			`path \"auth/kubernetes/config\"`,
			`path \"sys/mounts\"`,
			`request "write_secret_importer_policy"`,
			`request "write_secret_importer_role"`,
			`kv/data/guardian/guardian-mgmt/tenant-guardian/dns/external-dns`,
			`auth/kubernetes/role/guardian-secret-importer`,
			`sys/policies/acl/guardian-secret-importer`,
			`token_ttl = "600"`,
			`tls_disable = false`,
			`leader_api_addr = "https://guardian-openbao-active.tenant-guardian.svc:8200"`,
			`leader_tls_servername = "guardian-openbao-active.tenant-guardian.svc"`,
		} {
		assertTextContains(t, raw, want, path)
	}
	assertTextNotContains(t, raw, "http://guardian-openbao", path)

	pki := readText(t, runfilePath("src/infrastructure/deployments/guardian/system/openbao-pki.yaml"))
	assertTextContains(t, pki, "guardian-openbao-active.tenant-guardian.svc.cozy.local", "openbao PKI manifest")
	assertTextContains(t, pki, "guardian-openbao-0.guardian-openbao-internal.tenant-guardian.svc.cozy.local", "openbao PKI manifest")
}

func TestOpenBaoLocalStorageClassConformance(t *testing.T) {
	docs := yamlDocs(t, runfilePath("src/infrastructure/base/storage/storageclasses.yaml"))
	sc := findDoc(t, docs, "StorageClass", "openbao-local")

	assertNestedString(t, sc, "linstor.csi.linbit.com", "provisioner")
	assertNestedString(t, sc, "WaitForFirstConsumer", "volumeBindingMode")
	assertNestedString(t, sc, "Retain", "reclaimPolicy")
	assertNestedString(t, sc, "false", "parameters", "linstor.csi.linbit.com/allowRemoteVolumeAccess")
	assertNestedString(t, sc, "storage", "parameters", "linstor.csi.linbit.com/layerList")
}

func TestOpenBaoTenantAllowsStaticSealHostPathConformance(t *testing.T) {
	path := runfilePath("src/infrastructure/base/app-patches/tenant-guardian-namespace-pod-security.yaml")
	raw := readText(t, path)
	doc := singleYAMLDoc(t, path)

	assertNestedString(t, doc, "tenant-guardian", "metadata", "name")
	assertNestedString(t, doc, "tenant-root", "metadata", "namespace")
	for _, want := range []string{
		"kind: Namespace",
		"name: tenant-guardian",
		"path: /metadata/labels/pod-security.kubernetes.io~1enforce",
		"value: privileged",
		"path: /metadata/labels/pod-security.kubernetes.io~1audit",
		"value: baseline",
		"path: /metadata/labels/pod-security.kubernetes.io~1warn",
	} {
		assertTextContains(t, raw, want, path)
	}
}

func TestOpenBaoFluxOrderingConformance(t *testing.T) {
	docs := yamlDocs(t, runfilePath("src/infrastructure/base/flux/sync.yaml"))

	system := findDoc(t, docs, "Kustomization", "guardian-system")
	assertNestedBool(t, system, false, "spec", "wait")
	assertHealthCheck(t, system, "cert-manager.io/v1", "Certificate", "guardian-openbao-ca", "tenant-guardian")
	assertHealthCheck(t, system, "cert-manager.io/v1", "Certificate", "guardian-openbao-api", "tenant-guardian")
	assertHealthCheck(t, system, "helm.toolkit.fluxcd.io/v2", "HelmRelease", "guardian-openbao", "tenant-guardian")
	assertHealthCheck(t, system, "apps/v1", "StatefulSet", "guardian-openbao", "tenant-guardian")

	ops := findDoc(t, docs, "Kustomization", "guardian-openbao-ops")
	assertNestedBool(t, ops, true, "spec", "wait")
	assertDependsOn(t, ops, "guardian-system")

	dns := findDoc(t, docs, "Kustomization", "guardian-mgmt-dns-controller")
	assertDependsOn(t, dns, "guardian-openbao-ops")

	opsDocs := yamlDocs(t, runfilePath("src/infrastructure/deployments/guardian/openbao-ops/openbao-ops-controller.yaml"))
	crds := findDoc(t, opsDocs, "Kustomization", "guardian-openbao-ops-crds")
	assertDependsOn(t, crds, "guardian-system")

	controller := findDoc(t, opsDocs, "Kustomization", "guardian-openbao-ops-controller")
	assertNestedBool(t, controller, true, "spec", "wait")
	assertDependsOn(t, controller, "guardian-openbao-ops-crds")
	assertDependsOn(t, controller, "guardian-system")

	state := findDoc(t, opsDocs, "Kustomization", "guardian-openbao-ops-state")
	assertNestedBool(t, state, true, "spec", "wait")
	assertDependsOn(t, state, "guardian-openbao-ops-controller")
}

func TestOpenBaoConsumersUseTLSConformance(t *testing.T) {
	deployment := readText(t, runfilePath("src/services/secrets/openbao/deploy/base/deployment.yaml"))
	for _, want := range []string{
		"value: https://guardian-openbao-active:8200",
		"name: BAO_CACERT",
		"name: OPENBAO_KUBERNETES_JWT_PATH",
		"value: /var/run/secrets/openbao/token",
		"secretName: guardian-openbao-api-tls",
		"name: openbao-auth-token",
		"audience: openbao",
	} {
		assertTextContains(t, deployment, want, "ops-controller deployment")
	}
	assertTextNotContains(t, deployment, "value: http://guardian-openbao", "ops-controller deployment")

	dns := readText(t, runfilePath("src/infrastructure/base/dns/secrets.yaml"))
	for _, want := range []string{
		"kind: ClusterSecretStore",
		"name: external-dns-openbao",
		"server: https://guardian-openbao.tenant-guardian.svc:8200",
		"caProvider:",
		"name: guardian-openbao-api-tls",
		"namespace: tenant-guardian",
		"kind: ClusterSecretStore",
	} {
		assertTextContains(t, dns, want, "external-dns SecretStore")
	}
	assertTextNotContains(t, dns, "server: http://guardian-openbao", "external-dns SecretStore")
}

func readText(t *testing.T, path string) string {
	t.Helper()

	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(payload)
}

func singleYAMLDoc(t *testing.T, path string) map[string]interface{} {
	t.Helper()

	docs := yamlDocs(t, path)
	if len(docs) != 1 {
		t.Fatalf("%s: decoded %d YAML documents, want 1", path, len(docs))
	}
	return docs[0]
}

func yamlDocs(t *testing.T, path string) []map[string]interface{} {
	t.Helper()

	payload, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(payload))
	var docs []map[string]interface{}
	for {
		var doc map[string]interface{}
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if len(doc) > 0 {
			docs = append(docs, doc)
		}
	}
	return docs
}

func findDoc(t *testing.T, docs []map[string]interface{}, kind, name string) map[string]interface{} {
	t.Helper()

	for _, doc := range docs {
		if stringValue(doc["kind"]) != kind {
			continue
		}
		metadata := mapValue(doc["metadata"])
		if stringValue(metadata["name"]) == name {
			return doc
		}
	}
	t.Fatalf("did not find %s/%s", kind, name)
	return nil
}

func assertDependsOn(t *testing.T, doc map[string]interface{}, want string) {
	t.Helper()

	for _, dep := range sliceValue(nestedValue(t, doc, "spec", "dependsOn")) {
		if stringValue(mapValue(dep)["name"]) == want {
			return
		}
	}
	t.Fatalf("%s/%s missing dependsOn %q", stringValue(doc["kind"]), stringValue(mapValue(doc["metadata"])["name"]), want)
}

func assertHealthCheck(t *testing.T, doc map[string]interface{}, apiVersion, kind, name, namespace string) {
	t.Helper()

	for _, healthCheck := range sliceValue(nestedValue(t, doc, "spec", "healthChecks")) {
		check := mapValue(healthCheck)
		if stringValue(check["apiVersion"]) == apiVersion &&
			stringValue(check["kind"]) == kind &&
			stringValue(check["name"]) == name &&
			stringValue(check["namespace"]) == namespace {
			return
		}
	}
	t.Fatalf("%s/%s missing healthCheck %s %s/%s in %s", stringValue(doc["kind"]), stringValue(mapValue(doc["metadata"])["name"]), apiVersion, kind, name, namespace)
}

func assertNestedString(t *testing.T, m map[string]interface{}, want string, path ...string) {
	t.Helper()

	if got := stringValue(nestedValue(t, m, path...)); got != want {
		t.Fatalf("%s = %q, want %q", strings.Join(path, "."), got, want)
	}
}

func assertNestedBool(t *testing.T, m map[string]interface{}, want bool, path ...string) {
	t.Helper()

	got, ok := nestedValue(t, m, path...).(bool)
	if !ok {
		t.Fatalf("%s is not a bool", strings.Join(path, "."))
	}
	if got != want {
		t.Fatalf("%s = %v, want %v", strings.Join(path, "."), got, want)
	}
}

func assertNestedInt(t *testing.T, m map[string]interface{}, want int, path ...string) {
	t.Helper()

	got, ok := nestedValue(t, m, path...).(int)
	if !ok {
		t.Fatalf("%s is not an int", strings.Join(path, "."))
	}
	if got != want {
		t.Fatalf("%s = %v, want %v", strings.Join(path, "."), got, want)
	}
}

func nestedMap(t *testing.T, m map[string]interface{}, path ...string) map[string]interface{} {
	t.Helper()
	return mapValue(nestedValue(t, m, path...))
}

func nestedValue(t *testing.T, m map[string]interface{}, path ...string) interface{} {
	t.Helper()

	var current interface{} = m
	for _, segment := range path {
		next, ok := mapValue(current)[segment]
		if !ok {
			t.Fatalf("missing %s in %s", segment, strings.Join(path, "."))
		}
		current = next
	}
	return current
}

func mapValue(value interface{}) map[string]interface{} {
	if value == nil {
		return map[string]interface{}{}
	}
	m, ok := value.(map[string]interface{})
	if ok {
		return m
	}
	return map[string]interface{}{}
}

func sliceValue(value interface{}) []interface{} {
	if value == nil {
		return nil
	}
	s, ok := value.([]interface{})
	if ok {
		return s
	}
	return nil
}

func stringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	s, ok := value.(string)
	if ok {
		return s
	}
	return ""
}
