package imageset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureLock = `# comment line
ghcr.io/example/app:v1.0.0@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa

ghcr.io/example/chart:0.1.0@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb # trailing comment
`

func TestParseLock(t *testing.T) {
	refs, err := ParseLock([]byte(fixtureLock))
	if err != nil {
		t.Fatalf("ParseLock() error = %v", err)
	}
	want := []string{
		"ghcr.io/example/app:v1.0.0@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"ghcr.io/example/chart:0.1.0@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	if len(refs) != len(want) {
		t.Fatalf("ParseLock() = %d refs, want %d", len(refs), len(want))
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("ParseLock()[%d] = %q, want %q", i, refs[i], want[i])
		}
	}
}

func TestParseLockRejectsUnpinnedAndMalformed(t *testing.T) {
	for _, lock := range []string{
		"ghcr.io/example/app:v1.0.0\n",
		"ghcr.io/example/app@sha256:abc\n",
		"# only comments\n",
	} {
		if _, err := ParseLock([]byte(lock)); err == nil {
			t.Fatalf("ParseLock() accepted %q", lock)
		}
	}
}

// All lock violations must surface in one pass, not one per run.
func TestParseLockReportsAllViolations(t *testing.T) {
	lock := "ghcr.io/a/a:v1\n" +
		"ghcr.io/b/b:v2\n" +
		"ghcr.io/x/y:v1@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"ghcr.io/x/y@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"
	_, err := ParseLock([]byte(lock))
	if err == nil {
		t.Fatal("ParseLock() accepted a lock with three violations")
	}
	for _, want := range []string{"lock line 1", "lock line 2", "duplicate (repo, digest) pair"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ParseLock() error %v does not report %q; all violations must surface at once", err, want)
		}
	}
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

// baseTree gives every fixture a non-empty second tree so CollectRendered's
// per-tree emptiness check does not trip.
func baseTree() map[string]string {
	return map[string]string{
		"src/infrastructure/base/anchor.yaml": `image: docker.io/library/redis@sha256:9999999999999999999999999999999999999999999999999999999999999999
`,
	}
}

func collectFromFixture(t *testing.T, files map[string]string) ([]RenderedRef, error) {
	t.Helper()
	root := t.TempDir()
	merged := baseTree()
	for rel, content := range files {
		merged[rel] = content
	}
	writeTree(t, root, merged)
	return CollectRendered(root)
}

func TestCollectRenderedExtractionRules(t *testing.T) {
	refs, err := collectFromFixture(t, map[string]string{
		"src/infrastructure/deployments/app/web.yaml": `apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
        - name: web
          image: ghcr.io/example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        - name: templated
          image: "{{ .Values.image }}"
        - name: placeholder
          image: controller
`,
		"src/infrastructure/deployments/app/helm.yaml": `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
spec:
  values:
    image:
      registry: quay.io
      repository: openbao/openbao
      tag: 2.5.4@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
`,
		"src/infrastructure/deployments/app/kustomization.yaml": `kind: Kustomization
images:
  - name: controller
    newName: registry.k8s.io/example/controller
    digest: sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
`,
		"src/infrastructure/deployments/app/chart-source.yaml": `apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
spec:
  url: oci://ghcr.io/example/charts/app
  ref:
    tag: 1.2.3
    digest: sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
`,
		"src/infrastructure/deployments/app/dark-source.yaml": `apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
spec:
  url: oci://198.51.100.7:5000/guardian
  ref:
    tag: dark
`,
		"src/infrastructure/deployments/app/image-watch.yaml": `apiVersion: image.toolkit.fluxcd.io/v1
kind: ImageRepository
spec:
  image: ghcr.io/example/app
  interval: 1m0s
  digestReflectionMode: Always
`,
	})
	if err != nil {
		t.Fatalf("CollectRendered() error = %v", err)
	}
	got := map[string]bool{}
	for _, ref := range refs {
		got[ref.Ref] = true
	}
	for _, want := range []string{
		"ghcr.io/example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"quay.io/openbao/openbao:2.5.4@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"registry.k8s.io/example/controller@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		"ghcr.io/example/charts/app:1.2.3@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	} {
		if !got[want] {
			t.Fatalf("CollectRendered() missing %q; got %v", want, got)
		}
	}
	// templated + placeholder scalars and the digest-less dark-mode
	// OCIRepository and the watch-only ImageRepository are excluded; the
	// base-tree anchor adds one.
	if len(refs) != 5 {
		t.Fatalf("CollectRendered() = %d refs, want 5: %v", len(refs), got)
	}
}

func TestCollectRenderedRejectsRegistrylessImageRef(t *testing.T) {
	_, err := collectFromFixture(t, map[string]string{
		"src/infrastructure/deployments/a.yaml": `image: grafana/k6@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`,
		"src/infrastructure/deployments/b.yaml": `image: grafana/k6:2.1.0
`,
	})
	if err == nil || !strings.Contains(err.Error(), "registry-less") {
		t.Fatalf("CollectRendered() = %v, want registry-less rejection", err)
	}
	// Both the pinned and the tag-only form must be reported, in one pass.
	if !strings.Contains(err.Error(), "a.yaml") || !strings.Contains(err.Error(), "b.yaml") {
		t.Fatalf("CollectRendered() error %v must name both offending files at once", err)
	}
}

func TestRenderedRejectsRegistrylessHelmMapRef(t *testing.T) {
	// A Helm image map without registry: recomposes a registry-less ref;
	// Rendered must reject it (the host-keyed dark mirror cannot serve it).
	_, err := Rendered([]RenderedRef{{File: "a.yaml", Source: "helm image values", Ref: "openbao/openbao:2.5.4@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}})
	if err == nil || !strings.Contains(err.Error(), "registry-less repository") {
		t.Fatalf("Rendered() = %v, want registry-less repository rejection", err)
	}
}

func TestCollectRenderedRejectsUnpinnedHelmMap(t *testing.T) {
	_, err := collectFromFixture(t, map[string]string{
		"src/infrastructure/deployments/a.yaml": `spec:
  values:
    image:
      repository: ghcr.io/example/app
`,
	})
	if err == nil || !strings.Contains(err.Error(), "pins neither tag nor digest") {
		t.Fatalf("CollectRendered() = %v, want unpinned helm map rejection", err)
	}
}

func TestCollectRenderedRejectsConflictingHelmDigests(t *testing.T) {
	_, err := collectFromFixture(t, map[string]string{
		"src/infrastructure/deployments/a.yaml": `spec:
  values:
    image:
      repository: ghcr.io/example/app
      tag: v1@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
      digest: sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
`,
	})
	if err == nil || !strings.Contains(err.Error(), "resolve the conflict") {
		t.Fatalf("CollectRendered() = %v, want tag-embedded digest conflict rejection", err)
	}
}

func TestCollectRenderedSkipsHelmChartDirectories(t *testing.T) {
	refs, err := collectFromFixture(t, map[string]string{
		"src/infrastructure/deployments/chart/Chart.yaml": `apiVersion: v2
name: preview
version: 0.1.0
`,
		// Not plain YAML — must be skipped with the whole chart dir, not
		// parsed and not fatal.
		"src/infrastructure/deployments/chart/templates/web.yaml": `metadata:
  name: {{ include "preview.name" . }}
  labels:
    {{- include "preview.labels" . | nindent 4 }}
`,
		// A per-release placeholder values map — also excluded via the
		// chart-dir skip rather than a special placeholder rule.
		"src/infrastructure/deployments/chart/values.yaml": `image:
  repository: ghcr.io/example/app
  digest: ""
`,
		"src/infrastructure/deployments/a.yaml": `image: docker.io/library/redis@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
`,
	})
	if err != nil {
		t.Fatalf("CollectRendered() error = %v (chart dirs must be skipped whole)", err)
	}
	if len(refs) != 2 {
		t.Fatalf("CollectRendered() = %d refs, want 2 (chart dir excluded)", len(refs))
	}
}

// Outside chart directories a YAML parse error is fatal even when the file
// contains templating markers in strings — a manifest that cannot parse
// cannot be audited, and swallowing the error would silently drop every
// image ref in the file from the inventory.
func TestCollectRenderedFatalOnMalformedYAML(t *testing.T) {
	for name, content := range map[string]string{
		"plain":               "key: [unclosed\n  another: :\n",
		"with-template-chars": "# uses {{ ctx }} in a comment\nkey: [unclosed\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := collectFromFixture(t, map[string]string{
				"src/infrastructure/deployments/bad.yaml": content,
			})
			if err == nil {
				t.Fatal("CollectRendered() accepted malformed YAML outside a chart directory")
			}
		})
	}
}

// yaml.v3 decodes mappings with any non-string key into
// map[interface{}]interface{}; the walk must descend into those, not
// silently skip the subtree.
func TestCollectRenderedWalksNonStringKeyedMaps(t *testing.T) {
	refs, err := collectFromFixture(t, map[string]string{
		"src/infrastructure/deployments/a.yaml": `values:
  8080:
    image: ghcr.io/example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`,
	})
	if err != nil {
		t.Fatalf("CollectRendered() error = %v", err)
	}
	found := false
	for _, ref := range refs {
		if strings.HasPrefix(ref.Ref, "ghcr.io/example/app@") {
			found = true
		}
	}
	if !found {
		t.Fatalf("CollectRendered() skipped the subtree under a non-string map key; refs = %v", refs)
	}
}

func TestRenderedRejectsUnpinnedRef(t *testing.T) {
	_, err := Rendered([]RenderedRef{
		{File: "a.yaml", Source: "image field", Ref: "ghcr.io/x/y:v1"},
		{File: "b.yaml", Source: "image field", Ref: "ghcr.io/x/z:v2"},
	})
	if err == nil || !strings.Contains(err.Error(), "not digest-pinned") {
		t.Fatalf("Rendered() = %v, want digest-pin rejection naming the file", err)
	}
	if !strings.Contains(err.Error(), "a.yaml") || !strings.Contains(err.Error(), "b.yaml") {
		t.Fatalf("Rendered() error %v must report all violations at once", err)
	}
}

func TestRenderedDedupesPreferringTaggedForm(t *testing.T) {
	entries, err := Rendered([]RenderedRef{
		{File: "a.yaml", Ref: "ghcr.io/x/y@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{File: "b.yaml", Ref: "ghcr.io/x/y:v1@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	})
	if err != nil {
		t.Fatalf("Rendered() error = %v", err)
	}
	if len(entries) != 1 || entries[0] != "ghcr.io/x/y:v1@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("Rendered() = %v, want the single tagged form (mirror must answer tag pulls)", entries)
	}
}

func TestRenderedRejectsConflictingTags(t *testing.T) {
	_, err := Rendered([]RenderedRef{
		{File: "a.yaml", Ref: "ghcr.io/x/y:v1@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{File: "b.yaml", Ref: "ghcr.io/x/y:v2@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	})
	if err == nil || !strings.Contains(err.Error(), "conflicting tags") {
		t.Fatalf("Rendered() = %v, want conflicting-tag rejection", err)
	}
}

func TestUnionRejectsOverlap(t *testing.T) {
	declared := []string{"ghcr.io/x/y:v1@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	rendered := []string{"ghcr.io/x/y@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	_, err := Union(declared, rendered)
	if err == nil || !strings.Contains(err.Error(), "remove them from the declared lock") {
		t.Fatalf("Union() = %v, want disjointness rejection with the removal instruction", err)
	}
}

func TestUnionFileDeterministicAndParseable(t *testing.T) {
	declared := []string{"docker.io/library/redis@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	rendered := []string{
		"ghcr.io/a/a@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"quay.io/b/b:v2@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	first, err := UnionFile(declared, rendered)
	if err != nil {
		t.Fatalf("UnionFile() error = %v", err)
	}
	second, _ := UnionFile(declared, rendered)
	if string(first) != string(second) {
		t.Fatalf("UnionFile() output is not deterministic")
	}
	refs, err := ParseLock(first)
	if err != nil {
		t.Fatalf("UnionFile() output does not re-parse as a lock: %v", err)
	}
	if len(refs) != 3 || refs[0] != declared[0] || refs[1] != rendered[0] || refs[2] != rendered[1] {
		t.Fatalf("UnionFile() refs = %v, want declared order then sorted rendered", refs)
	}
	hosts, err := Hosts(refs)
	if err != nil {
		t.Fatalf("Hosts() error = %v", err)
	}
	if strings.Join(hosts, ",") != "docker.io,ghcr.io,quay.io" {
		t.Fatalf("Hosts() = %v, want sorted unique union hosts", hosts)
	}
}

func TestVerifyDarkMirrorHosts(t *testing.T) {
	dir := t.TempDir()
	values := filepath.Join(dir, "values.yaml")
	refs := []string{
		"docker.io/library/redis@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"ghcr.io/a/a@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	if err := os.WriteFile(values, []byte("darkBundleMirror:\n  registries:\n    - docker.io\n    - ghcr.io\n"), 0o644); err != nil {
		t.Fatalf("write values fixture: %v", err)
	}
	if err := VerifyDarkMirrorHosts(values, refs); err != nil {
		t.Fatalf("VerifyDarkMirrorHosts() = %v, want match", err)
	}
	if err := os.WriteFile(values, []byte("darkBundleMirror:\n  registries:\n    - docker.io\n"), 0o644); err != nil {
		t.Fatalf("write values fixture: %v", err)
	}
	if err := VerifyDarkMirrorHosts(values, refs); err == nil {
		t.Fatal("VerifyDarkMirrorHosts() accepted a registries list missing a union host")
	}
}
