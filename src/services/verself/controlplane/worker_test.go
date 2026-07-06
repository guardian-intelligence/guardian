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
