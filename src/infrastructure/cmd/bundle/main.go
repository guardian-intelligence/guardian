// bundle projects declared state into the offline-bundle pipeline.
//
// CHARTER — this command is a deterministic projection of declared state and
// must stay one. Its complete inputs are the images lock and flags; every
// fact in its output traces to a fact in an input. It must never: resolve a
// tag to a digest (an unpinned ref is a build failure, not a lookup), talk to
// a Kubernetes API, render manifests, own artifact transport (Hauler does),
// run a server, or read a config file. A new artifact type is handled by
// extending the lock format and this projection — nothing else; a new
// capability (SBOM, signing) is a separate pinned tool appended to the
// pipeline by the aspect task, never logic absorbed here.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
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
}

func main() {
	var imagesLock string
	var haulerManifestOut string
	flag.StringVar(&imagesLock, "images-lock", "src/infrastructure/bootstrap/bundle/images.lock", "digest-pinned OCI artifact inventory")
	flag.StringVar(&haulerManifestOut, "hauler-manifest-out", "", "path to write the generated content.hauler.cattle.io/v1 Images manifest")
	flag.Parse()

	if haulerManifestOut == "" {
		exitErr(errors.New("--hauler-manifest-out is required"))
	}
	lock, err := os.ReadFile(imagesLock)
	if err != nil {
		exitErr(err)
	}
	refs, err := parseImagesLock(lock)
	if err != nil {
		exitErr(err)
	}
	manifest, err := haulerManifest(refs)
	if err != nil {
		exitErr(err)
	}
	if err := os.WriteFile(haulerManifestOut, manifest, 0o644); err != nil {
		exitErr(err)
	}
	fmt.Printf("hauler manifest written: refs=%d out=%s\n", len(refs), haulerManifestOut)
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
// Images manifest.
func haulerManifest(refs []string) ([]byte, error) {
	images := make([]haulerImage, 0, len(refs))
	for _, ref := range refs {
		images = append(images, haulerImage{Name: ref})
	}
	manifest := haulerImages{
		APIVersion: "content.hauler.cattle.io/v1",
		Kind:       "Images",
		Metadata:   haulerMetadata{Name: "guardian-bundle-images"},
		Spec:       haulerImagesSpec{Images: images},
	}
	return yaml.Marshal(manifest)
}
