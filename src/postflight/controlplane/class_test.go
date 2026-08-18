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
			// Payload order wins, NOT lexicographic order: postflight's SQL
			// sweeper would have picked postflight-a here.
			name:   "payload order beats lexicographic",
			labels: []string{"postflight-z-huge", "postflight-a-small"},
			prefix: "postflight-",
			want:   "postflight-z-huge",
		},
		{
			name:   "first prefixed label among foreign ones",
			labels: []string{"self-hosted", "linux", "postflight-4cpu-16gb", "postflight-8cpu-32gb"},
			prefix: "postflight-",
			want:   "postflight-4cpu-16gb",
		},
		{
			name:   "whitespace trimmed",
			labels: []string{"  postflight-4cpu-16gb  "},
			prefix: "postflight-",
			want:   "postflight-4cpu-16gb",
		},
		{
			name:   "custom prefix",
			labels: []string{"gi-large", "postflight-4cpu-16gb"},
			prefix: "gi-",
			want:   "gi-large",
		},
		{name: "no match", labels: []string{"ubuntu-latest"}, prefix: "postflight-"},
		{name: "empty labels", labels: nil, prefix: "postflight-"},
		{name: "empty label entries skipped", labels: []string{"", "  "}, prefix: "postflight-"},
		{name: "case sensitive", labels: []string{"Postflight-4cpu"}, prefix: "postflight-"},
		{name: "empty prefix matches nothing", labels: []string{"postflight-4cpu"}, prefix: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runnerClassForLabels(tc.labels, tc.prefix); got != tc.want {
				t.Fatalf("class = %q, want %q", got, tc.want)
			}
		})
	}
}
