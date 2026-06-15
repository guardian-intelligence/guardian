package main

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

type ociRegistryManifest struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec ociRegistrySpec `yaml:"spec"`
}

type ociRegistrySpec struct {
	Site            string `yaml:"site"`
	Namespace       string `yaml:"namespace"`
	Image           string `yaml:"image"`
	Domain          string `yaml:"domain"`
	StoragePath     string `yaml:"storagePath"`
	PublisherSecret struct {
		Name        string `yaml:"name"`
		HtpasswdKey string `yaml:"htpasswdKey"`
	} `yaml:"publisherSecret"`
	Gateway struct {
		Name             string `yaml:"name"`
		Namespace        string `yaml:"namespace"`
		HTTPSSectionName string `yaml:"httpsSectionName"`
	} `yaml:"gateway"`
	Readiness struct {
		WaitForRollout *bool `yaml:"waitForRollout"`
	} `yaml:"readiness"`
}

func ociRegistries(site *Site) ([]ociRegistryManifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader(site.EnvironmentBundle.Raw))
	var out []ociRegistryManifest
	for {
		var doc ociRegistryManifest
		if err := dec.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode %s: %w", site.EnvironmentBundle.Path, err)
		}
		if doc.Kind != "OCIRegistry" {
			continue
		}
		if err := validateOCIRegistryManifest(site, doc); err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, validateOCIRegistryConfig(site, out)
}

func validateOCIRegistryManifest(site *Site, registry ociRegistryManifest) error {
	name := registry.Metadata.Name
	spec := registry.Spec
	if name == "" {
		return fmt.Errorf("environment %s: OCIRegistry metadata.name is required", site.EnvironmentBundle.Path)
	}
	required := []struct {
		field string
		value string
	}{
		{field: "spec.site", value: spec.Site},
		{field: "spec.namespace", value: spec.Namespace},
		{field: "spec.image", value: spec.Image},
		{field: "spec.domain", value: spec.Domain},
		{field: "spec.storagePath", value: spec.StoragePath},
		{field: "spec.publisherSecret.name", value: spec.PublisherSecret.Name},
		{field: "spec.publisherSecret.htpasswdKey", value: spec.PublisherSecret.HtpasswdKey},
		{field: "spec.gateway.name", value: spec.Gateway.Name},
		{field: "spec.gateway.namespace", value: spec.Gateway.Namespace},
		{field: "spec.gateway.httpsSectionName", value: spec.Gateway.HTTPSSectionName},
	}
	for _, field := range required {
		if field.value == "" {
			return fmt.Errorf("environment %s: OCIRegistry %s %s is required", site.EnvironmentBundle.Path, name, field.field)
		}
	}
	if spec.Site != site.Name {
		return fmt.Errorf("environment %s: OCIRegistry %s spec.site = %q, want %q", site.EnvironmentBundle.Path, name, spec.Site, site.Name)
	}
	return nil
}

func validateOCIRegistryConfig(site *Site, registries []ociRegistryManifest) error {
	if site.OCI.Domain == "" {
		if len(registries) > 0 {
			return fmt.Errorf("environment %s: OCIRegistry requires platform.oci.domain", site.EnvironmentBundle.Path)
		}
		return nil
	}
	if len(registries) != 1 {
		return fmt.Errorf("environment %s: platform.oci.domain requires exactly one OCIRegistry, found %d", site.EnvironmentBundle.Path, len(registries))
	}
	registry := registries[0]
	if registry.Spec.Domain != site.OCI.Domain {
		return fmt.Errorf("environment %s: OCIRegistry %s spec.domain = %q, want platform.oci.domain %q", site.EnvironmentBundle.Path, registry.Metadata.Name, registry.Spec.Domain, site.OCI.Domain)
	}
	return nil
}
