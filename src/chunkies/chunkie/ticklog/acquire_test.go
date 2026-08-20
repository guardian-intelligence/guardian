package ticklog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireMintsMonotonicGenerations(t *testing.T) {
	dir := t.TempDir()
	for want := uint32(1); want <= 3; want++ {
		g, err := Acquire(dir)
		if err != nil {
			t.Fatalf("acquire %d: %v", want, err)
		}
		if g.Generation() != want {
			t.Fatalf("generation = %d, want %d", g.Generation(), want)
		}
		if err := g.Release(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAcquireRefusesHeldVolume(t *testing.T) {
	dir := t.TempDir()
	g, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	// flock is per open file description, so a second in-process open
	// conflicts exactly like a second process would.
	if _, err := Acquire(dir); !errors.Is(err, ErrHeld) {
		t.Fatalf("second acquire = %v, want ErrHeld", err)
	}
	if err := g.Release(); err != nil {
		t.Fatal(err)
	}
	g2, err := Acquire(dir)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if g2.Generation() != 2 {
		t.Fatalf("generation after release = %d, want 2", g2.Generation())
	}
	g2.Release()
}

func TestAcquireRefusesCorruptCounter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "GENERATION"), []byte("not a number\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir); !errors.Is(err, ErrCorruptCounter) {
		t.Fatalf("acquire over corrupt counter = %v, want ErrCorruptCounter", err)
	}
}
