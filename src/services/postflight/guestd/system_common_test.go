package guestd

import "testing"

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
