package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

const platformTLSStateDir = "platform-tls"

type platformTLSSecretRef struct {
	namespace string
	name      string
}

type edgeGatewayCertificateRef struct {
	namespace string
	name      string
}

type edgeGatewayTLSManifest struct {
	Kind string `yaml:"kind"`
	Spec struct {
		ACME struct {
			PrivateKeySecretName      string `yaml:"privateKeySecretName"`
			DNS01CloudflareSecretName string `yaml:"dns01CloudflareSecretName"`
		} `yaml:"acme"`
		Certificates []struct {
			Name       string `yaml:"name"`
			Namespace  string `yaml:"namespace"`
			SecretName string `yaml:"secretName"`
		} `yaml:"certificates"`
	} `yaml:"spec"`
}

type kubeSecretBackup struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Labels      map[string]string `json:"labels,omitempty"`
		Annotations map[string]string `json:"annotations,omitempty"`
	} `json:"metadata"`
	Immutable *bool             `json:"immutable,omitempty"`
	Type      string            `json:"type,omitempty"`
	Data      map[string]string `json:"data,omitempty"`
}

func backupPlatformTLSSecrets(kubectl, kubeconfig, state string, site *Site) error {
	if !siteUsesPlatformTLS(site) {
		return nil
	}
	refs, err := platformTLSSurvivalSecretRefs(site)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		raw, err := outputTool(kubectl, "--kubeconfig", kubeconfig, "-n", ref.namespace, "get", "secret", ref.name, "-o", "json")
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: platform TLS secret %s/%s not backed up: %v\n", ref.namespace, ref.name, err)
			continue
		}
		sanitized, err := sanitizeSecretBackup([]byte(raw), ref)
		if err != nil {
			return fmt.Errorf("platform TLS secret %s/%s backup: %w", ref.namespace, ref.name, err)
		}
		path := platformTLSSecretBackupPath(state, ref)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("platform TLS state dir %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, sanitized, 0o600); err != nil {
			return fmt.Errorf("write platform TLS secret backup %s: %w", path, err)
		}
		fmt.Fprintf(os.Stderr, "backed up platform TLS secret %s/%s\n", ref.namespace, ref.name)
	}
	return nil
}

func restorePlatformTLSSecrets(kubectl, kubeconfig, state string, site *Site) error {
	if !siteUsesPlatformTLS(site) {
		return nil
	}
	refs, err := platformTLSSurvivalSecretRefs(site)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		path := platformTLSSecretBackupPath(state, ref)
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read platform TLS secret backup %s: %w", path, err)
		}
		sanitized, err := sanitizeSecretBackup(raw, ref)
		if err != nil {
			return fmt.Errorf("platform TLS secret backup %s: %w", path, err)
		}
		ns, err := namespaceApplyManifest(ref.namespace)
		if err != nil {
			return err
		}
		if err := runToolInput(ns, kubectl, "--kubeconfig", kubeconfig, "apply", "-f", "-"); err != nil {
			return fmt.Errorf("restore platform TLS namespace %s: %w", ref.namespace, err)
		}
		if err := runToolInput(sanitized, kubectl, "--kubeconfig", kubeconfig, "apply", "-f", "-"); err != nil {
			return fmt.Errorf("restore platform TLS secret %s/%s: %w", ref.namespace, ref.name, err)
		}
		fmt.Fprintf(os.Stderr, "restored platform TLS secret %s/%s from local state\n", ref.namespace, ref.name)
	}
	return nil
}

func platformTLSSurvivalSecretRefs(site *Site) ([]platformTLSSecretRef, error) {
	refs := map[platformTLSSecretRef]struct{}{}
	dec := yaml.NewDecoder(bytes.NewReader(site.EnvironmentBundle.Raw))
	for {
		var doc edgeGatewayTLSManifest
		if err := dec.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode %s: %w", site.EnvironmentBundle.Path, err)
		}
		if doc.Kind != "EdgeGateway" {
			continue
		}
		if doc.Spec.ACME.PrivateKeySecretName != "" {
			refs[platformTLSSecretRef{namespace: "cert-manager", name: doc.Spec.ACME.PrivateKeySecretName}] = struct{}{}
		}
		if doc.Spec.ACME.DNS01CloudflareSecretName != "" {
			refs[platformTLSSecretRef{namespace: "cert-manager", name: doc.Spec.ACME.DNS01CloudflareSecretName}] = struct{}{}
		}
		for _, cert := range doc.Spec.Certificates {
			if cert.Namespace == "" || cert.SecretName == "" {
				return nil, fmt.Errorf("edge gateway certificate in %s must set namespace and secretName", site.EnvironmentBundle.Path)
			}
			refs[platformTLSSecretRef{namespace: cert.Namespace, name: cert.SecretName}] = struct{}{}
		}
	}
	out := make([]platformTLSSecretRef, 0, len(refs))
	for ref := range refs {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].namespace != out[j].namespace {
			return out[i].namespace < out[j].namespace
		}
		return out[i].name < out[j].name
	})
	return out, nil
}

func edgeGatewayCertificateObjectNames(site *Site) ([]string, error) {
	var out []string
	dec := yaml.NewDecoder(bytes.NewReader(site.EnvironmentBundle.Raw))
	for {
		var doc edgeGatewayTLSManifest
		if err := dec.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode %s: %w", site.EnvironmentBundle.Path, err)
		}
		if doc.Kind != "EdgeGateway" {
			continue
		}
		for _, cert := range doc.Spec.Certificates {
			if cert.Name == "" {
				return nil, fmt.Errorf("edge gateway certificate in %s must set name", site.EnvironmentBundle.Path)
			}
			out = append(out, "edge-gateway-certificate-"+cert.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func edgeGatewayCertificateRefs(site *Site) ([]edgeGatewayCertificateRef, error) {
	refs := map[edgeGatewayCertificateRef]struct{}{}
	dec := yaml.NewDecoder(bytes.NewReader(site.EnvironmentBundle.Raw))
	for {
		var doc edgeGatewayTLSManifest
		if err := dec.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode %s: %w", site.EnvironmentBundle.Path, err)
		}
		if doc.Kind != "EdgeGateway" {
			continue
		}
		for _, cert := range doc.Spec.Certificates {
			if cert.Name == "" || cert.Namespace == "" {
				return nil, fmt.Errorf("edge gateway certificate in %s must set name and namespace", site.EnvironmentBundle.Path)
			}
			refs[edgeGatewayCertificateRef{namespace: cert.Namespace, name: cert.Name}] = struct{}{}
		}
	}
	out := make([]edgeGatewayCertificateRef, 0, len(refs))
	for ref := range refs {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].namespace != out[j].namespace {
			return out[i].namespace < out[j].namespace
		}
		return out[i].name < out[j].name
	})
	return out, nil
}

func sanitizeSecretBackup(raw []byte, want platformTLSSecretRef) ([]byte, error) {
	var secret kubeSecretBackup
	if err := json.Unmarshal(raw, &secret); err != nil {
		return nil, err
	}
	if secret.Kind != "Secret" {
		return nil, fmt.Errorf("kind = %q, want Secret", secret.Kind)
	}
	if secret.Metadata.Namespace != want.namespace || secret.Metadata.Name != want.name {
		return nil, fmt.Errorf("secret identity = %s/%s, want %s/%s", secret.Metadata.Namespace, secret.Metadata.Name, want.namespace, want.name)
	}
	if secret.APIVersion == "" {
		secret.APIVersion = "v1"
	}
	if secret.Metadata.Annotations != nil {
		delete(secret.Metadata.Annotations, "kubectl.kubernetes.io/last-applied-configuration")
		if len(secret.Metadata.Annotations) == 0 {
			secret.Metadata.Annotations = nil
		}
	}
	out, err := json.MarshalIndent(secret, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func namespaceApplyManifest(namespace string) ([]byte, error) {
	raw, err := json.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]string{
			"name": namespace,
		},
	})
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func platformTLSSecretBackupPath(state string, ref platformTLSSecretRef) string {
	return filepath.Join(state, platformTLSStateDir, ref.namespace, ref.name+".secret.json")
}
