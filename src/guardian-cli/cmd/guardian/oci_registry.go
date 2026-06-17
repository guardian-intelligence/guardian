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
	Site            string            `yaml:"site"`
	Namespace       string            `yaml:"namespace"`
	NamespaceLabels map[string]string `yaml:"namespaceLabels"`
	Image           string            `yaml:"image"`
	Domain          string            `yaml:"domain"`
	Persistence     persistenceSpec   `yaml:"persistence"`
	PublisherSecret struct {
		Name        string `yaml:"name"`
		UsernameKey string `yaml:"usernameKey"`
		PasswordKey string `yaml:"passwordKey"`
		HtpasswdKey string `yaml:"htpasswdKey"`
	} `yaml:"publisherSecret"`
	Secrets struct {
		Projection struct {
			WaitForSecrets *bool `yaml:"waitForSecrets"`
			OpenBao        struct {
				Role               string `yaml:"role"`
				ServiceAccountName string `yaml:"serviceAccountName"`
			} `yaml:"openbao"`
			RemotePath string `yaml:"remotePath"`
		} `yaml:"projection"`
	} `yaml:"secrets"`
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
		var node yaml.Node
		if err := dec.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode %s: %w", site.EnvironmentBundle.Path, err)
		}
		if node.Kind == 0 {
			continue
		}
		var header struct {
			Kind string `yaml:"kind"`
		}
		if err := node.Decode(&header); err != nil {
			return nil, fmt.Errorf("decode %s document header: %w", site.EnvironmentBundle.Path, err)
		}
		if header.Kind != "OCIRegistry" {
			continue
		}
		var doc ociRegistryManifest
		if err := node.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode %s OCIRegistry: %w", site.EnvironmentBundle.Path, err)
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
		{field: "spec.publisherSecret.name", value: spec.PublisherSecret.Name},
		{field: "spec.publisherSecret.usernameKey", value: spec.PublisherSecret.UsernameKey},
		{field: "spec.publisherSecret.passwordKey", value: spec.PublisherSecret.PasswordKey},
		{field: "spec.publisherSecret.htpasswdKey", value: spec.PublisherSecret.HtpasswdKey},
		{field: "spec.secrets.projection.openbao.role", value: spec.Secrets.Projection.OpenBao.Role},
		{field: "spec.secrets.projection.openbao.serviceAccountName", value: spec.Secrets.Projection.OpenBao.ServiceAccountName},
		{field: "spec.secrets.projection.remotePath", value: spec.Secrets.Projection.RemotePath},
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
	if err := validatePersistence(site, "OCIRegistry "+name, spec.Namespace, spec.Persistence); err != nil {
		return err
	}
	if err := validateSecretProjection(site, ociRegistrySecretProjection(registry)); err != nil {
		return err
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

func ociRegistrySecretProjection(registry ociRegistryManifest) secretProjectionManifest {
	spec := registry.Spec
	waitForSecrets := true
	if spec.Secrets.Projection.WaitForSecrets != nil {
		waitForSecrets = *spec.Secrets.Projection.WaitForSecrets
	}
	projection := secretProjectionManifest{Kind: "SecretProjection"}
	projection.Metadata.Name = registry.Metadata.Name + "-publisher-secrets"
	projection.Spec.WaitForSecrets = &waitForSecrets
	projection.Spec.Target.Namespace = spec.Namespace
	projection.Spec.Target.NamespaceLabels = spec.NamespaceLabels
	projection.Spec.OpenBao.Role = spec.Secrets.Projection.OpenBao.Role
	projection.Spec.OpenBao.ServiceAccountName = spec.Secrets.Projection.OpenBao.ServiceAccountName
	projection.Spec.Secrets = []secretProjectionSecret{
		{
			Name:       spec.PublisherSecret.Name,
			Type:       "Opaque",
			RemotePath: spec.Secrets.Projection.RemotePath,
			Data: []secretProjectionData{
				{SecretKey: spec.PublisherSecret.UsernameKey, Property: spec.PublisherSecret.UsernameKey},
				{SecretKey: spec.PublisherSecret.PasswordKey, Property: spec.PublisherSecret.PasswordKey},
				{SecretKey: spec.PublisherSecret.HtpasswdKey, Property: spec.PublisherSecret.HtpasswdKey},
			},
		},
	}
	return projection
}
