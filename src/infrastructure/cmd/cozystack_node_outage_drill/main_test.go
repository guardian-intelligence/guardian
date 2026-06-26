package main

import "testing"

func testConfig() drillConfig {
	return drillConfig{
		Kubectl:                "/kubectl",
		RequestTimeout:         "15s",
		DrainTimeout:           "10m",
		WaitTimeout:            "15m",
		Node:                   "ash-earth",
		ConfirmNode:            "ash-earth",
		OpenBaoNamespace:       "tenant-guardian-kms",
		OpenBaoApp:             "guardian",
		OpenBaoStatefulSet:     "openbao-guardian",
		OpenBaoBootstrapSecret: "openbao-guardian-bootstrap",
	}
}

func TestValidateConfig(t *testing.T) {
	base := testConfig()
	if err := validateConfig(base); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	missingConfirm := base
	missingConfirm.ConfirmNode = ""
	if err := validateConfig(missingConfirm); err == nil {
		t.Fatalf("config without matching confirm node was accepted")
	}

	badNode := base
	badNode.Node = "Not_A_Node"
	badNode.ConfirmNode = "Not_A_Node"
	if err := validateConfig(badNode); err == nil {
		t.Fatalf("invalid node name was accepted")
	}

	missingTimeout := base
	missingTimeout.WaitTimeout = ""
	if err := validateConfig(missingTimeout); err == nil {
		t.Fatalf("empty timeout was accepted")
	}

	badOpenBaoName := base
	badOpenBaoName.OpenBaoBootstrapSecret = "Not_A_Secret"
	if err := validateConfig(badOpenBaoName); err == nil {
		t.Fatalf("invalid OpenBao bootstrap Secret name was accepted")
	}
}

func TestDrainArgsRespectPDBs(t *testing.T) {
	got := drainArgs(drillConfig{
		Node:         "ash-earth",
		DrainTimeout: "10m",
	})
	want := []string{
		"drain",
		"ash-earth",
		"--ignore-daemonsets",
		"--delete-emptydir-data",
		"--timeout=10m",
	}
	if len(got) != len(want) {
		t.Fatalf("drainArgs length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("drainArgs[%d] = %q, want %q: %#v", i, got[i], want[i], got)
		}
	}
	for _, arg := range got {
		if arg == "--force" || arg == "--disable-eviction" {
			t.Fatalf("drainArgs includes unsafe bypass %q: %#v", arg, got)
		}
	}
}

func TestNodeReadyArgs(t *testing.T) {
	got := nodeReadyArgs("ash-earth", "15m")
	want := []string{"wait", "--for=condition=Ready", "node/ash-earth", "--timeout=15m"}
	if len(got) != len(want) {
		t.Fatalf("nodeReadyArgs length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("nodeReadyArgs[%d] = %q, want %q: %#v", i, got[i], want, got)
		}
	}
}

func TestNodeUnschedulableArgs(t *testing.T) {
	got := nodeUnschedulableArgs("ash-earth", "15m")
	want := []string{"wait", "--for=jsonpath={.spec.unschedulable}=true", "node/ash-earth", "--timeout=15m"}
	if len(got) != len(want) {
		t.Fatalf("nodeUnschedulableArgs length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("nodeUnschedulableArgs[%d] = %q, want %q: %#v", i, got[i], want, got)
		}
	}
}

func TestOutageWaitsProveServicesWhileNodeCordoned(t *testing.T) {
	got := outageWaits(testConfig())
	requireCheck(t, got, "wait outage target node cordoned", "node/ash-earth", "--for=jsonpath={.spec.unschedulable}=true", "--timeout=15m")
	requireCheck(t, got, "wait outage openbao authority app", "tenant-guardian-kms", "openbaos.apps.cozystack.io/guardian", "--timeout=15m")
	rejectCheck(t, got, "wait outage openbao authority statefulset")
	requireCheck(t, got, "wait outage tenant-root postgres workloads", "postgreses.apps.cozystack.io/guardian", "--timeout=15m")
	requireCheck(t, got, "wait outage tenant-root harbor workloads", "harbors.apps.cozystack.io/guardian", "--timeout=15m")
	requireCheck(t, got, "wait outage tenant-root harbor registry bucket ready", "--for=jsonpath={.status.bucketReady}=true", "bucketclaims.objectstorage.k8s.io/harbor-guardian-registry", "--timeout=15m")
	requireCheck(t, got, "wait outage tenant-root harbor registry bucket access granted", "--for=jsonpath={.status.accessGranted}=true", "bucketaccesses.objectstorage.k8s.io/harbor-guardian-registry", "--timeout=15m")
	requireCheck(t, got, "wait outage tenant-root clickhouse workloads", "clickhouses.apps.cozystack.io/guardian", "--timeout=15m")
	requireCheck(t, got, "wait outage dashboard console deployment", "deployment/cozy-dashboard-console", "--timeout=15m")
}

func TestRecoveryWaitsCoverGuardianSurfaces(t *testing.T) {
	got := recoveryWaits(testConfig())
	requireCheck(t, got, "wait recovered target node Ready", "node/ash-earth", "--timeout=15m")
	requireCheck(t, got, "wait recovered openbao authority app", "tenant-guardian-kms", "openbaos.apps.cozystack.io/guardian", "--timeout=15m")
	rejectCheck(t, got, "wait recovered openbao authority statefulset")
	requireCheck(t, got, "wait recovered tenant-root postgres workloads", "postgreses.apps.cozystack.io/guardian", "--timeout=15m")
	requireCheck(t, got, "wait recovered tenant-root harbor workloads", "harbors.apps.cozystack.io/guardian", "--timeout=15m")
	requireCheck(t, got, "wait recovered tenant-root harbor registry bucket ready", "--for=jsonpath={.status.bucketReady}=true", "bucketclaims.objectstorage.k8s.io/harbor-guardian-registry", "--timeout=15m")
	requireCheck(t, got, "wait recovered tenant-root harbor registry bucket access granted", "--for=jsonpath={.status.accessGranted}=true", "bucketaccesses.objectstorage.k8s.io/harbor-guardian-registry", "--timeout=15m")
	requireCheck(t, got, "wait recovered tenant-root clickhouse workloads", "clickhouses.apps.cozystack.io/guardian", "--timeout=15m")
	requireCheck(t, got, "wait recovered dashboard console deployment", "deployment/cozy-dashboard-console", "--timeout=15m")
}

func TestQuorumForReplicas(t *testing.T) {
	for _, tc := range []struct {
		replicas int
		want     int
	}{
		{replicas: 1, want: 1},
		{replicas: 2, want: 2},
		{replicas: 3, want: 2},
		{replicas: 4, want: 3},
		{replicas: 5, want: 3},
	} {
		if got := quorumForReplicas(tc.replicas); got != tc.want {
			t.Fatalf("quorumForReplicas(%d) = %d, want %d", tc.replicas, got, tc.want)
		}
	}
}

func TestParseStatefulSetReplicas(t *testing.T) {
	got, err := parseStatefulSetReplicas(`{"spec":{"replicas":3},"status":{"readyReplicas":2}}`)
	if err != nil {
		t.Fatalf("parseStatefulSetReplicas() error = %v", err)
	}
	if got.Replicas != 3 || got.ReadyReplicas != 2 {
		t.Fatalf("parseStatefulSetReplicas() = %#v", got)
	}

	got, err = parseStatefulSetReplicas(`{"spec":{},"status":{}}`)
	if err != nil {
		t.Fatalf("parseStatefulSetReplicas() default replicas error = %v", err)
	}
	if got.Replicas != 1 || got.ReadyReplicas != 0 {
		t.Fatalf("parseStatefulSetReplicas() default = %#v", got)
	}

	if _, err := parseStatefulSetReplicas(`{"spec":{"replicas":0},"status":{}}`); err == nil {
		t.Fatalf("zero replica StatefulSet accepted")
	}
}

func TestParsePodContainerRunning(t *testing.T) {
	running, err := parsePodContainerRunning(`{"status":{"containerStatuses":[{"name":"openbao","state":{"running":{"startedAt":"2026-06-23T00:00:00Z"}}}]}}`, "openbao")
	if err != nil {
		t.Fatalf("parsePodContainerRunning() error = %v", err)
	}
	if !running {
		t.Fatalf("running container was not detected")
	}

	running, err = parsePodContainerRunning(`{"status":{"containerStatuses":[{"name":"openbao","state":{"waiting":{"reason":"ContainerCreating"}}}]}}`, "openbao")
	if err != nil {
		t.Fatalf("parsePodContainerRunning() waiting error = %v", err)
	}
	if running {
		t.Fatalf("waiting container detected as running")
	}

	running, err = parsePodContainerRunning(`{"status":{"containerStatuses":[]}}`, "openbao")
	if err != nil {
		t.Fatalf("parsePodContainerRunning() missing error = %v", err)
	}
	if running {
		t.Fatalf("missing container detected as running")
	}
}

func TestOpenBaoHelpers(t *testing.T) {
	status, err := parseBaoStatus("wrapper warning\n{\"initialized\":true,\"sealed\":false}\n")
	if err != nil {
		t.Fatalf("parseBaoStatus() error = %v", err)
	}
	if !status.Initialized || status.Sealed {
		t.Fatalf("parseBaoStatus() = %#v", status)
	}
	if !looksLikeBaoStatusJSON("warning\n{\"initialized\":false,\"sealed\":true}\n") {
		t.Fatalf("looksLikeBaoStatusJSON() rejected status payload")
	}
	if got := podName("openbao-guardian", 2); got != "openbao-guardian-2" {
		t.Fatalf("podName() = %q", got)
	}

	args := baoExecArgs("tenant-root", "openbao-guardian-0", "root-token", "status")
	for _, want := range []string{"-n", "tenant-root", "pod/openbao-guardian-0", "BAO_ADDR=http://127.0.0.1:8200", "BAO_TOKEN=root-token", "bao", "status"} {
		if !hasArg(args, want) {
			t.Fatalf("baoExecArgs missing %q: %#v", want, args)
		}
	}
	if got := redactToken("token=root-token", "root-token"); got != "token=<redacted>" {
		t.Fatalf("redactToken() = %q", got)
	}
}

func requireCheck(t *testing.T, checks []kubectlCheck, label string, parts ...string) {
	t.Helper()
	for _, check := range checks {
		if check.Label != label {
			continue
		}
		for _, part := range parts {
			if !hasArg(check.Args, part) {
				t.Fatalf("%s missing arg %q: %#v", label, part, check.Args)
			}
		}
		return
	}
	t.Fatalf("missing check %q in %#v", label, checks)
}

func rejectCheck(t *testing.T, checks []kubectlCheck, label string) {
	t.Helper()
	for _, check := range checks {
		if check.Label == label {
			t.Fatalf("unexpected check %q in %#v", label, checks)
		}
	}
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
