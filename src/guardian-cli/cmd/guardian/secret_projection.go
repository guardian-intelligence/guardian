package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SecretProjection is the Crossplane API for OpenBao-backed Kubernetes Secret
// delivery. This file handles bootstrap-side Bao prep and convergence waits;
// the XRD/Composition lives in src/crossplane/packages/guardian-platform.
type secretProjectionManifest struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec secretProjectionSpec `yaml:"spec"`
}

type secretProjectionSpec struct {
	Target struct {
		Namespace       string            `yaml:"namespace"`
		NamespaceLabels map[string]string `yaml:"namespaceLabels"`
	} `yaml:"target"`
	OpenBao struct {
		Role               string `yaml:"role"`
		ServiceAccountName string `yaml:"serviceAccountName"`
	} `yaml:"openbao"`
	Secrets []secretProjectionSecret `yaml:"secrets"`
}

type secretProjectionSecret struct {
	Name       string                 `yaml:"name"`
	Type       string                 `yaml:"type"`
	RemotePath string                 `yaml:"remotePath"`
	Data       []secretProjectionData `yaml:"data"`
}

type secretProjectionData struct {
	SecretKey string `yaml:"secretKey"`
	Property  string `yaml:"property"`
}

func secretProjections(site *Site) ([]secretProjectionManifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader(site.EnvironmentBundle.Raw))
	var out []secretProjectionManifest
	for {
		var doc secretProjectionManifest
		if err := dec.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode %s: %w", site.EnvironmentBundle.Path, err)
		}
		if doc.Kind == "" {
			continue
		}
		if doc.Kind != "SecretProjection" {
			continue
		}
		if err := validateSecretProjection(site, doc); err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, nil
}

func validateSecretProjection(site *Site, projection secretProjectionManifest) error {
	name := projection.Metadata.Name
	spec := projection.Spec
	if name == "" {
		return fmt.Errorf("environment %s: SecretProjection metadata.name is required", site.EnvironmentBundle.Path)
	}
	if spec.Target.Namespace == "" {
		return fmt.Errorf("environment %s: SecretProjection %s target.namespace is required", site.EnvironmentBundle.Path, name)
	}
	if spec.OpenBao.Role == "" {
		return fmt.Errorf("environment %s: SecretProjection %s openbao.role is required", site.EnvironmentBundle.Path, name)
	}
	if spec.OpenBao.ServiceAccountName == "" {
		return fmt.Errorf("environment %s: SecretProjection %s openbao.serviceAccountName is required", site.EnvironmentBundle.Path, name)
	}
	if len(spec.Secrets) == 0 {
		return fmt.Errorf("environment %s: SecretProjection %s must declare at least one secret", site.EnvironmentBundle.Path, name)
	}
	for _, secret := range spec.Secrets {
		if secret.Name == "" {
			return fmt.Errorf("environment %s: SecretProjection %s secret name is required", site.EnvironmentBundle.Path, name)
		}
		if secret.Type == "" {
			return fmt.Errorf("environment %s: SecretProjection %s secret %s type is required", site.EnvironmentBundle.Path, name, secret.Name)
		}
		if strings.Trim(secret.RemotePath, "/") != secret.RemotePath || secret.RemotePath == "" {
			return fmt.Errorf("environment %s: SecretProjection %s secret %s remotePath must be non-empty and relative", site.EnvironmentBundle.Path, name, secret.Name)
		}
		if len(secret.Data) == 0 {
			return fmt.Errorf("environment %s: SecretProjection %s secret %s must declare data", site.EnvironmentBundle.Path, name, secret.Name)
		}
		for _, item := range secret.Data {
			if item.SecretKey == "" || item.Property == "" {
				return fmt.Errorf("environment %s: SecretProjection %s secret %s data entries require secretKey and property", site.EnvironmentBundle.Path, name, secret.Name)
			}
		}
	}
	return nil
}

func waitSecretProjections(kubectl, kubeconfig string, site *Site) error {
	projections, err := secretProjections(site)
	if err != nil {
		return err
	}
	for _, projection := range projections {
		name := projection.Metadata.Name
		if err := runTool(kubectl, "--kubeconfig", kubeconfig, "wait", "--for=condition=Ready", "secretprojections.platform.guardian.dev/"+name, "--timeout=3m"); err != nil {
			return err
		}
		namespace := projection.Spec.Target.Namespace
		if err := poll("secret projection namespace "+namespace, 3*time.Minute, 2*time.Second, func() error {
			_, err := outputTool(kubectl, "--kubeconfig", kubeconfig, "get", "namespace", namespace)
			return err
		}); err != nil {
			return err
		}
		for _, secret := range projection.Spec.Secrets {
			if err := runTool(kubectl, "--kubeconfig", kubeconfig, "-n", namespace, "wait", "--for=condition=Ready", "externalsecret/"+secret.Name, "--timeout=3m"); err != nil {
				return err
			}
			if err := runTool(kubectl, "--kubeconfig", kubeconfig, "-n", namespace, "get", "secret", secret.Name); err != nil {
				return err
			}
		}
	}
	return nil
}
