// Package imageset derives the complete OCI artifact inventory of a
// guardian checkout: the union of the hand-declared lock
// (src/infrastructure/bootstrap/bundle/images.declared.lock — artifacts
// that run without being rendered from repo manifests) and the artifact
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
// rendered artifact references to the union. Every caller (conformance
// tests, the imageset CLI, CI signing, bundle builds) must use this list —
// a tree added in one place but not the others would silently split the
// inventory.
var ManifestTrees = []string{
	"src/infrastructure/deployments",
	"src/infrastructure/base",
}

var sha256DigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// RenderedRef is one artifact reference extracted from a manifest tree,
// carrying enough provenance to make a violation actionable.
type RenderedRef struct {
	File   string // repo-relative path of the manifest
	Source string // extraction rule that produced the ref
	Ref    string
}

// ParseLock parses lock-file bytes into refs in file order. Comments (#)
// and blank lines are skipped. It enforces the lock invariants: every entry
// is digest-pinned and no (repo, digest) pair appears twice (tag+digest and
// digest-only forms of the same pair count as duplicates). All violations
// are reported in one error, not just the first.
func ParseLock(data []byte) ([]string, error) {
	var refs []string
	var problems []string
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
			problems = append(problems, fmt.Sprintf("lock line %d: %v", i+1, err))
			continue
		}
		pair := repo + "@" + digest
		if seen[pair] {
			problems = append(problems, fmt.Sprintf("lock line %d: duplicate (repo, digest) pair %s", i+1, pair))
			continue
		}
		seen[pair] = true
		refs = append(refs, entry)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%d lock violation(s):\n  %s", len(problems), strings.Join(problems, "\n  "))
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
// concrete artifact reference. Extraction rules (pragmatic, structural — no
// kustomize/helm execution):
//   - "image:" scalar fields whose value looks like an OCI reference
//     (contains "/", registry-ish first segment, not templated)
//   - "image:" mapping fields in Helm values (registry/repository/tag/digest)
//   - kustomize "images:" transformer entries (name/newName/newTag/digest)
//   - Flux OCIRepository CRs pinned by digest (spec.url + spec.ref)
//
// Directories containing a Chart.yaml are skipped whole: Helm chart
// templates are not plain YAML and a chart's values are per-release
// placeholders, consumable only through rendering. Everywhere else a YAML
// parse error is fatal — a manifest that cannot parse cannot be audited.
//
// Concrete-but-malformed references are hard errors, all reported at once:
// registry-less refs under an "image:" key (the runtime would default the
// registry invisibly, and host-keyed dark mirrors cannot serve them) and
// Helm image maps whose tag-embedded digest conflicts with their digest
// field or that pin neither tag nor digest.
func CollectRendered(root string) ([]RenderedRef, error) {
	c := &treeCollector{}
	for _, tree := range ManifestTrees {
		manifests := 0
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if _, statErr := os.Stat(filepath.Join(path, "Chart.yaml")); statErr == nil {
					return fs.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != ".yaml" && filepath.Ext(path) != ".yml" {
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
			file := filepath.ToSlash(rel)
			for _, doc := range docs {
				if ref, ok := ociRepositoryRef(doc); ok {
					c.refs = append(c.refs, RenderedRef{File: file, Source: "oci repository pin", Ref: ref})
				}
				c.collect(file, doc)
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
	if len(c.problems) > 0 {
		return nil, fmt.Errorf("%d rendered-reference violation(s):\n  %s", len(c.problems), strings.Join(c.problems, "\n  "))
	}
	if len(c.refs) == 0 {
		return nil, errors.New("extracted no artifact references from the manifest trees; the extractor is broken")
	}
	return c.refs, nil
}

type treeCollector struct {
	refs     []RenderedRef
	problems []string
}

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
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
}

// asStringMap normalizes a decoded YAML mapping. yaml.v3 produces
// map[string]interface{} for string-keyed mappings but falls back to
// map[interface{}]interface{} when any key is not a string — a subtree that
// must still be walked, not silently skipped.
func asStringMap(node interface{}) (map[string]interface{}, bool) {
	switch value := node.(type) {
	case map[string]interface{}:
		return value, true
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(value))
		for key, child := range value {
			out[fmt.Sprint(key)] = child
		}
		return out, true
	}
	return nil, false
}

func (c *treeCollector) collect(file string, node interface{}) {
	if items, ok := node.([]interface{}); ok {
		for _, item := range items {
			c.collect(file, item)
		}
		return
	}
	value, ok := asStringMap(node)
	if !ok {
		return
	}
	for key, child := range value {
		switch key {
		case "image":
			if scalar, ok := child.(string); ok {
				switch {
				case looksLikeImageRef(scalar):
					c.refs = append(c.refs, RenderedRef{File: file, Source: "image field", Ref: scalar})
				case isTemplated(scalar) || !strings.Contains(scalar, "/"):
					// Templated values and registry-less bare names
					// (kustomize pre-transform placeholders) are not
					// concrete refs; the transformer entry or rendered
					// values that realize them are checked instead.
				default:
					c.problems = append(c.problems, fmt.Sprintf("%s: registry-less image ref %q — the runtime would default the registry invisibly; name it explicitly (e.g. docker.io/...)", file, scalar))
				}
				continue
			}
			if ref, problem, ok := helmImageMapRef(child); ok {
				if problem != "" {
					c.problems = append(c.problems, fmt.Sprintf("%s: %s", file, problem))
				} else {
					c.refs = append(c.refs, RenderedRef{File: file, Source: "helm image values", Ref: ref})
				}
				continue
			}
			c.collect(file, child)
		case "images":
			items, isSlice := child.([]interface{})
			if !isSlice {
				// Helm-style images maps (images: {controller: {...}})
				// must still be walked, not silently skipped.
				c.collect(file, child)
				continue
			}
			for _, item := range items {
				if ref, ok := kustomizeImageEntryRef(item); ok {
					c.refs = append(c.refs, RenderedRef{File: file, Source: "kustomize images entry", Ref: ref})
					continue
				}
				c.collect(file, item)
			}
		default:
			c.collect(file, child)
		}
	}
}

func isTemplated(value string) bool {
	return strings.Contains(value, "{{") || strings.Contains(value, "${") || strings.Contains(value, "$(")
}

// looksLikeImageRef reports whether a scalar plausibly names a concrete OCI
// image: not templated, has a repository path, and its first segment is
// registry-ish (contains "." or ":", or is "localhost").
func looksLikeImageRef(value string) bool {
	if value == "" || strings.ContainsAny(value, " \t\"'") {
		return false
	}
	if isTemplated(value) {
		return false
	}
	slash := strings.Index(value, "/")
	if slash <= 0 {
		return false
	}
	return registryish(value[:slash])
}

func registryish(first string) bool {
	return first == "localhost" || strings.ContainsAny(first, ".:")
}

// helmImageMapRef recomposes Helm-style image values maps, e.g.
// {registry: quay.io, repository: openbao/openbao, tag: 2.5.4@sha256:...}.
// ok reports whether the node is an image values map at all; problem is
// non-empty when the map is one but cannot be a sound lock entry: it pins
// neither tag nor digest (the chart would default the tag — an invisible,
// unmirrorable dependency), or its tag-embedded digest conflicts with its
// digest field.
func helmImageMapRef(node interface{}) (ref string, problem string, ok bool) {
	image, isMap := asStringMap(node)
	if !isMap {
		return "", "", false
	}
	repository := stringValue(image["repository"])
	if repository == "" || strings.Contains(repository, "{{") {
		return "", "", false
	}
	ref = repository
	if registry := stringValue(image["registry"]); registry != "" {
		ref = registry + "/" + repository
	}
	tag := stringValue(image["tag"])
	digest := stringValue(image["digest"])
	if tag == "" && digest == "" {
		return "", fmt.Sprintf("helm image values map for %q pins neither tag nor digest — the chart would default the tag invisibly; pin a digest", ref), true
	}
	if tag != "" {
		ref += ":" + tag
	}
	if digest != "" {
		if _, embedded, found := strings.Cut(ref, "@"); found {
			if embedded != digest {
				return "", fmt.Sprintf("helm image values map for %q embeds digest %s in its tag but sets digest: %s — resolve the conflict to one digest", repository, embedded, digest), true
			}
		} else {
			ref += "@" + digest
		}
	}
	if strings.Contains(ref, "{{") {
		return "", "", false
	}
	return ref, "", true
}

// kustomizeImageEntryRef recomposes the effective reference produced by a
// kustomize images transformer entry (name/newName/newTag/digest).
func kustomizeImageEntryRef(node interface{}) (string, bool) {
	entry, isMap := asStringMap(node)
	if !isMap {
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

// ociRepositoryRef extracts the pinned artifact reference from a Flux
// OCIRepository document (spec.url oci://host/repo + spec.ref.tag/digest).
// These are pulled by source-controller, not containerd, but the dark
// mirror must serve them all the same. Digest-less OCIRepositories (the
// dark-mode branch-tip source) are not pinned artifacts and are skipped.
func ociRepositoryRef(doc interface{}) (string, bool) {
	m, ok := asStringMap(doc)
	if !ok || stringValue(m["kind"]) != "OCIRepository" {
		return "", false
	}
	spec, ok := asStringMap(m["spec"])
	if !ok {
		return "", false
	}
	url := stringValue(spec["url"])
	if !strings.HasPrefix(url, "oci://") {
		return "", false
	}
	pin, ok := asStringMap(spec["ref"])
	if !ok {
		return "", false
	}
	digest := stringValue(pin["digest"])
	if digest == "" {
		return "", false
	}
	ref := strings.TrimPrefix(url, "oci://")
	if tag := stringValue(pin["tag"]); tag != "" {
		ref += ":" + tag
	}
	return ref + "@" + digest, true
}

func stringValue(value interface{}) string {
	s, _ := value.(string)
	return s
}

// Rendered reduces extracted refs to the canonical, sorted, deduplicated
// entry list that joins the union. Every ref must be digest-pinned and name
// a registry-ish host. Refs deduplicate by (repo, digest); when both tagged
// and digest-only forms of the same pair are rendered, the tagged form wins
// (the dark mirror must answer tag-addressed pulls), and two different tags
// for one pair is an error — the lock format treats them as duplicates.
// All violations are reported in one error.
func Rendered(refs []RenderedRef) ([]string, error) {
	forms := map[string]map[string]bool{} // repo@digest -> full-ref forms
	provenance := map[string]string{}     // repo@digest -> first file seen
	var problems []string
	for _, rendered := range refs {
		repo, digest, err := SplitRef(rendered.Ref)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s %q: %v", rendered.File, rendered.Source, rendered.Ref, err))
			continue
		}
		if first, _, _ := strings.Cut(repo, "/"); !registryish(first) {
			problems = append(problems, fmt.Sprintf("%s: %s %q: registry-less repository %q — name the registry explicitly (e.g. docker.io/...)", rendered.File, rendered.Source, rendered.Ref, repo))
			continue
		}
		pair := repo + "@" + digest
		if forms[pair] == nil {
			forms[pair] = map[string]bool{}
			provenance[pair] = rendered.File
		}
		forms[pair][rendered.Ref] = true
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%d rendered-reference violation(s):\n  %s", len(problems), strings.Join(problems, "\n  "))
	}

	var entries []string
	for pair, set := range forms {
		repoAndDigest, _, _ := strings.Cut(pair, "@")
		var tagged, digestOnly []string
		for form := range set {
			name, _, _ := strings.Cut(form, "@")
			if name != repoAndDigest {
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
// digest) — the declared lock lists only what no manifest renders, so a
// declared entry that a manifest also renders is stale bookkeeping.
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
#   rendered: artifact references extracted from src/infrastructure/deployments
#             and src/infrastructure/base (all digest-pinned, conformance-tested)
`

// Hosts returns the sorted, unique registry hosts referenced by refs.
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

// VerifyDarkMirrorHosts checks that the darkBundleMirror.registries list in
// the given talm values file equals the union's registry host set. A host
// missing from the dark mirrors would make nodes dial the internet (or
// fail) for a locked artifact; an extra host is a stale mirror entry.
func VerifyDarkMirrorHosts(valuesPath string, unionRefs []string) error {
	payload, err := os.ReadFile(valuesPath)
	if err != nil {
		return err
	}
	var values struct {
		DarkBundleMirror struct {
			Registries []string `yaml:"registries"`
		} `yaml:"darkBundleMirror"`
	}
	if err := yaml.Unmarshal(payload, &values); err != nil {
		return fmt.Errorf("parse %s: %w", valuesPath, err)
	}
	declared := append([]string{}, values.DarkBundleMirror.Registries...)
	sort.Strings(declared)
	hosts, err := Hosts(unionRefs)
	if err != nil {
		return err
	}
	if strings.Join(declared, "\n") != strings.Join(hosts, "\n") {
		return fmt.Errorf("darkBundleMirror.registries in %s = %v, union lock hosts = %v; the dark mirror set must equal the locked upstreams", valuesPath, declared, hosts)
	}
	return nil
}
