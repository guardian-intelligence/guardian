package main

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

type environmentCapability struct {
	kind     string
	name     string
	resource string
	rollouts []environmentRollout
}

type environmentRollout struct {
	namespace string
	resource  string
}

func environmentCapabilities(site *Site) ([]environmentCapability, error) {
	dec := yaml.NewDecoder(bytes.NewReader(site.EnvironmentBundle.Raw))
	var out []environmentCapability
	for {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				Namespace string   `yaml:"namespace"`
				Domains   []string `yaml:"domains"`
				Readiness struct {
					WaitForRollout *bool `yaml:"waitForRollout"`
				} `yaml:"readiness"`
				Runtime struct {
					Suspend *bool `yaml:"suspend"`
				} `yaml:"runtime"`
			} `yaml:"spec"`
		}
		if err := dec.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode %s: %w", site.EnvironmentBundle.Path, err)
		}
		if doc.Kind == "" || doc.Metadata.Name == "" {
			continue
		}
		resource, ok := environmentCapabilityResource(doc.Kind)
		if !ok {
			continue
		}
		suspended := doc.Spec.Runtime.Suspend != nil && *doc.Spec.Runtime.Suspend
		waitForRollout := (doc.Spec.Readiness.WaitForRollout == nil || *doc.Spec.Readiness.WaitForRollout) && !suspended
		if doc.Kind == "StatusSurface" && len(doc.Spec.Domains) == 0 {
			waitForRollout = false
		}
		rollouts, err := environmentCapabilityRollouts(doc.Kind, doc.Spec.Namespace, waitForRollout)
		if err != nil {
			return nil, fmt.Errorf("environment %s %s: %w", doc.Kind, doc.Metadata.Name, err)
		}
		out = append(out, environmentCapability{
			kind:     doc.Kind,
			name:     doc.Metadata.Name,
			resource: resource,
			rollouts: rollouts,
		})
	}
	return out, nil
}

func environmentCapabilityResource(kind string) (string, bool) {
	switch kind {
	case "AisucksProduct":
		return "aisucksproducts.products.guardian.dev", true
	case "CompanySite":
		return "companysites.products.guardian.dev", true
	case "DirectusInstance":
		return "directusinstances.platform.guardian.dev", true
	case "ObservabilityStack":
		return "observabilitystacks.platform.guardian.dev", true
	case "OCIRegistry":
		return "ociregistries.platform.guardian.dev", true
	case "StoragePlane":
		return "storageplanes.platform.guardian.dev", true
	case "StatusSurface":
		return "statussurfaces.platform.guardian.dev", true
	default:
		return "", false
	}
}

func environmentCapabilityRollouts(kind, namespace string, waitForRollout bool) ([]environmentRollout, error) {
	if !waitForRollout {
		return nil, nil
	}
	switch kind {
	case "AisucksProduct":
		return []environmentRollout{{namespace: "aisucks", resource: "deployment/aisucks"}}, nil
	case "CompanySite":
		return []environmentRollout{{namespace: "company", resource: "deployment/company-site"}}, nil
	case "DirectusInstance":
		if namespace == "" {
			return nil, fmt.Errorf("spec.namespace is required")
		}
		return []environmentRollout{
			{namespace: namespace, resource: "statefulset/directus-postgres"},
			{namespace: namespace, resource: "deployment/directus"},
		}, nil
	case "ObservabilityStack":
		if namespace == "" {
			return nil, fmt.Errorf("spec.namespace is required")
		}
		return []environmentRollout{{namespace: namespace, resource: "deployment/victoria-metrics"}}, nil
	case "OCIRegistry":
		if namespace == "" {
			return nil, fmt.Errorf("spec.namespace is required")
		}
		return []environmentRollout{{namespace: namespace, resource: "deployment/zot"}}, nil
	case "StatusSurface":
		if namespace == "" {
			return nil, fmt.Errorf("spec.namespace is required")
		}
		return []environmentRollout{{namespace: namespace, resource: "deployment/status"}}, nil
	default:
		return nil, nil
	}
}

func waitEnvironmentCapabilities(kubectl, kubeconfig string, site *Site) error {
	capabilities, err := environmentCapabilities(site)
	if err != nil {
		return err
	}
	for _, cap := range capabilities {
		if err := runTool(kubectl, "--kubeconfig", kubeconfig, "wait", "--for=condition=Ready", cap.resource+"/"+cap.name, "--timeout=5m"); err != nil {
			return fmt.Errorf("wait %s %s: %w", cap.kind, cap.name, err)
		}
		for _, rollout := range cap.rollouts {
			if err := runTool(kubectl, "--kubeconfig", kubeconfig, "-n", rollout.namespace, "rollout", "status", rollout.resource, "--timeout=5m"); err != nil {
				return fmt.Errorf("wait %s %s rollout %s/%s: %w", cap.kind, cap.name, rollout.namespace, rollout.resource, err)
			}
		}
	}
	return nil
}
