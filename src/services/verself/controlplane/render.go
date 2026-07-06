package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// commentMarker identifies the one comment per PR this service owns.
const commentMarker = "<!-- verself-runner -->"

type commentJob struct {
	WorkflowName string
	Name         string
	RunnerClass  string
	Status       string
	Conclusion   string
}

// renderComment produces the per-PR comment body. Deterministic for a given
// job set (stable sort, no timestamps): the sha256 of the body is the comment
// loop's idempotency key. The Cache and Volume columns exist now but render
// "—" until stage (c) fills them (cache hit/miss, ZFS volume size).
func renderComment(jobs []commentJob) string {
	sorted := make([]commentJob, len(jobs))
	copy(sorted, jobs)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.WorkflowName != b.WorkflowName {
			return a.WorkflowName < b.WorkflowName
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.RunnerClass < b.RunnerClass
	})

	var b strings.Builder
	b.WriteString(commentMarker + "\n")
	b.WriteString("### Verself runners\n\n")
	if len(sorted) == 0 {
		b.WriteString("_No verself jobs observed for this pull request._\n")
		return b.String()
	}
	b.WriteString("| Job | Runner class | Status | Cache | Volume |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, j := range sorted {
		name := j.Name
		if j.WorkflowName != "" {
			name = j.WorkflowName + " / " + j.Name
		}
		fmt.Fprintf(&b, "| %s | `%s` | %s | — | — |\n",
			escapeCell(name), j.RunnerClass, renderStatus(j))
	}
	return b.String()
}

func renderStatus(j commentJob) string {
	if j.Status == "completed" && j.Conclusion != "" {
		return "completed (" + j.Conclusion + ")"
	}
	if j.Status == "" {
		return "unknown"
	}
	return j.Status
}

// escapeCell keeps job names from breaking the Markdown table.
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func renderedSHA256(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
