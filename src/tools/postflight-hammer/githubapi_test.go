package main

import (
	"context"
	"testing"
	"time"
)

func TestDispatchListAndAttemptReads(t *testing.T) {
	f := newFakeGitHub(t)
	c := f.client(t)
	ctx := context.Background()

	if err := c.dispatchWorkflow(ctx, "acme/demo", "ci.yml", "main"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	runs, err := c.listWorkflowRuns(ctx, "acme/demo", "ci.yml", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if runs[0].Status != "in_progress" {
		t.Fatalf("first list should observe in_progress, got %s", runs[0].Status)
	}
	if _, err := c.listWorkflowRuns(ctx, "acme/demo", "ci.yml", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("second list: %v", err)
	}

	run, err := c.getRun(ctx, "acme/demo", runs[0].ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.Status != "completed" || run.Conclusion != "success" {
		t.Fatalf("run = %s/%s, want completed/success", run.Status, run.Conclusion)
	}

	jobs, err := c.attemptJobs(ctx, "acme/demo", run.ID, 1)
	if err != nil {
		t.Fatalf("jobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Name != "build" || len(jobs[0].Steps) != 3 {
		t.Fatalf("jobs = %+v", jobs)
	}

	sizes, err := c.attemptLogs(ctx, "acme/demo", run.ID, 1)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if len(sizes) != 3 {
		t.Fatalf("log entries = %v, want 3", sizes)
	}
	for name, size := range sizes {
		if size == 0 {
			t.Fatalf("entry %s is empty", name)
		}
	}
}

func TestCancelAndRerun(t *testing.T) {
	f := newFakeGitHub(t)
	c := f.client(t)
	ctx := context.Background()

	if err := c.dispatchWorkflow(ctx, "acme/demo", "ci.yml", "main"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	runs, err := c.listWorkflowRuns(ctx, "acme/demo", "ci.yml", time.Now().Add(-time.Hour))
	if err != nil || len(runs) != 1 {
		t.Fatalf("list: %v (%d runs)", err, len(runs))
	}
	id := runs[0].ID

	if err := c.cancelRun(ctx, "acme/demo", id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	run, err := c.getRun(ctx, "acme/demo", id)
	if err != nil || run.Conclusion != "cancelled" {
		t.Fatalf("after cancel: %+v err=%v", run, err)
	}
	// A second cancel races an already-terminal run: the API refuses.
	if err := c.cancelRun(ctx, "acme/demo", id); err == nil {
		t.Fatal("second cancel should fail")
	}

	if err := c.rerunRun(ctx, "acme/demo", id); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	run, err = c.getRun(ctx, "acme/demo", id)
	if err != nil || run.RunAttempt != 2 || run.Status != "queued" {
		t.Fatalf("after rerun: %+v err=%v", run, err)
	}
	attempt1, err := c.runAttempt(ctx, "acme/demo", id, 1)
	if err != nil || attempt1.Conclusion != "cancelled" {
		t.Fatalf("attempt 1 view: %+v err=%v", attempt1, err)
	}
}
