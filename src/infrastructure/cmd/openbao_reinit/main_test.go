package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalRevisionProblemRejectsStaleCheckout(t *testing.T) {
	if err := localRevisionProblem("aaa", "aaa"); err != nil {
		t.Fatalf("localRevisionProblem() rejected a matching checkout: %v", err)
	}
	err := localRevisionProblem("aaa", "bbb")
	if err == nil {
		t.Fatalf("localRevisionProblem() accepted a stale checkout")
	}
	if !strings.Contains(err.Error(), "import plan") {
		t.Fatalf("localRevisionProblem() error = %v, want stale-import-plan detail", err)
	}
}

func TestDirtyTreeProblem(t *testing.T) {
	if err := dirtyTreeProblem(""); err != nil {
		t.Fatalf("dirtyTreeProblem() rejected a clean tree: %v", err)
	}
	if err := dirtyTreeProblem("\n"); err != nil {
		t.Fatalf("dirtyTreeProblem() rejected whitespace-only status: %v", err)
	}
	// An uncommitted import-plan edit is exactly the case the gate exists for:
	// HEAD can equal origin/main while the built importer carries the edit.
	status := " M src/infrastructure/cmd/openbao_secret_import/main.go\n?? scratch.env\n"
	err := dirtyTreeProblem(status)
	if err == nil {
		t.Fatalf("dirtyTreeProblem() accepted a dirty tree")
	}
	if !strings.Contains(err.Error(), "openbao_secret_import/main.go") || !strings.Contains(err.Error(), "scratch.env") {
		t.Fatalf("dirtyTreeProblem() error = %v, want it to name the dirty files", err)
	}
}

func TestResolveReplicas(t *testing.T) {
	if got, err := resolveReplicas(0, 3); err != nil || got != 3 {
		t.Fatalf("resolveReplicas(0, 3) = %d, %v; want 3 from the live spec", got, err)
	}
	if got, err := resolveReplicas(3, 3); err != nil || got != 3 {
		t.Fatalf("resolveReplicas(3, 3) = %d, %v; want agreement accepted", got, err)
	}
	// A stale flag against a moved member count would under-delete PVCs and
	// leave pre-wipe raft state to crashloop unattended.
	if _, err := resolveReplicas(3, 5); err == nil {
		t.Fatalf("resolveReplicas(3, 5) accepted a flag that disagrees with the live spec")
	} else if !strings.Contains(err.Error(), "5") {
		t.Fatalf("resolveReplicas(3, 5) error = %v, want the live count named", err)
	}
	if got, err := resolveReplicas(3, 0); err != nil || got != 3 {
		t.Fatalf("resolveReplicas(3, 0) = %d, %v; want explicit flag to recover an interrupted reset", got, err)
	}
	if _, err := resolveReplicas(0, 0); err == nil {
		t.Fatalf("resolveReplicas(0, 0) accepted a scaled-to-zero StatefulSet without an explicit --replicas")
	}
}

func TestImporterProblem(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-importer")
	if err := importerProblem(missing); err == nil {
		t.Fatalf("importerProblem() accepted a missing binary (would be discovered only after the raft wipe)")
	}
	notExecutable := filepath.Join(dir, "importer-plain")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := importerProblem(notExecutable); err == nil {
		t.Fatalf("importerProblem() accepted a non-executable file")
	}
	if err := importerProblem(dir); err == nil {
		t.Fatalf("importerProblem() accepted a directory")
	}
	executable := filepath.Join(dir, "importer")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := importerProblem(executable); err != nil {
		t.Fatalf("importerProblem() rejected an executable binary: %v", err)
	}
}

func TestKustomizationProblems(t *testing.T) {
	ready := `{"status":{"lastAppliedRevision":"main@sha1:abc123","conditions":[{"type":"Ready","status":"True","reason":"ReconciliationSucceeded","message":"Applied revision: main@sha1:abc123"}]}}`
	if problems := kustomizationProblems(ready, "abc123"); len(problems) != 0 {
		t.Fatalf("kustomizationProblems() = %v, want none", problems)
	}
	if problems := kustomizationProblems(ready, "def456"); len(problems) != 1 || !strings.Contains(problems[0], "def456") {
		t.Fatalf("kustomizationProblems() = %v, want revision mismatch", problems)
	}
	notReady := `{"status":{"lastAppliedRevision":"main@sha1:abc123","conditions":[{"type":"Ready","status":"False","reason":"BuildFailed","message":"kustomize build failed"}]}}`
	problems := kustomizationProblems(notReady, "abc123")
	if len(problems) != 1 || !strings.Contains(problems[0], "BuildFailed") {
		t.Fatalf("kustomizationProblems() = %v, want Ready problem with reason", problems)
	}
}

func TestNamespaceHasPrivilegedEnforce(t *testing.T) {
	labeled := `{"metadata":{"labels":{"pod-security.kubernetes.io/enforce":"privileged"}}}`
	ok, err := namespaceHasPrivilegedEnforce(labeled)
	if err != nil || !ok {
		t.Fatalf("namespaceHasPrivilegedEnforce() = %t, %v; want true", ok, err)
	}
	// Today's incident shape: the tenant-chart regeneration stomped the
	// postRenderer and the label is simply gone.
	stomped := `{"metadata":{"labels":{"kubernetes.io/metadata.name":"tenant-guardian"}}}`
	ok, err = namespaceHasPrivilegedEnforce(stomped)
	if err != nil || ok {
		t.Fatalf("namespaceHasPrivilegedEnforce() = %t, %v; want false", ok, err)
	}
}

func TestReconcileHandled(t *testing.T) {
	stamp := "2026-07-08T00:00:00Z"
	handled := fmt.Sprintf(`{"status":{"lastHandledReconcileAt":%q,"conditions":[{"type":"Ready","status":"True"}]}}`, stamp)
	ok, err := reconcileHandled(handled, stamp)
	if err != nil || !ok {
		t.Fatalf("reconcileHandled() = %t, %v; want true", ok, err)
	}
	stale := `{"status":{"lastHandledReconcileAt":"older","conditions":[{"type":"Ready","status":"True"}]}}`
	ok, err = reconcileHandled(stale, stamp)
	if err != nil || ok {
		t.Fatalf("reconcileHandled() = %t, %v; want false for unhandled request", ok, err)
	}
}

func TestDataPVCNames(t *testing.T) {
	got := dataPVCNames("guardian-openbao", 3)
	want := []string{"data-guardian-openbao-0", "data-guardian-openbao-1", "data-guardian-openbao-2"}
	if len(got) != len(want) {
		t.Fatalf("dataPVCNames() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dataPVCNames() = %v, want %v", got, want)
		}
	}
}

func podListJSON(pods ...string) string {
	return `{"items":[` + strings.Join(pods, ",") + `]}`
}

func readyPod(name string) string {
	return fmt.Sprintf(`{"metadata":{"name":%q},"status":{"phase":"Running","containerStatuses":[{"name":"openbao","ready":true,"state":{"running":{}}},{"name":"audit","ready":true,"state":{"running":{}}}]}}`, name)
}

func crashLoopPod(name string) string {
	return fmt.Sprintf(`{"metadata":{"name":%q},"status":{"phase":"Running","containerStatuses":[{"name":"openbao","ready":false,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}`, name)
}

func TestClassifyPodsAllReady(t *testing.T) {
	raw := podListJSON(readyPod("guardian-openbao-0"), readyPod("guardian-openbao-1"), readyPod("guardian-openbao-2"))
	state, err := classifyPods(raw, 3)
	if err != nil {
		t.Fatalf("classifyPods() error = %v", err)
	}
	if !state.AllReady || len(state.CrashLooping) != 0 {
		t.Fatalf("classifyPods() = %+v, want all ready", state)
	}
}

func TestClassifyPodsDetectsCrashLoop(t *testing.T) {
	raw := podListJSON(readyPod("guardian-openbao-0"), crashLoopPod("guardian-openbao-1"))
	state, err := classifyPods(raw, 3)
	if err != nil {
		t.Fatalf("classifyPods() error = %v", err)
	}
	if state.AllReady {
		t.Fatalf("classifyPods() reported ready with a crashlooping pod")
	}
	if len(state.CrashLooping) != 1 || state.CrashLooping[0] != "guardian-openbao-1" {
		t.Fatalf("classifyPods() crashloops = %v, want guardian-openbao-1", state.CrashLooping)
	}
}

func TestClassifyPodsRejectsMissingMembers(t *testing.T) {
	raw := podListJSON(readyPod("guardian-openbao-0"))
	state, err := classifyPods(raw, 3)
	if err != nil {
		t.Fatalf("classifyPods() error = %v", err)
	}
	if state.AllReady {
		t.Fatalf("classifyPods() reported ready with 1/3 pods")
	}
	if !strings.Contains(state.Summary, "1/3 pods exist") {
		t.Fatalf("classifyPods() summary = %q, want member-count detail", state.Summary)
	}
}

func TestDirtyRaftState(t *testing.T) {
	logs := "core: failed to initialize: error=\"cluster already has state\""
	if !dirtyRaftState(logs) {
		t.Fatalf("dirtyRaftState() missed the marker")
	}
	if dirtyRaftState("permission denied on /openbao/data") {
		t.Fatalf("dirtyRaftState() matched an unrelated failure")
	}
}

func TestImporterArgsSequence(t *testing.T) {
	opts := options{
		Kubectl:        "/tools/kubectl",
		Kubeconfig:     "/home/op/.kube/config",
		RequestTimeout: "15s",
		Namespace:      "tenant-guardian",
		Service:        "guardian-openbao-active",
		CASecret:       "guardian-openbao-api-tls",
		EnvFile:        "/dev/shm/guardian-custody/import.env",
	}
	args := strings.Join(importerArgs(opts, 43210), " ")
	for _, want := range []string{
		"--kubectl /tools/kubectl",
		"--env-file /dev/shm/guardian-custody/import.env",
		"--delete-env-file",
		"--local-port 43210",
		"--kubeconfig /home/op/.kube/config",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("importerArgs() = %q, want it to contain %q", args, want)
		}
	}
	if strings.Contains(args, "--kube-api-server") {
		t.Fatalf("importerArgs() = %q, must omit an unset --kube-api-server", args)
	}
}

func TestRelayPlanCoversTheFourGeneratedValues(t *testing.T) {
	plan := relayPlan()
	if len(plan) != 4 {
		t.Fatalf("relayPlan() has %d targets, want 4", len(plan))
	}
	wantPaths := map[string]string{
		"kv/data/guardian/guardian-mgmt/guardian-analytics/clickhouse": "guardian-analytics",
		"kv/data/guardian/guardian-mgmt/postflight-runner/postgres":    "postflight-runner",
		"kv/data/guardian/guardian-mgmt/external-dns/cloudflare":       "external-dns",
		"kv/data/guardian/guardian-mgmt/tenant-root/backups-r2":        "tenant-root",
	}
	for _, target := range plan {
		consumer, ok := wantPaths[target.APIPath]
		if !ok {
			t.Fatalf("relayPlan() has unexpected path %s", target.APIPath)
		}
		if target.ConsumerNamespace != consumer {
			t.Fatalf("relayPlan() %s writes as %s, want %s (the scoped writer role must match the path's namespace subtree)", target.APIPath, target.ConsumerNamespace, consumer)
		}
		// Every write path must live inside the consumer namespace's own
		// subtree; the server enforces this, catch it before a live run.
		prefix := "kv/data/guardian/guardian-mgmt/" + target.ConsumerNamespace + "/"
		if !strings.HasPrefix(target.APIPath, prefix) {
			t.Fatalf("relayPlan() %s escapes the %s subtree", target.APIPath, target.ConsumerNamespace)
		}
		if len(target.Keys) == 0 || target.MissingHint == "" {
			t.Fatalf("relayPlan() %s has no keys or no missing-source hint", target.APIPath)
		}
		delete(wantPaths, target.APIPath)
	}
	if len(wantPaths) != 0 {
		t.Fatalf("relayPlan() is missing paths: %v", wantPaths)
	}
}

func TestRelayPlanSources(t *testing.T) {
	sources := map[string][2]string{}
	for _, target := range relayPlan() {
		sources[target.APIPath] = [2]string{target.SourceNamespace, target.SourceSecret}
	}
	want := map[string][2]string{
		"kv/data/guardian/guardian-mgmt/guardian-analytics/clickhouse": {"guardian-analytics", "analytics-ch-ingest"},
		"kv/data/guardian/guardian-mgmt/postflight-runner/postgres":    {"tenant-root", "postgres-postflight-controlplane-app"},
		"kv/data/guardian/guardian-mgmt/external-dns/cloudflare":       {"external-dns", "cloudflare-external-dns"},
		"kv/data/guardian/guardian-mgmt/tenant-root/backups-r2":        {"tenant-root", "guardian-backups-creds"},
	}
	for path, wantSource := range want {
		if sources[path] != wantSource {
			t.Fatalf("relayPlan() %s sources from %v, want %v", path, sources[path], wantSource)
		}
	}
}

func TestRelayValuesRejectsEmptyValue(t *testing.T) {
	target := relayPlan()[0]
	if _, err := relayValues(target, map[string]string{"ingest": ""}); err == nil {
		t.Fatalf("relayValues() accepted an empty value (test -s semantics)")
	}
	if _, err := relayValues(target, map[string]string{}); err == nil {
		t.Fatalf("relayValues() accepted a missing key")
	}
	values, err := relayValues(target, map[string]string{"ingest": "sekrit"})
	if err != nil {
		t.Fatalf("relayValues() error = %v", err)
	}
	if values["ingest"] != "sekrit" {
		t.Fatalf("relayValues() = %v, want ingest property", values)
	}
}

func TestDecodeSecretData(t *testing.T) {
	raw := fmt.Sprintf(`{"data":{"uri":%q}}`, base64.StdEncoding.EncodeToString([]byte("postgresql://x")))
	data, err := decodeSecretData(raw)
	if err != nil {
		t.Fatalf("decodeSecretData() error = %v", err)
	}
	if data["uri"] != "postgresql://x" {
		t.Fatalf("decodeSecretData() = %v, want decoded uri", data)
	}
	if _, err := decodeSecretData(`{"data":{"uri":"not base64!!"}}`); err == nil {
		t.Fatalf("decodeSecretData() accepted invalid base64")
	}
}

func TestNotReadyItems(t *testing.T) {
	raw := `{"items":[
		{"metadata":{"namespace":"external-dns","name":"cloudflare-external-dns"},"status":{"conditions":[{"type":"Ready","status":"True","reason":"SecretSynced"}]}},
		{"metadata":{"namespace":"tenant-root","name":"guardian-backups-creds"},"status":{"conditions":[{"type":"Ready","status":"False","reason":"SecretSyncedError","message":"could not get secret data"}]}},
		{"metadata":{"name":"guardian-postflight-runner-openbao"},"status":{"conditions":[{"type":"Ready","status":"False","reason":"InvalidProviderConfig","message":"unable to create client"}]}}
	]}`
	stragglers, err := notReadyItems(raw)
	if err != nil {
		t.Fatalf("notReadyItems() error = %v", err)
	}
	if len(stragglers) != 2 {
		t.Fatalf("notReadyItems() = %v, want 2 stragglers", stragglers)
	}
	joined := strings.Join(stragglers, "\n")
	if !strings.Contains(joined, "tenant-root/guardian-backups-creds") || !strings.Contains(joined, "InvalidProviderConfig") {
		t.Fatalf("notReadyItems() = %v, want namespaced name and wedge reason", stragglers)
	}
}

func TestValidateOptions(t *testing.T) {
	base := options{
		Kubectl:          "/tools/kubectl",
		Importer:         "/bazel-bin/importer",
		RepoRoot:         "/home/op/guardian",
		EnvFile:          "/dev/shm/guardian-custody/import.env",
		ExpectedRevision: "abc",
		LocalRevision:    "abc",
		PodWaitTimeout:   time.Minute,
	}
	if err := validateOptions(base); err != nil {
		t.Fatalf("validateOptions() error = %v", err)
	}
	relative := base
	relative.EnvFile = "import.env"
	if err := validateOptions(relative); err == nil {
		t.Fatalf("validateOptions() accepted a relative env file (the tool runs from Bazel runfiles)")
	}
	noImporter := base
	noImporter.Importer = ""
	if err := validateOptions(noImporter); err == nil {
		t.Fatalf("validateOptions() accepted a missing importer path")
	}
	noRepoRoot := base
	noRepoRoot.RepoRoot = ""
	if err := validateOptions(noRepoRoot); err == nil {
		t.Fatalf("validateOptions() accepted a missing repo root (dirty-tree gate would be skipped)")
	}
	negativeReplicas := base
	negativeReplicas.Replicas = -1
	if err := validateOptions(negativeReplicas); err == nil {
		t.Fatalf("validateOptions() accepted negative replicas")
	}
	noRevisions := base
	noRevisions.ExpectedRevision = ""
	if err := validateOptions(noRevisions); err == nil {
		t.Fatalf("validateOptions() accepted missing revisions")
	}
}
