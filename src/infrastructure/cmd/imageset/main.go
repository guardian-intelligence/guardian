// imageset derives the generated union images lock from a checkout.
//
// CHARTER — this command is a deterministic projection of declared state and
// must stay one. Its complete inputs are the declared lock, the YAML
// manifest trees of the checkout named by --repo-root, and the optional
// dark-mirror values file; its only output is the union lock file. It must
// never: resolve a tag to a digest (an unpinned rendered ref is a build
// failure, not a lookup), talk to a Kubernetes API or any registry, execute
// kustomize/helm rendering, or read a config file. The extraction rules
// live in //src/infrastructure/imageset and are shared with the Tier-1
// conformance tests — a ref this tool sees is a ref the tests see, by
// construction.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/guardian-intelligence/guardian/src/infrastructure/imageset"
)

func main() {
	var declaredPath string
	var repoRoot string
	var out string
	var darkMirrorValues string
	flag.StringVar(&declaredPath, "declared", "", "path to images.declared.lock")
	flag.StringVar(&repoRoot, "repo-root", "", "checkout root containing the manifest trees")
	flag.StringVar(&out, "out", "", "path to write the generated union lock")
	flag.StringVar(&darkMirrorValues, "dark-mirror-values", "", "optional talm values.yaml; requires darkBundleMirror.registries to equal the union's registry host set")
	flag.Parse()

	if declaredPath == "" || repoRoot == "" || out == "" {
		exitErr(fmt.Errorf("--declared, --repo-root, and --out are all required"))
	}

	payload, err := os.ReadFile(declaredPath)
	if err != nil {
		exitErr(err)
	}
	declared, err := imageset.ParseLock(payload)
	if err != nil {
		exitErr(fmt.Errorf("%s: %w", declaredPath, err))
	}

	extracted, err := imageset.CollectRendered(repoRoot)
	if err != nil {
		exitErr(err)
	}
	rendered, err := imageset.Rendered(extracted)
	if err != nil {
		exitErr(err)
	}

	union, err := imageset.UnionFile(declared, rendered)
	if err != nil {
		exitErr(err)
	}
	if darkMirrorValues != "" {
		refs, err := imageset.ParseLock(union)
		if err != nil {
			exitErr(err)
		}
		if err := imageset.VerifyDarkMirrorHosts(darkMirrorValues, refs); err != nil {
			exitErr(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		exitErr(err)
	}
	if err := os.WriteFile(out, union, 0o644); err != nil {
		exitErr(err)
	}
	fmt.Printf("union lock written: declared=%d rendered=%d out=%s\n", len(declared), len(rendered), out)
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}
