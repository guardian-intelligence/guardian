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
	Site               string   `yaml:"site"`
	Namespace          string   `yaml:"namespace"`
	Image              string   `yaml:"image"`
	Domains            []string `yaml:"domains"`
	Monitor            bool     `yaml:"monitor"`
	Replicas           int      `yaml:"replicas"`
	ACMEEmail          string   `yaml:"acmeEmail"`
	CertDir            string   `yaml:"certDir"`
	VictoriaMetricsURL string   `yaml:"victoriaMetricsURL"`
	Resources          struct {
		Requests map[string]string `yaml:"requests"`
		Limits   map[string]string `yaml:"limits"`
	} `yaml:"resources"`
	Gateway struct {
		Name                 string `yaml:"name"`
		Namespace            string `yaml:"namespace"`
		TLSRouteAPIVersion   string `yaml:"tlsRouteAPIVersion"`
		TLSSectionNamePrefix string `yaml:"tlsSectionNamePrefix"`
	} `yaml:"gateway"`
	Readiness struct {
		WaitForRollout *bool `yaml:"waitForRollout"`
	} `yaml:"readiness"`
}

func statusSurfaces(site *Host) ([]statusSurfaceManifest, error) {
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

func validateStatusSurfaces(site *Host, surfaces []statusSurfaceManifest) error {
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

func validateStatusSurface(site *Host, surface statusSurfaceManifest) error {
	name := surface.Metadata.Name
	spec := surface.Spec
	if name == "" {
		return fmt.Errorf("environment %s: StatusSurface metadata.name is required", site.EnvironmentBundle.Path)
	}
	required := map[string]string{
		"spec.site":                         spec.Site,
		"spec.namespace":                    spec.Namespace,
		"spec.image":                        spec.Image,
		"spec.acmeEmail":                    spec.ACMEEmail,
		"spec.certDir":                      spec.CertDir,
		"spec.victoriaMetricsURL":           spec.VictoriaMetricsURL,
		"spec.resources.requests.cpu":       spec.Resources.Requests["cpu"],
		"spec.resources.requests.memory":    spec.Resources.Requests["memory"],
		"spec.resources.limits.memory":      spec.Resources.Limits["memory"],
		"spec.resources.limits.goMemory":    spec.Resources.Limits["goMemory"],
		"spec.gateway.name":                 spec.Gateway.Name,
		"spec.gateway.namespace":            spec.Gateway.Namespace,
		"spec.gateway.tlsRouteAPIVersion":   spec.Gateway.TLSRouteAPIVersion,
		"spec.gateway.tlsSectionNamePrefix": spec.Gateway.TLSSectionNamePrefix,
	}
	for field, value := range required {
		if value == "" {
			return fmt.Errorf("environment %s: StatusSurface %s %s is required", site.EnvironmentBundle.Path, name, field)
		}
	}
	if spec.Site != site.Name {
		return fmt.Errorf("environment %s: StatusSurface %s spec.site = %q, want %q", site.EnvironmentBundle.Path, name, spec.Site, site.Name)
	}
	if spec.Replicas < 0 {
		return fmt.Errorf("environment %s: StatusSurface %s spec.replicas must be non-negative", site.EnvironmentBundle.Path, name)
	}
	if len(spec.Domains) > 0 && spec.Replicas < 1 {
		return fmt.Errorf("environment %s: StatusSurface %s spec.replicas must be positive when spec.domains is non-empty", site.EnvironmentBundle.Path, name)
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
