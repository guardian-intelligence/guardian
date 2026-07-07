// Package imageset derives the complete OCI artifact inventory of a
// guardian checkout: the union of the hand-declared lock
// (src/infrastructure/bootstrap/bundle/images.declared.lock — artifacts
// that run without being rendered from repo manifests) and the image
// references extracted from the rendered manifest trees. The union is a
// pure function of the checkout — no registry, cluster, or network access —
// so CI signing, bundle builds, and offline operators all derive
// byte-identical inventories from the same revision.
package imageset

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// ManifestTrees are the repo-relative trees whose YAML documents contribute
// rendered image references to the union. Every caller (conformance tests,
// the imageset CLI, CI signing, bundle builds) must use this list — a tree
// added in one place but not the others would silently split the inventory.
var ManifestTrees = []string{
	"src/infrastructure/deployments",
	"src/infrastructure/base",
}

var sha256DigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// RenderedRef is one image reference extracted from a manifest tree,
// carrying enough provenance to make a violation actionable.
type RenderedRef struct {
	File   string // repo-relative path of the manifest
	Source string // extraction rule that produced the ref
	Ref    string
}

// ParseLock parses lock-file bytes into refs in file order. Comments (#)
// and blank lines are skipped. It enforces the lock invariants: every entry
// is digest-pinned and no (repo, digest) pair appears twice (tag+digest and
// digest-only forms of the same pair count as duplicates).
func ParseLock(data []byte) ([]string, error) {
	var refs []string
	seen := map[string]bool{}
	for i, line := range strings.Split(string(data), "\n") {
		entry := line
		if idx := strings.Index(entry, "#"); idx >= 0 {
			entry = entry[:idx]
		}
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		repo, digest, err := SplitRef(entry)
		if err != nil {
			return nil, fmt.Errorf("lock line %d: %w", i+1, err)
		}
		pair := repo + "@" + digest
		if seen[pair] {
			return nil, fmt.Errorf("lock line %d: duplicate (repo, digest) pair %s", i+1, pair)
		}
		seen[pair] = true
		refs = append(refs, entry)
	}
	if len(refs) == 0 {
		return nil, errors.New("lock contains no image entries")
	}
	return refs, nil
}

// SplitRef normalizes an OCI reference to (repository, digest), stripping
// any :tag so that tag+digest and digest-only forms compare equal.
func SplitRef(ref string) (string, string, error) {
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

// CollectRendered walks the manifest trees under root and extracts every
// concrete image reference. Extraction rules (pragmatic, structural — no
// kustomize/helm execution):
//   - "image:" scalar fields whose value looks like an OCI reference
//     (contains "/", registry-ish first segment, not templated)
//   - "image:" mapping fields in Helm values (registry/repository/tag/digest)
//   - kustomize "images:" transformer entries (name/newName/newTag/digest)
func CollectRendered(root string) ([]RenderedRef, error) {
	var refs []RenderedRef
	for _, tree := range ManifestTrees {
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
			docs, err := yamlDocuments(path)
			if err != nil {
				return err
			}
			for _, doc := range docs {
				collectFromNode(filepath.ToSlash(rel), doc, &refs)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", tree, err)
		}
		if manifests == 0 {
			return nil, fmt.Errorf("no YAML manifests found under %s; wrong --repo-root?", tree)
		}
	}
	if len(refs) == 0 {
		return nil, errors.New("extracted no image references from the manifest trees; the extractor is broken")
	}
	var hostless []string
	for _, ref := range refs {
		if ref.Source == sourceRegistryless {
			hostless = append(hostless, fmt.Sprintf("%s: %q", ref.File, ref.Ref))
		}
	}
	if len(hostless) > 0 {
		return nil, fmt.Errorf("registry-less digest-pinned image ref(s) — containerd would default the registry invisibly; name it explicitly (e.g. docker.io/...):\n  %s", strings.Join(hostless, "\n  "))
	}
	return refs, nil
}

const sourceRegistryless = "registry-less digest ref"

func yamlDocuments(path string) ([]interface{}, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var docs []interface{}
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	for {
		var doc interface{}
		if err := decoder.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				return docs, nil
			}
			// Helm chart templates (previews chart) are legitimately not
			// plain YAML; their image refs are values-templated and thus
			// excluded by the extraction rules anyway. Malformed YAML
			// WITHOUT templating stays a hard error — a manifest that
			// cannot parse cannot be audited for image refs.
			if bytes.Contains(payload, []byte("{{")) {
				return docs, nil
			}
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
}

func collectFromNode(file string, node interface{}, refs *[]RenderedRef) {
	switch value := node.(type) {
	case map[string]interface{}:
		for key, child := range value {
			switch key {
			case "image":
				if scalar, ok := child.(string); ok {
					if looksLikeImageRef(scalar) {
						*refs = append(*refs, RenderedRef{File: file, Source: "image field", Ref: scalar})
					} else if strings.Contains(scalar, "@sha256:") && !strings.ContainsAny(scalar, "{$") {
						// A digest-pinned ref whose first segment is not
						// registry-ish is a concrete image relying on the
						// runtime's default-registry resolution — invisible
						// to host-keyed dark mirrors. CollectRendered turns
						// these into a hard error.
						*refs = append(*refs, RenderedRef{File: file, Source: sourceRegistryless, Ref: scalar})
					}
					continue
				}
				if ref, ok := helmImageMapRef(child); ok {
					*refs = append(*refs, RenderedRef{File: file, Source: "helm image values", Ref: ref})
					continue
				}
				collectFromNode(file, child, refs)
			case "images":
				items, isSlice := child.([]interface{})
				if !isSlice {
					// Helm-style images maps (images: {controller: {...}})
					// must still be walked, not silently skipped.
					collectFromNode(file, child, refs)
					continue
				}
				for _, item := range items {
					if ref, ok := kustomizeImageEntryRef(item); ok {
						*refs = append(*refs, RenderedRef{File: file, Source: "kustomize images entry", Ref: ref})
						continue
					}
					collectFromNode(file, item, refs)
				}
			default:
				collectFromNode(file, child, refs)
			}
		}
	case []interface{}:
		for _, item := range value {
			collectFromNode(file, item, refs)
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
// A map carrying neither tag nor digest names a repository, not an
// artifact — a per-release placeholder (previews chart) — and is excluded
// like registry-less kustomize names; the moment a tag or digest appears,
// the recomposed ref is enforced.
func helmImageMapRef(node interface{}) (string, bool) {
	image, ok := node.(map[string]interface{})
	if !ok {
		return "", false
	}
	repository := stringValue(image["repository"])
	if repository == "" || strings.Contains(repository, "{{") {
		return "", false
	}
	ref := repository
	if registry := stringValue(image["registry"]); registry != "" {
		ref = registry + "/" + repository
	}
	tag := stringValue(image["tag"])
	digest := stringValue(image["digest"])
	if tag == "" && digest == "" {
		return "", false
	}
	if tag != "" {
		ref += ":" + tag
	}
	if digest != "" && !strings.Contains(ref, "@") {
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
	entry, ok := node.(map[string]interface{})
	if !ok {
		return "", false
	}
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

func stringValue(value interface{}) string {
	s, _ := value.(string)
	return s
}

// Rendered reduces extracted refs to the canonical, sorted, deduplicated
// entry list that joins the union. Every ref must be digest-pinned. Refs
// deduplicate by (repo, digest); when both tagged and digest-only forms of
// the same pair are rendered, the tagged form wins (the dark mirror must
// answer tag-addressed pulls), and two different tags for one pair is an
// error — the lock format treats them as duplicates.
func Rendered(refs []RenderedRef) ([]string, error) {
	forms := map[string]map[string]bool{} // repo@digest -> full-ref forms
	provenance := map[string]string{}     // repo@digest -> first file seen
	for _, rendered := range refs {
		repo, digest, err := SplitRef(rendered.Ref)
		if err != nil {
			return nil, fmt.Errorf("%s: %s %q: %w", rendered.File, rendered.Source, rendered.Ref, err)
		}
		pair := repo + "@" + digest
		if forms[pair] == nil {
			forms[pair] = map[string]bool{}
			provenance[pair] = rendered.File
		}
		forms[pair][rendered.Ref] = true
	}

	var entries []string
	for pair, set := range forms {
		var tagged, digestOnly []string
		for form := range set {
			name, _, _ := strings.Cut(form, "@")
			if idx := strings.LastIndex(name, ":"); idx > strings.LastIndex(name, "/") {
				tagged = append(tagged, form)
			} else {
				digestOnly = append(digestOnly, form)
			}
		}
		sort.Strings(tagged)
		switch {
		case len(tagged) > 1:
			return nil, fmt.Errorf("%s: rendered with conflicting tags %v; a (repo, digest) pair must resolve to one lock entry", provenance[pair], tagged)
		case len(tagged) == 1:
			entries = append(entries, tagged[0])
		default:
			entries = append(entries, digestOnly[0])
		}
	}
	sort.Strings(entries)
	return entries, nil
}

// Union assembles the complete inventory: declared entries in file order,
// then rendered entries sorted. The two sets must be disjoint by (repo,
// digest) — a declared entry that is also rendered is stale bookkeeping
// that would re-grow the hand-maintained duplication the split removed.
func Union(declared []string, rendered []string) ([]string, error) {
	declaredPairs := map[string]bool{}
	for _, ref := range declared {
		repo, digest, err := SplitRef(ref)
		if err != nil {
			return nil, fmt.Errorf("declared lock: %w", err)
		}
		declaredPairs[repo+"@"+digest] = true
	}
	var overlaps []string
	for _, ref := range rendered {
		repo, digest, err := SplitRef(ref)
		if err != nil {
			return nil, fmt.Errorf("rendered entry: %w", err)
		}
		if declaredPairs[repo+"@"+digest] {
			overlaps = append(overlaps, repo+"@"+digest)
		}
	}
	if len(overlaps) > 0 {
		sort.Strings(overlaps)
		return nil, fmt.Errorf("declared lock repeats %d rendered pair(s) — remove them from the declared lock:\n  %s", len(overlaps), strings.Join(overlaps, "\n  "))
	}
	return append(append([]string{}, declared...), rendered...), nil
}

// UnionFile renders the union as lock-file bytes. The output is a pure
// function of the inputs — CI signs these exact bytes and offline bring-up
// byte-compares a re-derivation against the drive copy.
func UnionFile(declared []string, rendered []string) ([]byte, error) {
	union, err := Union(declared, rendered)
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	out.WriteString(unionHeader)
	out.WriteString("\n# --- declared (images.declared.lock, file order) ---\n")
	for _, ref := range declared {
		out.WriteString(ref)
		out.WriteByte('\n')
	}
	out.WriteString("\n# --- rendered (extracted from manifest trees, sorted) ---\n")
	for _, ref := range union[len(declared):] {
		out.WriteString(ref)
		out.WriteByte('\n')
	}
	return []byte(out.String()), nil
}

const unionHeader = `# images.lock — GENERATED complete OCI artifact inventory: everything a
# cold guardian-mgmt bootstrap must be able to serve from the workstation
# mirror. One ref@digest per line; # starts a comment. DO NOT EDIT —
# regenerate with //src/infrastructure/cmd/imageset. Union of:
#   declared: src/infrastructure/bootstrap/bundle/images.declared.lock
#             (artifacts that run without being rendered from repo manifests)
#   rendered: image references extracted from src/infrastructure/deployments
#             and src/infrastructure/base (all digest-pinned, conformance-tested)
`

// Hosts returns the sorted, unique registry hosts referenced by refs. The
// darkBundleMirror.registries list in talm values.yaml must equal this set
// over the union: an upstream host missing from the dark mirrors would make
// nodes dial the internet (or fail) for a locked artifact.
func Hosts(refs []string) ([]string, error) {
	hosts := map[string]bool{}
	for _, ref := range refs {
		host, _, found := strings.Cut(ref, "/")
		if !found {
			return nil, fmt.Errorf("ref %q has no registry host", ref)
		}
		hosts[host] = true
	}
	var out []string
	for host := range hosts {
		out = append(out, host)
	}
	sort.Strings(out)
	return out, nil
}
