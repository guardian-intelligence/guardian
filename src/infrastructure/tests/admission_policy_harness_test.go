package tests

// Generic interpreter for the repo's admission policies (see
// docs/admission-doctrine.md). Every ValidatingAdmissionPolicy and
// MutatingAdmissionPolicy rendered anywhere in base/ or deployments/ is
// compiled with the apiserver's own compiler, evaluated against every
// rendered manifest its bindings match, and exercised by declarative
// fixtures under fixtures/admission/. Policies are authored once, in YAML;
// nothing here restates what any individual policy says.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/apiserver/pkg/admission"
	plugincel "k8s.io/apiserver/pkg/admission/plugin/cel"
	"k8s.io/apiserver/pkg/admission/plugin/policy/mutating/patch"
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/matchconditions"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/cel/environment"
	sigsyaml "sigs.k8s.io/yaml"
)

// policyUniverse is everything the harness discovers in one walk: policies,
// their bindings, candidate param ConfigMaps (rendered or synthesized from
// configMapGenerator entries), and every rendered object to evaluate against.
type policyUniverse struct {
	validating       []admissionregistrationv1.ValidatingAdmissionPolicy
	validatingBinds  []admissionregistrationv1.ValidatingAdmissionPolicyBinding
	mutating         []admissionregistrationv1.MutatingAdmissionPolicy
	mutatingBinds    []admissionregistrationv1.MutatingAdmissionPolicyBinding
	params           map[string]map[string]string
	objects          []renderedObject
}

type renderedObject struct {
	path string
	doc  map[string]interface{}
}

func loadPolicyUniverse(t *testing.T) *policyUniverse {
	t.Helper()

	u := &policyUniverse{params: map[string]map[string]string{}}
	root := repoRootFromRunfiles(t)
	for _, dir := range []string{"src/infrastructure/base", "src/infrastructure/deployments"} {
		walkYAMLFiles(t, filepath.Join(root, dir), func(path string, docs []map[string]interface{}) {
			for _, doc := range docs {
				u.absorb(t, path, doc)
			}
		})
	}
	if len(u.validating) == 0 || len(u.mutating) == 0 || len(u.objects) == 0 {
		t.Fatalf("policy walk found %d VAPs, %d MAPs, %d objects; walk roots or data deps are wrong",
			len(u.validating), len(u.mutating), len(u.objects))
	}
	return u
}

func (u *policyUniverse) absorb(t *testing.T, path string, doc map[string]interface{}) {
	t.Helper()

	// kustomization configMapGenerator entries synthesize the same ConfigMaps
	// kustomize renders, so paramRef resolution sees what the cluster sees.
	if generators, ok := doc["configMapGenerator"]; ok {
		for _, entry := range sliceValue(generators) {
			generator := mapValue(entry)
			data := map[string]string{}
			complete := true
			for _, file := range sliceValue(generator["files"]) {
				spec := stringValue(file)
				key, rel := filepath.Base(spec), spec
				if eq := strings.Index(spec, "="); eq >= 0 {
					key, rel = spec[:eq], spec[eq+1:]
				}
				payload, err := os.ReadFile(filepath.Join(filepath.Dir(path), rel))
				if err != nil {
					// Generator sources outside the test's data deps can't be
					// synthesized; policies whose params live here are skipped
					// (and logged) at evaluation time, not silently passed.
					complete = false
					break
				}
				data[key] = string(payload)
			}
			for _, literal := range sliceValue(generator["literals"]) {
				if key, value, ok := strings.Cut(stringValue(literal), "="); ok {
					data[key] = value
				}
			}
			if complete {
				u.params[stringValue(generator["name"])] = data
			}
		}
		return
	}

	kind := stringValue(doc["kind"])
	switch kind {
	case "ValidatingAdmissionPolicy":
		u.validating = append(u.validating, decodePolicyDoc[admissionregistrationv1.ValidatingAdmissionPolicy](t, path, doc))
		return
	case "ValidatingAdmissionPolicyBinding":
		u.validatingBinds = append(u.validatingBinds, decodePolicyDoc[admissionregistrationv1.ValidatingAdmissionPolicyBinding](t, path, doc))
		return
	case "MutatingAdmissionPolicy":
		u.mutating = append(u.mutating, decodePolicyDoc[admissionregistrationv1.MutatingAdmissionPolicy](t, path, doc))
		return
	case "MutatingAdmissionPolicyBinding":
		u.mutatingBinds = append(u.mutatingBinds, decodePolicyDoc[admissionregistrationv1.MutatingAdmissionPolicyBinding](t, path, doc))
		return
	case "ConfigMap":
		name := stringValue(mapValue(doc["metadata"])["name"])
		if _, exists := u.params[name]; !exists {
			data := map[string]string{}
			for key, value := range mapValue(doc["data"]) {
				data[key] = stringValue(value)
			}
			u.params[name] = data
		}
	}
	if kind == "" || stringValue(doc["apiVersion"]) == "" {
		return
	}
	// yaml.v3 yields int (and friends) where the apiserver machinery demands
	// JSON types; a JSON round-trip normalizes exactly the way a real request
	// body would arrive.
	payload, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("%s: normalize %s: %v", path, kind, err)
	}
	normalized := map[string]interface{}{}
	if err := json.Unmarshal(payload, &normalized); err != nil {
		t.Fatalf("%s: normalize %s: %v", path, kind, err)
	}
	u.objects = append(u.objects, renderedObject{path: path, doc: normalized})
}

func decodePolicyDoc[T any](t *testing.T, path string, doc map[string]interface{}) T {
	t.Helper()

	var decoded T
	payload, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("%s: marshal policy doc: %v", path, err)
	}
	if err := sigsyaml.UnmarshalStrict(payload, &decoded); err != nil {
		t.Fatalf("%s: decode %T: %v", path, decoded, err)
	}
	return decoded
}

// denyAllAuthorizer satisfies the authorizer binding the production compile
// environment declares; no repo policy consults it, and any future one that
// does will surface here as a NoOpinion-driven failure instead of a panic.
type denyAllAuthorizer struct{}

func (denyAllAuthorizer) Authorize(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
	return authorizer.DecisionNoOpinion, "harness authorizer", nil
}

type compiledVAP struct {
	policy     admissionregistrationv1.ValidatingAdmissionPolicy
	bindings   []admissionregistrationv1.ValidatingAdmissionPolicyBinding
	matcher    matchconditions.Matcher
	validation plugincel.ConditionEvaluator
}

type compiledMAP struct {
	policy   admissionregistrationv1.MutatingAdmissionPolicy
	bindings []admissionregistrationv1.MutatingAdmissionPolicyBinding
	matcher  matchconditions.Matcher
	patchers []patch.Patcher
}

// compileVAP mirrors the apiserver's validating compilePolicy composition:
// same environment, same variable store, same failure semantics.
func compileVAP(t *testing.T, u *policyUniverse, policy admissionregistrationv1.ValidatingAdmissionPolicy) *compiledVAP {
	t.Helper()

	opts := plugincel.OptionalVariableDeclarations{HasParams: policy.Spec.ParamKind != nil, HasAuthorizer: true}
	compiler := newCompositedCompiler(t)
	compileVariables(t, compiler, policy.Name, policy.Spec.Variables, opts)

	validations := make([]plugincel.ExpressionAccessor, len(policy.Spec.Validations))
	for i, v := range policy.Spec.Validations {
		assertCompiles(t, compiler, policy.Name, &validating.ValidationCondition{Expression: v.Expression, Message: v.Message}, opts)
		validations[i] = &validating.ValidationCondition{Expression: v.Expression, Message: v.Message}
	}

	compiled := &compiledVAP{
		policy:     policy,
		matcher:    compileMatchConditions(t, compiler, policy.Name, policy.Spec.MatchConditions, policy.Spec.FailurePolicy, opts),
		validation: compiler.CompileCondition(validations, opts, environment.StoredExpressions),
	}
	for _, binding := range u.validatingBinds {
		if binding.Spec.PolicyName == policy.Name {
			compiled.bindings = append(compiled.bindings, binding)
		}
	}
	return compiled
}

func compileMAP(t *testing.T, u *policyUniverse, policy admissionregistrationv1.MutatingAdmissionPolicy) *compiledMAP {
	t.Helper()

	opts := plugincel.OptionalVariableDeclarations{HasParams: policy.Spec.ParamKind != nil, HasAuthorizer: true}
	compiler := newCompositedCompiler(t)
	compileVariables(t, compiler, policy.Name, policy.Spec.Variables, opts)

	patchOptions := opts
	patchOptions.HasPatchTypes = true
	compiled := &compiledMAP{
		policy:  policy,
		matcher: compileMatchConditions(t, compiler, policy.Name, policy.Spec.MatchConditions, policy.Spec.FailurePolicy, opts),
	}
	for _, m := range policy.Spec.Mutations {
		var evaluator plugincel.MutatingEvaluator
		switch m.PatchType {
		case admissionregistrationv1.PatchTypeApplyConfiguration:
			evaluator = compiler.CompileMutatingEvaluator(&patch.ApplyConfigurationCondition{Expression: m.ApplyConfiguration.Expression}, patchOptions, environment.StoredExpressions)
			compiled.patchers = append(compiled.patchers, patch.NewApplyConfigurationPatcher(evaluator))
		case admissionregistrationv1.PatchTypeJSONPatch:
			evaluator = compiler.CompileMutatingEvaluator(&patch.JSONPatchCondition{Expression: m.JSONPatch.Expression}, patchOptions, environment.StoredExpressions)
			compiled.patchers = append(compiled.patchers, patch.NewJSONPatcher(evaluator))
		default:
			t.Fatalf("policy %s: unsupported patchType %q", policy.Name, m.PatchType)
		}
		for _, err := range evaluator.CompilationErrors() {
			t.Errorf("policy %s: mutation does not compile: %v", policy.Name, err)
		}
	}
	for _, binding := range u.mutatingBinds {
		if binding.Spec.PolicyName == policy.Name {
			compiled.bindings = append(compiled.bindings, binding)
		}
	}
	return compiled
}

func newCompositedCompiler(t *testing.T) *plugincel.CompositedCompiler {
	t.Helper()

	compiler, err := plugincel.NewCompositedCompiler(environment.MustBaseEnvSet(environment.DefaultCompatibilityVersion()))
	if err != nil {
		t.Fatalf("cel compiler: %v", err)
	}
	return compiler
}

func compileVariables(t *testing.T, compiler *plugincel.CompositedCompiler, policyName string, variables []admissionregistrationv1.Variable, opts plugincel.OptionalVariableDeclarations) {
	t.Helper()

	for _, v := range variables {
		result := compiler.CompileAndStoreVariable(&validating.Variable{Name: v.Name, Expression: v.Expression}, opts, environment.StoredExpressions)
		if result.Error != nil {
			t.Errorf("policy %s: variable %s does not compile: %v", policyName, v.Name, result.Error)
		}
	}
}

func compileMatchConditions(t *testing.T, compiler *plugincel.CompositedCompiler, policyName string, conditions []admissionregistrationv1.MatchCondition, failurePolicy *admissionregistrationv1.FailurePolicyType, opts plugincel.OptionalVariableDeclarations) matchconditions.Matcher {
	t.Helper()

	if len(conditions) == 0 {
		return nil
	}
	accessors := make([]plugincel.ExpressionAccessor, len(conditions))
	for i := range conditions {
		assertCompiles(t, compiler, policyName, (*matchconditions.MatchCondition)(&conditions[i]), opts)
		accessors[i] = (*matchconditions.MatchCondition)(&conditions[i])
	}
	return matchconditions.NewMatcher(compiler.CompileCondition(accessors, opts, environment.StoredExpressions), failurePolicy, "policy", "harness", policyName)
}

// assertCompiles is the lockout guard: under failurePolicy Fail, an
// expression that fails to compile denies every matched request in the
// cluster (or locks the platform agent out entirely), so no policy may
// merge with one.
func assertCompiles(t *testing.T, compiler *plugincel.CompositedCompiler, policyName string, accessor plugincel.ExpressionAccessor, opts plugincel.OptionalVariableDeclarations) {
	t.Helper()

	if result := compiler.CompileCELExpression(accessor, opts, environment.StoredExpressions); result.Error != nil {
		t.Errorf("policy %s: %q does not compile: %v", policyName, accessor.GetExpression(), result.Error)
	}
}

// namespaceMatches resolves a namespaceSelector against a rendered object's
// declared namespace. Only kubernetes.io/metadata.name is available at CI
// time (there are no live namespace labels in a checkout), so objects whose
// namespace is injected at apply time only meet unscoped selectors.
func namespaceMatches(t *testing.T, selector *metav1.LabelSelector, namespace string) bool {
	t.Helper()

	if selector == nil || (len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0) {
		return true
	}
	if namespace == "" {
		return false
	}
	compiled, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		t.Fatalf("namespaceSelector: %v", err)
	}
	return compiled.Matches(labels.Set{"kubernetes.io/metadata.name": namespace})
}

func resourceRulesMatch(rules []admissionregistrationv1.NamedRuleWithOperations, group, resource string) bool {
	for _, rule := range rules {
		if !containsOrStar(rule.APIGroups, group) || !containsOrStar(rule.Resources, resource) {
			continue
		}
		for _, op := range rule.Operations {
			if op == admissionregistrationv1.OperationAll || op == "CREATE" {
				return true
			}
		}
	}
	return false
}

func containsOrStar(values []string, want string) bool {
	for _, value := range values {
		if value == "*" || value == want {
			return true
		}
	}
	return false
}

func pluralResource(kind string) string {
	lower := strings.ToLower(kind)
	switch {
	case strings.HasSuffix(lower, "s"), strings.HasSuffix(lower, "x"), strings.HasSuffix(lower, "ch"):
		return lower + "es"
	case strings.HasSuffix(lower, "cy"), strings.HasSuffix(lower, "ry"), strings.HasSuffix(lower, "ty"):
		return lower[:len(lower)-1] + "ies"
	default:
		return lower + "s"
	}
}

// admissionInputs builds the same request shape the apiserver hands a
// policy: attributes for a Flux server-side apply CREATE of the object.
func admissionInputs(doc map[string]interface{}) (*unstructured.Unstructured, *admission.VersionedAttributes, schema.GroupVersionResource, *corev1.Namespace) {
	obj := &unstructured.Unstructured{Object: doc}
	gvk := obj.GroupVersionKind()
	gvr := schema.GroupVersionResource{Group: gvk.Group, Version: gvk.Version, Resource: pluralResource(gvk.Kind)}
	attrs := admission.NewAttributesRecord(
		obj, nil, gvk, obj.GetNamespace(), obj.GetName(), gvr, "",
		admission.Create, nil, false,
		&user.DefaultInfo{Name: "system:serviceaccount:cozy-fluxcd:kustomize-controller", Groups: []string{"system:serviceaccounts", "system:authenticated"}},
	)
	versioned := &admission.VersionedAttributes{Attributes: attrs, VersionedKind: gvk, VersionedObject: obj.DeepCopyObject()}
	namespace := &corev1.Namespace{}
	if ns := obj.GetNamespace(); ns != "" {
		namespace.Name = ns
		namespace.Labels = map[string]string{"kubernetes.io/metadata.name": ns}
	}
	return obj, versioned, gvr, namespace
}

func (u *policyUniverse) paramObject(t *testing.T, paramRef *admissionregistrationv1.ParamRef) runtime.Object {
	t.Helper()

	if paramRef == nil {
		return nil
	}
	data, ok := u.params[paramRef.Name]
	if !ok {
		return nil
	}
	values := map[string]interface{}{}
	for key, value := range data {
		values[key] = value
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": paramRef.Name, "namespace": paramRef.Namespace},
		"data":       values,
	}}
}

func matchesForEval(t *testing.T, matcher matchconditions.Matcher, versioned *admission.VersionedAttributes, params runtime.Object, namespace *corev1.Namespace) bool {
	t.Helper()

	if matcher == nil {
		return true
	}
	result := matcher.Match(context.Background(), versioned, params, denyAllAuthorizer{})
	if result.Error != nil {
		t.Fatalf("matchConditions eval error: %v", result.Error)
	}
	_ = namespace
	return result.Matches
}

// validateObject runs one VAP's validations against one object and reports
// each false validation with the policy's own message.
func (c *compiledVAP) validateObject(t *testing.T, u *policyUniverse, source string, versioned *admission.VersionedAttributes, gvr schema.GroupVersionResource, namespace *corev1.Namespace, params runtime.Object) {
	t.Helper()

	request := plugincel.CreateAdmissionRequest(versioned.Attributes, metav1.GroupVersionResource(gvr), metav1.GroupVersionKind(versioned.VersionedKind))
	results, _, err := c.validation.ForInput(context.Background(), versioned, request, plugincel.OptionalVariableBindings{VersionedParams: params, Authorizer: denyAllAuthorizer{}}, namespace, celconfig.RuntimeCELCostBudget)
	if err != nil {
		t.Errorf("%s: policy %s evaluation error: %v", source, c.policy.Name, err)
		return
	}
	for i, result := range results {
		if result.Error != nil {
			t.Errorf("%s: policy %s validation %d error: %v", source, c.policy.Name, i, result.Error)
			continue
		}
		if verdict, ok := result.EvalResult.Value().(bool); !ok || !verdict {
			message := c.policy.Spec.Validations[i].Message
			if message == "" {
				message = c.policy.Spec.Validations[i].Expression
			}
			t.Errorf("%s: denied by %s: %s", source, c.policy.Name, message)
		}
	}
}

// mutateObject applies one MAP to one object, asserting the mutation
// evaluates cleanly and is idempotent, and returns the mutated object.
func (c *compiledMAP) mutateObject(t *testing.T, source string, versioned *admission.VersionedAttributes, gvr schema.GroupVersionResource, namespace *corev1.Namespace, params runtime.Object) *unstructured.Unstructured {
	t.Helper()

	current := versioned
	for i, patcher := range c.patchers {
		mutated := applyPatcher(t, source, c.policy.Name, i, patcher, current, gvr, namespace, params)
		if mutated == nil {
			return nil
		}
		again := applyPatcher(t, source, c.policy.Name, i, patcher, versionedFrom(mutated, current), gvr, namespace, params)
		if again != nil && !reflect.DeepEqual(mutated.Object, again.Object) {
			t.Errorf("%s: policy %s mutation %d is not idempotent; a Flux re-apply would churn the object forever", source, c.policy.Name, i)
		}
		current = versionedFrom(mutated, current)
	}
	final, ok := current.VersionedObject.(*unstructured.Unstructured)
	if !ok {
		t.Fatalf("%s: policy %s produced %T, want *unstructured.Unstructured", source, c.policy.Name, current.VersionedObject)
	}
	return final
}

func applyPatcher(t *testing.T, source, policyName string, index int, patcher patch.Patcher, versioned *admission.VersionedAttributes, gvr schema.GroupVersionResource, namespace *corev1.Namespace, params runtime.Object) *unstructured.Unstructured {
	t.Helper()

	patched, err := patcher.Patch(context.Background(), patch.Request{
		MatchedResource:     gvr,
		VersionedAttributes: versioned,
		ObjectInterfaces:    admission.NewObjectInterfacesFromScheme(runtime.NewScheme()),
		OptionalVariables:   plugincel.OptionalVariableBindings{VersionedParams: params, Authorizer: denyAllAuthorizer{}},
		Namespace:           namespace,
		TypeConverter:       managedfields.NewDeducedTypeConverter(),
	}, celconfig.RuntimeCELCostBudget)
	if err != nil {
		t.Errorf("%s: policy %s mutation %d failed: %v", source, policyName, index, err)
		return nil
	}
	mutated, ok := patched.(*unstructured.Unstructured)
	if !ok {
		t.Fatalf("%s: policy %s mutation %d returned %T", source, policyName, index, patched)
		return nil
	}
	return mutated
}

func versionedFrom(obj *unstructured.Unstructured, prior *admission.VersionedAttributes) *admission.VersionedAttributes {
	return &admission.VersionedAttributes{Attributes: prior.Attributes, VersionedKind: prior.VersionedKind, VersionedObject: obj}
}

// TestAdmissionPoliciesAgainstRenderedManifests is the denial preflight: a
// manifest that a policy would reject at Flux apply time fails here, at PR
// time, with the policy's own message. Mutations are applied first (the
// apiserver orders mutating before validating admission), so validations see
// what the cluster would actually persist.
func TestAdmissionPoliciesAgainstRenderedManifests(t *testing.T) {
	u := loadPolicyUniverse(t)

	var vaps []*compiledVAP
	for _, policy := range u.validating {
		vaps = append(vaps, compileVAP(t, u, policy))
	}
	var maps []*compiledMAP
	for _, policy := range u.mutating {
		maps = append(maps, compileMAP(t, u, policy))
	}
	if t.Failed() {
		t.Fatal("compilation failures above; not evaluating")
	}

	evaluated := 0
	exercised := map[string]int{}
	for _, rendered := range u.objects {
		obj, versioned, gvr, namespace := admissionInputs(rendered.doc)
		source := fmt.Sprintf("%s (%s/%s)", filepath.Base(rendered.path), obj.GetKind(), obj.GetName())

		for _, compiled := range maps {
			if !resourceRulesMatch(compiled.policy.Spec.MatchConstraints.ResourceRules, gvr.Group, gvr.Resource) {
				continue
			}
			if !namespaceMatches(t, compiled.policy.Spec.MatchConstraints.NamespaceSelector, obj.GetNamespace()) {
				continue
			}
			for _, binding := range compiled.bindings {
				if binding.Spec.MatchResources != nil && !namespaceMatches(t, binding.Spec.MatchResources.NamespaceSelector, obj.GetNamespace()) {
					continue
				}
				params := u.paramObject(t, binding.Spec.ParamRef)
				if compiled.policy.Spec.ParamKind != nil && params == nil {
					t.Logf("%s: policy %s params %v not resolvable in a checkout; skipping mutation", source, compiled.policy.Name, binding.Spec.ParamRef)
					continue
				}
				if !matchesForEval(t, compiled.matcher, versioned, params, namespace) {
					continue
				}
				if mutated := compiled.mutateObject(t, source, versioned, gvr, namespace, params); mutated != nil {
					versioned = versionedFrom(mutated, versioned)
					evaluated++
					exercised[compiled.policy.Name]++
				}
				break
			}
		}

		for _, compiled := range vaps {
			if !resourceRulesMatch(compiled.policy.Spec.MatchConstraints.ResourceRules, gvr.Group, gvr.Resource) {
				continue
			}
			if !namespaceMatches(t, compiled.policy.Spec.MatchConstraints.NamespaceSelector, obj.GetNamespace()) {
				continue
			}
			for _, binding := range compiled.bindings {
				if binding.Spec.MatchResources != nil && !namespaceMatches(t, binding.Spec.MatchResources.NamespaceSelector, obj.GetNamespace()) {
					continue
				}
				params := u.paramObject(t, binding.Spec.ParamRef)
				if compiled.policy.Spec.ParamKind != nil && params == nil {
					t.Logf("%s: policy %s params %v not resolvable in a checkout; skipping evaluation", source, compiled.policy.Name, binding.Spec.ParamRef)
					continue
				}
				if !matchesForEval(t, compiled.matcher, versioned, params, namespace) {
					continue
				}
				compiled.validateObject(t, u, source, versioned, gvr, namespace, params)
				evaluated++
				exercised[compiled.policy.Name]++
				break
			}
		}
	}
	if evaluated == 0 {
		t.Fatal("no rendered object matched any policy; the harness matching logic is wrong")
	}

	// Every policy must be exercised somewhere — by rendered manifests here,
	// or by fixtures (request-shaped policies, deny cases, policies whose
	// subjects only exist at runtime). A policy exercised by neither is
	// invisible to CI: a matching bug, a binding typo, or a missing fixture.
	fixtureCases := loadFixtureCoverage(t)
	for _, compiled := range vaps {
		t.Logf("policy %s: %d rendered matches, %d fixture cases", compiled.policy.Name, exercised[compiled.policy.Name], fixtureCases[compiled.policy.Name])
		if exercised[compiled.policy.Name] == 0 && fixtureCases[compiled.policy.Name] == 0 {
			t.Errorf("policy %s is exercised by neither rendered manifests nor fixtures; add fixture cases or fix harness matching", compiled.policy.Name)
		}
	}
	for _, compiled := range maps {
		t.Logf("policy %s: %d rendered matches, %d fixture cases", compiled.policy.Name, exercised[compiled.policy.Name], fixtureCases[compiled.policy.Name])
		if exercised[compiled.policy.Name] == 0 && fixtureCases[compiled.policy.Name] == 0 {
			t.Errorf("policy %s is exercised by neither rendered manifests nor fixtures; add fixture cases or fix harness matching", compiled.policy.Name)
		}
	}
	t.Logf("evaluated %d policy×object matches", evaluated)
}

// Fixture format for policies whose behavior cannot be derived from rendered
// manifests: request-shaped rules (identity carve-outs, subresources), deny
// cases (the rendered tree is compliant by construction), and mutations
// whose subjects only exist at runtime:
//
//	policy: <policy name>
//	cases:
//	  - name: <case name>
//	    operation: CREATE | UPDATE | DELETE | CONNECT   # default CREATE
//	    resource: {group: "", version: v1, resource: pods}
//	    subResource: exec        # optional
//	    objectName: some-pod     # optional
//	    user: {username: "...", groups: ["..."]}        # optional
//	    object: {<full object YAML>}                    # optional
//	    expect: allow | deny | no-match | mutate
//	    expectField: {path: spec.x.y, value: "z"}       # with expect: mutate
type fixtureCase struct {
	Name        string `json:"name"`
	Operation   string `json:"operation"`
	Resource    struct {
		Group    string `json:"group"`
		Version  string `json:"version"`
		Resource string `json:"resource"`
	} `json:"resource"`
	SubResource string                 `json:"subResource"`
	ObjectName  string                 `json:"objectName"`
	Object      map[string]interface{} `json:"object"`
	User        struct {
		Username string   `json:"username"`
		Groups   []string `json:"groups"`
	} `json:"user"`
	Expect      string `json:"expect"`
	ExpectField struct {
		Path  string `json:"path"`
		Value string `json:"value"`
	} `json:"expectField"`
}

type policyFixture struct {
	Policy string        `json:"policy"`
	Cases  []fixtureCase `json:"cases"`
}

func loadFixtures(t *testing.T) []policyFixture {
	t.Helper()

	fixturesDir := runfilePath("src/infrastructure/tests/fixtures/admission")
	entries, err := os.ReadDir(fixturesDir)
	if err != nil {
		t.Fatalf("read fixtures dir: %v", err)
	}
	var fixtures []policyFixture
	for _, entry := range entries {
		var fixture policyFixture
		if err := sigsyaml.UnmarshalStrict([]byte(readText(t, filepath.Join(fixturesDir, entry.Name()))), &fixture); err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		if fixture.Policy == "" || len(fixture.Cases) == 0 {
			t.Fatalf("%s: fixture has no policy or no cases", entry.Name())
		}
		fixtures = append(fixtures, fixture)
	}
	if len(fixtures) == 0 {
		t.Fatal("no admission fixtures found")
	}
	return fixtures
}

func loadFixtureCoverage(t *testing.T) map[string]int {
	t.Helper()

	coverage := map[string]int{}
	for _, fixture := range loadFixtures(t) {
		coverage[fixture.Policy] += len(fixture.Cases)
	}
	return coverage
}

// fixtureInputs builds admission attributes for one fixture case; the object
// is optional (request-shaped policies never read it).
func fixtureInputs(tc fixtureCase) (*admission.VersionedAttributes, schema.GroupVersionResource, *corev1.Namespace) {
	gvr := schema.GroupVersionResource{Group: tc.Resource.Group, Version: tc.Resource.Version, Resource: tc.Resource.Resource}
	operation := admission.Create
	if tc.Operation != "" {
		operation = admission.Operation(tc.Operation)
	}
	username := tc.User.Username
	if username == "" {
		username = "fixture"
	}

	var obj runtime.Object
	gvk := schema.GroupVersionKind{Version: tc.Resource.Version}
	name, ns := tc.ObjectName, ""
	namespace := &corev1.Namespace{}
	if tc.Object != nil {
		unstructuredObj := &unstructured.Unstructured{Object: tc.Object}
		obj = unstructuredObj
		gvk = unstructuredObj.GroupVersionKind()
		if name == "" {
			name = unstructuredObj.GetName()
		}
		ns = unstructuredObj.GetNamespace()
		if ns != "" {
			namespace.Name = ns
			namespace.Labels = map[string]string{"kubernetes.io/metadata.name": ns}
		}
	}

	attrs := admission.NewAttributesRecord(
		obj, nil, gvk, ns, name, gvr, tc.SubResource, operation, nil, false,
		&user.DefaultInfo{Name: username, Groups: tc.User.Groups},
	)
	versioned := &admission.VersionedAttributes{Attributes: attrs, VersionedKind: gvk, VersionedObject: obj}
	return versioned, gvr, namespace
}

func firstBindingParams(t *testing.T, u *policyUniverse, policyName string) runtime.Object {
	t.Helper()

	for _, binding := range u.validatingBinds {
		if binding.Spec.PolicyName == policyName && binding.Spec.ParamRef != nil {
			return u.paramObject(t, binding.Spec.ParamRef)
		}
	}
	return nil
}

func TestAdmissionPolicyFixtures(t *testing.T) {
	u := loadPolicyUniverse(t)

	for _, fixture := range loadFixtures(t) {
		if vap := findVAP(u, fixture.Policy); vap != nil {
			compiled := compileVAP(t, u, *vap)
			params := firstBindingParams(t, u, fixture.Policy)
			for _, tc := range fixture.Cases {
				runValidatingFixture(t, fixture.Policy, compiled, tc, params)
			}
			continue
		}
		if mapPolicy := findMAP(u, fixture.Policy); mapPolicy != nil {
			compiled := compileMAP(t, u, *mapPolicy)
			for _, tc := range fixture.Cases {
				runMutatingFixture(t, fixture.Policy, compiled, tc)
			}
			continue
		}
		t.Errorf("fixture references policy %s, which no rendered manifest defines", fixture.Policy)
	}
}

func findVAP(u *policyUniverse, name string) *admissionregistrationv1.ValidatingAdmissionPolicy {
	for i := range u.validating {
		if u.validating[i].Name == name {
			return &u.validating[i]
		}
	}
	return nil
}

func findMAP(u *policyUniverse, name string) *admissionregistrationv1.MutatingAdmissionPolicy {
	for i := range u.mutating {
		if u.mutating[i].Name == name {
			return &u.mutating[i]
		}
	}
	return nil
}

func runValidatingFixture(t *testing.T, policyName string, compiled *compiledVAP, tc fixtureCase, params runtime.Object) {
	t.Helper()

	versioned, gvr, namespace := fixtureInputs(tc)
	matched := matchesForEval(t, compiled.matcher, versioned, params, namespace)
	if tc.Expect == "no-match" {
		if matched {
			t.Errorf("%s/%s: matched, want no-match", policyName, tc.Name)
		}
		return
	}
	if !matched {
		t.Errorf("%s/%s: did not match, want %s", policyName, tc.Name, tc.Expect)
		return
	}

	request := plugincel.CreateAdmissionRequest(versioned.Attributes, metav1.GroupVersionResource(gvr), metav1.GroupVersionKind(versioned.VersionedKind))
	results, _, err := compiled.validation.ForInput(context.Background(), versioned, request, plugincel.OptionalVariableBindings{VersionedParams: params, Authorizer: denyAllAuthorizer{}}, namespace, celconfig.RuntimeCELCostBudget)
	if err != nil {
		t.Errorf("%s/%s: evaluation error: %v", policyName, tc.Name, err)
		return
	}
	allowed := true
	for _, result := range results {
		// An eval error under failurePolicy Fail denies — but an expression
		// that errors instead of returning false is a latent lockout, so no
		// fixture may produce one.
		if result.Error != nil {
			t.Errorf("%s/%s: validation error (latent lockout): %v", policyName, tc.Name, result.Error)
			continue
		}
		if verdict, ok := result.EvalResult.Value().(bool); !ok || !verdict {
			allowed = false
		}
	}
	if want := tc.Expect == "allow"; allowed != want {
		t.Errorf("%s/%s: allowed=%v, want %s", policyName, tc.Name, allowed, tc.Expect)
	}
}

func runMutatingFixture(t *testing.T, policyName string, compiled *compiledMAP, tc fixtureCase) {
	t.Helper()

	if tc.Object == nil {
		t.Errorf("%s/%s: mutation fixtures need an object", policyName, tc.Name)
		return
	}
	versioned, gvr, namespace := fixtureInputs(tc)
	matched := matchesForEval(t, compiled.matcher, versioned, nil, namespace)
	if tc.Expect == "no-match" {
		if matched {
			t.Errorf("%s/%s: matched, want no-match", policyName, tc.Name)
		}
		return
	}
	if !matched {
		t.Errorf("%s/%s: did not match, want %s", policyName, tc.Name, tc.Expect)
		return
	}
	if tc.Expect != "mutate" {
		t.Errorf("%s/%s: mutating fixtures expect mutate or no-match, got %q", policyName, tc.Name, tc.Expect)
		return
	}

	source := fmt.Sprintf("fixture %s", tc.Name)
	mutated := compiled.mutateObject(t, source, versioned, gvr, namespace, nil)
	if mutated == nil {
		return
	}
	if tc.ExpectField.Path != "" {
		value, found, err := unstructured.NestedFieldNoCopy(mutated.Object, strings.Split(tc.ExpectField.Path, ".")...)
		if err != nil || !found {
			t.Errorf("%s/%s: mutated object has no %s (err=%v)", policyName, tc.Name, tc.ExpectField.Path, err)
			return
		}
		if got := fmt.Sprintf("%v", value); got != tc.ExpectField.Value {
			t.Errorf("%s/%s: %s = %q, want %q", policyName, tc.Name, tc.ExpectField.Path, got, tc.ExpectField.Value)
		}
	}
}
