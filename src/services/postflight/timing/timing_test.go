package timing

import "testing"

func TestRecorderOrdersProcessLocalPoints(t *testing.T) {
	r, err := New("guest:vm-1", "boot-1")
	if err != nil {
		t.Fatal(err)
	}
	first := r.Point("assignment")
	second := r.Point("worker-ready")
	if first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("sequences %d, %d", first.Sequence, second.Sequence)
	}
	if first.MonotonicNS <= 0 || second.MonotonicNS < first.MonotonicNS {
		t.Fatalf("monotonic samples %d, %d", first.MonotonicNS, second.MonotonicNS)
	}
	if first.UnixNS <= 0 || second.UnixNS <= 0 {
		t.Fatal("missing wall samples")
	}
}

func TestRecorderRequiresIdentity(t *testing.T) {
	if _, err := New("", "boot"); err == nil {
		t.Fatal("empty source accepted")
	}
}
