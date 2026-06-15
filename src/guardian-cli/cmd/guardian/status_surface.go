package main

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type statusSurfaceManifest struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec statusSurfaceSpec `yaml:"spec"`
}

type statusSurfaceSpec struct {
	Site    string   `yaml:"site"`
	Domains []string `yaml:"domains"`
	Monitor bool     `yaml:"monitor"`
}

func statusSurfaces(site *Site) ([]statusSurfaceManifest, error) {
	var out []statusSurfaceManifest
	if err := decodeEnvironmentDocuments(site.EnvironmentBundle.Raw, site.EnvironmentBundle.Path, "StatusSurface", func(node *yaml.Node) error {
		var doc statusSurfaceManifest
		if err := node.Decode(&doc); err != nil {
			return err
		}
		out = append(out, doc)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := validateStatusSurfaces(site, out); err != nil {
		return nil, err
	}
	return out, nil
}

func validateStatusSurfaces(site *Site, surfaces []statusSurfaceManifest) error {
	if len(surfaces) != 1 {
		return fmt.Errorf("environment %s: exactly one StatusSurface is required, found %d", site.EnvironmentBundle.Path, len(surfaces))
	}
	for _, surface := range surfaces {
		if err := validateStatusSurface(site, surface); err != nil {
			return err
		}
	}
	return nil
}

func validateStatusSurface(site *Site, surface statusSurfaceManifest) error {
	name := surface.Metadata.Name
	spec := surface.Spec
	if name == "" {
		return fmt.Errorf("environment %s: StatusSurface metadata.name is required", site.EnvironmentBundle.Path)
	}
	if spec.Site == "" {
		return fmt.Errorf("environment %s: StatusSurface %s spec.site is required", site.EnvironmentBundle.Path, name)
	}
	if spec.Site != site.Name {
		return fmt.Errorf("environment %s: StatusSurface %s spec.site = %q, want %q", site.EnvironmentBundle.Path, name, spec.Site, site.Name)
	}
	seen := map[string]bool{}
	for _, domain := range spec.Domains {
		if domain == "" || strings.Contains(domain, "://") || strings.Contains(domain, "/") {
			return fmt.Errorf("environment %s: StatusSurface %s spec.domains entries must be hostnames, got %q", site.EnvironmentBundle.Path, name, domain)
		}
		if seen[domain] {
			return fmt.Errorf("environment %s: StatusSurface %s spec.domains contains duplicate domain %q", site.EnvironmentBundle.Path, name, domain)
		}
		seen[domain] = true
	}
	if spec.Monitor && len(spec.Domains) == 0 {
		return fmt.Errorf("environment %s: StatusSurface %s spec.monitor requires spec.domains", site.EnvironmentBundle.Path, name)
	}
	return nil
}
