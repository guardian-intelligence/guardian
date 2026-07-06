package tests

import (
	"path/filepath"
	"strings"
	"testing"
)

// Cozystack renders every apps.cozystack.io CR (Postgres, ClickHouse,
// Tenant, Monitoring, ...) through a per-object HelmRelease that resolves its
// chart values from a `cozystack-values` Secret in the CR's OWN namespace.
// That Secret only exists in tenant-* namespaces (the root tenant-root plus
// every namespace a Tenant CR creates). Declaring such a CR in any other
// namespace leaves the HelmRelease stuck on "secrets cozystack-values not
// found", so the operator never generates the app's credential Secret and the
// consuming workload sits in CreateContainerConfigError — with the only
// runtime signal a HelmReleaseNotReady alert.
//
// This has bitten twice (the original analytics ClickHouse, then the
// verself-runner Postgres in PR #426, fixed by #427 moving it to tenant-root).
// Both times the manifest passed review and only failed live. This test moves
// the constraint to CI: any apps.cozystack.io CR whose namespace is not a
// tenant-* namespace fails here instead of hanging in the cluster.
func TestCozystackAppNamespaceConformance(t *testing.T) {
	const anchor = "src/infrastructure/base/flux/sync.yaml"
	anchorPath := filepath.ToSlash(runfilePath(anchor))
	root := strings.TrimSuffix(anchorPath, anchor)
	if root == anchorPath {
		t.Fatalf("cannot derive runfiles repo root from %s", anchorPath)
	}

	apps := 0
	walk := func(path string, docs []map[string]interface{}) {
		for _, doc := range docs {
			apiVersion := stringValue(doc["apiVersion"])
			group, _, _ := strings.Cut(apiVersion, "/")
			if group != "apps.cozystack.io" {
				continue
			}
			apps++
			metadata := mapValue(doc["metadata"])
			namespace := stringValue(metadata["namespace"])
			kind := stringValue(doc["kind"])
			name := stringValue(metadata["name"])
			if namespace == "" {
				t.Fatalf("%s: %s %s declares no namespace; a Cozystack app CR must be pinned to a tenant-* namespace so its cozystack-values Secret resolves", path, kind, name)
			}
			if !strings.HasPrefix(namespace, "tenant-") {
				t.Fatalf("%s: %s %s is in namespace %q; Cozystack app CRs render only inside a tenant-* namespace (the cozystack-values Secret exists nowhere else) — a non-tenant namespace leaves the HelmRelease stuck on 'secrets cozystack-values not found'", path, kind, name, namespace)
			}
		}
	}
	// base + deployments cover every Flux-applied manifest; talm/ holds Go
	// templates that are not plain YAML and declares no app CRs.
	walkYAMLFiles(t, filepath.Join(root, "src/infrastructure/base"), walk)
	walkYAMLFiles(t, filepath.Join(root, "src/infrastructure/deployments"), walk)
	if apps == 0 {
		t.Fatalf("namespace conformance walked 0 apps.cozystack.io CRs; the walk roots or data deps are wrong")
	}
}
