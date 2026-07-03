package tests

// Tier-1 images.lock conformance: every image reference rendered from this
// repo must be digest-pinned and present in
// src/infrastructure/bootstrap/bundle/images.lock with the same digest, so a
// cold guardian-mgmt bootstrap can serve every artifact from the workstation
// mirror.
//
// Extraction rules (pragmatic, see collectImageRefsFromNode):
//   - "image:" scalar fields whose value looks like an OCI reference
//     (contains "/", registry-ish first segment, not templated)
//   - "image:" mapping fields in Helm values (registry/repository/tag/digest)
//   - kustomize "images:" transformer entries (name/newName/newTag/digest)

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const imagesLockRunfile = "src/infrastructure/bootstrap/bundle/images.lock"

// Manifest trees whose rendered image references must appear in images.lock.
var imageManifestTrees = []string{
	"src/infrastructure/deployments",
	"src/infrastructure/base",
}

var sha256DigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type imageRef struct {
	file   string
	source string
	ref    string
}

func TestImagesLockWellFormed(t *testing.T) {
	locked := parseImagesLock(t)
	if len(locked) == 0 {
		t.Fatal("images.lock contains no image entries")
	}
}

func TestRenderedImagesDigestPinnedAndLocked(t *testing.T) {
	locked := parseImagesLock(t)
	refs := collectRenderedImageRefs(t)
	if len(refs) == 0 {
		t.Fatal("extracted no image references from rendered manifests; the extractor is broken")
	}

	for _, ref := range refs {
		repo, digest, err := splitImageRef(ref.ref)
		if err != nil {
			t.Errorf("%s: %s %q: %v", ref.file, ref.source, ref.ref, err)
			continue
		}
		if !locked[repo][digest] {
			t.Errorf("%s: %s %q: %s@%s is not in %s", ref.file, ref.source, ref.ref, repo, digest, imagesLockRunfile)
		}
	}
}

// parseImagesLock reads images.lock and returns repo -> set of digests. It
// enforces the lock's own invariants: every non-comment line is digest-pinned
// and no (repo, digest) pair appears twice (tag+digest and digest-only forms
// of the same pair count as duplicates).
func parseImagesLock(t *testing.T) map[string]map[string]bool {
	t.Helper()

	raw := readText(t, runfilePath(imagesLockRunfile))
	locked := map[string]map[string]bool{}
	for i, line := range strings.Split(raw, "\n") {
		entry := line
		if idx := strings.Index(entry, "#"); idx >= 0 {
			entry = entry[:idx]
		}
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		repo, digest, err := splitImageRef(entry)
		if err != nil {
			t.Errorf("images.lock:%d: %v", i+1, err)
			continue
		}
		if locked[repo][digest] {
			t.Errorf("images.lock:%d: duplicate (repo, digest) pair %s@%s", i+1, repo, digest)
			continue
		}
		if locked[repo] == nil {
			locked[repo] = map[string]bool{}
		}
		locked[repo][digest] = true
	}
	return locked
}

// splitImageRef normalizes an OCI reference to (repository, digest), stripping
// any :tag so that tag+digest and digest-only forms compare equal.
func splitImageRef(ref string) (string, string, error) {
	if strings.ContainsAny(ref, " \t\"'") {
		return "", "", fmt.Errorf("%q is not a valid image reference", ref)
	}
	name, digest, pinned := strings.Cut(ref, "@")
	if !pinned {
		return "", "", fmt.Errorf("%q is not digest-pinned (missing @sha256:<digest>)", ref)
	}
	if !sha256DigestPattern.MatchString(digest) {
		return "", "", fmt.Errorf("%q has malformed digest %q", ref, digest)
	}
	if idx := strings.LastIndex(name, ":"); idx > strings.LastIndex(name, "/") {
		name = name[:idx]
	}
	if name == "" || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return "", "", fmt.Errorf("%q has malformed repository %q", ref, name)
	}
	return name, digest, nil
}

func collectRenderedImageRefs(t *testing.T) []imageRef {
	t.Helper()

	const anchor = "src/infrastructure/base/flux/sync.yaml"
	anchorPath := filepath.ToSlash(runfilePath(anchor))
	root := strings.TrimSuffix(anchorPath, anchor)
	if root == anchorPath {
		t.Fatalf("cannot derive runfiles repo root from %s", anchorPath)
	}

	var refs []imageRef
	for _, tree := range imageManifestTrees {
		manifests := 0
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || (filepath.Ext(path) != ".yaml" && filepath.Ext(path) != ".yml") {
				return nil
			}
			manifests++
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			for _, doc := range yamlDocs(t, path) {
				collectImageRefsFromNode(filepath.ToSlash(rel), doc, &refs)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", tree, err)
		}
		if manifests == 0 {
			t.Fatalf("no YAML manifests found under %s in runfiles; check the test's data deps", tree)
		}
	}
	return refs
}

func collectImageRefsFromNode(file string, node interface{}, refs *[]imageRef) {
	switch value := node.(type) {
	case map[string]interface{}:
		for key, child := range value {
			switch key {
			case "image":
				if scalar, ok := child.(string); ok {
					if looksLikeImageRef(scalar) {
						*refs = append(*refs, imageRef{file: file, source: "image field", ref: scalar})
					}
					continue
				}
				if ref, ok := helmImageMapRef(child); ok {
					*refs = append(*refs, imageRef{file: file, source: "helm image values", ref: ref})
					continue
				}
				collectImageRefsFromNode(file, child, refs)
			case "images":
				items, isSlice := child.([]interface{})
				if !isSlice {
					// Helm-style images maps (images: {controller: {...}})
					// must still be walked, not silently skipped.
					collectImageRefsFromNode(file, child, refs)
					continue
				}
				for _, item := range items {
					if ref, ok := kustomizeImageEntryRef(item); ok {
						*refs = append(*refs, imageRef{file: file, source: "kustomize images entry", ref: ref})
						continue
					}
					collectImageRefsFromNode(file, item, refs)
				}
			default:
				collectImageRefsFromNode(file, child, refs)
			}
		}
	case []interface{}:
		for _, item := range value {
			collectImageRefsFromNode(file, item, refs)
		}
	}
}

// looksLikeImageRef reports whether a scalar plausibly names a concrete OCI
// image: not templated, has a repository path, and its first segment is
// registry-ish (contains "." or ":", or is "localhost"). Registry-less
// placeholders such as kustomize pre-transform names are excluded; the
// transformer entry that rewrites them is checked instead.
func looksLikeImageRef(value string) bool {
	if value == "" || strings.ContainsAny(value, " \t\"'") {
		return false
	}
	if strings.Contains(value, "{{") || strings.Contains(value, "${") || strings.Contains(value, "$(") {
		return false
	}
	slash := strings.Index(value, "/")
	if slash <= 0 {
		return false
	}
	first := value[:slash]
	return first == "localhost" || strings.ContainsAny(first, ".:")
}

// helmImageMapRef recomposes Helm-style image values maps, e.g.
// {registry: quay.io, repository: openbao/openbao, tag: 2.5.4@sha256:...}.
func helmImageMapRef(node interface{}) (string, bool) {
	image := mapValue(node)
	repository := stringValue(image["repository"])
	if repository == "" || strings.Contains(repository, "{{") {
		return "", false
	}
	ref := repository
	if registry := stringValue(image["registry"]); registry != "" {
		ref = registry + "/" + repository
	}
	if tag := stringValue(image["tag"]); tag != "" {
		ref += ":" + tag
	}
	if digest := stringValue(image["digest"]); digest != "" && !strings.Contains(ref, "@") {
		ref += "@" + digest
	}
	if strings.Contains(ref, "{{") {
		return "", false
	}
	return ref, true
}

// kustomizeImageEntryRef recomposes the effective reference produced by a
// kustomize images transformer entry (name/newName/newTag/digest).
func kustomizeImageEntryRef(node interface{}) (string, bool) {
	entry := mapValue(node)
	name := stringValue(entry["newName"])
	if name == "" {
		name = stringValue(entry["name"])
	}
	newTag := stringValue(entry["newTag"])
	digest := stringValue(entry["digest"])
	if name == "" || (stringValue(entry["newName"]) == "" && newTag == "" && digest == "") {
		return "", false
	}
	ref := name
	if newTag != "" {
		ref += ":" + newTag
	}
	if digest != "" {
		ref += "@" + digest
	}
	return ref, true
}
