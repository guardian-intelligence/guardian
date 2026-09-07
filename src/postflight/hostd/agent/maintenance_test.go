package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guardian-intelligence/guardian/src/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/postflight/hostd/vm"
)

func requestMaintenance(t *testing.T, a *Agent) string {
	t.Helper()
	a.cfg.MaintenanceDir = t.TempDir()
	a.cfg.MaintenanceProcessIdentity = "123:456"
	token := strings.Repeat("a", 64)
	if err := os.WriteFile(filepath.Join(a.cfg.MaintenanceDir, "request.json"),
		[]byte(`{"protocol":1,"token":"`+token+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	return token
}

func TestMaintenancePreservesListenerAndConcurrentLocalAssignment(t *testing.T) {
	a, vms, _ := newTestAgent(t, 2)
	members := poolMembers(t, a, vms, 1)
	if err := vms.Launch(context.Background(), "unregistered", testRunnerClass); err != nil {
		t.Fatal(err)
	}
	requestMaintenance(t, a)
	a.Tick(context.Background())
	statuses, err := vms.List(context.Background())
	if err != nil || len(statuses) != 1 || statuses[0].Phase != vm.PhaseListening {
		t.Fatalf("drain should retain only the registered listener: %+v, %v", statuses, err)
	}
	// GitHub can deliver a job after the drain begins and before the control
	// plane knows about it. It must remain alive through this unowned interval.
	spec := assignmentSpec(0, members[0])
	assignVM(t, vms, members[0], spec)
	a.Tick(context.Background())
	statuses, err = vms.List(context.Background())
	if err != nil || len(statuses) != 1 || statuses[0].Assignment.RequestID != spec.RequestID {
		t.Fatalf("drain destroyed a concurrently acquired assignment: %+v, %v", statuses, err)
	}
	a.HandleSync(syncproto.SyncResponse{BootID: a.bootID, Members: members,
		Assignments: []syncproto.DesiredAssignment{spec}, PoolTargets: map[string]int{string(testRunnerClass): 2}})
	a.Tick(context.Background())
	report, err := a.Report(context.Background())
	if err != nil || len(report.Slots) != 1 || report.Slots[0].Total != 0 || len(report.Assignments) != 1 {
		t.Fatalf("drain must stop capacity while servicing existing assignment: %+v, %v", report, err)
	}
	data, err := os.ReadFile(filepath.Join(a.cfg.MaintenanceDir, "state.json"))
	if err != nil || !strings.Contains(string(data), `"drained":false`) {
		t.Fatalf("busy host acknowledged a completed drain: %s, %v", data, err)
	}
}

func TestMaintenanceAcknowledgesEmptyPoolAndPreventsRefill(t *testing.T) {
	a, vms, _ := newTestAgent(t, 1)
	a.HandleSync(syncproto.SyncResponse{BootID: a.bootID, PoolTargets: map[string]int{string(testRunnerClass): 1}})
	a.Tick(context.Background())
	token := requestMaintenance(t, a)
	a.Tick(context.Background())
	a.Tick(context.Background())
	statuses, err := vms.List(context.Background())
	if err != nil || len(statuses) != 0 {
		t.Fatalf("drained host refilled: %+v, %v", statuses, err)
	}
	data, err := os.ReadFile(filepath.Join(a.cfg.MaintenanceDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state["token"] != token || state["process_identity"] != "123:456" || state["drained"] != true || state["vms"] != float64(0) {
		t.Fatalf("unexpected drain acknowledgement: %+v", state)
	}
	if err := os.Remove(filepath.Join(a.cfg.MaintenanceDir, "request.json")); err != nil {
		t.Fatal(err)
	}
	a.Tick(context.Background())
	statuses, err = vms.List(context.Background())
	if err != nil || len(statuses) != 1 {
		t.Fatalf("new runtime did not reopen capacity: %+v, %v", statuses, err)
	}
}

func TestInvalidMaintenanceRequestClosesAdmissionWithoutAcknowledgement(t *testing.T) {
	a, vms, _ := newTestAgent(t, 1)
	requestMaintenance(t, a)
	if err := os.WriteFile(filepath.Join(a.cfg.MaintenanceDir, "request.json"), []byte("truncated"), 0600); err != nil {
		t.Fatal(err)
	}
	a.HandleSync(syncproto.SyncResponse{BootID: a.bootID, PoolTargets: map[string]int{string(testRunnerClass): 1}})
	a.Tick(context.Background())
	statuses, err := vms.List(context.Background())
	if err != nil || len(statuses) != 0 {
		t.Fatalf("invalid request opened capacity: %+v, %v", statuses, err)
	}
	if _, err := os.Stat(filepath.Join(a.cfg.MaintenanceDir, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("invalid request produced an acknowledgement: %v", err)
	}
}
