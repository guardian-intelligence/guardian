package main

import "testing"

func TestTrustClassForRun(t *testing.T) {
	cases := []struct {
		name string
		obs  runObservation
		want string
	}{
		{
			name: "pull_request same repo",
			obs:  runObservation{Event: "pull_request", RepositoryFullName: "org/repo", HeadRepositoryFullName: "org/repo"},
			want: trustClassPR,
		},
		{
			name: "pull_request fork",
			obs:  runObservation{Event: "pull_request", RepositoryFullName: "org/repo", HeadRepositoryFullName: "fork/repo"},
			want: trustClassPRFork,
		},
		{
			name: "pull_request_target fork",
			obs:  runObservation{Event: "pull_request_target", RepositoryFullName: "org/repo", HeadRepositoryFullName: "fork/repo"},
			want: trustClassPRFork,
		},
		{
			name: "repo name compare is case-insensitive",
			obs:  runObservation{Event: "pull_request", RepositoryFullName: "Org/Repo", HeadRepositoryFullName: "org/repo"},
			want: trustClassPR,
		},
		{
			name: "push",
			obs:  runObservation{Event: "push", RepositoryFullName: "org/repo"},
			want: trustClassBranch,
		},
		{
			name: "workflow_dispatch",
			obs:  runObservation{Event: "workflow_dispatch", RepositoryFullName: "org/repo"},
			want: trustClassBranch,
		},
		{
			name: "schedule",
			obs:  runObservation{Event: "schedule", RepositoryFullName: "org/repo"},
			want: trustClassBranch,
		},
		{
			name: "other event with PR number inferred",
			obs:  runObservation{Event: "dynamic", RepositoryFullName: "org/repo", PullRequestNumber: 12},
			want: trustClassPR,
		},
		{
			name: "other event with PR number and fork head",
			obs:  runObservation{Event: "dynamic", RepositoryFullName: "org/repo", HeadRepositoryFullName: "fork/repo", PullRequestNumber: 12},
			want: trustClassPRFork,
		},
		{
			name: "other event with head evidence only",
			obs:  runObservation{Event: "dynamic", RepositoryFullName: "org/repo", HeadSHA: "abc123"},
			want: trustClassUnknown,
		},
		{
			name: "other event with head branch only",
			obs:  runObservation{Event: "dynamic", RepositoryFullName: "org/repo", HeadBranch: "main"},
			want: trustClassUnknown,
		},
		{
			name: "workflow_run from a fork PR never reaches branch trust",
			obs: runObservation{Event: "workflow_run", RepositoryFullName: "org/repo",
				HeadRepositoryFullName: "fork/repo", HeadBranch: "main", HeadSHA: "abc123"},
			want: trustClassUnknown,
		},
		{
			name: "workflow_run same repo stays unknown",
			obs: runObservation{Event: "workflow_run", RepositoryFullName: "org/repo",
				HeadRepositoryFullName: "org/repo", HeadBranch: "main", HeadSHA: "abc123"},
			want: trustClassUnknown,
		},
		{
			name: "push with a fork head repository stays unknown",
			obs: runObservation{Event: "push", RepositoryFullName: "org/repo",
				HeadRepositoryFullName: "fork/repo"},
			want: trustClassUnknown,
		},
		{
			name: "no evidence",
			obs:  runObservation{Event: "dynamic", RepositoryFullName: "org/repo"},
			want: trustClassUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := trustClassForRun(tc.obs); got != tc.want {
				t.Fatalf("trust class = %q, want %q", got, tc.want)
			}
		})
	}
}
