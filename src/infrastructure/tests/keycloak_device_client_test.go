package tests

import (
	"os"
	"regexp"
	"testing"
)

// `aspect infra auth --platform-agent` performs a device-code login against
// the guardian-owned kubernetes-device Keycloak client. Nothing at runtime
// ties the axl task to the CR that declares that client: renaming the
// clientId, dropping the manifest from the kustomization, or disabling the
// device grant would keep CI green while every platform-agent login fails at
// the device-authorization endpoint. This test pins the coupling, and pins
// the token-pipeline fields the apiserver depends on: aud=kubernetes rides
// the realm's kubernetes-client scope, groups carries RBAC groups,
// preferred_username rides profile, and offline_access is what makes the
// cached refresh token a 30-day-idle offline token so agent sessions run
// unattended after a single device approval per machine.
func TestPlatformAgentDeviceClientConformance(t *testing.T) {
	axl, err := os.ReadFile(runfilePath(".aspect/tasks/infra.axl"))
	if err != nil {
		t.Fatalf("read infra.axl: %v", err)
	}
	m := regexp.MustCompile(`(?m)^PLATFORM_OIDC_CLIENT_ID = "([^"]+)"`).FindSubmatch(axl)
	if m == nil {
		t.Fatalf("infra.axl no longer defines PLATFORM_OIDC_CLIENT_ID; the platform-agent kubelogin flow depends on it")
	}
	clientID := string(m[1])
	assertTextContains(t, string(axl), "--exec-arg=--grant-type=device-code",
		"infra.axl platform-agent kubelogin flow (device-code is what makes login work headless/over SSH)")
	assertTextContains(t, string(axl), "--exec-arg=--oidc-extra-scope=offline_access",
		"infra.axl platform-agent kubelogin flow (offline_access is what keeps agent sessions unattended)")

	listed := false
	for _, doc := range yamlDocs(t, runfilePath("src/infrastructure/base/cozystack/kustomization.yaml")) {
		for _, res := range sliceValue(doc["resources"]) {
			if stringValue(res) == "kubernetes-device-client.yaml" {
				listed = true
			}
		}
	}
	if !listed {
		t.Fatalf("base/cozystack/kustomization.yaml does not apply kubernetes-device-client.yaml; the %s client would never reach Keycloak", clientID)
	}

	found := false
	for _, doc := range yamlDocs(t, runfilePath("src/infrastructure/base/cozystack/kubernetes-device-client.yaml")) {
		if stringValue(doc["kind"]) != "KeycloakClient" {
			continue
		}
		found = true
		spec := mapValue(doc["spec"])
		if got := stringValue(spec["clientId"]); got != clientID {
			t.Fatalf("KeycloakClient clientId is %q but infra.axl logs in with PLATFORM_OIDC_CLIENT_ID=%q; kubelogin would hit a nonexistent client", got, clientID)
		}
		if got := stringValue(mapValue(spec["attributes"])["oauth2.device.authorization.grant.enabled"]); got != "true" {
			t.Fatalf("KeycloakClient %s must enable the device authorization grant (attribute is %q); without it every platform-agent login fails with unauthorized_client", clientID, got)
		}
		if public, _ := spec["public"].(bool); !public {
			t.Fatalf("KeycloakClient %s must be public; kubelogin has no client secret", clientID)
		}
		if standardFlow, ok := spec["standardFlowEnabled"].(bool); !ok || standardFlow {
			t.Fatalf("KeycloakClient %s must keep standardFlowEnabled: false; the device client has no redirect URIs to make an authorization-code flow safe", clientID)
		}
		if got := stringValue(mapValue(spec["realmRef"])["name"]); got != "keycloakrealm-cozy" {
			t.Fatalf("KeycloakClient %s realmRef is %q; the apiserver trusts issuer realm cozy only", clientID, got)
		}
		defaults := map[string]bool{}
		for _, scope := range sliceValue(spec["defaultClientScopes"]) {
			defaults[stringValue(scope)] = true
		}
		for scope, why := range map[string]string{
			"kubernetes-client": "maps aud=kubernetes, the apiserver's oidc-client-id",
			"groups":            "carries the RBAC groups claim",
			"profile":           "carries preferred_username, the apiserver's username claim",
		} {
			if !defaults[scope] {
				t.Fatalf("KeycloakClient %s defaultClientScopes must include %s: it %s", clientID, scope, why)
			}
		}
		optional := map[string]bool{}
		for _, scope := range sliceValue(spec["optionalClientScopes"]) {
			optional[stringValue(scope)] = true
		}
		if !optional["offline_access"] {
			t.Fatalf("KeycloakClient %s optionalClientScopes must include offline_access; without it kubelogin's offline_access request is silently dropped and agent sessions expire with the SSO session", clientID)
		}
	}
	if !found {
		t.Fatalf("kubernetes-device-client.yaml no longer declares a KeycloakClient")
	}
}
