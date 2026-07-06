package main

import (
	"strings"
	"testing"
)

func TestRenderCommentTable(t *testing.T) {
	jobs := []commentJob{
		{WorkflowName: "ci", Name: "build", RunnerClass: "verself-4cpu-16gb", Status: "in_progress"},
		{WorkflowName: "ci", Name: "test", RunnerClass: "verself-8cpu-32gb", Status: "completed", Conclusion: "success"},
		{WorkflowName: "release", Name: "publish", RunnerClass: "verself-4cpu-16gb", Status: "queued"},
	}
	body := renderComment(jobs)

	if !strings.HasPrefix(body, commentMarker+"\n") {
		t.Fatalf("comment must start with the marker; got %q", body[:40])
	}
	if !strings.Contains(body, "| Job | Runner class | Status | Cache | Volume |") {
		t.Fatal("header row missing")
	}
	// Stage (c) columns exist but display "—".
	if !strings.Contains(body, "| ci / build | `verself-4cpu-16gb` | in_progress | — | — |") {
		t.Fatalf("in_progress row missing:\n%s", body)
	}
	if !strings.Contains(body, "| ci / test | `verself-8cpu-32gb` | completed (success) | — | — |") {
		t.Fatalf("completed row missing:\n%s", body)
	}
	if !strings.Contains(body, "| release / publish | `verself-4cpu-16gb` | queued | — | — |") {
		t.Fatalf("queued row missing:\n%s", body)
	}
	// Stable ordering: workflow, then job name.
	buildIdx := strings.Index(body, "ci / build")
	testIdx := strings.Index(body, "ci / test")
	publishIdx := strings.Index(body, "release / publish")
	if !(buildIdx < testIdx && testIdx < publishIdx) {
		t.Fatalf("rows out of order:\n%s", body)
	}
}

func TestRenderCommentIdempotentHash(t *testing.T) {
	jobs := []commentJob{
		{WorkflowName: "ci", Name: "b", RunnerClass: "verself-x", Status: "queued"},
		{WorkflowName: "ci", Name: "a", RunnerClass: "verself-x", Status: "queued"},
	}
	reversed := []commentJob{jobs[1], jobs[0]}
	h1 := renderedSHA256(renderComment(jobs))
	h2 := renderedSHA256(renderComment(reversed))
	if h1 != h2 {
		t.Fatal("render must be input-order independent (stable sort)")
	}
	if h1 != renderedSHA256(renderComment(jobs)) {
		t.Fatal("render must be deterministic")
	}
}

func TestRenderCommentEscapesPipes(t *testing.T) {
	body := renderComment([]commentJob{
		{Name: "weird|name", RunnerClass: "verself-x", Status: "queued"},
	})
	if !strings.Contains(body, `weird\|name`) {
		t.Fatalf("pipe not escaped:\n%s", body)
	}
}

func TestRenderCommentNoJobs(t *testing.T) {
	body := renderComment(nil)
	if !strings.HasPrefix(body, commentMarker+"\n") {
		t.Fatal("marker missing on empty render")
	}
	if !strings.Contains(body, "No verself jobs observed") {
		t.Fatalf("empty render missing placeholder:\n%s", body)
	}
}

func TestRenderStatusFallbacks(t *testing.T) {
	if got := renderStatus(commentJob{Status: "completed"}); got != "completed" {
		t.Fatalf("completed without conclusion = %q", got)
	}
	if got := renderStatus(commentJob{}); got != "unknown" {
		t.Fatalf("empty status = %q, want unknown", got)
	}
}
