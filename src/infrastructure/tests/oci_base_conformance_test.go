package tests

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// First-party images ship the binary and the files it dlopens or reads —
// never a distro userland. The nodes already live by this rule (Talos has no
// shell or package manager); the app images follow it: static Go binaries run
// from an empty base, and the only sanctioned external base is the
// glibc/libstdc++ layer for dynamically linked runtimes (node). A fat base
// cannot sneak in silently because every external base has exactly one entry
// point: an oci.pull in MODULE.bazel. This test allowlists those pulls.
//
// If this test failed your change: either link the binary statically
// (go_binary pure = "on") and drop the base (oci_image os/architecture, no
// base attr), vendor the specific file you need as a layer (CA roots via
// @cacert_pem — see the company-site image), or, if you genuinely need a new
// shared object at runtime, add the minimal base here WITH a digest pin and a
// justification comment in MODULE.bazel.
var allowedOCIPulls = map[string]string{
	"distroless_cc_base": "gcr.io/distroless/cc-debian12",
}

func TestExternalImageBasesAreAllowlisted(t *testing.T) {
	root := repoRootFromRunfiles(t)
	raw, err := os.ReadFile(root + "MODULE.bazel")
	if err != nil {
		t.Fatal(err)
	}
	module := string(raw)

	pullRE := regexp.MustCompile(`(?s)oci\.pull\(\s*(.*?)\)`)
	nameRE := regexp.MustCompile(`name\s*=\s*"([^"]+)"`)
	imageRE := regexp.MustCompile(`image\s*=\s*"([^"]+)"`)
	digestRE := regexp.MustCompile(`digest\s*=\s*"sha256:[0-9a-f]{64}"`)

	pulls := pullRE.FindAllStringSubmatch(module, -1)
	seen := 0
	for _, m := range pulls {
		body := m[1]
		name := submatch(nameRE, body)
		image := submatch(imageRE, body)
		if name == "" || image == "" {
			t.Fatalf("MODULE.bazel: oci.pull without a literal name/image cannot be conformance-checked:\n%s", strings.TrimSpace(m[0]))
		}
		seen++
		want, ok := allowedOCIPulls[name]
		if !ok {
			t.Fatalf("MODULE.bazel: oci.pull %q (%s) is not an allowlisted image base; first-party images run from an empty base (static binary) or an allowlisted minimal base — vendor the file you need as a layer instead of importing a userland (see oci_base_conformance_test.go for the doctrine)", name, image)
		}
		if image != want {
			t.Fatalf("MODULE.bazel: oci.pull %q pulls %q, allowlist pins it to %q — update both together, deliberately", name, image, want)
		}
		if !digestRE.MatchString(body) {
			t.Fatalf("MODULE.bazel: oci.pull %q has no sha256 digest pin; tags are mutable and the build must be reproducible", name)
		}
	}
	if seen == 0 {
		t.Fatal("MODULE.bazel: no oci.pull stanzas found; if bases moved elsewhere, move this conformance test with them")
	}
}

func submatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}
