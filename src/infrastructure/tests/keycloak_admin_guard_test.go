package tests

import (
	"os"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// The platform Keycloak's admin console must never be publicly routable.
// Upstream Cozystack renders a single Ingress routing path / on
// keycloak.<root-host> — admin console included — with no values knob to
// restrict it; publicly serving /admin is exactly why platform OIDC was
// disabled on 2026-07-05 (f82b8e2). The re-enable relies on
// base/cozystack/keycloak-admin-guard.yaml shadowing /admin and
// /realms/master to an endpointless Service, and on edge probes gating both
// prefixes on 503. Nothing at runtime ties the guard to the oidc.enabled
// flag, so a refactor could drop the guard while Keycloak stays up and CI
// would stay green. This test pins the coupling: whenever platform.yaml
// enables OIDC, the guard and its probes must exist in the exact shape the
// 503 behavior depends on.
func TestPlatformKeycloakAdminGuardConformance(t *testing.T) {
	const (
		guardHost    = "keycloak.guardianintelligence.org"
		blackholeSvc = "keycloak-admin-blackhole"
	)
	guardedPrefixes := []string{"/admin", "/realms/master"}

	oidcEnabled := false
	for _, doc := range yamlDocs(t, runfilePath("src/infrastructure/base/cozystack/platform.yaml")) {
		if stringValue(mapValue(doc["metadata"])["name"]) != "cozystack.cozystack-platform" {
			continue
		}
		values := mapValue(mapValue(mapValue(mapValue(doc["spec"])["components"])["platform"])["values"])
		enabled, _ := mapValue(mapValue(values["authentication"])["oidc"])["enabled"].(bool)
		oidcEnabled = enabled
	}
	if !oidcEnabled {
		// Platform OIDC off means Cozystack removes the Keycloak stack and
		// its Ingress; there is no admin surface to guard.
		return
	}

	kustomization := yamlDocs(t, runfilePath("src/infrastructure/base/cozystack/kustomization.yaml"))
	guardListed := false
	for _, doc := range kustomization {
		for _, res := range sliceValue(doc["resources"]) {
			if stringValue(res) == "keycloak-admin-guard.yaml" {
				guardListed = true
			}
		}
	}
	if !guardListed {
		t.Fatalf("platform.yaml enables OIDC but base/cozystack/kustomization.yaml does not apply keycloak-admin-guard.yaml; the chart's / Ingress would serve the Keycloak admin console publicly")
	}

	var haveNamespace, haveService bool
	guarded := map[string]bool{}
	for _, doc := range yamlDocs(t, runfilePath("src/infrastructure/base/cozystack/keycloak-admin-guard.yaml")) {
		metadata := mapValue(doc["metadata"])
		switch stringValue(doc["kind"]) {
		case "Namespace":
			if stringValue(metadata["name"]) == "cozy-keycloak" {
				haveNamespace = true
			}
		case "Service":
			if stringValue(metadata["name"]) != blackholeSvc || stringValue(metadata["namespace"]) != "cozy-keycloak" {
				continue
			}
			if _, hasSelector := mapValue(doc["spec"])["selector"]; hasSelector {
				t.Fatalf("keycloak-admin-guard: %s must stay endpointless — a selector gives the guarded paths a real backend instead of a 503", blackholeSvc)
			}
			haveService = true
		case "Ingress":
			spec := mapValue(doc["spec"])
			if got := stringValue(spec["ingressClassName"]); got != "tenant-root" {
				t.Fatalf("keycloak-admin-guard Ingress class is %q; it must be tenant-root so ingress-nginx merges it into the chart Ingress's server block (different class = different controller = no shadowing)", got)
			}
			for _, rule := range sliceValue(spec["rules"]) {
				ruleMap := mapValue(rule)
				if stringValue(ruleMap["host"]) != guardHost {
					continue
				}
				for _, path := range sliceValue(mapValue(ruleMap["http"])["paths"]) {
					pathMap := mapValue(path)
					backend := mapValue(mapValue(pathMap["backend"])["service"])
					if stringValue(backend["name"]) != blackholeSvc {
						continue
					}
					if stringValue(pathMap["pathType"]) != "Prefix" {
						t.Fatalf("keycloak-admin-guard path %q must be pathType Prefix; Exact covers a single URL, not the admin surface", stringValue(pathMap["path"]))
					}
					guarded[stringValue(pathMap["path"])] = true
				}
			}
		}
	}
	if !haveNamespace {
		t.Fatalf("keycloak-admin-guard.yaml must declare namespace cozy-keycloak so the guard Ingress applies before the chart's Ingress exists (no first-enable exposure window)")
	}
	if !haveService {
		t.Fatalf("keycloak-admin-guard.yaml must declare the endpointless %s Service in cozy-keycloak", blackholeSvc)
	}
	for _, prefix := range guardedPrefixes {
		if !guarded[prefix] {
			t.Fatalf("keycloak-admin-guard Ingress does not shadow %s on %s; the chart's / Ingress would serve it publicly", prefix, guardHost)
		}
	}

	// Both guarded prefixes must be gated on 503 by edge-health so a partial
	// guard regression cannot hide behind the other path, and the realm
	// discovery endpoint must be gated on 200 so "everything 503s because
	// keycloak is down" cannot masquerade as the guard working.
	raw, err := os.ReadFile(runfilePath("src/infrastructure/edge/http-targets.file_sd.yaml"))
	if err != nil {
		t.Fatalf("read http-targets: %v", err)
	}
	var probes []struct {
		Targets []string          `yaml:"targets"`
		Labels  map[string]string `yaml:"labels"`
	}
	if err := yaml.Unmarshal(raw, &probes); err != nil {
		t.Fatalf("parse http-targets: %v", err)
	}
	expected := map[string]string{
		"https://" + guardHost + "/admin/master/console/":                          "503",
		"https://" + guardHost + "/realms/master/.well-known/openid-configuration": "503",
		"https://" + guardHost + "/realms/cozy/.well-known/openid-configuration":   "200",
	}
	for _, probe := range probes {
		for _, target := range probe.Targets {
			want, ok := expected[target]
			if !ok {
				continue
			}
			if got := probe.Labels["guardian_expected_statuses"]; got != want {
				t.Fatalf("edge probe %s expects %q, want %q", target, got, want)
			}
			delete(expected, target)
		}
	}
	for target, want := range expected {
		t.Fatalf("edge/http-targets.file_sd.yaml is missing the %s probe for %s; the guard invariant would go unmonitored", want, target)
	}
}
