package tests

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// walkYAMLFiles decodes every *.yaml under dir (recursively) and passes each
// file's documents to fn.
func walkYAMLFiles(t *testing.T, dir string, fn func(path string, docs []map[string]interface{})) {
	t.Helper()

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		fn(path, yamlDocs(t, path))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

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
	assertNestedString(t, server, "local-encrypted-retain", "dataStorage", "storageClass")
	assertNestedString(t, server, "local-encrypted-retain", "auditStorage", "storageClass")
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
		// Self-init is now the sole source of truth for steady-state OpenBao
		// config (there is no reconciling operator): Kubernetes auth, the kv
		// and transit engines, and one reader+writer policy/role pair per
		// consumer namespace, each scoped to that namespace's own kv subtree.
		`initialize "guardian_self_init"`,
		`request "enable_kubernetes_auth"`,
		`request "enable_kv_mount"`,
		`type = "kv-v2"`,
		`request "enable_transit_mount"`,
		`type = "transit"`,
		`request "write_reader_policy_external_dns"`,
		`request "write_reader_role_external_dns"`,
		`bound_service_account_names = ["secrets-reader"]`,
		`bound_service_account_names = ["secrets-writer"]`,
		`token_policies = ["guardian-reader-external-dns"]`,
		`token_policies = ["guardian-writer-external-dns"]`,
		`kv/data/guardian/guardian-mgmt/external-dns/*`,
		`request "write_reader_policy_company_site"`,
		`token_policies = ["guardian-reader-company-site"]`,
		`kv/data/guardian/guardian-mgmt/company-site/*`,
		`kv/data/guardian/guardian-mgmt/guardian-iam/*`,
		`kv/data/guardian/guardian-mgmt/tenant-guardian-prod/*`,
		`request "write_secret_importer_policy"`,
		`request "write_secret_importer_role"`,
		`bound_service_account_names = ["guardian-secret-importer"]`,
		`kv/data/guardian/guardian-mgmt/*`,
		`auth/kubernetes/role/guardian-secret-importer`,
		`sys/policies/acl/guardian-secret-importer`,
		`token_ttl = "600"`,
		`tls_disable = false`,
		`leader_api_addr = "https://guardian-openbao-active.tenant-guardian.svc:8200"`,
		`leader_tls_servername = "guardian-openbao-active.tenant-guardian.svc"`,
		`exec tail -n 0 -F /openbao/audit/audit.log`,
	} {
		assertTextContains(t, raw, want, path)
	}
	assertTextNotContains(t, raw, "tail -n+1", path)
	assertTextNotContains(t, raw, "http://guardian-openbao", path)
	assertTextNotContains(t, raw, "pki/openbao-api", path)
	// The reconciling operator is gone; self-init must not re-create the
	// operator's own bootstrap access.
	assertTextNotContains(t, raw, "write_ops_controller_role", path)
	assertTextNotContains(t, raw, "guardian-openbao-ops-controller", path)
	// The per-integration consumer model (one policy per secret path, reinit
	// per integration) is gone; access is per-namespace subtree only.
	assertTextNotContains(t, raw, `guardian-external-dns`, path)
	assertTextNotContains(t, raw, `token_policies = ["guardian-promotion"]`, path)
	assertTextNotContains(t, raw, "tenant-guardian/dns/external-dns", path)

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

	// The custom operator, its CRDs, and the hand-authored operation CRs are
	// gone: the transit engine (and every other mount/role/policy) is now
	// created by the OpenBao self-init block, asserted in
	// TestOpenBaoStaticSealTLSAndStorageConformance.
	for _, deleted := range []string{
		"src/infrastructure/deployments/guardian/system/openbao-vault-issuer.yaml",
		"src/infrastructure/operations/openbao/guardian-mgmt/mounts/transit.yaml",
		"src/infrastructure/operations/openbao/guardian-mgmt/mounts/pki-openbao-api.yaml",
		"src/infrastructure/operations/openbao/guardian-mgmt/pkiroles/openbao-api.yaml",
		"src/infrastructure/operations/openbao/guardian-mgmt/pkirootissuers/openbao-api-root-2026.yaml",
		"src/infrastructure/deployments/guardian/openbao-ops/openbao-ops-controller.yaml",
		"src/services/secrets/openbao/deploy/base/deployment.yaml",
		"src/services/secrets/openbao/api/v1alpha1/mount_types.go",
	} {
		assertPathMissing(t, runfilePath(deleted))
	}
}

func TestOpenBaoLocalStorageClassConformance(t *testing.T) {
	docs := yamlDocs(t, runfilePath("src/infrastructure/base/storage/storageclasses.yaml"))
	sc := findDoc(t, docs, "StorageClass", "local-encrypted-retain")

	assertNestedString(t, sc, "linstor.csi.linbit.com", "provisioner")
	assertNestedString(t, sc, "WaitForFirstConsumer", "volumeBindingMode")
	assertNestedString(t, sc, "Retain", "reclaimPolicy")
	assertNestedString(t, sc, "false", "parameters", "linstor.csi.linbit.com/allowRemoteVolumeAccess")
	assertNestedString(t, sc, "luks storage", "parameters", "linstor.csi.linbit.com/layerList")
	assertNestedString(t, sc, "true", "parameters", "linstor.csi.linbit.com/encryption")
}

func TestTenantGuardianPodSecurityConformance(t *testing.T) {
	for _, name := range []string{"tenant-guardian", "tenant-guardian-prod"} {
		path := runfilePath("src/infrastructure/base/app-patches/" + name + "-namespace-pod-security.yaml")
		doc := singleYAMLDoc(t, path)

		assertNestedString(t, doc, "Namespace", "kind")
		assertNestedString(t, doc, name, "metadata", "name")
		assertNestedString(t, doc, "privileged", "metadata", "labels", "pod-security.kubernetes.io/enforce")
		assertNestedString(t, doc, "latest", "metadata", "labels", "pod-security.kubernetes.io/enforce-version")
		assertNestedString(t, doc, "baseline", "metadata", "labels", "pod-security.kubernetes.io/audit")
		assertNestedString(t, doc, "latest", "metadata", "labels", "pod-security.kubernetes.io/audit-version")
		assertNestedString(t, doc, "baseline", "metadata", "labels", "pod-security.kubernetes.io/warn")
		assertNestedString(t, doc, "latest", "metadata", "labels", "pod-security.kubernetes.io/warn-version")
	}
}

func TestOpenBaoStaticSealAdmissionConformance(t *testing.T) {
	path := runfilePath("src/infrastructure/base/admission/openbao-static-seal.yaml")
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

	// With the operator gone, the OpenBao operation config lives in the
	// self-init block (created before the StatefulSet reports Ready), so the
	// dns controller depends directly on guardian-system. Its ExternalSecret
	// only goes Ready once self-init has created the kv mount and external-dns
	// role and ESO can read them — that is the functional proof the config
	// landed, standing in for the removed operation-CR health checks.
	dns := findDoc(t, docs, "Kustomization", "guardian-mgmt-dns-controller")
	assertDependsOn(t, dns, "guardian-system")
	// kstatus treats ESO CRs as trivially Current, so wait:true alone is
	// vacuous for them; the CEL expressions are what make the wait real.
	assertHealthCheckExpr(t, dns, "external-secrets.io/v1beta1", "ClusterSecretStore")
	assertHealthCheckExpr(t, dns, "external-secrets.io/v1beta1", "ExternalSecret")
}

// The declared OpenBao security inventory must stay declared. It now lives in
// the self-init block: on a cold boot this is the ONLY thing that creates the
// mounts, auth, roles, and policies (no reconciling operator re-asserts them),
// so a request silently dropped from Git would leave a fresh cluster missing
// that object with nothing to notice. Pin every required request by name.
func TestOpenBaoOperationsInventoryConformance(t *testing.T) {
	raw := readText(t, runfilePath("src/infrastructure/deployments/guardian/system/openbao-helmrelease.yaml"))
	inventory := []string{
		`request "enable_kubernetes_auth"`,
		`request "configure_kubernetes_auth"`,
		`request "tune_kubernetes_auth"`,
		`request "enable_kv_mount"`,
		`request "tune_kv_mount"`,
		`request "enable_transit_mount"`,
		`request "tune_transit_mount"`,
		// The image countersigner's standing transit-sign capability. The
		// guardian-images KEY itself is importer-created/restored from
		// custody, not a self-init request — a request here would mint fresh
		// material on every reinit and orphan existing countersignatures.
		`request "write_countersigner_policy"`,
		`request "write_countersigner_role"`,
		`request "write_secret_importer_policy"`,
		`request "write_secret_importer_role"`,
	}
	// Consumer namespaces get exactly one reader+writer policy/role set each.
	// Adding an INTEGRATION never extends this list; adding a NAMESPACE does
	// (that is the structural change that re-initializes).
	for _, scope := range []string{
		"external_dns",
		"company_site",
		"guardian_iam",
		"guardian_analytics",
		"guardian_imageops",
		"guardian_products",
		"tenant_root",
		"tenant_guardian",
		"tenant_guardian_prod",
		"postflight_runner",
	} {
		inventory = append(inventory,
			`request "write_reader_policy_`+scope+`"`,
			`request "write_reader_role_`+scope+`"`,
			`request "write_writer_policy_`+scope+`"`,
			`request "write_writer_role_`+scope+`"`,
		)
	}
	for _, want := range inventory {
		assertTextContains(t, raw, want, "openbao self-init inventory")
	}
}

// The write asymmetry is load-bearing: docs/secrets.md "Permission model"
// has the platform-agent minting secrets-writer tokens headlessly, so the
// moment a guardian-writer policy carries "read" that identity can read
// every namespace's secret subtree and escalate to cluster-admin through
// the platform-admin password. The docs call the agent "structurally
// unable to read" a secret; this is what makes that true. Each writer
// subtree must grant exactly create+update on kv/data and kv/metadata,
// never read; the reader pair is the only read path.
func TestOpenBaoWriterPoliciesAreWriteOnlyConformance(t *testing.T) {
	raw := readText(t, runfilePath("src/infrastructure/deployments/guardian/system/openbao-helmrelease.yaml"))
	for _, ns := range []string{
		"external-dns",
		"company-site",
		"guardian-iam",
		"guardian-analytics",
		"guardian-imageops",
		"guardian-products",
		"tenant-root",
		"tenant-guardian",
		"tenant-guardian-prod",
		"postflight-runner",
	} {
		for _, kvType := range []string{"data", "metadata"} {
			prefix := `kv/` + kvType + `/guardian/guardian-mgmt/` + ns + `/*\" {\n  capabilities = `
			assertTextContains(t, raw, prefix+`[\"create\", \"update\"]`, "writer policy "+ns)
			assertTextNotContains(t, raw, prefix+`[\"create\", \"read\", \"update\"]`, "writer policy "+ns)
		}
	}
}

// Every Flux Kustomization must source from the substitutable placeholders:
// dark-uplink bring-up flips all sources to the mirror-served OCIRepository
// via the guardian-source ConfigMap, and a literal sourceRef anywhere would
// silently keep pulling from GitHub (or fail dark). The overlays under
// bootstrap/sync-{steady,dark} resolve the placeholders for the one kubectl
// apply that bootstraps Flux.
func TestFluxSourceParameterizationConformance(t *testing.T) {
	const wantKind = "${GUARDIAN_SOURCE_KIND:=GitRepository}"
	const wantName = "${GUARDIAN_SOURCE_NAME:=guardian}"

	for _, path := range []string{
		"src/infrastructure/base/flux/sync.yaml",
	} {
		for _, doc := range yamlDocs(t, runfilePath(path)) {
			if stringValue(doc["kind"]) != "Kustomization" {
				continue
			}
			name := stringValue(mapValue(doc["metadata"])["name"])
			sourceRef := mapValue(nestedValue(t, doc, "spec", "sourceRef"))
			if stringValue(sourceRef["kind"]) != wantKind || stringValue(sourceRef["name"]) != wantName {
				t.Fatalf("%s Kustomization %s sourceRef = %v; every source must go through the GUARDIAN_SOURCE placeholders", path, name, sourceRef)
			}
			substituted := false
			for _, from := range sliceValue(nestedValue(t, doc, "spec", "postBuild", "substituteFrom")) {
				entry := mapValue(from)
				if stringValue(entry["kind"]) != "ConfigMap" || stringValue(entry["name"]) != "guardian-source" {
					continue
				}
				// Required, not optional: an absent (optional) ConfigMap yields
				// an empty variable map, and Flux then skips substitution and
				// applies the placeholders literally. The bootstrap overlays
				// always create it.
				if entry["optional"] == true {
					t.Fatalf("%s Kustomization %s marks guardian-source substituteFrom optional; an empty variable map makes Flux skip substitution and the CRD rejects the literal placeholders", path, name)
				}
				substituted = true
			}
			if !substituted {
				t.Fatalf("%s Kustomization %s lacks the guardian-source substituteFrom; Flux would apply the placeholders literally and the CRD enum would reject them", path, name)
			}
		}
	}

	// Both bootstrap overlays must ship the guardian-source ConfigMap so the
	// variable map is never empty; steady points it at the GitRepository.
	steadyCM := singleYAMLDoc(t, runfilePath("src/infrastructure/bootstrap/sync-steady/guardian-source-configmap.yaml"))
	steadyData := mapValue(steadyCM["data"])
	if stringValue(steadyData["GUARDIAN_SOURCE_KIND"]) != "GitRepository" || stringValue(steadyData["GUARDIAN_SOURCE_NAME"]) != "guardian" {
		t.Fatalf("steady guardian-source ConfigMap = %v, want GitRepository/guardian", steadyData)
	}

	configMap := singleYAMLDoc(t, runfilePath("src/infrastructure/bootstrap/sync-dark/guardian-source-configmap.yaml"))
	ociRepo := singleYAMLDoc(t, runfilePath("src/infrastructure/bootstrap/sync-dark/oci-repository.yaml"))
	data := mapValue(configMap["data"])
	if stringValue(data["GUARDIAN_SOURCE_KIND"]) != "OCIRepository" || stringValue(data["GUARDIAN_SOURCE_NAME"]) != stringValue(mapValue(ociRepo["metadata"])["name"]) {
		t.Fatalf("dark guardian-source ConfigMap %v does not point at the declared OCIRepository %v", data, mapValue(ociRepo["metadata"])["name"])
	}
	if nestedValue(t, ociRepo, "spec", "insecure") != true {
		t.Fatalf("dark OCIRepository must set insecure: true (the haul mirror is plain HTTP)")
	}
	if !strings.HasPrefix(stringValue(nestedValue(t, ociRepo, "spec", "url")), "oci://148.113.198.223:5000/") {
		t.Fatalf("dark OCIRepository url = %v, want the haul mirror endpoint", nestedValue(t, ociRepo, "spec", "url"))
	}

	for path, wantValue := range map[string]string{
		"src/infrastructure/bootstrap/sync-steady/kustomization.yaml": "value: GitRepository",
		"src/infrastructure/bootstrap/sync-dark/kustomization.yaml":   "value: OCIRepository",
	} {
		raw := readText(t, runfilePath(path))
		for _, want := range []string{"kind: Kustomization", "op: replace", "path: /spec/sourceRef/kind", wantValue} {
			assertTextContains(t, raw, want, path)
		}
	}
}

// Flux postBuild variable substitution runs envsubst over every manifest a
// Kustomization applies, blanking any unescaped ${...} it cannot resolve —
// including shell parameter expansions like ${var#prefix}. The only vars we
// declare are GUARDIAN_SOURCE_*; anything else under a substituted path must
// either escape as $${...} or carry kustomize.toolkit.fluxcd.io/substitute:
// disabled. This is the guard that would have caught the OpenBao
// tls-reloader script being blanked.
func TestFluxSubstitutionSafetyConformance(t *testing.T) {
	// Paths reconciled by a Kustomization that declares postBuild
	// substitution.
	roots := []string{
		"src/infrastructure/base",
		"src/infrastructure/deployments/alerting",
		"src/infrastructure/deployments/analytics/system",
		"src/infrastructure/deployments/authorization/prod",
		"src/infrastructure/deployments/guardian/system",
		"src/infrastructure/deployments/iam/prod",
		"src/infrastructure/deployments/products",
		"src/infrastructure/deployments/postflight-runner",
	}
	// ${GUARDIAN_SOURCE_KIND...} / ${GUARDIAN_SOURCE_NAME...} are the declared
	// vars; $${ is an escaped literal. Anything else is unhandled collateral.
	unresolved := regexp.MustCompile(`([^$]|^)\$\{[^}]*\}`)
	allowed := regexp.MustCompile(`\$\{GUARDIAN_SOURCE_(KIND|NAME)`)

	for _, root := range roots {
		dir := filepath.Dir(runfilePath(root + "/kustomization.yaml"))
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			dir = filepath.Join(repoRootFromRunfiles(t), root)
		} else if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		walkYAMLFiles(t, dir, func(path string, docs []map[string]interface{}) {
			for _, doc := range docs {
				annotations := mapValue(mapValue(doc["metadata"])["annotations"])
				if stringValue(annotations["kustomize.toolkit.fluxcd.io/substitute"]) == "disabled" {
					continue
				}
				raw, err := yaml.Marshal(doc)
				if err != nil {
					t.Fatalf("re-marshal doc in %s: %v", path, err)
				}
				for _, match := range unresolved.FindAllString(string(raw), -1) {
					if allowed.MatchString(match) {
						continue
					}
					t.Fatalf("%s: unescaped %q under a Flux-substituted path would be blanked by envsubst; escape as $${...} or annotate the object kustomize.toolkit.fluxcd.io/substitute: disabled", path, strings.TrimSpace(match))
				}
			}
		})
	}
}

func TestOpenBaoConsumersUseTLSConformance(t *testing.T) {
	dns := readText(t, runfilePath("src/infrastructure/base/dns/secrets.yaml"))
	for _, want := range []string{
		"kind: ClusterSecretStore",
		"name: external-dns-openbao",
		"server: https://guardian-openbao.tenant-guardian.svc:8200",
		"caProvider:",
		"name: guardian-openbao-api-tls",
		"namespace: tenant-guardian",
		"kind: ClusterSecretStore",
		"role: guardian-reader-external-dns",
	} {
		assertTextContains(t, dns, want, "external-dns SecretStore")
	}
	assertTextNotContains(t, dns, "server: http://guardian-openbao", "external-dns SecretStore")

	promotion := readText(t, runfilePath("src/infrastructure/deployments/guardian/promotion/pipelines/secrets.yaml"))
	for _, want := range []string{
		"kind: ClusterSecretStore",
		"name: promotion-openbao",
		"server: https://guardian-openbao.tenant-guardian.svc:8200",
		"caProvider:",
		"name: guardian-openbao-api-tls",
		"role: guardian-reader-company-site",
		"kargo.akuity.io/cred-type: git",
	} {
		assertTextContains(t, promotion, want, "promotion SecretStore")
	}
	assertTextNotContains(t, promotion, "server: http://guardian-openbao", "promotion SecretStore")
	// Only the App private key is OpenBao-backed; a plaintext key in Git or a
	// value-bearing template would defeat the custody model. "PRIVATE KEY"
	// matches every PEM label (PKCS#1, PKCS#8, OpenSSH).
	assertTextNotContains(t, promotion, "PRIVATE KEY", "promotion SecretStore")
}

// Every ExternalSecret must read exclusively from its own namespace's kv
// subtree (guardian/guardian-mgmt/<namespace>/...), and every vault-backed
// (Cluster)SecretStore must authenticate as that namespace's secrets-reader
// SA through its guardian-reader-<namespace> role. The per-namespace OpenBao
// policies enforce this at runtime; this test enforces it at review time, so
// a gamma manifest referencing a prod path (or vice versa) fails CI instead
// of failing — or worse, succeeding — live.
func TestOpenBaoSecretScopeConformance(t *testing.T) {
	root := repoRootFromRunfiles(t)

	externalSecrets, stores := 0, 0
	walk := func(path string, docs []map[string]interface{}) {
		for _, doc := range docs {
			switch stringValue(doc["kind"]) {
			case "ExternalSecret":
				externalSecrets++
				metadata := mapValue(doc["metadata"])
				namespace := stringValue(metadata["namespace"])
				name := stringValue(metadata["name"])
				if namespace == "" {
					t.Fatalf("%s: ExternalSecret %s declares no namespace; scope enforcement needs an explicit one", path, name)
				}
				wantPrefix := "guardian/guardian-mgmt/" + namespace + "/"
				var keys []string
				generatorSources := 0
				for _, item := range sliceValue(mapValue(doc["spec"])["data"]) {
					remoteRef := mapValue(mapValue(item)["remoteRef"])
					if key := stringValue(remoteRef["key"]); key != "" {
						keys = append(keys, key)
					}
				}
				for _, item := range sliceValue(mapValue(doc["spec"])["dataFrom"]) {
					dataFrom := mapValue(item)
					if key := stringValue(mapValue(dataFrom["extract"])["key"]); key != "" {
						keys = append(keys, key)
					}
					generatorRef := mapValue(mapValue(dataFrom["sourceRef"])["generatorRef"])
					if stringValue(generatorRef["kind"]) != "" && stringValue(generatorRef["name"]) != "" {
						generatorSources++
					}
				}
				if len(keys) == 0 && generatorSources == 0 {
					t.Fatalf("%s: ExternalSecret %s/%s declares neither remoteRef keys nor a generator source", path, namespace, name)
				}
				for _, key := range keys {
					if !strings.HasPrefix(key, wantPrefix) {
						t.Fatalf("%s: ExternalSecret %s/%s reads %q; every remoteRef must stay inside %q (its own namespace's subtree)", path, namespace, name, key, wantPrefix)
					}
				}
			case "ClusterSecretStore", "SecretStore":
				vault := mapValue(mapValue(mapValue(doc["spec"])["provider"])["vault"])
				if len(vault) == 0 {
					continue
				}
				stores++
				name := stringValue(mapValue(doc["metadata"])["name"])
				kubernetes := mapValue(mapValue(vault["auth"])["kubernetes"])
				saRef := mapValue(kubernetes["serviceAccountRef"])
				saName := stringValue(saRef["name"])
				saNamespace := stringValue(saRef["namespace"])
				role := stringValue(kubernetes["role"])
				if saName != "secrets-reader" {
					t.Fatalf("%s: SecretStore %s authenticates as SA %q; stores must use the namespace's dedicated secrets-reader SA", path, name, saName)
				}
				if saNamespace == "" {
					t.Fatalf("%s: SecretStore %s has no serviceAccountRef namespace", path, name)
				}
				if want := "guardian-reader-" + saNamespace; role != want {
					t.Fatalf("%s: SecretStore %s uses role %q with SA namespace %q; want role %q so the store can only read its own namespace's subtree", path, name, role, saNamespace, want)
				}
			}
		}
	}
	// base + deployments cover every Flux-applied manifest; talm/ holds Go
	// templates that are not plain YAML and carries no ESO objects.
	walkYAMLFiles(t, filepath.Join(root, "src/infrastructure/base"), walk)
	walkYAMLFiles(t, filepath.Join(root, "src/infrastructure/deployments"), walk)
	if externalSecrets == 0 || stores == 0 {
		t.Fatalf("scope conformance walked %d ExternalSecrets and %d vault SecretStores; the walk roots or data deps are wrong", externalSecrets, stores)
	}
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
