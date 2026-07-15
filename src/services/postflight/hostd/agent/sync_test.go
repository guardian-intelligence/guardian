package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

func testAgent(t *testing.T, origin string) *Agent {
	t.Helper()
	instance, err := New(Config{
		HostID:              "host-test",
		ControlPlaneOrigin:  origin,
		Slots:               map[vm.Class]int{"c": 2},
		CheckoutGuestOrigin: "http://198.51.100.1:8480",
	}, zvol.NewFake(), vm.NewFake(), "cred-abc", []byte("0123456789abcdef0123456789abcdef"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	return instance
}

func TestSyncExchange(t *testing.T) {
	var gotAuth, gotPath string
	var gotRequest syncproto.SyncRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		json.NewEncoder(w).Encode(syncproto.SyncResponse{
			BootID: gotRequest.BootID,
			Leases: []syncproto.DesiredLease{{
				LeaseID:            "l1",
				State:              syncproto.DesiredRun,
				ExecutionID:        "e1",
				AttemptID:          "a1",
				RepositoryFullName: "acme/widget",
				RunnerClass:        "c",
				JITConfig:          "jit",
				Workspace:          syncproto.WorkspaceSpec{SizeBytes: 1},
			}},
			PoolTargets:     map[string]int{"c": 1},
			PollAfterMillis: 250,
		})
	}))
	defer server.Close()

	instance := testAgent(t, server.URL)
	pollAfter, err := instance.syncOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer cred-abc" {
		t.Fatalf("authorization %q", gotAuth)
	}
	if gotPath != syncproto.SyncPath {
		t.Fatalf("path %q", gotPath)
	}
	if gotRequest.HostID != "host-test" || gotRequest.BootID == "" {
		t.Fatalf("request identity: %+v", gotRequest)
	}
	if pollAfter != 250*time.Millisecond {
		t.Fatalf("pollAfter %v", pollAfter)
	}
	snapshots := instance.Snapshot()
	if len(snapshots) != 1 || snapshots[0].LeaseID != "l1" || snapshots[0].State != syncproto.StatePending {
		t.Fatalf("desired lease not applied: %+v", snapshots)
	}
}

func TestSyncRejectsNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	instance := testAgent(t, server.URL)
	if _, err := instance.syncOnce(context.Background()); err == nil {
		t.Fatal("expected an error on 503")
	}
	// A failed exchange must not unlock destructive convergence.
	instance.Tick(context.Background())
	if instance.synced {
		t.Fatal("agent considers itself synced after a failed exchange")
	}
}

func TestSyncDropsResponseWithoutBootIDEcho(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A default-constructed (or misrouted) full-state response would
		// cancel every job on the host; the missing echo must reject it.
		json.NewEncoder(w).Encode(syncproto.SyncResponse{})
	}))
	defer server.Close()
	instance := testAgent(t, server.URL)
	if _, err := instance.syncOnce(context.Background()); err == nil {
		t.Fatal("expected an error on a response without the boot_id echo")
	}
	if instance.Synced() {
		t.Fatal("agent applied a response that was not computed for it")
	}
}

func TestSyncClampsPollAfter(t *testing.T) {
	cases := map[int]time.Duration{
		1:       minPollAfter,
		3600000: maxPollAfter,
	}
	for millis, want := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request syncproto.SyncRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decoding request: %v", err)
			}
			json.NewEncoder(w).Encode(syncproto.SyncResponse{BootID: request.BootID, PollAfterMillis: millis})
		}))
		instance := testAgent(t, server.URL)
		pollAfter, err := instance.syncOnce(context.Background())
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		if pollAfter != want {
			t.Fatalf("poll_after_millis=%d clamped to %v, want %v", millis, pollAfter, want)
		}
	}
}

func TestDesiredLeaseValidation(t *testing.T) {
	valid := syncproto.DesiredLease{
		LeaseID: "l1", State: syncproto.DesiredRun, ExecutionID: "e", AttemptID: "a",
		RepositoryFullName: "acme/widget", RunnerClass: "c", JITConfig: "j",
	}
	if err := validateDesired(valid); err != nil {
		t.Fatalf("valid lease rejected: %v", err)
	}
	cases := map[string]func(*syncproto.DesiredLease){
		"empty id":            func(d *syncproto.DesiredLease) { d.LeaseID = "" },
		"traversal id":        func(d *syncproto.DesiredLease) { d.LeaseID = "../evil" },
		"unknown state":       func(d *syncproto.DesiredLease) { d.State = "explode" },
		"seal sans gen":       func(d *syncproto.DesiredLease) { d.State = syncproto.DesiredSeal; d.SealGeneration = "" },
		"run sans identity":   func(d *syncproto.DesiredLease) { d.ExecutionID = "" },
		"run sans jit":        func(d *syncproto.DesiredLease) { d.JITConfig = "" },
		"run sans repository": func(d *syncproto.DesiredLease) { d.RepositoryFullName = "" },
		"bad generation":      func(d *syncproto.DesiredLease) { d.Workspace.Generation = "a/../b" },
	}
	for name, mutate := range cases {
		lease := valid
		mutate(&lease)
		if err := validateDesired(lease); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
