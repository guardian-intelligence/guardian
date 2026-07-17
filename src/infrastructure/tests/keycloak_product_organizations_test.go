package tests

import (
	"encoding/json"
	"os"
	"testing"
)

func TestProductKeycloakOrganizationsConformance(t *testing.T) {
	t.Parallel()

	const root = "src/infrastructure/deployments/iam/prod/"
	listed := false
	for _, doc := range yamlDocs(t, runfilePath(root+"kustomization.yaml")) {
		for _, resource := range sliceValue(doc["resources"]) {
			if stringValue(resource) == "realm-reconciler.yaml" {
				listed = true
			}
		}
	}
	if !listed {
		t.Fatal("IAM kustomization does not apply realm-reconciler.yaml; mutable realm settings would drift after first boot")
	}

	organizationsEnabled := false
	for _, doc := range yamlDocs(t, runfilePath(root+"realm-configmap.yaml")) {
		if stringValue(doc["kind"]) != "ConfigMap" {
			continue
		}
		raw := stringValue(mapValue(doc["data"])["postflight-realm.json"])
		var realm struct {
			OrganizationsEnabled bool `json:"organizationsEnabled"`
		}
		if err := json.Unmarshal([]byte(raw), &realm); err != nil {
			t.Fatalf("decode postflight realm JSON: %v", err)
		}
		organizationsEnabled = realm.OrganizationsEnabled
	}
	if !organizationsEnabled {
		t.Fatal("postflight realm import must enable native Keycloak Organizations")
	}

	reconciler, err := os.ReadFile(runfilePath(root + "realm-reconciler.yaml"))
	if err != nil {
		t.Fatalf("read realm reconciler: %v", err)
	}
	assertTextContains(t, string(reconciler), `update realms/postflight`,
		"realm reconciler must target the product realm")
	assertTextContains(t, string(reconciler), `--set organizationsEnabled=true`,
		"realm reconciler must enforce Organizations on existing realms")
	assertTextContains(t, string(reconciler), `name: KC_CLI_PASSWORD`,
		"realm reconciler must pass the admin password through Keycloak CLI's environment")
	assertTextNotContains(t, string(reconciler), `--password`,
		"realm reconciler must not expose the admin password in a child process argument")
}
