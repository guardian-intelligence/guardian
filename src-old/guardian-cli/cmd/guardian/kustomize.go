package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type kustomizeImageOverride struct {
	name string
	ref  string
}

type kustomizePatch struct {
	kind  string
	name  string
	op    string
	path  string
	value any
}

func buildComponentKustomization(kubectl string, c component, images map[string]string, site *Host) ([]byte, error) {
	overrides := make([]kustomizeImageOverride, 0, len(c.imageLayouts()))
	for _, img := range c.imageLayouts() {
		ref := images[img.name]
		if ref == "" {
			return nil, fmt.Errorf("kustomize %s: image %s was not pushed", c.name, img.name)
		}
		overrides = append(overrides, kustomizeImageOverride{
			name: mirrorHost + "/" + img.name,
			ref:  ref,
		})
	}
	if c.name == "local-storage-bootstrap" {
		ref := images["postgres"]
		if ref == "" {
			return nil, fmt.Errorf("kustomize %s: image postgres was not pushed", c.name)
		}
		overrides = append(overrides, kustomizeImageOverride{
			name: mirrorHost + "/postgres",
			ref:  ref,
		})
	}
	patches, err := componentKustomizePatches(c, images, site)
	if err != nil {
		return nil, err
	}
	return buildKustomization(kubectl, c.kustomization, overrides, patches)
}

func componentKustomizePatches(c component, images map[string]string, site *Host) ([]kustomizePatch, error) {
	switch c.name {
	case "cert-manager":
		ref := images["cert-manager-acmesolver"]
		if ref == "" {
			return nil, nil
		}
		return []kustomizePatch{{
			kind:  "Deployment",
			name:  "cert-manager",
			path:  "/spec/template/spec/containers/0/args/4",
			value: "--acme-http01-solver-image=" + ref,
		}}, nil
	case "local-storage-bootstrap":
		if site == nil || site.StoragePlane == nil {
			return nil, fmt.Errorf("kustomize %s: site with validated StoragePlane is required", c.name)
		}
		if len(site.Storage.ProductPool.DeviceSerials) == 0 {
			return nil, fmt.Errorf("kustomize %s: bootstrap storage pool %s has no device serials", c.name, site.Storage.ProductPool.Name)
		}
		paths := make([]string, 0, len(site.StoragePlane.Spec.Volumes))
		for _, volume := range site.StoragePlane.Spec.Volumes {
			paths = append(paths, volume.LocalPath)
		}
		return []kustomizePatch{{
			kind:  "ConfigMap",
			name:  "zfs-pool-init-values",
			path:  "/data/pool",
			value: site.Storage.ProductPool.Name,
		}, {
			kind:  "ConfigMap",
			name:  "zfs-pool-init-values",
			path:  "/data/mountpoint",
			value: site.Storage.ProductPool.Mountpoint,
		}, {
			kind:  "ConfigMap",
			name:  "zfs-pool-init-values",
			path:  "/data/serial",
			value: site.Storage.ProductPool.DeviceSerials[0],
		}, {
			kind:  "ConfigMap",
			name:  "zfs-pool-init-values",
			path:  "/data/datasetPaths",
			value: strings.Join(paths, "\n"),
		}}, nil
	case "gatus":
		if site == nil {
			return nil, fmt.Errorf("kustomize %s: host context is required", c.name)
		}
		patches := []kustomizePatch{{
			kind:  "ConfigMap",
			name:  "gatus-config",
			path:  "/data/config.yaml",
			value: gatusConfig(site),
		}}
		if site.Aisucks.Domain != "" {
			patches = append(patches, kustomizePatch{
				kind: "Deployment",
				name: "gatus",
				path: "/spec/template/spec/hostAliases",
				value: []map[string]any{{
					"ip":        "10.96.111.43",
					"hostnames": []string{site.Aisucks.Domain},
				}},
			})
		}
		return patches, nil
	case "otel-collector":
		if site == nil {
			return nil, fmt.Errorf("kustomize %s: host context is required", c.name)
		}
		config := otelCollectorConfig(site)
		configHash, err := stableHash(struct {
			Component string
			Config    string
		}{Component: c.name, Config: config})
		if err != nil {
			return nil, fmt.Errorf("kustomize %s: hash config: %w", c.name, err)
		}
		patches := []kustomizePatch{{
			kind:  "ConfigMap",
			name:  "otel-collector-config",
			path:  "/data/config.yaml",
			value: config,
		}, {
			kind:  "Deployment",
			name:  "otel-collector",
			path:  "/spec/template/metadata/annotations/guardian.dev~1render-sha256",
			value: configHash,
		}}
		if site.Clickhouse.Enabled {
			patches = append(patches, otelCollectorLedgerPatches()...)
		}
		return patches, nil
	default:
		return nil, nil
	}
}

func buildEnvironmentKustomization(kubectl string, site *Host, images map[string]string) ([]byte, error) {
	path := filepath.Dir(site.EnvironmentBundle.Path)
	return buildKustomization(kubectl, path, nil, environmentImagePatches(site, images))
}

func environmentImagePatches(site *Host, images map[string]string) []kustomizePatch {
	patches := make([]kustomizePatch, 0, 8)
	add := func(kind, name, path, imageKey string) {
		if ref := images[imageKey]; ref != "" {
			patches = append(patches, kustomizePatch{
				kind:  kind,
				name:  name,
				path:  path,
				value: ref,
			})
		}
	}
	add("ObservabilityStack", "observability", "/spec/victoriaMetrics/image", "victoria-metrics")
	add("StatusSurface", "status", "/spec/image", "status")
	if hostUsesPlatformOCI(site) {
		add("OCIRegistry", "zot", "/spec/image", "zot")
	}
	add("AisucksProduct", "aisucks", "/spec/image", "aisucks")
	add("CompanySite", "company-site", "/spec/image", "company-site")
	add("DirectusInstance", "directus", "/spec/image", "directus")
	add("DirectusInstance", "directus", "/spec/postgresImage", "postgres")
	return patches
}

func buildKustomization(kubectl, base string, images []kustomizeImageOverride, patches []kustomizePatch) ([]byte, error) {
	tmp, err := os.MkdirTemp("", "guardian-kustomize-*")
	if err != nil {
		return nil, fmt.Errorf("create kustomize workspace: %w", err)
	}
	defer os.RemoveAll(tmp)
	baseCopy := filepath.Join(tmp, "base")
	if err := copyKustomizeRoot(base, baseCopy); err != nil {
		return nil, fmt.Errorf("copy kustomize base %s: %w", base, err)
	}
	if len(images) == 0 && len(patches) == 0 {
		out, err := outputTool(kubectl, "kustomize", baseCopy)
		return []byte(out), err
	}
	overlay := filepath.Join(tmp, "overlay")
	if err := os.MkdirAll(overlay, 0o755); err != nil {
		return nil, fmt.Errorf("create kustomize overlay: %w", err)
	}
	overlayText, err := kustomizationOverlay(images, patches)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(overlay, "kustomization.yaml"), []byte(overlayText), 0o644); err != nil {
		return nil, fmt.Errorf("write kustomize overlay: %w", err)
	}
	out, err := outputTool(kubectl, "kustomize", overlay)
	return []byte(out), err
}

func kustomizationOverlay(images []kustomizeImageOverride, patches []kustomizePatch) (string, error) {
	var b strings.Builder
	b.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\n")
	b.WriteString("kind: Kustomization\n")
	b.WriteString("resources:\n")
	b.WriteString("  - ../base\n")
	if len(images) > 0 {
		b.WriteString("images:\n")
		for _, img := range images {
			repo, digest, ok := strings.Cut(img.ref, "@")
			if !ok {
				repo = img.ref
				digest = ""
			}
			b.WriteString("  - name: ")
			b.WriteString(img.name)
			b.WriteString("\n    newName: ")
			b.WriteString(repo)
			if digest != "" {
				b.WriteString("\n    digest: ")
				b.WriteString(digest)
			}
			b.WriteByte('\n')
		}
	}
	if len(patches) > 0 {
		b.WriteString("patches:\n")
		for _, patch := range patches {
			op := patch.op
			if op == "" {
				op = "replace"
			}
			b.WriteString("  - target:\n")
			b.WriteString("      kind: ")
			b.WriteString(patch.kind)
			b.WriteString("\n      name: ")
			b.WriteString(patch.name)
			b.WriteString("\n    patch: |-\n")
			b.WriteString("      - op: ")
			b.WriteString(op)
			b.WriteByte('\n')
			b.WriteString("        path: ")
			b.WriteString(patch.path)
			b.WriteString("\n        value:")
			rendered, err := yaml.Marshal(patch.value)
			if err != nil {
				return "", fmt.Errorf("marshal kustomize patch value for %s/%s %s: %w", patch.kind, patch.name, patch.path, err)
			}
			value := strings.TrimSuffix(string(rendered), "\n")
			_, isString := patch.value.(string)
			if !isString || strings.Contains(value, "\n") {
				b.WriteByte('\n')
				for _, line := range strings.Split(value, "\n") {
					b.WriteString("          ")
					b.WriteString(line)
					b.WriteByte('\n')
				}
			} else {
				b.WriteByte(' ')
				b.WriteString(value)
				b.WriteByte('\n')
			}
		}
	}
	return b.String(), nil
}

func copyKustomizeRoot(src, dst string) error {
	resolved, err := resolveRepoInputPath(src)
	if err == nil {
		return copyDir(resolved, dst)
	}
	kustomizationPath := filepath.Join(src, "kustomization.yaml")
	resolvedKustomization, kerr := resolveRepoInputPath(kustomizationPath)
	if kerr != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	if err := copyFile(resolvedKustomization, filepath.Join(dst, "kustomization.yaml")); err != nil {
		return err
	}
	raw, err := os.ReadFile(resolvedKustomization)
	if err != nil {
		return err
	}
	var k struct {
		Resources []string `yaml:"resources"`
	}
	if err := yaml.Unmarshal(raw, &k); err != nil {
		return fmt.Errorf("decode %s: %w", resolvedKustomization, err)
	}
	for _, resource := range k.Resources {
		if filepath.IsAbs(resource) {
			return fmt.Errorf("%s: absolute resource %s is not supported", kustomizationPath, resource)
		}
		resourcePath := filepath.Clean(filepath.Join(src, resource))
		target := filepath.Join(dst, resource)
		if resolvedResource, err := resolveRepoInputPath(resourcePath); err == nil {
			info, err := os.Stat(resolvedResource)
			if err != nil {
				return err
			}
			if info.IsDir() {
				if err := copyDir(resolvedResource, target); err != nil {
					return err
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					return err
				}
				if err := copyFile(resolvedResource, target); err != nil {
					return err
				}
			}
			continue
		}
		if err := copyKustomizeRoot(resourcePath, target); err != nil {
			return err
		}
	}
	return nil
}

func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if info.IsDir() {
				return fmt.Errorf("kustomize base %s contains symlinked directory %s", src, path)
			}
			return copyFile(path, target)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
