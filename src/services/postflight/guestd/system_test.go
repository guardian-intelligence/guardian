package guestd

import (
	"context"
	"testing"

	"golang.org/x/sys/unix"
)

// TestMountOptionsRouteDiscardToFilesystemData pins the flag/data split the
// kernel sees: discard is a filesystem option, not a mount flag, and must
// arrive in the data string or TRIM never reaches the zvol.
func TestMountOptionsRouteDiscardToFilesystemData(t *testing.T) {
	flags, data := mountOptions([]string{"discard", "noatime", "nodev", "nosuid", "noexec", "ro"})
	want := uintptr(unix.MS_NOATIME | unix.MS_NODEV | unix.MS_NOSUID | unix.MS_NOEXEC | unix.MS_RDONLY)
	if flags != want {
		t.Fatalf("flags %#x, want %#x", flags, want)
	}
	if data != "discard" {
		t.Fatalf("data %q, want discard", data)
	}
}

func TestMakeFilesystemRefusesUnprovisionedTypes(t *testing.T) {
	if err := (RealSystem{}).MakeFilesystem(context.Background(), "/dev/null", "xfs"); err == nil {
		t.Fatal("mkfs argv splice not refused")
	}
}

func TestUnescapeMountPath(t *testing.T) {
	for escaped, want := range map[string]string{
		`/plain`:            "/plain",
		`/with\040space`:    "/with space",
		`/tab\011and\134bs`: "/tab\tand\\bs",
		`/trailing\`:        `/trailing\`,
	} {
		if got := unescapeMountPath(escaped); got != want {
			t.Fatalf("unescapeMountPath(%q) = %q, want %q", escaped, got, want)
		}
	}
}
