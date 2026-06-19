package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateConfigValidationDoesNotNeedProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "create.cue")
	if err := os.WriteFile(path, []byte(`
package create

provider: {
	name: "latitude"
	project: "guardian"
}
cluster: {
	name: "guardian-dev"
	endpoint: "https://203.0.113.10:6443"
}
host: {
	address: "203.0.113.10"
	hostname: "gi-dev"
	interfaceMac: "90:5a:08:33:ba:9f"
}
talos: version: "v1.13.4"
cozystack: version: "v0.35.0"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadCreateConfig(path)
	if err == nil {
		t.Fatal("loadCreateConfig succeeded, want missing field error")
	}
	for _, want := range []string{"host.installDiskSerial", "provider.serverId or create"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %s", err, want)
		}
	}
}

func TestCreateConfigLoadsCuePackageWithSiblingFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "create.cue"), []byte(`
package create

provider: {
	name: "latitude"
	project: "guardian"
}
create: {
	hostname: "gi-dev"
	metro: "ash"
	plan: versions.plan
}
cluster: {
	name: "guardian-dev"
	endpoint: "https://203.0.113.10:6443"
}
host: {
	address: "203.0.113.10"
	hostname: "gi-dev"
	interfaceMac: "90:5a:08:33:ba:9f"
	installDiskSerial: "disk-serial"
}
talos: version: versions.talos
cozystack: version: versions.cozystack
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "versions.cue"), []byte(`
package create

plan: "f4.metal.small"
versions: {
	plan: "f4.metal.small"
	talos: "v1.13.4"
	cozystack: "v0.35.0"
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.cue"), []byte(`
package other

not: "part of this create config"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadCreateConfig(filepath.Join(dir, "create.cue"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Spec.Create == nil || cfg.Spec.Create.Plan != "f4.metal.small" {
		t.Fatalf("create spec = %#v, want sibling file value", cfg.Spec.Create)
	}
}

func TestCreateConfigRequiresEntrypointFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "create.cue"), []byte(validCreateCue()), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadCreateConfig(filepath.Join(dir, "missing.cue"))
	if err == nil {
		t.Fatal("loadCreateConfig succeeded for missing entrypoint")
	}
	if !strings.Contains(err.Error(), "missing.cue") {
		t.Fatalf("error = %q, want missing path", err)
	}
}

func TestCreateConfigDigestDeterministic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "create.cue")
	if err := os.WriteFile(path, []byte(validCreateCue()), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := loadCreateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadCreateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.SpecDigest == "" || first.SpecDigest != second.SpecDigest {
		t.Fatalf("digests = %q, %q; want stable non-empty digest", first.SpecDigest, second.SpecDigest)
	}
	if !bytes.Equal(first.Canonical, second.Canonical) {
		t.Fatalf("canonical specs differ:\n%s\n---\n%s", first.Canonical, second.Canonical)
	}
}

func TestCreateOutputGoldens(t *testing.T) {
	t.Run("needs config text", func(t *testing.T) {
		var buf bytes.Buffer
		printCreateResult(&buf, createResult{Outcome: createOutcomeNeedsConfig, Reason: "expected one create config path"}, "text")
		want := "outcome\tNeedsConfig\nreason\texpected one create config path\n"
		if buf.String() != want {
			t.Fatalf("text output = %q, want %q", buf.String(), want)
		}
	})
	t.Run("needs approval json", func(t *testing.T) {
		var buf bytes.Buffer
		printCreateResult(&buf, createResult{
			Outcome: createOutcomeNeedsApproval,
			Reason:  "server allocation requires approval",
			Plan: &createPlan{
				ClusterName:       "guardian-dev",
				Provider:          "latitude",
				SpecDigest:        "abc123",
				Create:            &serverCreatePlan{Project: "guardian", Hostname: "gi-dev", Metro: "ash", Plan: "f4.metal.small"},
				Mutations:         []string{"create Latitude server", "bootstrap Kubernetes"},
				RequiresApproval:  true,
				ApprovalRerunHint: "rerun with --yes",
			},
		}, "json")
		want := `{
  "outcome": "NeedsApproval",
  "reason": "server allocation requires approval",
  "plan": {
    "clusterName": "guardian-dev",
    "provider": "latitude",
    "specDigest": "abc123",
    "create": {
      "project": "guardian",
      "hostname": "gi-dev",
      "metro": "ash",
      "plan": "f4.metal.small"
    },
    "mutations": [
      "create Latitude server",
      "bootstrap Kubernetes"
    ],
    "requiresApproval": true,
    "approvalRerunHint": "rerun with --yes"
  }
}
`
		if buf.String() != want {
			t.Fatalf("json output:\n%s\nwant:\n%s", buf.String(), want)
		}
	})
}

func TestCreateCmdMissingTokenStopsBeforeState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "create.cue")
	if err := os.WriteFile(path, []byte(validCreateCue()), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("LATITUDE_API_KEY", "")

	err := runCreateCmd([]string{path})
	if err == nil {
		t.Fatal("runCreateCmd succeeded without Latitude token")
	}
	if !errors.Is(err, errUsage) || !strings.Contains(err.Error(), "LATITUDE_API_KEY is not set") {
		t.Fatalf("error = %v, want usage token error", err)
	}
	if _, err := os.Stat(filepath.Join(state, "guardian", "create")); !os.IsNotExist(err) {
		t.Fatalf("create state root exists or stat failed: %v", err)
	}
}

func TestCreateServerAbsentWithoutApprovalPlansOnly(t *testing.T) {
	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	fakes.provider.server = &serverObservation{}

	result := runCreate(context.Background(), createRunInput{Config: cfg}, fakes.deps)
	if result.Outcome != createOutcomeNeedsApproval {
		t.Fatalf("outcome = %s, want %s: %#v", result.Outcome, createOutcomeNeedsApproval, result)
	}
	fakes.provider.wantCalls(t, "GetServer")
	if result.Plan == nil || !result.Plan.RequiresApproval {
		t.Fatalf("plan = %#v, want approval plan", result.Plan)
	}
	if result.Plan.Create == nil ||
		result.Plan.Create.Hostname != "gi-dev" ||
		result.Plan.Create.Metro != "ash" ||
		result.Plan.Create.Plan != "f4.metal.small" {
		t.Fatalf("plan create = %#v, want concrete Latitude allocation target", result.Plan.Create)
	}
	var out bytes.Buffer
	printCreateResult(&out, result, "text")
	for _, want := range []string{
		"plan.create.project\tguardian\n",
		"plan.create.hostname\tgi-dev\n",
		"plan.create.metro\tash\n",
		"plan.create.plan\tf4.metal.small\n",
		"approval\trerun with --yes\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("text output missing %q:\n%s", want, out.String())
		}
	}
	if dirs := readDirNames(t, fakes.root); len(dirs) != 0 {
		t.Fatalf("state dirs = %v, want none before approval", dirs)
	}
}

func TestCreateApprovedHappyPathWritesMarkerLocksAndRerunsConverged(t *testing.T) {
	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	fakes.provider.server = &serverObservation{}
	fakes.provider.createID = "sv_created"

	result := runCreate(context.Background(), createRunInput{
		Config:  cfg,
		Options: createOptions{Approved: true},
	}, fakes.deps)
	if result.Outcome != createOutcomeConverged {
		t.Fatalf("outcome = %s, want %s: %#v", result.Outcome, createOutcomeConverged, result)
	}
	fakes.provider.wantCalls(t, "GetServer", "CreateServer", "GetJITAccess", "WriteMarker", "LockServer")
	fakes.node.wantCalls(t, "Render", "Preflight", "Apply")
	fakes.kubernetes.wantCalls(t, "Bootstrap", "WriteMarker")
	fakes.cozystack.wantCalls(t, "Install")
	if result.Operation == nil || result.Operation.ServerID != "sv_created" {
		t.Fatalf("operation = %#v, want created server id", result.Operation)
	}
	if result.Operation.Stage != createStageWriteMarkerAndLock {
		t.Fatalf("stage = %s, want %s", result.Operation.Stage, createStageWriteMarkerAndLock)
	}

	fakes.provider.server = &serverObservation{
		Exists: true,
		ID:     "sv_created",
		Locked: true,
		Marker: fakes.provider.marker,
	}
	fakes.resetCalls()
	second := runCreate(context.Background(), createRunInput{Config: cfg}, fakes.deps)
	if second.Outcome != createOutcomeConverged {
		t.Fatalf("second outcome = %s, want Converged: %#v", second.Outcome, second)
	}
	fakes.provider.wantCalls(t, "GetServer")
	fakes.node.wantCalls(t)
	fakes.kubernetes.wantCalls(t)
	fakes.cozystack.wantCalls(t)
}

func TestCreateCoreDoesNotDependOnAmbientPath(t *testing.T) {
	t.Setenv("PATH", "")
	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	fakes.provider.server = &serverObservation{}
	fakes.provider.createID = "sv_created"

	result := runCreate(context.Background(), createRunInput{
		Config:  cfg,
		Options: createOptions{Approved: true},
	}, fakes.deps)
	if result.Outcome != createOutcomeConverged {
		t.Fatalf("outcome = %s, want Converged: %#v", result.Outcome, result)
	}
	fakes.provider.wantCalls(t, "GetServer", "CreateServer", "GetJITAccess", "WriteMarker", "LockServer")
	fakes.node.wantCalls(t, "Render", "Preflight", "Apply")
	fakes.kubernetes.wantCalls(t, "Bootstrap", "WriteMarker")
	fakes.cozystack.wantCalls(t, "Install")
}

func TestCreateLockedUnknownRefusesWithoutJIT(t *testing.T) {
	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	fakes.provider.server = &serverObservation{Exists: true, ID: "sv_live", Locked: true}

	result := runCreate(context.Background(), createRunInput{
		Config:  cfg,
		Options: createOptions{Approved: true},
	}, fakes.deps)
	if result.Outcome != createOutcomeRefused {
		t.Fatalf("outcome = %s, want Refused: %#v", result.Outcome, result)
	}
	fakes.provider.wantCalls(t, "GetServer")
	fakes.node.wantCalls(t)
	fakes.kubernetes.wantCalls(t)
}

func TestCreateUnknownLiveKubernetesRefusesWithoutMutation(t *testing.T) {
	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	fakes.provider.server = &serverObservation{Exists: true, ID: "sv_live"}
	fakes.observer.host = &hostObservation{KubernetesReachable: true}

	result := runCreate(context.Background(), createRunInput{
		Config:  cfg,
		Options: createOptions{Approved: true},
	}, fakes.deps)
	if result.Outcome != createOutcomeRefused {
		t.Fatalf("outcome = %s, want Refused: %#v", result.Outcome, result)
	}
	fakes.provider.wantCalls(t, "GetServer")
	fakes.node.wantCalls(t)
	fakes.kubernetes.wantCalls(t)
}

func TestCreateUnknownLiveCozystackRefusesWithoutMutation(t *testing.T) {
	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	fakes.provider.server = &serverObservation{Exists: true, ID: "sv_live"}
	fakes.observer.host = &hostObservation{CozystackReachable: true}

	result := runCreate(context.Background(), createRunInput{
		Config:  cfg,
		Options: createOptions{Approved: true},
	}, fakes.deps)
	if result.Outcome != createOutcomeRefused {
		t.Fatalf("outcome = %s, want Refused: %#v", result.Outcome, result)
	}
	fakes.provider.wantCalls(t, "GetServer")
	fakes.node.wantCalls(t)
	fakes.kubernetes.wantCalls(t)
}

func TestCreateExistingStockOSNeedsApproval(t *testing.T) {
	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	fakes.provider.server = &serverObservation{Exists: true, ID: "sv_stock", StockOS: true}

	result := runCreate(context.Background(), createRunInput{Config: cfg}, fakes.deps)
	if result.Outcome != createOutcomeNeedsApproval {
		t.Fatalf("outcome = %s, want NeedsApproval: %#v", result.Outcome, result)
	}
	if result.Plan == nil || !result.Plan.DestructiveReplacement {
		t.Fatalf("plan = %#v, want destructive replacement approval", result.Plan)
	}
	fakes.provider.wantCalls(t, "GetServer")
	fakes.node.wantCalls(t)
}

func TestCreateProviderRetryableDoesNotFallback(t *testing.T) {
	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	fakes.provider.getErr = retryableCreate("provider", errors.New("rate limited"))

	result := runCreate(context.Background(), createRunInput{
		Config:  cfg,
		Options: createOptions{Approved: true},
	}, fakes.deps)
	if result.Outcome != createOutcomeRetryable || !result.Retryable {
		t.Fatalf("result = %#v, want retryable", result)
	}
	fakes.provider.wantCalls(t, "GetServer")
	fakes.node.wantCalls(t)
}

func TestCreateJITRetryableDoesNotRunNodeStages(t *testing.T) {
	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	fakes.provider.server = &serverObservation{Exists: true, ID: "sv_live"}
	fakes.provider.jitErr = retryableCreate("provider", errors.New("jit expired"))

	result := runCreate(context.Background(), createRunInput{
		Config:  cfg,
		Options: createOptions{Approved: true},
	}, fakes.deps)
	if result.Outcome != createOutcomeRetryable || !result.Retryable {
		t.Fatalf("result = %#v, want retryable", result)
	}
	fakes.provider.wantCalls(t, "GetServer", "GetJITAccess")
	fakes.node.wantCalls(t)
	fakes.kubernetes.wantCalls(t)
}

func TestCreatePreflightFailureSkipsApply(t *testing.T) {
	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	fakes.provider.server = &serverObservation{Exists: true, ID: "sv_live"}
	fakes.node.preflightErr = errors.New("wrong disk selector")

	result := runCreate(context.Background(), createRunInput{
		Config:  cfg,
		Options: createOptions{Approved: true},
	}, fakes.deps)
	if result.Outcome != createOutcomeRefused {
		t.Fatalf("outcome = %s, want Refused: %#v", result.Outcome, result)
	}
	fakes.provider.wantCalls(t, "GetServer", "GetJITAccess")
	fakes.node.wantCalls(t, "Render", "Preflight")
	fakes.kubernetes.wantCalls(t)
}

func TestCreateResumeUsesRecordedServerIDAndDoesNotCreateSecondServer(t *testing.T) {
	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	record := &operationRecord{
		OperationID: "op_test",
		ClusterName: cfg.Spec.Cluster.Name,
		ServerID:    "sv_recorded",
		SpecDigest:  cfg.SpecDigest,
		Stage:       createStageProvisionServer,
		StateDir:    filepath.Join(fakes.root, "op_test"),
		CreatedAt:   fakes.deps.Now(),
		UpdatedAt:   fakes.deps.Now(),
	}
	if err := writeOperationRecord(record); err != nil {
		t.Fatal(err)
	}
	fakes.provider.serverByID = map[string]*serverObservation{
		"sv_recorded": {Exists: true, ID: "sv_recorded", StockOS: true},
	}

	result := runCreate(context.Background(), createRunInput{Config: cfg}, fakes.deps)
	if result.Outcome != createOutcomeConverged {
		t.Fatalf("outcome = %s, want Converged: %#v", result.Outcome, result)
	}
	fakes.provider.wantCalls(t, "GetServer", "GetJITAccess", "WriteMarker", "LockServer")
	if len(fakes.provider.targets) != 1 || fakes.provider.targets[0].ServerID != "sv_recorded" {
		t.Fatalf("targets = %#v, want recorded server id lookup", fakes.provider.targets)
	}
}

func TestCreateRecordedServerMissingIsRetryable(t *testing.T) {
	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	record := &operationRecord{
		OperationID: "op_test",
		ClusterName: cfg.Spec.Cluster.Name,
		ServerID:    "sv_recorded",
		SpecDigest:  cfg.SpecDigest,
		Stage:       createStageProvisionServer,
		StateDir:    filepath.Join(fakes.root, "op_test"),
		CreatedAt:   fakes.deps.Now(),
		UpdatedAt:   fakes.deps.Now(),
	}
	if err := writeOperationRecord(record); err != nil {
		t.Fatal(err)
	}
	fakes.provider.server = &serverObservation{}

	result := runCreate(context.Background(), createRunInput{Config: cfg}, fakes.deps)
	if result.Outcome != createOutcomeRetryable {
		t.Fatalf("outcome = %s, want Retryable: %#v", result.Outcome, result)
	}
	fakes.provider.wantCalls(t, "GetServer")
}

func TestCreateMismatchedPartialOperationRefuses(t *testing.T) {
	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	record := &operationRecord{
		OperationID: "op_old",
		ClusterName: cfg.Spec.Cluster.Name,
		ServerID:    "sv_old",
		SpecDigest:  "different",
		Stage:       createStageProvisionServer,
		StateDir:    filepath.Join(fakes.root, "op_old"),
		CreatedAt:   fakes.deps.Now(),
		UpdatedAt:   fakes.deps.Now(),
	}
	if err := writeOperationRecord(record); err != nil {
		t.Fatal(err)
	}
	fakes.provider.server = &serverObservation{Exists: true, ID: "sv_old"}

	result := runCreate(context.Background(), createRunInput{
		Config:  cfg,
		Options: createOptions{Approved: true},
	}, fakes.deps)
	if result.Outcome != createOutcomeRefused {
		t.Fatalf("outcome = %s, want Refused: %#v", result.Outcome, result)
	}
	fakes.provider.wantCalls(t, "GetServer")
	fakes.node.wantCalls(t)
}

func TestCreateResumeAfterKubernetesBootstrapInstallsCozystack(t *testing.T) {
	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	record := &operationRecord{
		OperationID: "op_test",
		ClusterName: cfg.Spec.Cluster.Name,
		ServerID:    "sv_created",
		SpecDigest:  cfg.SpecDigest,
		Stage:       createStageBootstrapKubernetes,
		StateDir:    filepath.Join(fakes.root, "op_test"),
		Kubeconfig:  filepath.Join(fakes.root, "op_test", "kubeconfig"),
		CreatedAt:   fakes.deps.Now(),
		UpdatedAt:   fakes.deps.Now(),
	}
	if err := writeOperationRecord(record); err != nil {
		t.Fatal(err)
	}
	fakes.provider.serverByID = map[string]*serverObservation{
		"sv_created": {Exists: true, ID: "sv_created"},
	}
	fakes.observer.host = &hostObservation{KubernetesReachable: true}

	result := runCreate(context.Background(), createRunInput{Config: cfg}, fakes.deps)
	if result.Outcome != createOutcomeConverged {
		t.Fatalf("outcome = %s, want Converged: %#v", result.Outcome, result)
	}
	fakes.provider.wantCalls(t, "GetServer", "WriteMarker", "LockServer")
	fakes.node.wantCalls(t)
	fakes.kubernetes.wantCalls(t, "WriteMarker")
	fakes.cozystack.wantCalls(t, "Install")
}

func TestCreateSecretRedactionAndStateCustody(t *testing.T) {
	secret := newSecretString("sentinel-latitude-token-123")
	if got := fmt.Sprintf("%s %#v", secret, secret); strings.Contains(got, "sentinel") {
		t.Fatalf("secret formatted as %q", got)
	}
	raw, err := secret.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sentinel") {
		t.Fatalf("secret json leaked: %s", raw)
	}

	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	fakes.provider.server = &serverObservation{}
	fakes.provider.createID = "sv_created"
	fakes.provider.jit = jitAccess{Endpoint: "oob", Token: newSecretString("sentinel-jit-token-456")}

	result := runCreate(context.Background(), createRunInput{
		Config:  cfg,
		Options: createOptions{Approved: true},
	}, fakes.deps)
	if result.Outcome != createOutcomeConverged {
		t.Fatalf("outcome = %s, want Converged", result.Outcome)
	}
	assertTreeDoesNotContain(t, fakes.root, "sentinel")
	assertModes(t, fakes.root)
}

func TestCreateResumeAfterMarkerWriteLockFailureLocksOnly(t *testing.T) {
	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	fakes.provider.server = &serverObservation{}
	fakes.provider.createID = "sv_created"
	fakes.provider.lockErr = retryableCreate("provider", errors.New("lock timeout"))

	first := runCreate(context.Background(), createRunInput{
		Config:  cfg,
		Options: createOptions{Approved: true},
	}, fakes.deps)
	if first.Outcome != createOutcomeRetryable {
		t.Fatalf("first outcome = %s, want Retryable: %#v", first.Outcome, first)
	}
	if first.Operation == nil {
		t.Fatal("first operation is nil")
	}

	fakes.provider.lockErr = nil
	fakes.provider.server = &serverObservation{Exists: true, ID: "sv_created", Marker: fakes.provider.marker}
	fakes.observer.host = &hostObservation{KubernetesReachable: true, KubernetesMarker: fakes.kubernetes.marker}
	fakes.resetCalls()
	second := runCreate(context.Background(), createRunInput{Config: cfg}, fakes.deps)
	if second.Outcome != createOutcomeConverged {
		t.Fatalf("second outcome = %s, want Converged: %#v", second.Outcome, second)
	}
	fakes.provider.wantCalls(t, "GetServer", "WriteMarker", "LockServer")
	fakes.node.wantCalls(t)
	fakes.kubernetes.wantCalls(t)
	fakes.cozystack.wantCalls(t)
}

func TestCreateClusterMarkerMatchMissingProviderLockDoesProviderOnly(t *testing.T) {
	cfg := testCreateConfig(t)
	fakes := newCreateFakes(t, cfg)
	marker := createMarker{
		Guardian:         true,
		ClusterName:      cfg.Spec.Cluster.Name,
		ServerID:         "sv_created",
		SpecDigest:       cfg.SpecDigest,
		OperationID:      "op_existing",
		CozystackVersion: cfg.Spec.Cozystack.Version,
		CreatedAt:        fakes.deps.Now(),
	}
	fakes.provider.server = &serverObservation{Exists: true, ID: "sv_created"}
	fakes.observer.host = &hostObservation{KubernetesReachable: true, KubernetesMarker: &marker}

	result := runCreate(context.Background(), createRunInput{Config: cfg}, fakes.deps)
	if result.Outcome != createOutcomeConverged {
		t.Fatalf("outcome = %s, want Converged: %#v", result.Outcome, result)
	}
	fakes.provider.wantCalls(t, "GetServer", "WriteMarker", "LockServer")
	fakes.node.wantCalls(t)
	fakes.kubernetes.wantCalls(t)
	fakes.cozystack.wantCalls(t)
}

func TestCreateJSONOutputRedactsAndIsStable(t *testing.T) {
	result := createResult{
		Outcome:     createOutcomeRetryable,
		Reason:      "temporary",
		Diagnostics: []createDiagnostic{{Subsystem: "provider", Message: "rate limited"}},
	}
	var buf bytes.Buffer
	printCreateResult(&buf, result, "json")
	if strings.Contains(buf.String(), "sentinel") {
		t.Fatalf("json output leaked sentinel: %s", buf.String())
	}
	var decoded createResult
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("json output did not decode: %v\n%s", err, buf.String())
	}
	if decoded.Outcome != createOutcomeRetryable || decoded.Diagnostics[0].Subsystem != "provider" {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestCreateCodeDoesNotImportPostBootstrapDomains(t *testing.T) {
	files, err := filepath.Glob("create_*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			for _, forbidden := range []string{"/opentaco", "/release", "/releases", "/products/"} {
				if strings.Contains(importPath, forbidden) {
					t.Fatalf("%s imports post-bootstrap domain %q via %q", path, forbidden, importPath)
				}
			}
		}
	}
}

type createFakes struct {
	root       string
	provider   *fakeCreateProvider
	observer   *fakeTargetObserver
	node       *fakeNodeConfigurator
	kubernetes *fakeKubernetesBootstrapper
	cozystack  *fakeCozystackInstaller
	deps       createDeps
}

func newCreateFakes(t *testing.T, cfg createConfig) *createFakes {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state", "guardian", "create")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	provider := &fakeCreateProvider{jit: jitAccess{Endpoint: "oob", Token: newSecretString("jit-token")}}
	observer := &fakeTargetObserver{}
	node := &fakeNodeConfigurator{}
	k8s := &fakeKubernetesBootstrapper{}
	cozy := &fakeCozystackInstaller{}
	f := &createFakes{
		root:       root,
		provider:   provider,
		observer:   observer,
		node:       node,
		kubernetes: k8s,
		cozystack:  cozy,
	}
	f.deps = createDeps{
		Provider:       provider,
		Observer:       observer,
		Node:           node,
		Kubernetes:     k8s,
		Cozystack:      cozy,
		StateRoot:      root,
		Now:            func() time.Time { return time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC) },
		NewOperationID: func() (string, error) { return "op_test", nil },
	}
	_ = cfg
	return f
}

func (f *createFakes) resetCalls() {
	f.provider.calls = nil
	f.node.calls = nil
	f.kubernetes.calls = nil
	f.cozystack.calls = nil
}

type fakeCreateProvider struct {
	server     *serverObservation
	serverByID map[string]*serverObservation
	createID   string
	jit        jitAccess
	marker     *createMarker
	calls      []string
	targets    []providerTarget
	getErr     error
	jitErr     error
	lockErr    error
}

func (p *fakeCreateProvider) call(name string) { p.calls = append(p.calls, name) }

func (p *fakeCreateProvider) GetServer(_ context.Context, target providerTarget) (*serverObservation, error) {
	p.call("GetServer")
	p.targets = append(p.targets, target)
	if p.getErr != nil {
		return nil, p.getErr
	}
	if target.ServerID != "" && p.serverByID != nil {
		if server := p.serverByID[target.ServerID]; server != nil {
			cp := *server
			return &cp, nil
		}
		return &serverObservation{}, nil
	}
	if p.server == nil {
		return &serverObservation{}, nil
	}
	cp := *p.server
	return &cp, nil
}

func (p *fakeCreateProvider) CreateServer(context.Context, serverCreatePlan) (*serverObservation, error) {
	p.call("CreateServer")
	if p.createID == "" {
		p.createID = "sv_created"
	}
	return &serverObservation{Exists: true, ID: p.createID, StockOS: true}, nil
}

func (p *fakeCreateProvider) GetJITAccess(context.Context, string) (*jitAccess, error) {
	p.call("GetJITAccess")
	if p.jitErr != nil {
		return nil, p.jitErr
	}
	return &p.jit, nil
}

func (p *fakeCreateProvider) WriteMarker(_ context.Context, _ string, marker createMarker) error {
	p.call("WriteMarker")
	cp := marker
	p.marker = &cp
	return nil
}

func (p *fakeCreateProvider) LockServer(context.Context, string) error {
	p.call("LockServer")
	if p.lockErr != nil {
		return p.lockErr
	}
	return nil
}

func (p *fakeCreateProvider) wantCalls(t *testing.T, want ...string) {
	t.Helper()
	if strings.Join(p.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("provider calls = %v, want %v", p.calls, want)
	}
}

type fakeTargetObserver struct {
	host *hostObservation
}

func (o *fakeTargetObserver) Observe(context.Context, createSpec, *operationRecord, *serverObservation) (*hostObservation, error) {
	if o.host == nil {
		return &hostObservation{}, nil
	}
	cp := *o.host
	return &cp, nil
}

type fakeNodeConfigurator struct {
	calls        []string
	preflightErr error
}

func (n *fakeNodeConfigurator) Render(context.Context, createSpec, operationRecord) (*nodeRender, error) {
	n.calls = append(n.calls, "Render")
	return &nodeRender{Digest: "render-digest", Path: "/state/render"}, nil
}

func (n *fakeNodeConfigurator) Preflight(context.Context, nodeRender, jitAccess) (*preflightEvidence, error) {
	n.calls = append(n.calls, "Preflight")
	if n.preflightErr != nil {
		return nil, n.preflightErr
	}
	return &preflightEvidence{RenderDigest: "render-digest", Path: "/state/preflight"}, nil
}

func (n *fakeNodeConfigurator) Apply(context.Context, nodeRender, jitAccess) error {
	n.calls = append(n.calls, "Apply")
	return nil
}

func (n *fakeNodeConfigurator) wantCalls(t *testing.T, want ...string) {
	t.Helper()
	if strings.Join(n.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("node calls = %v, want %v", n.calls, want)
	}
}

type fakeKubernetesBootstrapper struct {
	calls  []string
	marker *createMarker
}

func (k *fakeKubernetesBootstrapper) Bootstrap(_ context.Context, _ createSpec, record operationRecord) (string, error) {
	k.calls = append(k.calls, "Bootstrap")
	return writeFakeKubeconfigForTest(record)
}

func (k *fakeKubernetesBootstrapper) WriteMarker(_ context.Context, _ string, marker createMarker) error {
	k.calls = append(k.calls, "WriteMarker")
	cp := marker
	k.marker = &cp
	return nil
}

func (k *fakeKubernetesBootstrapper) wantCalls(t *testing.T, want ...string) {
	t.Helper()
	if strings.Join(k.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("kubernetes calls = %v, want %v", k.calls, want)
	}
}

type fakeCozystackInstaller struct {
	calls []string
}

func (c *fakeCozystackInstaller) Install(context.Context, createSpec, operationRecord, string) error {
	c.calls = append(c.calls, "Install")
	return nil
}

func (c *fakeCozystackInstaller) wantCalls(t *testing.T, want ...string) {
	t.Helper()
	if strings.Join(c.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("cozystack calls = %v, want %v", c.calls, want)
	}
}

func testCreateConfig(t *testing.T) createConfig {
	t.Helper()
	spec := createSpec{
		Provider: createProviderSpec{Name: "latitude", Project: "guardian"},
		Create:   &serverCreateSpec{Hostname: "gi-dev", Metro: "ash", Plan: "f4.metal.small"},
		Cluster:  createClusterSpec{Name: "guardian-dev", Endpoint: "https://203.0.113.10:6443"},
		Host: createHostSpec{
			Address:           "203.0.113.10",
			Hostname:          "gi-dev",
			InterfaceMAC:      "90:5a:08:33:ba:9f",
			InstallDiskSerial: "disk-serial",
		},
		Talos:     createTalosSpec{Version: "v1.13.4"},
		Cozystack: createCozystackSpec{Version: "v0.35.0"},
	}
	canonical, digest, err := canonicalCreateSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	return createConfig{Spec: spec, SpecDigest: digest, Canonical: canonical}
}

func validCreateCue() string {
	return `
package create

provider: {
	name: "latitude"
	project: "guardian"
}
create: {
	hostname: "gi-dev"
	metro: "ash"
	plan: "f4.metal.small"
}
cluster: {
	name: "guardian-dev"
	endpoint: "https://203.0.113.10:6443"
}
host: {
	address: "203.0.113.10"
	hostname: "gi-dev"
	interfaceMac: "90:5a:08:33:ba:9f"
	installDiskSerial: "disk-serial"
}
talos: version: "v1.13.4"
cozystack: version: "v0.35.0"
`
}

func readDirNames(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func assertTreeDoesNotContain(t *testing.T, root, needle string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), needle) {
			t.Fatalf("%s contains %q", path, needle)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertModes(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if d.IsDir() {
			if mode != 0o700 {
				t.Fatalf("%s mode = %o, want 700", path, mode)
			}
			return nil
		}
		if mode != 0o600 {
			t.Fatalf("%s mode = %o, want 600", path, mode)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
