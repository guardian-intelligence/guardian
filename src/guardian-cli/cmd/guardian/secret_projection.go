package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SecretProjection is the Crossplane API for OpenBao-backed Kubernetes Secret
// delivery. This file handles bootstrap-side Bao prep and convergence waits;
// the XRD/Composition lives in src/crossplane/packages/guardian-platform.
type secretProjectionManifest struct {
	Kind            string `yaml:"kind"`
	DerivedFromKind string `yaml:"-"`
	DerivedFromName string `yaml:"-"`
	Metadata        struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec secretProjectionSpec `yaml:"spec"`
}

type secretProjectionSpec struct {
	WaitForSecrets *bool `yaml:"waitForSecrets"`
	Target         struct {
		Namespace       string            `yaml:"namespace"`
		CreateNamespace *bool             `yaml:"createNamespace"`
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

func (p secretProjectionManifest) waitForSecrets() bool {
	return p.Spec.WaitForSecrets == nil || *p.Spec.WaitForSecrets
}

func (p secretProjectionManifest) createsNamespace() bool {
	return p.Spec.Target.CreateNamespace == nil || *p.Spec.Target.CreateNamespace
}

func secretProjections(site *Site) ([]secretProjectionManifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader(site.EnvironmentBundle.Raw))
	var out []secretProjectionManifest
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
		if header.Kind != "SecretProjection" {
			continue
		}
		var doc secretProjectionManifest
		if err := node.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode %s SecretProjection: %w", site.EnvironmentBundle.Path, err)
		}
		if err := validateSecretProjection(site, doc); err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	directus, err := directusInstances(site)
	if err != nil {
		return nil, err
	}
	for _, instance := range directus {
		out = append(out, directusSecretProjection(instance))
	}
	registries, err := ociRegistries(site)
	if err != nil {
		return nil, err
	}
	for _, registry := range registries {
		out = append(out, ociRegistrySecretProjection(registry))
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
	if !projection.createsNamespace() && len(spec.Target.NamespaceLabels) > 0 {
		return fmt.Errorf("environment %s: SecretProjection %s target.namespaceLabels require target.createNamespace", site.EnvironmentBundle.Path, name)
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
		if projection.DerivedFromKind != "" {
			if err := cleanupSupersededTopLevelSecretProjection(kubectl, kubeconfig, projection); err != nil {
				return err
			}
		}
		if err := waitSecretProjectionExists(kubectl, kubeconfig, name); err != nil {
			return err
		}
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
		if !projection.waitForSecrets() {
			for _, secret := range projection.Spec.Secrets {
				if _, err := outputTool(kubectl, "--kubeconfig", kubeconfig, "-n", namespace, "get", "externalsecret", secret.Name); err != nil {
					return fmt.Errorf("get non-blocking ExternalSecret %s/%s: %w", namespace, secret.Name, err)
				}
				if _, err := outputTool(kubectl, "--kubeconfig", kubeconfig, "-n", namespace, "get", "secret", secret.Name); err != nil {
					fmt.Fprintf(os.Stderr, "warning: non-blocking SecretProjection %s has no ready Kubernetes Secret %s/%s yet; rerun with %s and %s=1 to create the missing OpenBao value\n", name, namespace, secret.Name, baoRootTokenEnv, baoAllowSecretMigrationEnv)
				}
			}
			continue
		}
		for _, secret := range projection.Spec.Secrets {
			if err := runTool(kubectl, "--kubeconfig", kubeconfig, "-n", namespace, "wait", "--for=condition=Ready", "externalsecret/"+secret.Name, "--timeout=3m"); err != nil {
				return fmt.Errorf("wait for ExternalSecret %s/%s: %w; if this projection is new on an existing unsealed OpenBao, rerun with %s and %s=1 so guardian can create the required Bao secret", namespace, secret.Name, err, baoRootTokenEnv, baoAllowSecretMigrationEnv)
			}
			if err := runTool(kubectl, "--kubeconfig", kubeconfig, "-n", namespace, "get", "secret", secret.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

func waitSecretProjectionExists(kubectl, kubeconfig, name string) error {
	return poll("SecretProjection "+name, 3*time.Minute, 2*time.Second, func() error {
		_, err := outputTool(kubectl, "--kubeconfig", kubeconfig, "get", "secretprojection", name)
		return err
	})
}

func cleanupSupersededTopLevelSecretProjection(kubectl, kubeconfig string, projection secretProjectionManifest) error {
	name := projection.Metadata.Name
	raw, err := outputTool(kubectl, "--kubeconfig", kubeconfig, "get", "secretprojection", name, "-o", "jsonpath={range .metadata.ownerReferences[*]}{.kind}{\"/\"}{.name}{\"\\n\"}{end}")
	if err != nil {
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "not found") {
			return nil
		}
		return err
	}
	owners := strings.TrimSpace(raw)
	expectedOwner := projection.DerivedFromKind + "/" + projection.DerivedFromName
	if owners == expectedOwner {
		return nil
	}
	if owners != "" {
		return fmt.Errorf("refusing to replace SecretProjection %s owned by %s", name, owners)
	}
	fmt.Fprintf(os.Stderr, "retiring top-level SecretProjection %s; %s now owns it\n", name, expectedOwner)
	return runTool(kubectl, "--kubeconfig", kubeconfig, "delete", "secretprojection", name, "--wait=true", "--timeout=3m")
}
