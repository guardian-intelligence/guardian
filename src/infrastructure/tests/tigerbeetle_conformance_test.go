package tests

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
)

func TestTigerBeetleRuntimeConformance(t *testing.T) {
	const (
		addresses = "--addresses=10.8.0.11:3000,10.8.0.12:3000,10.8.0.13:3000"
		image     = "ghcr.io/tigerbeetle/tigerbeetle:0.17.9@sha256:48f623f9c1e9b6cc44d77ca93634595ae99cce3246ded418763eb1a62eee45e9"
		exporter  = "quay.io/prometheus/statsd-exporter:v0.30.0@sha256:378cb79c4ac7d6941e5ed71b1f3cd6f4707cf42c72c104ffe81fa4d9026ef659"
	)

	identityPath := runfilePath("src/infrastructure/deployments/tigerbeetle/system/identity.yaml")
	identity := readText(t, identityPath)

	for _, want := range []string{
		"name: tigerbeetle",
		"namespace: tenant-guardian",
		"storageClassName: local-encrypted-retain",
		"kustomize.toolkit.fluxcd.io/prune: disabled",
		"guardian.dev/data-classification: production",
		"automountServiceAccountToken: false",
	} {
		assertTextContains(t, identity, want, identityPath)
	}
	for _, forbidden := range []string{
		"replicated-encrypted",
		"synthetic-",
		"\nkind: Job\n",
		"\nkind: Deployment\n",
	} {
		assertTextNotContains(t, identity, forbidden, identityPath)
	}
	for value, want := range map[string]int{
		"kind: PersistentVolumeClaim": 3,
		"storage: 100Gi":              3,
	} {
		if got := strings.Count(identity, value); got != want {
			t.Fatalf("%s contains %q %d times, want %d", identityPath, value, got, want)
		}
	}

	replicasPath := runfilePath("src/infrastructure/deployments/tigerbeetle/system/replicas.yaml")
	replicas := readText(t, replicasPath)
	for _, want := range []string{
		"kind: PodDisruptionBudget",
		"minAvailable: 2",
		"strategy:",
		"type: Recreate",
		"hostNetwork: true",
		"dnsPolicy: Default",
		"automountServiceAccountToken: false",
		"runAsNonRoot: true",
		"allowPrivilegeEscalation: false",
		"readOnlyRootFilesystem: true",
		"- IPC_LOCK",
		"type: Unconfined",
		"- start",
		addresses,
		"--cache-grid=4GiB",
		"--experimental",
		"--statsd=127.0.0.1:8125",
		"failureThreshold: 720",
		image,
		exporter,
	} {
		assertTextContains(t, replicas, want, replicasPath)
	}
	for _, forbidden := range []string{
		"format",
		"privileged: true",
		"\nkind: Service\n",
		"\nkind: Ingress\n",
		"\nkind: Gateway\n",
		"hostPath:",
	} {
		assertTextNotContains(t, replicas, forbidden, replicasPath)
	}
	for value, want := range map[string]int{
		"kind: Deployment":           3,
		"hostNetwork: true":          3,
		"- start":                    3,
		addresses:                    3,
		"--cache-grid=4GiB":          3,
		image:                        3,
		exporter:                     3,
		"claimName: tigerbeetle-data": 3,
	} {
		if got := strings.Count(replicas, value); got != want {
			t.Fatalf("%s contains %q %d times, want %d", replicasPath, value, got, want)
		}
	}
	nodes := []string{"ash-earth", "ash-wind", "ash-water"}
	for replica, node := range nodes {
		index := fmt.Sprintf("%d", replica)
		for _, want := range []string{
			"name: tigerbeetle-" + index,
			"kubernetes.io/hostname: " + node,
			"claimName: tigerbeetle-data-" + index,
		} {
			assertTextContains(t, replicas, want, replicasPath)
		}
	}

	observabilityPath := runfilePath("src/infrastructure/deployments/tigerbeetle/system/observability.yaml")
	observability := readText(t, observabilityPath)
	for _, want := range []string{
		"name: tigerbeetle-metrics",
		"kind: VMServiceScrape",
		"kind: VMRule",
		"port: 9102",
		"tb_replica_status != 0",
		"tb_replica_sync_stage != 0",
		"TigerBeetleReplicaCountDegraded",
		"TigerBeetleProcessRestarted",
		"TigerBeetleVolumeSpaceLow",
	} {
		assertTextContains(t, observability, want, observabilityPath)
	}
	for _, forbidden := range []string{
		"port: 3000",
		"targetPort: replica",
		"\nkind: Ingress\n",
		"\nkind: Gateway\n",
	} {
		assertTextNotContains(t, observability, forbidden, observabilityPath)
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
		"object.spec.hostNetwork == true",
		"'kubernetes.io/hostname' in object.spec.nodeSelector",
		"has(volume.persistentVolumeClaim)",
		"container.securityContext.capabilities.drop.exists",
		"argument != 'format'",
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

func TestTigerBeetleFluxRuntimeGate(t *testing.T) {
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
		"kind: Deployment",
		"name: tigerbeetle-0",
		"name: tigerbeetle-1",
		"name: tigerbeetle-2",
	} {
		assertTextContains(t, slice, want, path)
	}
	assertTextNotContains(t, slice, "kind: Job", path)
	assertTextNotContains(t, slice, "tigerbeetle-format", path)
}
