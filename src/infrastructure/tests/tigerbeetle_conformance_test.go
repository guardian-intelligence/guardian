package tests

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
)

func TestTigerBeetleBootstrapConformance(t *testing.T) {
	const (
		clusterID = "49532141921164377784457307205600684260"
		image     = "ghcr.io/tigerbeetle/tigerbeetle:0.17.9@sha256:48f623f9c1e9b6cc44d77ca93634595ae99cce3246ded418763eb1a62eee45e9"
	)

	path := runfilePath("src/infrastructure/deployments/tigerbeetle/system/bootstrap.yaml")
	raw := readText(t, path)

	for _, want := range []string{
		"name: tigerbeetle",
		"namespace: tenant-guardian",
		"storageClassName: local-encrypted-retain",
		"kustomize.toolkit.fluxcd.io/prune: disabled",
		"guardian.dev/data-classification: production",
		"automountServiceAccountToken: false",
		"runAsNonRoot: true",
		"allowPrivilegeEscalation: false",
		"readOnlyRootFilesystem: true",
		"- IPC_LOCK",
		"type: Unconfined",
		"--cluster=" + clusterID,
		"--replica-count=3",
		image,
	} {
		assertTextContains(t, raw, want, path)
	}
	for _, forbidden := range []string{
		"replicated-encrypted",
		"synthetic-",
		"hostNetwork: true",
		"privileged: true",
		"\nkind: Service\n",
		"\nkind: Ingress\n",
	} {
		assertTextNotContains(t, raw, forbidden, path)
	}
	for value, want := range map[string]int{
		"kind: PersistentVolumeClaim": 3,
		"storage: 100Gi":              3,
		"kind: Job":                   3,
		"--cluster=" + clusterID:      3,
		"--replica-count=3":           3,
		image:                         3,
	} {
		if got := strings.Count(raw, value); got != want {
			t.Fatalf("%s contains %q %d times, want %d", path, value, got, want)
		}
	}

	nodes := []string{"ash-earth", "ash-wind", "ash-water"}
	for replica, node := range nodes {
		index := fmt.Sprintf("%d", replica)
		for _, want := range []string{
			"name: tigerbeetle-data-" + index,
			"name: tigerbeetle-format-" + index,
			"kubernetes.io/hostname: " + node,
			"--replica=" + index,
			"claimName: tigerbeetle-data-" + index,
		} {
			assertTextContains(t, raw, want, path)
		}
	}
}

func TestTigerBeetleAdmissionAndNetworkBoundary(t *testing.T) {
	admissionPath := runfilePath("src/infrastructure/base/admission/tigerbeetle-host-network.yaml")
	admission := readText(t, admissionPath)
	policy := findDoc(t, yamlDocs(t, admissionPath), "ValidatingAdmissionPolicy", "guardian-tigerbeetle-runtime")
	for _, want := range []string{
		"name: guardian-tigerbeetle-runtime",
		"failurePolicy: Fail",
		"object.spec.serviceAccountName == 'tigerbeetle'",
		"object.spec.hostNetwork == false",
		"variables.images.all(image, image in variables.allowedImages)",
		"object.spec.automountServiceAccountToken == false",
		"container.securityContext.allowPrivilegeEscalation == false",
		"container.securityContext.readOnlyRootFilesystem == true",
		"validationActions:",
		"- Deny",
	} {
		assertTextContains(t, admission, want, admissionPath)
	}
	env, err := cel.NewEnv(
		cel.Variable("object", cel.DynType),
		cel.Variable("variables", cel.DynType),
	)
	if err != nil {
		t.Fatalf("CEL environment: %v", err)
	}
	for _, variable := range sliceValue(nestedValue(t, policy, "spec", "variables")) {
		expression := stringValue(mapValue(variable)["expression"])
		if _, issues := env.Compile(expression); issues != nil && issues.Err() != nil {
			t.Fatalf("compile TigerBeetle admission variable %q: %v", expression, issues.Err())
		}
	}
	for _, validation := range sliceValue(nestedValue(t, policy, "spec", "validations")) {
		expression := stringValue(mapValue(validation)["expression"])
		if _, issues := env.Compile(expression); issues != nil && issues.Err() != nil {
			t.Fatalf("compile TigerBeetle admission validation %q: %v", expression, issues.Err())
		}
	}

	templatePath := runfilePath("src/infrastructure/talm/templates/_helpers.tpl")
	template := readText(t, templatePath)
	for _, want := range []string{
		"name: cluster-node-tcp",
		"name: cluster-pod-tcp",
		"range $.Values.ingressFirewall.podTCPPorts",
		"name: cluster-internal-udp",
	} {
		assertTextContains(t, template, want, templatePath)
	}

	valuesPath := runfilePath("src/infrastructure/talm/values.yaml")
	values := readText(t, valuesPath)
	assertTextContains(t, values, `- "1-2999"`, valuesPath)
	assertTextContains(t, values, `- "3001-65535"`, valuesPath)

	for _, node := range []string{"ash-earth", "ash-wind", "ash-water"} {
		path := runfilePath("src/infrastructure/talm/nodes/" + node + ".yaml")
		raw := readText(t, path)
		start := strings.Index(raw, "name: cluster-pod-tcp")
		if start < 0 {
			t.Fatalf("%s has no cluster-pod-tcp rule", path)
		}
		end := strings.Index(raw[start:], "\n---")
		if end < 0 {
			t.Fatalf("%s cluster-pod-tcp rule has no document boundary", path)
		}
		podRule := raw[start : start+end]
		assertTextContains(t, podRule, "- 1-2999", path)
		assertTextContains(t, podRule, "- 3001-65535", path)
		assertTextNotContains(t, podRule, "\n    - 3000\n", path)
	}
}

func TestTigerBeetleFluxBootstrapGate(t *testing.T) {
	path := runfilePath("src/infrastructure/base/flux/sync.yaml")
	raw := readText(t, path)
	start := strings.Index(raw, "name: guardian-tigerbeetle")
	if start < 0 {
		t.Fatalf("%s has no guardian-tigerbeetle Kustomization", path)
	}
	end := strings.Index(raw[start:], "\n---")
	if end < 0 {
		t.Fatalf("%s guardian-tigerbeetle Kustomization has no document boundary", path)
	}
	slice := raw[start : start+end]
	for _, want := range []string{
		"path: ./src/infrastructure/deployments/tigerbeetle/system",
		"- name: guardian-mgmt-storage",
		"- name: guardian-mgmt-admission",
		"name: tigerbeetle-format-0",
		"name: tigerbeetle-format-1",
		"name: tigerbeetle-format-2",
	} {
		assertTextContains(t, slice, want, path)
	}
}
