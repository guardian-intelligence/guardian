package tests

import "testing"

// The Mythra agent surface crosses three authorities that admission sees one
// object at a time: the platform-agent mints short-lived workload identities,
// Kubernetes confines each identity to one operation, and PostgreSQL makes the
// journal console read-only even after exec succeeds. Keep those names and
// privilege tiers joined so a chart or RBAC edit cannot silently turn journal
// observation into shared-products database administration.
func TestMythraAgentOperationCapabilities(t *testing.T) {
	path := runfilePath("src/infrastructure/deployments/mythra/prod/agent-ops.yaml")
	docs := yamlDocs(t, path)

	for _, name := range []string{"guardian-mythra-observer", "guardian-mythra-operator"} {
		sa := findDoc(t, docs, "ServiceAccount", name)
		if enabled, ok := sa["automountServiceAccountToken"].(bool); !ok || enabled {
			t.Fatalf("ServiceAccount %s automountServiceAccountToken = %v, want false", name, sa["automountServiceAccountToken"])
		}
	}

	observerRole := findDoc(t, docs, "Role", "guardian-mythra-observer")
	observerRules := sliceValue(observerRole["rules"])
	if len(observerRules) != 2 {
		t.Fatalf("Role guardian-mythra-observer rules = %d, want 2", len(observerRules))
	}
	assertCapabilityRule(t, observerRules[0], "guardian-mythra-observer", "pods", "get", []string{"mythra-journal-console-0"})
	assertCapabilityRule(t, observerRules[1], "guardian-mythra-observer", "pods/exec", "create", []string{"mythra-journal-console-0"})
	assertSingleCapabilityRule(t, docs, "guardian-mythra-operator", "pods", "delete", nil)
	assertSingleCapabilityRule(t, docs, "guardian-mythra-token-minter", "serviceaccounts/token", "create", []string{
		"guardian-mythra-observer",
		"guardian-mythra-operator",
	})

	minterBinding := findDoc(t, docs, "RoleBinding", "guardian-mythra-token-minter")
	assertNestedString(t, minterBinding, "guardian-mythra-token-minter", "roleRef", "name")
	subjects := sliceValue(minterBinding["subjects"])
	if len(subjects) != 2 ||
		stringValue(mapValue(subjects[0])["kind"]) != "Group" ||
		stringValue(mapValue(subjects[0])["name"]) != "guardian-persona-read" ||
		stringValue(mapValue(subjects[1])["kind"]) != "ServiceAccount" ||
		stringValue(mapValue(subjects[1])["name"]) != "guardian-cloud-agent-cursor" ||
		stringValue(mapValue(subjects[1])["namespace"]) != "tenant-root" {
		t.Fatalf("Mythra token minter subjects = %v, want read persona and Cursor cloud ServiceAccount", subjects)
	}

	for _, name := range []string{"guardian-mythra-observer", "guardian-mythra-operator"} {
		binding := findDoc(t, docs, "RoleBinding", name)
		assertNestedString(t, binding, name, "roleRef", "name")
		subjects := sliceValue(binding["subjects"])
		if len(subjects) != 1 || stringValue(mapValue(subjects[0])["name"]) != name {
			t.Fatalf("RoleBinding %s subjects = %v, want only ServiceAccount %s", name, subjects, name)
		}
	}

	operatorPolicy := findDoc(t, docs, "ValidatingAdmissionPolicy", "guardian-mythra-operator")
	validations := sliceValue(mapValue(operatorPolicy["spec"])["validations"])
	if len(validations) != 1 {
		t.Fatalf("guardian-mythra-operator validations = %d, want 1", len(validations))
	}
	operatorExpression := stringValue(mapValue(validations[0])["expression"])
	for _, name := range []string{"chunkies-gateway", "chunkies-park"} {
		assertTextContains(t, operatorExpression, `request.name.startsWith("`+name+`-")`, "guardian-mythra-operator admission expression")
		assertTextContains(t, operatorExpression, `oldObject.metadata.labels["app.kubernetes.io/name"] == "`+name+`"`, "guardian-mythra-operator admission expression")
	}

	console := findDoc(t, docs, "StatefulSet", "mythra-journal-console")
	podSpec := mapValue(mapValue(mapValue(console["spec"])["template"])["spec"])
	if enabled, ok := podSpec["automountServiceAccountToken"].(bool); !ok || enabled {
		t.Fatalf("journal console automountServiceAccountToken = %v, want false", podSpec["automountServiceAccountToken"])
	}
	containers := sliceValue(podSpec["containers"])
	if len(containers) != 1 {
		t.Fatalf("journal console containers = %d, want 1", len(containers))
	}
	env := map[string]map[string]interface{}{}
	for _, item := range sliceValue(mapValue(containers[0])["env"]) {
		entry := mapValue(item)
		env[stringValue(entry["name"])] = entry
	}
	if stringValue(env["PGUSER"]["value"]) != "mythra_observer" {
		t.Fatalf("journal console PGUSER = %q, want mythra_observer", stringValue(env["PGUSER"]["value"]))
	}
	secretKeyRef := mapValue(mapValue(env["PGPASSWORD"]["valueFrom"])["secretKeyRef"])
	if stringValue(secretKeyRef["name"]) != "postgres-products-credentials" || stringValue(secretKeyRef["key"]) != "mythra_observer" {
		t.Fatalf("journal console password ref = %v, want postgres-products-credentials/mythra_observer", secretKeyRef)
	}

	postgresPath := runfilePath("src/infrastructure/deployments/products/prod/postgres.yaml")
	postgres := findDoc(t, yamlDocs(t, postgresPath), "Postgres", "products")
	spec := mapValue(postgres["spec"])
	if _, ok := mapValue(spec["users"])["mythra_observer"]; !ok {
		t.Fatal("products Postgres does not declare the mythra_observer login")
	}
	roles := mapValue(mapValue(mapValue(spec["databases"])["mythra"])["roles"])
	if !stringSliceContains(sliceValue(roles["readonly"]), "mythra_observer") {
		t.Fatal("mythra_observer is not assigned the Mythra database readonly role")
	}
	if stringSliceContains(sliceValue(roles["admin"]), "mythra_observer") {
		t.Fatal("mythra_observer must not carry the Mythra database admin role")
	}
}

func assertSingleCapabilityRule(t *testing.T, docs []map[string]interface{}, roleName, resource, verb string, resourceNames []string) {
	t.Helper()
	role := findDoc(t, docs, "Role", roleName)
	rules := sliceValue(role["rules"])
	if len(rules) != 1 {
		t.Fatalf("Role %s rules = %d, want 1", roleName, len(rules))
	}
	assertCapabilityRule(t, rules[0], roleName, resource, verb, resourceNames)
}

func assertCapabilityRule(t *testing.T, rawRule interface{}, roleName, resource, verb string, resourceNames []string) {
	t.Helper()
	rule := mapValue(rawRule)
	resources := sliceValue(rule["resources"])
	verbs := sliceValue(rule["verbs"])
	if len(resources) != 1 || stringValue(resources[0]) != resource {
		t.Fatalf("Role %s resources = %v, want [%s]", roleName, resources, resource)
	}
	if len(verbs) != 1 || stringValue(verbs[0]) != verb {
		t.Fatalf("Role %s verbs = %v, want [%s]", roleName, verbs, verb)
	}
	names := sliceValue(rule["resourceNames"])
	if len(names) != len(resourceNames) {
		t.Fatalf("Role %s resourceNames = %v, want %v", roleName, names, resourceNames)
	}
	for i, want := range resourceNames {
		if stringValue(names[i]) != want {
			t.Fatalf("Role %s resourceNames = %v, want %v", roleName, names, resourceNames)
		}
	}
}

func stringSliceContains(values []interface{}, want string) bool {
	for _, value := range values {
		if stringValue(value) == want {
			return true
		}
	}
	return false
}
