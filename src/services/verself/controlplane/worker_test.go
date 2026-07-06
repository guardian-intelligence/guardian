package main

import (
	"testing"
	"time"
)

func TestRetryDelayBoundaries(t *testing.T) {
	cases := []struct {
		attempt int32
		want    time.Duration
	}{
		{-3, time.Second}, // below 1 coerced to 1
		{0, time.Second},
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 32 * time.Second}, // cap
		{7, 32 * time.Second},
		{100, 32 * time.Second},
	}
	for _, tc := range cases {
		if got := retryDelay(tc.attempt); got != tc.want {
			t.Errorf("retryDelay(%d) = %s, want %s", tc.attempt, got, tc.want)
		}
	}
}

func TestJobLockKeysMaskedPositive(t *testing.T) {
	for _, jobID := range []int64{0, 1, 1<<62 + 12345, -1} {
		hi, lo := jobLockKeys(jobID)
		if hi < 0 || lo < 0 {
			t.Errorf("jobLockKeys(%d) = (%d, %d): keys must be masked positive", jobID, hi, lo)
		}
	}
	h1, l1 := jobLockKeys(100)
	h2, l2 := jobLockKeys(101)
	if h1 == h2 && l1 == l2 {
		t.Error("distinct job ids must map to distinct key pairs")
	}
}

func TestValidRepoFullName(t *testing.T) {
	valid := []string{
		"guardian-intelligence/guardian",
		"org/repo.name",
		"a/b",
		"user_name/repo-name",
		"o.rg/re_po",
		"...a/b", // ugly but not a traversal segment
	}
	for _, s := range valid {
		if !validRepoFullName(s) {
			t.Errorf("validRepoFullName(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"",
		"norepo",
		"org/",
		"/repo",
		"org/repo/extra",
		"org/../secrets",        // traversal segment
		"../repo",               // traversal segment
		"org/.",                 // self segment
		"./repo",                // self segment
		"org/repo?x=1",          // query smuggling
		"org/repo#frag",         // fragment smuggling
		"org/repo%2f..",         // percent smuggling
		"org/repo repo",         // whitespace
		"org\\repo",             // backslash
		"org/repo\n",            // control char
		"app/installations/123", // would re-aim at another endpoint
	}
	for _, s := range invalid {
		if validRepoFullName(s) {
			t.Errorf("validRepoFullName(%q) = true, want false", s)
		}
	}
}
