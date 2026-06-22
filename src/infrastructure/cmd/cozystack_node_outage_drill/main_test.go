package main

import "testing"

func TestValidateConfig(t *testing.T) {
	base := drillConfig{
		Kubectl:        "/kubectl",
		RequestTimeout: "15s",
		DrainTimeout:   "10m",
		WaitTimeout:    "15m",
		Node:           "ash-earth",
		ConfirmNode:    "ash-earth",
	}
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
	got := outageWaits("ash-earth", "15m")
	requireCheck(t, got, "wait outage target node cordoned", "node/ash-earth", "--for=jsonpath={.spec.unschedulable}=true", "--timeout=15m")
	requireCheck(t, got, "wait outage root openbao statefulset", "statefulset.apps/openbao-guardian", "--timeout=15m")
	requireCheck(t, got, "wait outage tenant-dev postgres workloads", "postgreses.apps.cozystack.io/guardian", "--timeout=15m")
	requireCheck(t, got, "wait outage tenant-gamma harbor workloads", "harbors.apps.cozystack.io/guardian", "--timeout=15m")
	requireCheck(t, got, "wait outage tenant-gamma harbor registry bucket ready", "--for=jsonpath={.status.bucketReady}=true", "bucketclaims.objectstorage.k8s.io/harbor-guardian-registry", "--timeout=15m")
	requireCheck(t, got, "wait outage tenant-gamma harbor registry bucket access granted", "--for=jsonpath={.status.accessGranted}=true", "bucketaccesses.objectstorage.k8s.io/harbor-guardian-registry", "--timeout=15m")
	requireCheck(t, got, "wait outage tenant-prod clickhouse workloads", "clickhouses.apps.cozystack.io/guardian", "--timeout=15m")
	requireCheck(t, got, "wait outage tenant-prod company-site deployment", "deployment/company-site", "--timeout=15m")
	requireCheck(t, got, "wait outage dashboard console deployment", "deployment/cozy-dashboard-console", "--timeout=15m")
}

func TestRecoveryWaitsCoverGuardianSurfaces(t *testing.T) {
	got := recoveryWaits("ash-earth", "15m")
	requireCheck(t, got, "wait recovered target node Ready", "node/ash-earth", "--timeout=15m")
	requireCheck(t, got, "wait recovered root openbao statefulset", "statefulset.apps/openbao-guardian", "--timeout=15m")
	requireCheck(t, got, "wait recovered tenant-dev postgres workloads", "postgreses.apps.cozystack.io/guardian", "--timeout=15m")
	requireCheck(t, got, "wait recovered tenant-gamma harbor workloads", "harbors.apps.cozystack.io/guardian", "--timeout=15m")
	requireCheck(t, got, "wait recovered tenant-gamma harbor registry bucket ready", "--for=jsonpath={.status.bucketReady}=true", "bucketclaims.objectstorage.k8s.io/harbor-guardian-registry", "--timeout=15m")
	requireCheck(t, got, "wait recovered tenant-gamma harbor registry bucket access granted", "--for=jsonpath={.status.accessGranted}=true", "bucketaccesses.objectstorage.k8s.io/harbor-guardian-registry", "--timeout=15m")
	requireCheck(t, got, "wait recovered tenant-prod clickhouse workloads", "clickhouses.apps.cozystack.io/guardian", "--timeout=15m")
	requireCheck(t, got, "wait recovered tenant-prod company-site deployment", "deployment/company-site", "--timeout=15m")
	requireCheck(t, got, "wait recovered dashboard console deployment", "deployment/cozy-dashboard-console", "--timeout=15m")
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

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
