// bundle projects declared state into the offline-bundle pipeline.
//
// CHARTER — this command is a deterministic projection of declared state and
// must stay one. Its complete inputs are the images lock, flags, and the
// local files those flags name; every fact in its output traces to a fact in
// an input. It must never: resolve a tag to a digest (an unpinned ref is a
// build failure, not a lookup), talk to a Kubernetes API or any registry,
// render manifests, own artifact transport (Hauler does), run a server, or
// read a config file. A new artifact type is handled by extending the lock
// format and this projection — nothing else; a new capability (SBOM,
// signing) is a separate pinned tool appended to the pipeline by the aspect
// task, never logic absorbed here.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

// Hauler manifest shape, verified field-for-field against
// pkg/apis/hauler.cattle.io/v1 at the commit pinned in
// src/tools/hauler/go.mod. The types are restated here rather than imported
// because hauler's module graph is deliberately isolated from the root
// module (see src/tools/hauler/go.mod); schema compatibility is enforced by
// syncing generator output through the source-built hauler binary.
type haulerImages struct {
	APIVersion string           `json:"apiVersion"`
	Kind       string           `json:"kind"`
	Metadata   haulerMetadata   `json:"metadata"`
	Spec       haulerImagesSpec `json:"spec"`
}

type haulerMetadata struct {
	Name string `json:"name"`
}

type haulerImagesSpec struct {
	Images []haulerImage `json:"images"`
}

type haulerImage struct {
	// Name is the full OCI reference. Every entry is an image add — OCI Helm
	// charts and Flux artifacts included — because `hauler store add chart`
	// re-packages charts and would break digest pinning.
	Name string `json:"name"`
	// ExcludeExtras stops hauler from probing every ref's cosign-convention
	// tags (.sig/.att/.sbom) and OCI referrers: the lock enumerates the
	// bundle's complete contents, and each probe is an upstream request that
	// counts against anonymous rate limits.
	ExcludeExtras bool `json:"exclude-extras"`
}

// ociIndex is the subset of an OCI image layout index.json this tool reads
// to learn which digests an interrupted hauler store already holds.
type ociIndex struct {
	Manifests []struct {
		Digest string `json:"digest"`
	} `json:"manifests"`
}

// bundleManifest records what a built bundle is: the exact revision and the
// digests of its inputs and output. No timestamps — output is a pure
// function of the inputs.
type bundleManifest struct {
	Revision         string `json:"revision"`
	Refs             int    `json:"refs"`
	ImagesLockSHA256 string `json:"imagesLockSha256"`
	HaulPath         string `json:"haulPath"`
	HaulSHA256       string `json:"haulSha256"`
}

func main() {
	var imagesLock string
	var haulerManifestOut string
	var skipDigestsIn string
	var haulPath string
	var revision string
	var bundleManifestOut string
	var verify bool
	var bundleDir string
	flag.StringVar(&imagesLock, "images-lock", "src/infrastructure/bootstrap/bundle/images.lock", "digest-pinned OCI artifact inventory")
	flag.StringVar(&haulerManifestOut, "hauler-manifest-out", "", "path to write the generated content.hauler.cattle.io/v1 Images manifest")
	flag.StringVar(&skipDigestsIn, "skip-digests-in", "", "optional OCI layout index.json; lock refs whose digest it already holds are omitted from the projection (true incremental resume)")
	flag.StringVar(&haulPath, "haul-path", "", "built haul archive to record in the bundle manifest")
	flag.StringVar(&revision, "revision", "", "git revision the bundle was built from (with --verify: expected revision to enforce)")
	flag.StringVar(&bundleManifestOut, "bundle-manifest-out", "", "path to write the bundle manifest (requires --haul-path and --revision)")
	flag.BoolVar(&verify, "verify", false, "recompute the bundle bindings from --bundle-dir and compare against its bundle-manifest.yaml")
	flag.StringVar(&bundleDir, "bundle-dir", "", "built bundle directory to verify (requires --verify)")
	flag.Parse()

	modes := 0
	for _, set := range []bool{haulerManifestOut != "", bundleManifestOut != "", verify} {
		if set {
			modes++
		}
	}
	if modes > 1 {
		exitErr(errors.New("--hauler-manifest-out, --bundle-manifest-out, and --verify are separate modes; pass exactly one"))
	}

	lock, err := os.ReadFile(imagesLock)
	if err != nil {
		exitErr(err)
	}
	refs, err := parseImagesLock(lock)
	if err != nil {
		exitErr(err)
	}

	switch {
	case haulerManifestOut != "":
		projected := refs
		if skipDigestsIn != "" {
			projected, err = filterStoredRefs(refs, skipDigestsIn)
			if err != nil {
				exitErr(err)
			}
			fmt.Printf("skipping %d refs already in the store\n", len(refs)-len(projected))
		}
		manifest, err := haulerManifest(projected)
		if err != nil {
			exitErr(err)
		}
		if err := writeFile(haulerManifestOut, manifest); err != nil {
			exitErr(err)
		}
		fmt.Printf("hauler manifest written: refs=%d out=%s\n", len(projected), haulerManifestOut)
	case bundleManifestOut != "":
		if haulPath == "" || revision == "" {
			exitErr(errors.New("--bundle-manifest-out requires --haul-path and --revision"))
		}
		manifest, err := describeBundle(lock, refs, haulPath, revision)
		if err != nil {
			exitErr(err)
		}
		if err := writeFile(bundleManifestOut, manifest); err != nil {
			exitErr(err)
		}
		fmt.Printf("bundle manifest written: revision=%s refs=%d out=%s\n", revision, len(refs), bundleManifestOut)
	case verify:
		if bundleDir == "" {
			exitErr(errors.New("--verify requires --bundle-dir"))
		}
		lines, err := verifyBundle(bundleDir, lock, refs, revision)
		if err != nil {
			exitErr(err)
		}
		for _, line := range lines {
			fmt.Println(line)
		}
	default:
		exitErr(errors.New("one of --hauler-manifest-out, --bundle-manifest-out, or --verify is required"))
	}
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// filterStoredRefs drops lock refs whose digest already appears in the given
// OCI layout index.json. An empty projection is valid here (a fully-synced
// store resumes to zero work), so this bypasses parse-level emptiness rules.
func filterStoredRefs(refs []string, indexPath string) ([]string, error) {
	payload, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}
	var index ociIndex
	if err := json.Unmarshal(payload, &index); err != nil {
		return nil, fmt.Errorf("parse %s: %w", indexPath, err)
	}
	stored := make(map[string]bool, len(index.Manifests))
	for _, manifest := range index.Manifests {
		stored[manifest.Digest] = true
	}
	var missing []string
	for _, ref := range refs {
		_, digest, _ := strings.Cut(ref, "@")
		if !stored[digest] {
			missing = append(missing, ref)
		}
	}
	return missing, nil
}

// describeBundle hashes the lock and the built haul into a bundle manifest.
// Hashing local outputs is projection, not transport.
func describeBundle(lock []byte, refs []string, haulPath, revision string) ([]byte, error) {
	haulSum, err := sha256File(haulPath)
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(bundleManifest{
		Revision:         revision,
		Refs:             len(refs),
		ImagesLockSHA256: fmt.Sprintf("%x", sha256.Sum256(lock)),
		HaulPath:         filepath.Base(haulPath),
		HaulSHA256:       haulSum,
	})
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// verifyBundle is dark bring-up step 0: recompute every binding recorded in
// the bundle's bundle-manifest.yaml from the drive contents and compare.
// Hash digests are hard failures; the revision is compared only when the
// caller passes one (report-only otherwise, since a dark host may have no
// git to ask). It also re-derives the hauler projection of every lock ref
// and requires each derived entry to appear in the bundle's
// hauler-manifest.yaml — a pure local set check. Rehashing local files is
// projection, not transport; signature verification of the lock stays in
// the separate pinned tool the runbook invokes before this step.
func verifyBundle(dir string, lock []byte, refs []string, revision string) ([]string, error) {
	manifestPath := filepath.Join(dir, "bundle-manifest.yaml")
	payload, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var recorded bundleManifest
	if err := yaml.Unmarshal(payload, &recorded); err != nil {
		return nil, fmt.Errorf("parse %s: %w", manifestPath, err)
	}

	var lines []string

	lockSum := fmt.Sprintf("%x", sha256.Sum256(lock))
	if lockSum != recorded.ImagesLockSHA256 {
		return nil, fmt.Errorf("images.lock binding failed: manifest records sha256 %s, lock on disk hashes to %s", recorded.ImagesLockSHA256, lockSum)
	}
	lines = append(lines, fmt.Sprintf("verified: images.lock sha256=%s", lockSum))

	if len(refs) != recorded.Refs {
		return nil, fmt.Errorf("refs binding failed: manifest records %d refs, lock on disk parses to %d", recorded.Refs, len(refs))
	}
	lines = append(lines, fmt.Sprintf("verified: refs=%d", len(refs)))

	haulPath := filepath.Join(dir, filepath.Base(recorded.HaulPath))
	haulSum, err := sha256File(haulPath)
	if err != nil {
		return nil, err
	}
	if haulSum != recorded.HaulSHA256 {
		return nil, fmt.Errorf("haul binding failed: manifest records sha256 %s, %s hashes to %s", recorded.HaulSHA256, haulPath, haulSum)
	}
	lines = append(lines, fmt.Sprintf("verified: %s sha256=%s", recorded.HaulPath, haulSum))

	haulerPath := filepath.Join(dir, "hauler-manifest.yaml")
	if err := verifyHaulerCoverage(haulerPath, refs); err != nil {
		return nil, err
	}
	lines = append(lines, fmt.Sprintf("verified: hauler-manifest.yaml covers all %d lock refs", len(refs)))

	switch {
	case revision == "":
		lines = append(lines, fmt.Sprintf("recorded: revision=%s (pass --revision to enforce)", recorded.Revision))
	case revision != recorded.Revision:
		return nil, fmt.Errorf("revision binding failed: manifest records %s, --revision is %s", recorded.Revision, revision)
	default:
		lines = append(lines, fmt.Sprintf("verified: revision=%s", revision))
	}

	return lines, nil
}

// verifyHaulerCoverage checks that the bundle's hauler manifest entry set
// EQUALS the set derived from the lock refs (digest form, plus tag form for
// tagged refs — the same derivation rules haulerManifest applies). A missing
// entry means the haul was built from a projection that never saw a lock
// ref; an extra entry means the haul carries content the lock never
// declared. Both are binding failures.
func verifyHaulerCoverage(haulerPath string, refs []string) error {
	payload, err := os.ReadFile(haulerPath)
	if err != nil {
		return err
	}
	var manifest haulerImages
	if err := yaml.Unmarshal(payload, &manifest); err != nil {
		return fmt.Errorf("parse %s: %w", haulerPath, err)
	}
	names := make(map[string]bool, len(manifest.Spec.Images))
	for _, image := range manifest.Spec.Images {
		names[image.Name] = true
	}
	derived := make(map[string]bool, 2*len(refs))
	for _, ref := range refs {
		digestForm, tagForm := haulerRefForms(ref)
		derived[digestForm] = true
		if !names[digestForm] {
			return fmt.Errorf("hauler manifest binding failed: lock ref %s has no digest entry %s in %s", ref, digestForm, haulerPath)
		}
		if tagForm != "" {
			derived[tagForm] = true
			if !names[tagForm] {
				return fmt.Errorf("hauler manifest binding failed: lock ref %s has no tag entry %s in %s", ref, tagForm, haulerPath)
			}
		}
	}
	for _, image := range manifest.Spec.Images {
		if !derived[image.Name] {
			return fmt.Errorf("hauler manifest binding failed: entry %s in %s derives from no lock ref", image.Name, haulerPath)
		}
	}
	return nil
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

// parseImagesLock returns the lock's refs in file order. Comments (#) and
// blank lines are skipped. Any ref without a well-formed sha256 digest is an
// error: this tool projects pins, it never creates them, and a malformed pin
// must fail here rather than downstream inside hauler.
func parseImagesLock(data []byte) ([]string, error) {
	var refs []string
	for i, line := range strings.Split(string(data), "\n") {
		ref := strings.TrimSpace(line)
		if comment := strings.Index(ref, "#"); comment >= 0 {
			ref = strings.TrimSpace(ref[:comment])
		}
		if ref == "" {
			continue
		}
		name, digest, found := strings.Cut(ref, "@sha256:")
		if !found || name == "" || !isHex64(digest) {
			return nil, fmt.Errorf("images.lock line %d: ref %q is not digest-pinned (want <name>@sha256:<64 hex chars>)", i+1, ref)
		}
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return nil, errors.New("images.lock contains no refs")
	}
	return refs, nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// haulerManifest projects lock refs into a content.hauler.cattle.io/v1
// Images manifest. A ref pinned as <repo>:<tag>@<digest> becomes TWO entries:
// the digest form (<repo>@<digest>, byte-pinned, serves digest pulls) and the
// tag form (<repo>:<tag>, so the served mirror answers tag-addressed pulls).
// The digest-only workload refs need only the single digest entry. Talos
// renders its system images (kube-apiserver, kubelet, etcd, pause, coredns)
// by TAG, and skipFallback makes an unserved tag a fatal 404 — so tag
// coverage is not optional for a dark boot.
func haulerManifest(refs []string) ([]byte, error) {
	var images []haulerImage
	for _, ref := range refs {
		digestForm, tagForm := haulerRefForms(ref)
		images = append(images, haulerImage{Name: digestForm, ExcludeExtras: true})
		if tagForm != "" {
			images = append(images, haulerImage{Name: tagForm, ExcludeExtras: true})
		}
	}
	manifest := haulerImages{
		APIVersion: "content.hauler.cattle.io/v1",
		Kind:       "Images",
		Metadata:   haulerMetadata{Name: "guardian-bundle-images"},
		Spec:       haulerImagesSpec{Images: images},
	}
	return yaml.Marshal(manifest)
}

// haulerRefForms splits a lock ref into its digest form and, when the ref
// carries a tag, its tag form. Input is always <name>@sha256:<hex> where
// <name> is <repo> or <repo>:<tag>.
func haulerRefForms(ref string) (digestForm, tagForm string) {
	name, digest, _ := strings.Cut(ref, "@sha256:")
	repo := name
	if lastColon := strings.LastIndex(name, ":"); lastColon > strings.LastIndex(name, "/") {
		repo = name[:lastColon]
		tagForm = name
	}
	digestForm = repo + "@sha256:" + digest
	return digestForm, tagForm
}
