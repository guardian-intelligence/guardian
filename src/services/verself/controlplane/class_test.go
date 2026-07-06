package main

import "testing"

func TestRunnerClassForLabels(t *testing.T) {
	cases := []struct {
		name   string
		labels []string
		prefix string
		want   string
	}{
		{
			// Payload order wins, NOT lexicographic order: verself's SQL
			// sweeper would have picked verself-a here.
			name:   "payload order beats lexicographic",
			labels: []string{"verself-z-huge", "verself-a-small"},
			prefix: "verself-",
			want:   "verself-z-huge",
		},
		{
			name:   "first prefixed label among foreign ones",
			labels: []string{"self-hosted", "linux", "verself-4cpu-16gb", "verself-8cpu-32gb"},
			prefix: "verself-",
			want:   "verself-4cpu-16gb",
		},
		{
			name:   "whitespace trimmed",
			labels: []string{"  verself-4cpu-16gb  "},
			prefix: "verself-",
			want:   "verself-4cpu-16gb",
		},
		{
			name:   "custom prefix",
			labels: []string{"gi-large", "verself-4cpu-16gb"},
			prefix: "gi-",
			want:   "gi-large",
		},
		{name: "no match", labels: []string{"ubuntu-latest"}, prefix: "verself-"},
		{name: "empty labels", labels: nil, prefix: "verself-"},
		{name: "empty label entries skipped", labels: []string{"", "  "}, prefix: "verself-"},
		{name: "case sensitive", labels: []string{"Verself-4cpu"}, prefix: "verself-"},
		{name: "empty prefix matches nothing", labels: []string{"verself-4cpu"}, prefix: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runnerClassForLabels(tc.labels, tc.prefix); got != tc.want {
				t.Fatalf("class = %q, want %q", got, tc.want)
			}
		})
	}
}
