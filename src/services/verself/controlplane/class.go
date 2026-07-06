package main

import "strings"

// runnerClassForLabels resolves a job's runner class: the FIRST label in
// payload order carrying the configured prefix is the class — the full label
// string (e.g. "verself-4cpu-16gb"), matched case-sensitively, with no
// further grammar. "" means unresolved, which is not an error condition for
// the pipeline: the job row is still persisted and the delivery processed;
// the job just never creates demand.
//
// This is the ONLY resolution rule in the service. verself had a second,
// SQL-side rule in its sweeper (lexicographically first prefixed label) that
// could disagree with this one on multi-label jobs; here the class is
// resolved once in Go at persist time and stored on the job row, so the
// sweeper reads back the answer this function gave.
func runnerClassForLabels(labels []string, prefix string) string {
	prefix = strings.TrimSpace(prefix)
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		if prefix != "" && strings.HasPrefix(label, prefix) {
			return label
		}
	}
	return ""
}
