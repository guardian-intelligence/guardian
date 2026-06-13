package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestGatewayCRDsPrecedeCilium renders each site's machine config with the
// pinned talosctl — the same `gen config` invocation up.go issues, site
// patches in site.yaml list order — and asserts the machine-config
// invariants that Talos owns for us:
//
//   - cluster.secretboxEncryptionSecret is present, so Kubernetes Secret
//     encryption-at-rest is wired by Talos instead of a custom
//     EncryptionConfiguration path;
//   - cluster.inlineManifests lists gateway-api-crds before cilium. The
//     CRDs must exist before the Cilium render that watches them, and list
//     position in talos.patches is the only thing ordering them: nothing in
//     Go enforces it, so this test is what a careless site.yaml edit (or a
//     new site copied from a stale template) trips.
//
// up.go appends three programmatic --config-patch flags after the site
// patches (disk selector, static network, HostnameConfig delete); they are
// omitted here because they patch only machine/network documents and run
// after every site patch — they cannot reorder inlineManifests.
func TestGatewayCRDsPrecedeCilium(t *testing.T) {
	talosctl, err := talosctlPath()
	if err != nil {
		t.Fatalf("locate talosctl: %v", err)
	}
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets.yaml")
	if out, err := outputTool(talosctl, "gen", "secrets", "-o", secrets); err != nil {
		t.Fatalf("gen secrets: %v\n%s", err, out)
	}

	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			sitePath, err := toolPath("_main/src/sites/" + siteName + "/site.yaml")
			if err != nil {
				t.Fatalf("locate site.yaml: %v", err)
			}
			site, err := loadSite(sitePath)
			if err != nil {
				t.Fatal(err)
			}
			// Single --output-types value: --output must be a file path,
			// not a directory.
			rendered := filepath.Join(dir, siteName+"-controlplane.yaml")
			args := []string{
				"gen", "config", site.Cluster.Name, site.Cluster.Endpoint,
				"--with-secrets", secrets,
				"--output-types", "controlplane",
				"--output", rendered,
				"--force",
			}
			for _, p := range site.Talos.Patches {
				resolved, err := toolPath("_main/" + p)
				if err != nil {
					t.Fatalf("locate patch %s: %v", p, err)
				}
				args = append(args, "--config-patch", "@"+resolved)
			}
			if out, err := outputTool(talosctl, args...); err != nil {
				t.Fatalf("gen config: %v\n%s", err, out)
			}

			if !hasSecretboxEncryptionSecret(t, rendered) {
				t.Fatal("generated controlplane config is missing cluster.secretboxEncryptionSecret; Kubernetes Secrets must be encrypted at rest by Talos")
			}

			names := inlineManifestNames(t, rendered)
			crds := slices.Index(names, "gateway-api-crds")
			cilium := slices.Index(names, "cilium")
			if crds < 0 || cilium < 0 {
				t.Fatalf("inlineManifests = %v; want both gateway-api-crds and cilium", names)
			}
			if crds > cilium {
				t.Fatalf("inlineManifests = %v; gateway-api-crds must precede cilium", names)
			}
		})
	}
}

// inlineManifestNames collects cluster.inlineManifests entry names across
// all documents of a rendered machine config (the v1alpha1 Config carries
// them; the firewall patch's network documents decode to empty).
func inlineManifestNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	var names []string
	for {
		var doc struct {
			Cluster struct {
				InlineManifests []struct {
					Name string `yaml:"name"`
				} `yaml:"inlineManifests"`
			} `yaml:"cluster"`
		}
		if err := dec.Decode(&doc); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, m := range doc.Cluster.InlineManifests {
			names = append(names, m.Name)
		}
	}
	return names
}

func hasSecretboxEncryptionSecret(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	for {
		var doc struct {
			Cluster struct {
				SecretboxEncryptionSecret string `yaml:"secretboxEncryptionSecret"`
			} `yaml:"cluster"`
		}
		if err := dec.Decode(&doc); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if doc.Cluster.SecretboxEncryptionSecret != "" {
			return true
		}
	}
	return false
}
