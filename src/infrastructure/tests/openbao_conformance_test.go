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

const openBaoStaticSealKeyID = "d1bad73a1cc200c277ef24d23231d99ff6b424d4d4e397bc08f285a6767af013"

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
	assertToleration(t, server, "guardian.dev/openbao-static-seal", "Exists", "NoSchedule")
	assertHostPathVolume(t, server, "openbao-static-seal", "/var/lib/guardian/openbao/static-seal", "DirectoryOrCreate")
	assertNestedString(t, server, "https://guardian-openbao-active.tenant-guardian.svc:8200", "ha", "apiAddr")
	assertNestedBool(t, doc, true, "spec", "upgrade", "disableWait")
	assertNestedInt(t, doc, 0, "spec", "upgrade", "remediation", "retries")

	wantFile := "file:///openbao/secrets/unseal-" + openBaoStaticSealKeyID + ".key"
	for _, want := range []string{
		`seal "static"`,
		`current_key_id = "` + openBaoStaticSealKeyID + `"`,
		`current_key = "` + wantFile + `"`,
		`dir_mode="$(stat -c '%a' /openbao/secrets)"`,
		`case "$dir_mode" in 700|750)`,
		`key_mode="$(stat -c '%a' "$key")"`,
		`case "$key_mode" in 400|440|600|640)`,
		`test "$(sha256sum "$key" | awk '{print $1}')" = "` + openBaoStaticSealKeyID + `"`,
		`initialize "guardian_self_init"`,
		`request "write_ops_controller_role"`,
		`path \"sys/auth\"`,
		`path \"auth/kubernetes/config\"`,
		`path \"sys/mounts\"`,
		`path \"sys/mounts/transit\"`,
		`path \"sys/mounts/transit/tune\"`,
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
	assertTextNotContains(t, raw, "pki/openbao-api", path)

	listener := readText(t, runfilePath("src/infrastructure/deployments/guardian/system/openbao-listener-tls.yaml"))
	assertTextContains(t, listener, "guardian-openbao-listener-ca", "openbao listener TLS manifest")
	assertTextContains(t, listener, "secretName: guardian-openbao-api-tls", "openbao listener TLS manifest")
	assertTextContains(t, listener, "guardian-openbao-active.tenant-guardian.svc.cozy.local", "openbao listener TLS manifest")
	assertTextContains(t, listener, "guardian-openbao-0.guardian-openbao-internal.tenant-guardian.svc.cozy.local", "openbao listener TLS manifest")
}

func TestOpenBaoListenerTLSAndTransitConformance(t *testing.T) {
	listenerPath := runfilePath("src/infrastructure/deployments/guardian/system/openbao-listener-tls.yaml")
	listenerDocs := yamlDocs(t, listenerPath)
	findDoc(t, listenerDocs, "Issuer", "guardian-openbao-listener-selfsigned")
	ca := findDoc(t, listenerDocs, "Certificate", "guardian-openbao-listener-ca")
	assertNestedString(t, ca, "guardian-openbao-listener-ca-tls", "spec", "secretName")
	assertNestedBool(t, ca, true, "spec", "isCA")
	issuer := findDoc(t, listenerDocs, "Issuer", "guardian-openbao-listener-ca")
	assertNestedString(t, issuer, "guardian-openbao-listener-ca-tls", "spec", "ca", "secretName")
	leaf := findDoc(t, listenerDocs, "Certificate", "guardian-openbao-api")
	assertNestedString(t, leaf, "guardian-openbao-api-tls", "spec", "secretName")
	assertNestedString(t, leaf, "guardian-openbao-listener-ca", "spec", "issuerRef", "name")

	networkPolicy := readText(t, runfilePath("src/infrastructure/deployments/guardian/system/openbao-networkpolicy.yaml"))
	assertTextNotContains(t, networkPolicy, "allow-cert-manager-to-openbao", "openbao network policy")
	assertTextNotContains(t, networkPolicy, "cozy-cert-manager", "openbao network policy")

	transitPath := runfilePath("src/infrastructure/operations/openbao/guardian-mgmt/mounts/transit.yaml")
	transit := singleYAMLDoc(t, transitPath)
	assertNestedString(t, transit, "OpenBaoMount", "kind")
	assertNestedString(t, transit, "transit", "spec", "path")
	assertNestedString(t, transit, "transit", "spec", "type")

	for _, deleted := range []string{
		"src/infrastructure/deployments/guardian/system/openbao-vault-issuer.yaml",
		"src/infrastructure/operations/openbao/guardian-mgmt/mounts/pki-openbao-api.yaml",
		"src/infrastructure/operations/openbao/guardian-mgmt/pkiroles/openbao-api.yaml",
		"src/infrastructure/operations/openbao/guardian-mgmt/pkirootissuers/openbao-api-root-2026.yaml",
	} {
		assertPathMissing(t, runfilePath(deleted))
	}
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

func TestOpenBaoStaticSealAdmissionConformance(t *testing.T) {
	path := runfilePath("src/infrastructure/base/app-patches/openbao-static-seal-admission.yaml")
	raw := readText(t, path)
	docs := yamlDocs(t, path)
	policy := findDoc(t, docs, "ValidatingAdmissionPolicy", "guardian-openbao-static-seal-hostpath")
	binding := findDoc(t, docs, "ValidatingAdmissionPolicyBinding", "guardian-openbao-static-seal-hostpath")

	assertNestedString(t, policy, "Fail", "spec", "failurePolicy")
	assertNestedString(t, binding, "guardian-openbao-static-seal-hostpath", "spec", "policyName")
	for _, want := range []string{
		`resources:`,
		`- pods`,
		`/var/lib/guardian/openbao/static-seal`,
		`startsWith(volume.hostPath.path + "/")`,
		`object.metadata.namespace == "tenant-guardian"`,
		`object.metadata.labels["app.kubernetes.io/name"] == "openbao"`,
		`object.metadata.labels["app.kubernetes.io/instance"] == "guardian-openbao"`,
		`object.metadata.labels["component"] == "server"`,
		`privileged containers are not allowed in tenant-guardian`,
		`validationActions:`,
		`- Deny`,
	} {
		assertTextContains(t, raw, want, path)
	}
}

func TestOpenBaoStaticSealDocsConformance(t *testing.T) {
	for _, path := range []string{
		runfilePath("docs/openbao-design.md"),
		runfilePath("src/infrastructure/runbooks/openbao-static-seal-self-init.md"),
	} {
		raw := readText(t, path)
		assertTextContains(t, raw, "Node/root compromise", path)
		for _, forbidden := range []string{
			"~/.guardian",
			"TPM-sealed",
			"Secure Boot",
			"secure boot",
			"generated under",
		} {
			assertTextNotContains(t, raw, forbidden, path)
		}
	}
}

func TestOpenBaoFluxOrderingConformance(t *testing.T) {
	docs := yamlDocs(t, runfilePath("src/infrastructure/base/flux/sync.yaml"))

	system := findDoc(t, docs, "Kustomization", "guardian-system")
	assertNestedBool(t, system, false, "spec", "wait")
	// Certificate Ready latches, so the issuing controller's liveness must be
	// gated explicitly; without this a dead cert-manager converges green
	// until a leaf misses renewal.
	assertHealthCheck(t, system, "apps/v1", "Deployment", "cert-manager", "cozy-cert-manager")
	assertHealthCheck(t, system, "cert-manager.io/v1", "Certificate", "guardian-openbao-listener-ca", "tenant-guardian")
	assertHealthCheck(t, system, "cert-manager.io/v1", "Certificate", "guardian-openbao-api", "tenant-guardian")
	assertHealthCheck(t, system, "helm.toolkit.fluxcd.io/v2", "HelmRelease", "guardian-openbao", "tenant-guardian")
	assertHealthCheck(t, system, "apps/v1", "StatefulSet", "guardian-openbao", "tenant-guardian")

	ops := findDoc(t, docs, "Kustomization", "guardian-openbao-ops")
	assertNestedBool(t, ops, true, "spec", "wait")
	assertDependsOn(t, ops, "guardian-system")

	dns := findDoc(t, docs, "Kustomization", "guardian-mgmt-dns-controller")
	assertDependsOn(t, dns, "guardian-openbao-ops")
	// kstatus treats ESO CRs as trivially Current, so wait:true alone is
	// vacuous for them; the CEL expressions are what make the wait real.
	assertHealthCheckExpr(t, dns, "external-secrets.io/v1beta1", "ClusterSecretStore")
	assertHealthCheckExpr(t, dns, "external-secrets.io/v1beta1", "ExternalSecret")

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
	// Same kstatus vacuity applies to the OpenBao operation CRs: without these
	// expressions the state slice reports Ready before the ops-controller has
	// authenticated, applied, and drift-checked each CR. OpenBaoAuditDevice is
	// deliberately absent: no reconciler writes its status yet, and a guard
	// expression over a never-written status wedges the slice at timeout.
	for _, kind := range []string{
		"OpenBaoAuthBackend",
		"OpenBaoKubernetesAuthRole",
		"OpenBaoMount",
		"OpenBaoMountTune",
		"OpenBaoPolicy",
	} {
		assertHealthCheckExpr(t, state, "openbao.guardian.dev/v1alpha1", kind)
	}
	assertNoHealthCheckExpr(t, state, "openbao.guardian.dev/v1alpha1", "OpenBaoAuditDevice")
}

// The declared OpenBao security inventory must stay declared. Flux health
// expressions assess only objects present in the inventory, so a CR
// accidentally deleted from Git would be pruned and converge green; this
// pins existence at the source instead.
func TestOpenBaoOperationsInventoryConformance(t *testing.T) {
	dir := "src/infrastructure/operations/openbao/guardian-mgmt"
	kustomization := singleYAMLDoc(t, runfilePath(dir+"/kustomization.yaml"))
	declared := map[string]bool{}
	for _, resource := range sliceValue(nestedValue(t, kustomization, "resources")) {
		for _, doc := range yamlDocs(t, runfilePath(dir+"/"+stringValue(resource))) {
			declared[stringValue(doc["kind"])+"/"+stringValue(mapValue(doc["metadata"])["name"])] = true
		}
	}
	for _, want := range []string{
		"OpenBaoAuthBackend/kubernetes",
		"OpenBaoKubernetesAuthRole/external-dns",
		"OpenBaoKubernetesAuthRole/ops-controller",
		"OpenBaoMount/kv",
		"OpenBaoMount/transit",
		"OpenBaoMountTune/kv",
		"OpenBaoPolicy/external-dns",
		"OpenBaoPolicy/ops-controller",
	} {
		if !declared[want] {
			t.Fatalf("operations inventory is missing %s; removing a declared OpenBao security object must be deliberate", want)
		}
	}
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

func assertPathMissing(t *testing.T, path string) {
	t.Helper()

	_, err := os.Stat(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	t.Fatalf("%s exists; expected it to be removed", path)
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

func assertHealthCheckExpr(t *testing.T, doc map[string]interface{}, apiVersion, kind string) {
	t.Helper()

	for _, healthCheckExpr := range sliceValue(nestedValue(t, doc, "spec", "healthCheckExprs")) {
		expr := mapValue(healthCheckExpr)
		if stringValue(expr["apiVersion"]) != apiVersion || stringValue(expr["kind"]) != kind {
			continue
		}
		current := stringValue(expr["current"])
		failed := stringValue(expr["failed"])
		if !strings.Contains(current, "'Ready'") || !strings.Contains(current, "'True'") {
			t.Fatalf("%s healthCheckExpr for %s: current = %q, want a Ready==True condition check", stringValue(mapValue(doc["metadata"])["name"]), kind, current)
		}
		if !strings.Contains(failed, "'Ready'") || !strings.Contains(failed, "'False'") {
			t.Fatalf("%s healthCheckExpr for %s: failed = %q, want a Ready==False condition check", stringValue(mapValue(doc["metadata"])["name"]), kind, failed)
		}
		return
	}
	t.Fatalf("%s/%s missing healthCheckExpr for %s %s", stringValue(doc["kind"]), stringValue(mapValue(doc["metadata"])["name"]), apiVersion, kind)
}

func assertNoHealthCheckExpr(t *testing.T, doc map[string]interface{}, apiVersion, kind string) {
	t.Helper()

	for _, healthCheckExpr := range sliceValue(nestedValue(t, doc, "spec", "healthCheckExprs")) {
		expr := mapValue(healthCheckExpr)
		if stringValue(expr["apiVersion"]) == apiVersion && stringValue(expr["kind"]) == kind {
			t.Fatalf("%s declares a healthCheckExpr for %s %s, which has no reconciler writing status and would wedge the slice at timeout", stringValue(mapValue(doc["metadata"])["name"]), apiVersion, kind)
		}
	}
}

func assertToleration(t *testing.T, server map[string]interface{}, key, operator, effect string) {
	t.Helper()

	for _, item := range sliceValue(server["tolerations"]) {
		toleration := mapValue(item)
		if stringValue(toleration["key"]) == key &&
			stringValue(toleration["operator"]) == operator &&
			stringValue(toleration["effect"]) == effect {
			return
		}
	}
	t.Fatalf("server.tolerations missing %s/%s/%s", key, operator, effect)
}

func assertHostPathVolume(t *testing.T, server map[string]interface{}, name, path, hostPathType string) {
	t.Helper()

	for _, item := range sliceValue(server["volumes"]) {
		volume := mapValue(item)
		if stringValue(volume["name"]) != name {
			continue
		}
		hostPath := mapValue(volume["hostPath"])
		if stringValue(hostPath["path"]) != path {
			t.Fatalf("volume %s hostPath.path = %q, want %q", name, stringValue(hostPath["path"]), path)
		}
		if stringValue(hostPath["type"]) != hostPathType {
			t.Fatalf("volume %s hostPath.type = %q, want %q", name, stringValue(hostPath["type"]), hostPathType)
		}
		return
	}
	t.Fatalf("server.volumes missing hostPath volume %s", name)
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
