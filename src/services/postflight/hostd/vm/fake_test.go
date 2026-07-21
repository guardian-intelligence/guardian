package vm

import (
	"context"
	"testing"
)

// TestFakeMirrorsRealDriverMismatchErrors pins the Fake to the Driver
// contract edges the real driver enforces: relaunching a live ID with its
// own class and re-assigning a VM's own lease are no-ops, while a class or
// lease mismatch errors. An agent bug that double-assigns must fail in the
// sim exactly as it would in production.
func TestFakeMirrorsRealDriverMismatchErrors(t *testing.T) {
	fake := NewFake()
	ctx := context.Background()
	if err := fake.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatal(err)
	}
	if err := fake.Launch(ctx, "vm-a", testClass); err != nil {
		t.Fatalf("repeat launch: %v", err)
	}
	if err := fake.Launch(ctx, "vm-a", Class("other-class")); err == nil {
		t.Fatal("relaunched an existing id under a different class")
	}
	if !fake.AdvanceBoot("vm-a") {
		t.Fatal("advance boot")
	}
	if err := fake.Prepare(ctx, "vm-a", Preparation{Lease: "lease-1"}); err != nil {
		t.Fatal(err)
	}
	if err := fake.Prepare(ctx, "vm-a", Preparation{Lease: "lease-1"}); err != nil {
		t.Fatalf("repeat prepare: %v", err)
	}
	if err := fake.Prepare(ctx, "vm-a", Preparation{Lease: "lease-2"}); err == nil {
		t.Fatal("reassigned a vm to a different lease")
	}
	if !fake.MarkListening("vm-a") {
		t.Fatal("mark listening")
	}
	if !fake.MarkAssigned("vm-a", Assignment{RequestID: "request-1", RunnerName: "lease-1"}) {
		t.Fatal("mark assigned")
	}
	if err := fake.Rendezvous(ctx, "vm-a", Rendezvous{
		Lease: "lease-1", WorkspaceDevice: "/dev/ws", WorkspaceMountpoint: "/work", ToolDevice: "/dev/tool", ProcessDevice: "/dev/process",
	}); err != nil {
		t.Fatal(err)
	}
	if !fake.MarkBound("vm-a") {
		t.Fatal("mark bound")
	}
}
