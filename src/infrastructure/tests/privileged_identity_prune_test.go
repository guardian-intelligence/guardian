package tests

import "testing"

func TestPrivilegedIdentitiesHavePruningFluxInventory(t *testing.T) {
	const platformPath = "src/infrastructure/base/cozystack/kustomization.yaml"
	platform := readText(t, runfilePath(platformPath))
	for _, forbidden := range []string{
		"platform-admins.yaml",
		"keycloak-admin-guard.yaml",
		"kubernetes-device-client.yaml",
	} {
		assertTextNotContains(t, platform, forbidden, platformPath)
	}

	const identitiesPath = "src/infrastructure/base/cozystack-identities/kustomization.yaml"
	identities := readText(t, runfilePath(identitiesPath))
	for _, want := range []string{
		"platform-admins.yaml",
		"keycloak-admin-guard.yaml",
		"kubernetes-device-client.yaml",
	} {
		assertTextContains(t, identities, want, identitiesPath)
	}

	const syncPath = "src/infrastructure/base/flux/sync.yaml"
	syncDocs := yamlDocs(t, runfilePath(syncPath))
	fluxInventory := findDoc(t, syncDocs, "Kustomization", "guardian-mgmt-identities")
	assertNestedString(t, fluxInventory, "./src/infrastructure/base/cozystack-identities", "spec", "path")
	if nestedValue(t, fluxInventory, "spec", "prune") != true {
		t.Fatalf("%s guardian-mgmt-identities must set prune: true", syncPath)
	}
	dependsOn := sliceValue(nestedValue(t, fluxInventory, "spec", "dependsOn"))
	if len(dependsOn) != 1 || stringValue(mapValue(dependsOn[0])["name"]) != "guardian-mgmt-platform" {
		t.Fatalf("%s guardian-mgmt-identities dependsOn = %v, want only guardian-mgmt-platform", syncPath, dependsOn)
	}
}
