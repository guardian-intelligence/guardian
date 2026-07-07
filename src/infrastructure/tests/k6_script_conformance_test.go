package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// k6 scripts are the load/synthetic surface of the platform. Two properties
// have to hold by construction, not by review:
//
//  1. The script is a real .js file, never JavaScript embedded in a YAML
//     string. Inline scripts get no lint/typecheck, force the Flux envsubst
//     opt-out dance (k6 ${...} template literals collide with substitution),
//     and drift per-stage — the login canary carried three byte-identical
//     copies until it was extracted to a single configMapGenerator source.
//
//  2. No k6 script imports over the network. k6 resolves its own modules
//     (k6, k6/http, k6/metrics, ...) from inside the pinned binary — those
//     are not fetches. But k6 ALSO supports `import x from 'https://...'`,
//     which downloads and executes code at run time. A canary pod has
//     world:443 egress for the flows it exercises, so network policy does
//     not close this; a remote import is arbitrary third-party code running
//     with the canary's credentials, outside the digest-pinned image and the
//     dark bundle. Scripts load only from k6 built-ins and repo-local files.
//
// This test is the guardrail behind docs/loadtest.md: an agent adding a load
// test to any surface cannot reintroduce inline JS or a remote import without
// failing here first.

// A k6 module import: `import ... from 'k6'` or `from 'k6/http'` etc. The
// bare/subpath k6 specifier is what makes a blob unambiguously a k6 script
// rather than incidental prose containing the word "import".
var k6ImportPattern = regexp.MustCompile(`(?m)\bfrom\s+['"]k6(/[a-z0-9-]+)?['"]`)

// A remote ES module import or require: the specifier is an http(s) URL.
var remoteImportPattern = regexp.MustCompile(`(?m)\b(?:from|require\()\s*['"]https?://`)

func TestNoInlineK6ScriptsInYAML(t *testing.T) {
	root := repoRootFromRunfiles(t)

	scanned := 0
	inspect := func(path string, docs []map[string]interface{}) {
		for _, doc := range docs {
			walkStringValues(doc, func(value string) {
				scanned++
				if k6ImportPattern.MatchString(value) {
					rel := relPath(root, path)
					t.Errorf("%s: a YAML string contains a k6 script (matched a `from 'k6...'` import). "+
						"Move the script to a repo-local .js file and render it with a kustomize configMapGenerator "+
						"(see src/infrastructure/deployments/iam/login-canary/ and docs/loadtest.md); inline k6 JS in YAML is banned.", rel)
				}
			})
		}
	}

	walkYAMLFiles(t, filepath.Join(root, "src/infrastructure/base"), inspect)
	walkYAMLFiles(t, filepath.Join(root, "src/infrastructure/deployments"), inspect)

	if scanned == 0 {
		t.Fatal("scanned 0 YAML string values; walk roots or data deps are wrong")
	}
}

func TestK6ScriptsHaveNoRemoteImports(t *testing.T) {
	root := repoRootFromRunfiles(t)

	scripts := 0
	for _, dir := range []string{
		"src/infrastructure/load",
		"src/infrastructure/deployments",
	} {
		walkJSFiles(t, filepath.Join(root, dir), func(path, content string) {
			scripts++
			if remoteImportPattern.MatchString(content) {
				t.Errorf("%s: k6 script imports code over the network (matched an http(s):// specifier). "+
					"k6 executes remote imports at run time — that is arbitrary third-party code outside the "+
					"pinned image and dark bundle. Vendor what you need into the repo and import it by relative path.", relPath(root, path))
			}
		})
	}

	if scripts == 0 {
		t.Fatal("found 0 k6 .js scripts to scan; walk roots or data deps are wrong")
	}
}

// walkStringValues visits every string leaf in a decoded YAML document tree.
func walkStringValues(value interface{}, fn func(string)) {
	switch v := value.(type) {
	case string:
		fn(v)
	case map[string]interface{}:
		for _, child := range v {
			walkStringValues(child, fn)
		}
	case []interface{}:
		for _, child := range v {
			walkStringValues(child, fn)
		}
	}
}

func walkJSFiles(t *testing.T, dir string, fn func(path, content string)) {
	t.Helper()

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".js") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fn(path, string(content))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

func relPath(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil {
		return rel
	}
	return path
}
