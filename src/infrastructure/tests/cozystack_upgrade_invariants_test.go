package tests

import "testing"

func TestStageTenantHostnameOwnership(t *testing.T) {
	path := runfilePath("src/infrastructure/deployments/guardian/system/stage-tenants.yaml")
	docs := yamlDocs(t, path)

	prod := findDoc(t, docs, "Tenant", "prod")
	assertNestedString(t, prod, "guardianintelligence.org", "spec", "host")

	previews := findDoc(t, docs, "Tenant", "previews")
	assertNestedString(t, previews, "previews.guardianintelligence.org", "spec", "host")
}

func TestVPAForVPABootstrapMemoryFloor(t *testing.T) {
	path := runfilePath("src/infrastructure/base/platform-patches/cozystack-vpa-for-vpa.yaml")
	vpa := singleYAMLDoc(t, path)

	policies := sliceValue(nestedValue(t, vpa, "spec", "resourcePolicy", "containerPolicies"))
	if len(policies) != 1 {
		t.Fatalf("spec.resourcePolicy.containerPolicies has %d entries, want 1", len(policies))
	}
	recommender := mapValue(policies[0])
	assertNestedString(t, recommender, "recommender", "containerName")
	assertNestedString(t, recommender, "1Gi", "minAllowed", "memory")
}
