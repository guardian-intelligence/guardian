package main

import (
	"testing"
	"time"
)

func TestTOTPMatchesRFC6238SHA1Vector(t *testing.T) {
	t.Parallel()
	code, err := totp(
		"GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ",
		time.Unix(59, 0),
	)
	if err != nil {
		t.Fatal(err)
	}
	if code != "287082" {
		t.Fatalf("totp = %q, want 287082", code)
	}
}
