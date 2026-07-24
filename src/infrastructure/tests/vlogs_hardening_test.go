package tests

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestGenericVictoriaLogsHardeningOwnership(t *testing.T) {
	path := runfilePath("src/infrastructure/base/vlogs-hardening/vlcluster.yaml")
	patch := singleYAMLDoc(t, path)
	assertNestedString(t, patch, "operator.victoriametrics.com/v1", "apiVersion")
	assertNestedString(t, patch, "VLCluster", "kind")
	assertNestedString(t, patch, "generic", "metadata", "name")
	assertNestedString(t, patch, "tenant-root", "metadata", "namespace")
	assertNestedString(t, patch, "Override", "metadata", "annotations", "kustomize.toolkit.fluxcd.io/ssa")

	spec := nestedMap(t, patch, "spec")
	if len(spec) != 4 {
		t.Fatalf("VLCluster patch owns %d spec fields, want only useStrictSecurity and the three components", len(spec))
	}
	assertNestedBool(t, spec, true, "useStrictSecurity")

	assertVLComponentHardening(t, nestedMap(t, spec, "vlinsert"), "select.disable", "true")
	assertVLComponentHardening(t, nestedMap(t, spec, "vlselect"), "insert.disable", "true")
	assertVLComponentHardening(t, nestedMap(t, spec, "vlstorage"), "retention.maxDiskUsagePercent", "80")
}

func TestGenericVictoriaLogsRetentionUsesOperatorMonthSyntax(t *testing.T) {
	path := runfilePath("src/infrastructure/base/apps/observability.yaml")
	monitoring := singleYAMLDoc(t, path)
	for _, item := range sliceValue(nestedValue(t, monitoring, "spec", "logsStorages")) {
		storage := mapValue(item)
		if stringValue(storage["name"]) != "generic" {
			continue
		}
		if got := stringValue(storage["retentionPeriod"]); got != "3" {
			t.Fatalf("generic logs retentionPeriod = %q, want bare month value %q", got, "3")
		}
		return
	}
	t.Fatal("generic logs storage is missing")
}

func TestVLogsHardeningInventoriesAreIsolated(t *testing.T) {
	prerequisites := singleYAMLDoc(t, runfilePath("src/infrastructure/base/vlogs-hardening-prerequisites/kustomization.yaml"))
	assertNestedStringSlice(t, prerequisites, []string{"rbac.yaml"}, "resources")

	hardening := singleYAMLDoc(t, runfilePath("src/infrastructure/base/vlogs-hardening/kustomization.yaml"))
	assertNestedStringSlice(t, hardening, []string{"vlcluster.yaml"}, "resources")

	appPatches := singleYAMLDoc(t, runfilePath("src/infrastructure/base/app-patches/kustomization.yaml"))
	for _, resource := range nestedStringSlice(t, appPatches, "resources") {
		if strings.Contains(resource, "vlogs-hardening") || resource == "vlcluster.yaml" {
			t.Fatalf("shared app-patches inventory contains VL hardening resource %q", resource)
		}
	}

	admission := singleYAMLDoc(t, runfilePath("src/infrastructure/base/admission/kustomization.yaml"))
	if !containsString(nestedStringSlice(t, admission, "resources"), "vlogs-patcher-update-only.yaml") {
		t.Fatal("admission inventory is missing vlogs-patcher-update-only.yaml")
	}
}

func TestVLogsPatcherRBACLimitsMutationToGeneric(t *testing.T) {
	path := runfilePath("src/infrastructure/base/vlogs-hardening-prerequisites/rbac.yaml")
	docs := yamlDocs(t, path)
	serviceAccount := findDoc(t, docs, "ServiceAccount", "guardian-vlogs-patcher")
	role := findDoc(t, docs, "Role", "guardian-vlogs-patcher")
	roleBinding := findDoc(t, docs, "RoleBinding", "guardian-vlogs-patcher")

	assertNestedString(t, serviceAccount, "cozy-fluxcd", "metadata", "namespace")
	assertNestedBool(t, serviceAccount, false, "automountServiceAccountToken")
	assertNestedString(t, role, "tenant-root", "metadata", "namespace")

	var gotRules []string
	for _, item := range sliceValue(nestedValue(t, role, "rules")) {
		gotRules = append(gotRules, canonicalRBACRule(t, mapValue(item)))
	}
	wantRules := []string{
		"|pods||get,list",
		"apps|deployments,replicasets,statefulsets||get,list",
		"operator.victoriametrics.com|vlclusters||list",
		"operator.victoriametrics.com|vlclusters|generic|get,patch",
	}
	sort.Strings(gotRules)
	sort.Strings(wantRules)
	if !reflect.DeepEqual(gotRules, wantRules) {
		t.Fatalf("guardian-vlogs-patcher Role rules = %#v, want %#v", gotRules, wantRules)
	}

	assertNestedString(t, roleBinding, "tenant-root", "metadata", "namespace")
	assertNestedString(t, roleBinding, "rbac.authorization.k8s.io", "roleRef", "apiGroup")
	assertNestedString(t, roleBinding, "Role", "roleRef", "kind")
	assertNestedString(t, roleBinding, "guardian-vlogs-patcher", "roleRef", "name")
	subjects := sliceValue(nestedValue(t, roleBinding, "subjects"))
	if len(subjects) != 1 {
		t.Fatalf("guardian-vlogs-patcher RoleBinding has %d subjects, want one", len(subjects))
	}
	subject := mapValue(subjects[0])
	assertNestedString(t, subject, "ServiceAccount", "kind")
	assertNestedString(t, subject, "guardian-vlogs-patcher", "name")
	assertNestedString(t, subject, "cozy-fluxcd", "namespace")
}

func TestVLogsPatcherAdmissionDeniesOnlyCreateOfGeneric(t *testing.T) {
	path := runfilePath("src/infrastructure/base/admission/vlogs-patcher-update-only.yaml")
	docs := yamlDocs(t, path)
	policy := findDoc(t, docs, "ValidatingAdmissionPolicy", "guardian-vlogs-patcher-update-only")
	binding := findDoc(t, docs, "ValidatingAdmissionPolicyBinding", "guardian-vlogs-patcher-update-only")

	assertNestedString(t, policy, "Fail", "spec", "failurePolicy")
	rules := sliceValue(nestedValue(t, policy, "spec", "matchConstraints", "resourceRules"))
	if len(rules) != 1 {
		t.Fatalf("admission policy has %d resource rules, want one", len(rules))
	}
	rule := mapValue(rules[0])
	assertNestedStringSlice(t, rule, []string{"operator.victoriametrics.com"}, "apiGroups")
	assertNestedStringSlice(t, rule, []string{"*"}, "apiVersions")
	assertNestedStringSlice(t, rule, []string{"CREATE", "UPDATE"}, "operations")
	assertNestedStringSlice(t, rule, []string{"vlclusters"}, "resources")

	validations := sliceValue(nestedValue(t, policy, "spec", "validations"))
	if len(validations) != 1 {
		t.Fatalf("admission policy has %d validations, want one", len(validations))
	}
	gotExpression := compactWhitespace(stringValue(mapValue(validations[0])["expression"]))
	wantExpression := "request.userInfo.username != 'system:serviceaccount:cozy-fluxcd:guardian-vlogs-patcher' || request.namespace != 'tenant-root' || object.metadata.name != 'generic' || oldObject != null"
	if gotExpression != wantExpression {
		t.Fatalf("admission expression = %q, want %q", gotExpression, wantExpression)
	}

	assertNestedString(t, binding, "guardian-vlogs-patcher-update-only", "spec", "policyName")
	assertNestedStringSlice(t, nestedMap(t, binding, "spec"), []string{"Deny"}, "validationActions")
}

func TestVLogsHardeningFluxOrderingAndHealth(t *testing.T) {
	syncPath := runfilePath("src/infrastructure/base/flux/sync.yaml")
	docs := yamlDocs(t, syncPath)
	if hasNamedDoc(docs, "Kustomization", "guardian-mgmt-app-patch-prerequisites") {
		t.Fatal("global app-patch prerequisite gate must not exist")
	}

	appPatches := findDoc(t, docs, "Kustomization", "guardian-mgmt-app-patches")
	assertDependencyNames(t, appPatches, []string{"guardian-mgmt-base"})

	prerequisites := findDoc(t, docs, "Kustomization", "guardian-vlogs-hardening-prerequisites")
	assertNestedString(t, prerequisites, "./src/infrastructure/base/vlogs-hardening-prerequisites", "spec", "path")
	assertNestedBool(t, prerequisites, true, "spec", "prune")
	assertNestedBool(t, prerequisites, false, "spec", "wait")
	assertDependencyNames(t, prerequisites, []string{"guardian-mgmt-admission", "guardian-mgmt-base"})
	assertHealthCheckIdentities(t, prerequisites, []string{
		"operator.victoriametrics.com/v1 VLCluster tenant-root/generic",
	})
	assertVLClusterHealthExpression(t, prerequisites)
	if _, found := nestedMap(t, prerequisites, "spec")["serviceAccountName"]; found {
		t.Fatal("prerequisite slice cannot use the ServiceAccount it bootstraps")
	}

	hardening := findDoc(t, docs, "Kustomization", "guardian-vlogs-hardening")
	assertNestedString(t, hardening, "./src/infrastructure/base/vlogs-hardening", "spec", "path")
	assertNestedString(t, hardening, "guardian-vlogs-patcher", "spec", "serviceAccountName")
	assertNestedBool(t, hardening, false, "spec", "prune")
	assertNestedBool(t, hardening, false, "spec", "wait")
	assertDependencyNames(t, hardening, []string{"guardian-vlogs-hardening-prerequisites"})
	assertHealthCheckIdentities(t, hardening, []string{
		"apps/v1 Deployment tenant-root/vlinsert-generic",
		"apps/v1 Deployment tenant-root/vlselect-generic",
		"apps/v1 StatefulSet tenant-root/vlstorage-generic",
		"operator.victoriametrics.com/v1 VLCluster tenant-root/generic",
	})
	assertVLClusterHealthExpression(t, hardening)
}

func assertVLComponentHardening(t *testing.T, component map[string]interface{}, extraArg, want string) {
	t.Helper()

	if len(component) != 3 {
		t.Fatalf("VLCluster component patch owns %d fields, want only strict security, service-account token hardening, and extraArgs", len(component))
	}
	assertNestedBool(t, component, true, "useStrictSecurity")
	assertNestedBool(t, component, true, "disableAutomountServiceAccountToken")
	extraArgs := nestedMap(t, component, "extraArgs")
	if len(extraArgs) != 1 {
		t.Fatalf("VLCluster component patch owns %d extraArgs, want only %s", len(extraArgs), extraArg)
	}
	assertNestedString(t, extraArgs, want, extraArg)
}

func canonicalRBACRule(t *testing.T, rule map[string]interface{}) string {
	t.Helper()

	parts := [][]string{
		nestedStringSlice(t, rule, "apiGroups"),
		nestedStringSlice(t, rule, "resources"),
		optionalStringSlice(t, rule, "resourceNames"),
		nestedStringSlice(t, rule, "verbs"),
	}
	for _, part := range parts {
		sort.Strings(part)
	}
	return fmt.Sprintf("%s|%s|%s|%s", strings.Join(parts[0], ","), strings.Join(parts[1], ","), strings.Join(parts[2], ","), strings.Join(parts[3], ","))
}

func assertDependencyNames(t *testing.T, kustomization map[string]interface{}, want []string) {
	t.Helper()

	var got []string
	for _, item := range sliceValue(nestedValue(t, kustomization, "spec", "dependsOn")) {
		got = append(got, stringValue(mapValue(item)["name"]))
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s dependencies = %#v, want %#v", stringValue(mapValue(kustomization["metadata"])["name"]), got, want)
	}
}

func assertHealthCheckIdentities(t *testing.T, kustomization map[string]interface{}, want []string) {
	t.Helper()

	var got []string
	for _, item := range sliceValue(nestedValue(t, kustomization, "spec", "healthChecks")) {
		check := mapValue(item)
		got = append(got, fmt.Sprintf("%s %s %s/%s", stringValue(check["apiVersion"]), stringValue(check["kind"]), stringValue(check["namespace"]), stringValue(check["name"])))
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s health checks = %#v, want %#v", stringValue(mapValue(kustomization["metadata"])["name"]), got, want)
	}
}

func assertVLClusterHealthExpression(t *testing.T, kustomization map[string]interface{}) {
	t.Helper()

	expressions := sliceValue(nestedValue(t, kustomization, "spec", "healthCheckExprs"))
	if len(expressions) != 1 {
		t.Fatalf("%s has %d health expressions, want one", stringValue(mapValue(kustomization["metadata"])["name"]), len(expressions))
	}
	expression := mapValue(expressions[0])
	assertNestedString(t, expression, "operator.victoriametrics.com/v1", "apiVersion")
	assertNestedString(t, expression, "VLCluster", "kind")
	wantCurrent := "has(status.observedGeneration) && status.observedGeneration == metadata.generation && has(status.updateStatus) && status.updateStatus == 'operational'"
	if got := compactWhitespace(stringValue(expression["current"])); got != wantCurrent {
		t.Fatalf("VLCluster current expression = %q, want %q", got, wantCurrent)
	}
	wantFailed := "(has(status.updateStatus) && status.updateStatus == 'failed') || (has(status.conditions) && status.conditions.exists(c, c.type == 'Degraded' && c.status == 'True'))"
	if got := compactWhitespace(stringValue(expression["failed"])); got != wantFailed {
		t.Fatalf("VLCluster failed expression = %q, want %q", got, wantFailed)
	}
}

func assertNestedStringSlice(t *testing.T, object map[string]interface{}, want []string, path ...string) {
	t.Helper()

	got := nestedStringSlice(t, object, path...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", strings.Join(path, "."), got, want)
	}
}

func nestedStringSlice(t *testing.T, object map[string]interface{}, path ...string) []string {
	t.Helper()

	items := sliceValue(nestedValue(t, object, path...))
	values := make([]string, len(items))
	for i, item := range items {
		value, ok := item.(string)
		if !ok {
			t.Fatalf("%s[%d] is not a string", strings.Join(path, "."), i)
		}
		values[i] = value
	}
	return values
}

func optionalStringSlice(t *testing.T, object map[string]interface{}, key string) []string {
	t.Helper()

	value, found := object[key]
	if !found {
		return nil
	}
	items := sliceValue(value)
	values := make([]string, len(items))
	for i, item := range items {
		var ok bool
		values[i], ok = item.(string)
		if !ok {
			t.Fatalf("%s[%d] is not a string", key, i)
		}
	}
	return values
}

func hasNamedDoc(docs []map[string]interface{}, kind, name string) bool {
	for _, doc := range docs {
		if stringValue(doc["kind"]) == kind && stringValue(mapValue(doc["metadata"])["name"]) == name {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func compactWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
