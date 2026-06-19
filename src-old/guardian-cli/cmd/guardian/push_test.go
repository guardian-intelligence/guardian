package main

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// TestPushLayout pushes the real Bazel-built OpenBao OCI layout (a runfiles
// data dependency) into an in-process registry and reads it back by digest —
// the same path `up` exercises through the port-forward.
func TestPushLayout(t *testing.T) {
	dir, err := toolPath("_main/src/infrastructure-components/openbao/image")
	if err != nil {
		t.Fatalf("locate layout: %v", err)
	}
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	endpoint := strings.TrimPrefix(srv.URL, "http://")

	digest, err := pushLayout(dir, endpoint, "openbao")
	if err != nil {
		t.Fatalf("pushLayout: %v", err)
	}

	ref, err := name.NewDigest(fmt.Sprintf("%s/openbao@%s", endpoint, digest), name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	img, err := remote.Image(ref)
	if err != nil {
		t.Fatalf("pull back: %v", err)
	}
	got, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if got != digest {
		t.Fatalf("round-trip digest mismatch: pushed %s, pulled %s", digest, got)
	}
}
