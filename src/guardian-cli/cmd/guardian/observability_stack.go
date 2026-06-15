package main

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

type observabilityStackManifest struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec observabilityStackSpec `yaml:"spec"`
}

type observabilityStackSpec struct {
	Site            string            `yaml:"site"`
	Namespace       string            `yaml:"namespace"`
	NamespaceLabels map[string]string `yaml:"namespaceLabels"`
	VictoriaMetrics struct {
		Image              string `yaml:"image"`
		StoragePath        string `yaml:"storagePath"`
		RetentionPeriod    string `yaml:"retentionPeriod"`
		MemoryAllowedBytes string `yaml:"memoryAllowedBytes"`
		Ports              struct {
			HTTP int `yaml:"http"`
		} `yaml:"ports"`
		Resources struct {
			Requests map[string]string `yaml:"requests"`
			Limits   map[string]string `yaml:"limits"`
		} `yaml:"resources"`
	} `yaml:"victoriaMetrics"`
	Clickhouse struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"clickhouse"`
}

func observabilityStacks(site *Site) ([]observabilityStackManifest, error) {
	var out []observabilityStackManifest
	if err := decodeEnvironmentDocuments(site.EnvironmentBundle.Raw, site.EnvironmentBundle.Path, "ObservabilityStack", func(node *yaml.Node) error {
		var doc observabilityStackManifest
		if err := node.Decode(&doc); err != nil {
			return err
		}
		out = append(out, doc)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := validateObservabilityStacks(site, out); err != nil {
		return nil, err
	}
	return out, nil
}

func validateObservabilityStacks(site *Site, stacks []observabilityStackManifest) error {
	if len(stacks) != 1 {
		return fmt.Errorf("environment %s: exactly one ObservabilityStack is required, found %d", site.EnvironmentBundle.Path, len(stacks))
	}
	for _, stack := range stacks {
		if err := validateObservabilityStack(site, stack); err != nil {
			return err
		}
	}
	return nil
}

func validateObservabilityStack(site *Site, stack observabilityStackManifest) error {
	name := stack.Metadata.Name
	spec := stack.Spec
	if name == "" {
		return fmt.Errorf("environment %s: ObservabilityStack metadata.name is required", site.EnvironmentBundle.Path)
	}
	required := map[string]string{
		"site":      spec.Site,
		"namespace": spec.Namespace,
	}
	for field, value := range required {
		if value == "" {
			return fmt.Errorf("environment %s: ObservabilityStack %s spec.%s is required", site.EnvironmentBundle.Path, name, field)
		}
	}
	if spec.Site != site.Name {
		return fmt.Errorf("environment %s: ObservabilityStack %s spec.site = %q, want %q", site.EnvironmentBundle.Path, name, spec.Site, site.Name)
	}
	vm := spec.VictoriaMetrics
	vmRequired := map[string]string{
		"victoriaMetrics.image":              vm.Image,
		"victoriaMetrics.storagePath":        vm.StoragePath,
		"victoriaMetrics.retentionPeriod":    vm.RetentionPeriod,
		"victoriaMetrics.memoryAllowedBytes": vm.MemoryAllowedBytes,
	}
	for field, value := range vmRequired {
		if value == "" {
			return fmt.Errorf("environment %s: ObservabilityStack %s spec.%s is required", site.EnvironmentBundle.Path, name, field)
		}
	}
	if vm.Ports.HTTP == 0 {
		return fmt.Errorf("environment %s: ObservabilityStack %s spec.victoriaMetrics.ports.http is required", site.EnvironmentBundle.Path, name)
	}
	if vm.Resources.Requests["cpu"] == "" || vm.Resources.Requests["memory"] == "" {
		return fmt.Errorf("environment %s: ObservabilityStack %s spec.victoriaMetrics.resources.requests require cpu and memory", site.EnvironmentBundle.Path, name)
	}
	if vm.Resources.Limits["memory"] == "" {
		return fmt.Errorf("environment %s: ObservabilityStack %s spec.victoriaMetrics.resources.limits.memory is required", site.EnvironmentBundle.Path, name)
	}
	return nil
}
