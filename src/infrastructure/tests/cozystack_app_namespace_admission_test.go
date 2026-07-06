package tests

import "testing"

// The source-tree guard (TestCozystackAppNamespaceConformance) only sees
// manifests in Git; a hand-applied kubectl CR bypasses it. The admission
// policy is the runtime half — it rejects any apps.cozystack.io CR outside a
// tenant-* namespace at the API server, for every actor. This test pins its
// shape so the policy can't silently regress to matching the wrong group,
// dropping the tenant-* check, or losing its enforcement action.
func TestCozystackAppNamespaceAdmissionConformance(t *testing.T) {
	path := runfilePath("src/infrastructure/base/app-patches/cozystack-app-namespace-admission.yaml")
	raw := readText(t, path)
	docs := yamlDocs(t, path)
	policy := findDoc(t, docs, "ValidatingAdmissionPolicy", "guardian-cozystack-app-tenant-namespace")
	binding := findDoc(t, docs, "ValidatingAdmissionPolicyBinding", "guardian-cozystack-app-tenant-namespace")

	assertNestedString(t, policy, "Fail", "spec", "failurePolicy")
	assertNestedString(t, binding, "guardian-cozystack-app-tenant-namespace", "spec", "policyName")
	for _, want := range []string{
		"- apps.cozystack.io",
		"- CREATE",
		"- UPDATE",
		`object.metadata.namespace.startsWith("tenant-")`,
		"validationActions:",
	} {
		assertTextContains(t, raw, want, path)
	}
}
