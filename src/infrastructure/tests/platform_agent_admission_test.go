package tests

import (
	"testing"

	"github.com/google/cel-go/cel"
)

// guardian-platform-agent-readonly is failurePolicy Fail: a malformed
// expression locks the agent identity out of every write it is allowed to
// make, and text pins cannot catch a CEL type error or a carve-out that
// quietly widens. This test compiles the policy's expressions and evaluates
// them the way the apiserver does: `request` is the unstructured
// AdmissionRequest, whose omitempty fields (subResource, name) are absent
// keys — exactly what has() observes.
func TestPlatformAgentReadonlyAdmissionExpression(t *testing.T) {
	path := runfilePath("src/infrastructure/base/cozystack/platform-admins.yaml")
	policy := findDoc(t, yamlDocs(t, path), "ValidatingAdmissionPolicy", "guardian-platform-agent-readonly")

	env, err := cel.NewEnv(cel.Variable("request", cel.DynType))
	if err != nil {
		t.Fatalf("cel env: %v", err)
	}
	compile := func(expression string) cel.Program {
		ast, issues := env.Compile(expression)
		if issues != nil && issues.Err() != nil {
			t.Fatalf("compile %q: %v", expression, issues.Err())
		}
		program, err := env.Program(ast)
		if err != nil {
			t.Fatalf("program %q: %v", expression, err)
		}
		return program
	}
	evaluate := func(program cel.Program, request map[string]interface{}) bool {
		t.Helper()
		out, _, err := program.Eval(map[string]interface{}{"request": request})
		if err != nil {
			// An evaluation error denies under failurePolicy Fail — but an
			// expression that errors instead of returning false is a latent
			// lockout, so no fixture may produce one.
			t.Fatalf("eval on %v: %v", request, err)
		}
		verdict, ok := out.Value().(bool)
		if !ok {
			t.Fatalf("eval on %v returned %T, want bool", request, out.Value())
		}
		return verdict
	}

	validations := sliceValue(nestedValue(t, policy, "spec", "validations"))
	if len(validations) != 1 {
		t.Fatalf("policy has %d validations, want exactly 1", len(validations))
	}
	validation := compile(stringValue(mapValue(validations[0])["expression"]))

	// operation/group/resource/subResource/name; empty subResource and name
	// mean the omitempty key is absent from the AdmissionRequest.
	admissionRequest := func(operation, group, resource, subResource, name string) map[string]interface{} {
		request := map[string]interface{}{
			"operation": operation,
			"resource": map[string]interface{}{
				"group":    group,
				"version":  "v1",
				"resource": resource,
			},
		}
		if subResource != "" {
			request["subResource"] = subResource
		}
		if name != "" {
			request["name"] = name
		}
		return request
	}

	for _, tc := range []struct {
		name    string
		request map[string]interface{}
		allowed bool
	}{
		{"delete pod", admissionRequest("DELETE", "", "pods", "", "some-pod"), true},
		{"delete job", admissionRequest("DELETE", "batch", "jobs", "", "some-job"), true},
		{"pod port-forward", admissionRequest("CONNECT", "", "pods", "portforward", "some-pod"), true},
		{"mint secrets-writer token", admissionRequest("CREATE", "", "serviceaccounts", "token", "secrets-writer"), true},
		{"mint secrets-reader token", admissionRequest("CREATE", "", "serviceaccounts", "token", "secrets-reader"), false},
		{"mint default SA token", admissionRequest("CREATE", "", "serviceaccounts", "token", "default"), false},
		{"mint token without a name", admissionRequest("CREATE", "", "serviceaccounts", "token", ""), false},
		{"create serviceaccount named secrets-writer", admissionRequest("CREATE", "", "serviceaccounts", "", "secrets-writer"), false},
		{"update serviceaccounts/token", admissionRequest("UPDATE", "", "serviceaccounts", "token", "secrets-writer"), false},
		{"delete serviceaccounts/token", admissionRequest("DELETE", "", "serviceaccounts", "token", "secrets-writer"), false},
		{"create secret", admissionRequest("CREATE", "", "secrets", "", "stolen"), false},
		{"create pod via generateName", admissionRequest("CREATE", "", "pods", "", ""), false},
		{"create pod eviction", admissionRequest("CREATE", "", "pods", "eviction", "some-pod"), false},
		{"update pod", admissionRequest("UPDATE", "", "pods", "", "some-pod"), false},
		{"pod exec", admissionRequest("CONNECT", "", "pods", "exec", "some-pod"), false},
		{"delete deployment", admissionRequest("DELETE", "apps", "deployments", "", "some-deployment"), false},
	} {
		if got := evaluate(validation, tc.request); got != tc.allowed {
			t.Fatalf("%s: validation returned %v, want %v", tc.name, got, tc.allowed)
		}
	}

	matchConditions := sliceValue(nestedValue(t, policy, "spec", "matchConditions"))
	if len(matchConditions) != 1 {
		t.Fatalf("policy has %d matchConditions, want exactly 1", len(matchConditions))
	}
	match := compile(stringValue(mapValue(matchConditions[0])["expression"]))

	userInfo := func(username string, groups ...string) map[string]interface{} {
		return map[string]interface{}{
			"userInfo": map[string]interface{}{
				"username": username,
				"groups":   groups,
			},
		}
	}
	for _, tc := range []struct {
		name    string
		request map[string]interface{}
		matched bool
	}{
		{"agent by username", userInfo("keycloak:cozy#platform-agent", "system:authenticated"), true},
		{"agent by group", userInfo("keycloak:cozy#other", "guardian-platform-agent", "system:authenticated"), true},
		{"human admin", userInfo("keycloak:cozy#platform-admin", "cozystack-cluster-admin", "system:authenticated"), false},
		{"controller service account", userInfo("system:serviceaccount:tenant-root:flux", "system:serviceaccounts", "system:authenticated"), false},
	} {
		if got := evaluate(match, tc.request); got != tc.matched {
			t.Fatalf("%s: matchCondition returned %v, want %v", tc.name, got, tc.matched)
		}
	}
}
