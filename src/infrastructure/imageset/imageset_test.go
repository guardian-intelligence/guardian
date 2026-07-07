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

func TestParseLockRejectsDuplicatePair(t *testing.T) {
	lock := "ghcr.io/x/y:v1@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"ghcr.io/x/y@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"
	_, err := ParseLock([]byte(lock))
	if err == nil || !strings.Contains(err.Error(), "duplicate (repo, digest) pair") {
		t.Fatalf("ParseLock() = %v, want duplicate-pair rejection (tag and digest-only forms must compare equal)", err)
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

func TestCollectRenderedExtractionRules(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
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
		"src/infrastructure/base/helm-values.yaml": `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
spec:
  values:
    image:
      registry: quay.io
      repository: openbao/openbao
      tag: 2.5.4@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
`,
		"src/infrastructure/base/kustomization.yaml": `kind: Kustomization
images:
  - name: controller
    newName: registry.k8s.io/example/controller
    digest: sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
`,
	})
	refs, err := CollectRendered(root)
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
	} {
		if !got[want] {
			t.Fatalf("CollectRendered() missing %q; got %v", want, got)
		}
	}
	if len(refs) != 3 {
		t.Fatalf("CollectRendered() = %d refs, want 3 (templated and placeholder scalars excluded): %v", len(refs), got)
	}
}

func TestCollectRenderedRejectsRegistrylessDigestRef(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/infrastructure/deployments/a.yaml": `image: grafana/k6@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`,
		"src/infrastructure/base/b.yaml": `image: docker.io/library/redis@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
`,
	})
	_, err := CollectRendered(root)
	if err == nil || !strings.Contains(err.Error(), "registry-less digest-pinned") || !strings.Contains(err.Error(), "grafana/k6") {
		t.Fatalf("CollectRendered() = %v, want registry-less rejection naming grafana/k6", err)
	}
}

func TestCollectRenderedSkipsHelmTemplates(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/infrastructure/deployments/chart/templates/web.yaml": `metadata:
  name: {{ include "preview.name" . }}
  labels:
    {{- include "preview.labels" . | nindent 4 }}
`,
		"src/infrastructure/deployments/a.yaml": `image: docker.io/library/redis@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
`,
		"src/infrastructure/base/b.yaml": `image: docker.io/library/redis@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
`,
	})
	refs, err := CollectRendered(root)
	if err != nil {
		t.Fatalf("CollectRendered() error = %v (helm templates must be skipped, not fatal)", err)
	}
	if len(refs) != 2 {
		t.Fatalf("CollectRendered() = %d refs, want 2", len(refs))
	}
}

func TestCollectRenderedFatalOnMalformedPlainYAML(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/infrastructure/deployments/bad.yaml": "key: [unclosed\n  another: :\n",
		"src/infrastructure/base/b.yaml": `image: docker.io/library/redis@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
`,
	})
	if _, err := CollectRendered(root); err == nil {
		t.Fatalf("CollectRendered() accepted malformed non-template YAML")
	}
}

func TestRenderedRejectsUnpinnedRef(t *testing.T) {
	_, err := Rendered([]RenderedRef{{File: "a.yaml", Source: "image field", Ref: "ghcr.io/x/y:v1"}})
	if err == nil || !strings.Contains(err.Error(), "not digest-pinned") {
		t.Fatalf("Rendered() = %v, want digest-pin rejection naming the file", err)
	}
	if !strings.Contains(err.Error(), "a.yaml") {
		t.Fatalf("Rendered() error %v does not name the offending file", err)
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
