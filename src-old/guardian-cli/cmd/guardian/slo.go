package main

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type sloProfileManifest struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec sloProfileSpec `yaml:"spec"`
}

type sloProfileSpec struct {
	Site    string          `yaml:"site"`
	Surface string          `yaml:"surface"`
	Window  string          `yaml:"window"`
	Apps    []sloProfileApp `yaml:"apps"`
	Signals sloSignals      `yaml:"signals"`
}

type sloProfileApp struct {
	Name       string `yaml:"name"`
	Namespace  string `yaml:"namespace"`
	Deployment string `yaml:"deployment"`
	Metric     string `yaml:"metric"`
}

type sloSignals struct {
	PublicScrape *bool `yaml:"publicScrape"`
	ErrorRate    *bool `yaml:"errorRate"`
	RestartDelta *bool `yaml:"restartDelta"`
	PageAlerts   *bool `yaml:"pageAlerts"`
	Synthetic    *bool `yaml:"synthetic"`
	DirectHTTP   *bool `yaml:"directHTTP"`
}

type syntheticCheckManifest struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec syntheticCheckSpec `yaml:"spec"`
}

type syntheticCheckSpec struct {
	Site    string                 `yaml:"site"`
	Surface string                 `yaml:"surface"`
	Targets []syntheticCheckTarget `yaml:"targets"`
}

type syntheticCheckTarget struct {
	Name           string `yaml:"name"`
	Product        string `yaml:"product"`
	TargetKind     string `yaml:"kind"`
	URL            string `yaml:"url"`
	ExpectedStatus int    `yaml:"expectedStatus"`
	BodyContains   string `yaml:"bodyContains"`
	Gate           *bool  `yaml:"gate"`
}

func (t syntheticCheckTarget) expectedStatus() int {
	if t.ExpectedStatus == 0 {
		return 200
	}
	return t.ExpectedStatus
}

func (t syntheticCheckTarget) gatesPromotion() bool {
	return t.Gate == nil || *t.Gate
}

func sloProfiles(site *Host) ([]sloProfileManifest, error) {
	var out []sloProfileManifest
	if err := decodeEnvironmentDocuments(site.EnvironmentBundle.Raw, site.EnvironmentBundle.Path, "SLOProfile", func(node *yaml.Node) error {
		var doc sloProfileManifest
		if err := node.Decode(&doc); err != nil {
			return err
		}
		out = append(out, doc)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := validateSLOProfiles(site, out); err != nil {
		return nil, err
	}
	return out, nil
}

func syntheticChecks(site *Host) ([]syntheticCheckManifest, error) {
	var out []syntheticCheckManifest
	if err := decodeEnvironmentDocuments(site.EnvironmentBundle.Raw, site.EnvironmentBundle.Path, "SyntheticCheck", func(node *yaml.Node) error {
		var doc syntheticCheckManifest
		if err := node.Decode(&doc); err != nil {
			return err
		}
		out = append(out, doc)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := validateSyntheticChecks(site, out); err != nil {
		return nil, err
	}
	return out, nil
}

func decodeEnvironmentDocuments(raw []byte, path, kind string, decode func(*yaml.Node) error) error {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var node yaml.Node
		if err := dec.Decode(&node); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("decode %s: %w", path, err)
		}
		if node.Kind == 0 {
			continue
		}
		var header struct {
			Kind string `yaml:"kind"`
		}
		if err := node.Decode(&header); err != nil {
			return fmt.Errorf("decode %s document header: %w", path, err)
		}
		if header.Kind != kind {
			continue
		}
		if err := decode(&node); err != nil {
			return fmt.Errorf("decode %s %s: %w", path, kind, err)
		}
	}
}

func validateSLOProfiles(site *Host, profiles []sloProfileManifest) error {
	seenPublicHTTP := false
	for _, profile := range profiles {
		name := profile.Metadata.Name
		spec := profile.Spec
		if name == "" {
			return fmt.Errorf("environment %s: SLOProfile metadata.name is required", site.EnvironmentBundle.Path)
		}
		required := map[string]string{
			"site":    spec.Site,
			"surface": spec.Surface,
			"window":  spec.Window,
		}
		for field, value := range required {
			if value == "" {
				return fmt.Errorf("environment %s: SLOProfile %s spec.%s is required", site.EnvironmentBundle.Path, name, field)
			}
		}
		if spec.Site != site.Name {
			return fmt.Errorf("environment %s: SLOProfile %s spec.site = %q, want %q", site.EnvironmentBundle.Path, name, spec.Site, site.Name)
		}
		if _, err := time.ParseDuration(spec.Window); err != nil {
			return fmt.Errorf("environment %s: SLOProfile %s spec.window = %q: %w", site.EnvironmentBundle.Path, name, spec.Window, err)
		}
		if len(spec.Apps) == 0 {
			return fmt.Errorf("environment %s: SLOProfile %s spec.apps is required", site.EnvironmentBundle.Path, name)
		}
		apps := map[string]bool{}
		for _, app := range spec.Apps {
			appRequired := map[string]string{
				"name":       app.Name,
				"namespace":  app.Namespace,
				"deployment": app.Deployment,
				"metric":     app.Metric,
			}
			for field, value := range appRequired {
				if value == "" {
					return fmt.Errorf("environment %s: SLOProfile %s app %s is required", site.EnvironmentBundle.Path, name, field)
				}
			}
			key := app.Namespace + "/" + app.Deployment
			if apps[key] {
				return fmt.Errorf("environment %s: SLOProfile %s has duplicate app %s", site.EnvironmentBundle.Path, name, key)
			}
			apps[key] = true
		}
		if spec.Surface == "public-http" {
			if seenPublicHTTP {
				return fmt.Errorf("environment %s: multiple public-http SLOProfile documents are not supported", site.EnvironmentBundle.Path)
			}
			seenPublicHTTP = true
		}
	}
	if site.Aisucks.Domain != "" && !seenPublicHTTP {
		return fmt.Errorf("environment %s: public HTTP products require a public-http SLOProfile", site.EnvironmentBundle.Path)
	}
	return nil
}

func validateSyntheticChecks(site *Host, checks []syntheticCheckManifest) error {
	for _, check := range checks {
		name := check.Metadata.Name
		spec := check.Spec
		if name == "" {
			return fmt.Errorf("environment %s: SyntheticCheck metadata.name is required", site.EnvironmentBundle.Path)
		}
		if spec.Site != site.Name {
			return fmt.Errorf("environment %s: SyntheticCheck %s spec.site = %q, want %q", site.EnvironmentBundle.Path, name, spec.Site, site.Name)
		}
		if spec.Surface == "" {
			return fmt.Errorf("environment %s: SyntheticCheck %s spec.surface is required", site.EnvironmentBundle.Path, name)
		}
		if len(spec.Targets) == 0 {
			return fmt.Errorf("environment %s: SyntheticCheck %s spec.targets is required", site.EnvironmentBundle.Path, name)
		}
		targets := map[string]bool{}
		for _, target := range spec.Targets {
			required := map[string]string{
				"name":    target.Name,
				"product": target.Product,
				"kind":    target.TargetKind,
				"url":     target.URL,
			}
			for field, value := range required {
				if value == "" {
					return fmt.Errorf("environment %s: SyntheticCheck %s target %s is required", site.EnvironmentBundle.Path, name, field)
				}
			}
			if target.expectedStatus() < 200 || target.expectedStatus() > 299 {
				return fmt.Errorf("environment %s: SyntheticCheck %s target %s expectedStatus = %d, want 200-299", site.EnvironmentBundle.Path, name, target.Name, target.expectedStatus())
			}
			parsed, err := url.Parse(target.URL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return fmt.Errorf("environment %s: SyntheticCheck %s target %s url must be absolute http(s), got %q", site.EnvironmentBundle.Path, name, target.Name, target.URL)
			}
			if target.Product != "aisucks" {
				return fmt.Errorf("environment %s: SyntheticCheck %s target %s product = %q, want aisucks", site.EnvironmentBundle.Path, name, target.Name, target.Product)
			}
			if target.TargetKind != "health" && target.TargetKind != "page" {
				return fmt.Errorf("environment %s: SyntheticCheck %s target %s kind = %q, want health or page", site.EnvironmentBundle.Path, name, target.Name, target.TargetKind)
			}
			if targets[target.URL] {
				return fmt.Errorf("environment %s: SyntheticCheck %s duplicate target url %q", site.EnvironmentBundle.Path, name, target.URL)
			}
			targets[target.URL] = true
		}
	}
	return nil
}

func applySLOAndSyntheticConfig(site *Host) error {
	profiles, err := sloProfiles(site)
	if err != nil {
		return err
	}
	for i := range profiles {
		if profiles[i].Spec.Surface == "public-http" {
			site.SLO.PublicHTTP = &profiles[i].Spec
			break
		}
	}
	checks, err := syntheticChecks(site)
	if err != nil {
		return err
	}
	var aisucksHealth []string
	var aisucksPages []string
	for _, check := range checks {
		if check.Spec.Surface != "public-http" {
			continue
		}
		for _, target := range check.Spec.Targets {
			if !target.gatesPromotion() {
				continue
			}
			switch target.Product {
			case "aisucks":
				if target.TargetKind == "health" {
					aisucksHealth = append(aisucksHealth, target.URL)
				} else {
					aisucksPages = append(aisucksPages, target.URL)
				}
			}
			site.Synthetic.PublicHTTPTargets = append(site.Synthetic.PublicHTTPTargets, target)
		}
	}
	site.Aisucks.Watch = uniqueStrings(aisucksHealth)
	site.Aisucks.WatchPages = uniqueStrings(aisucksPages)
	return nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func signalEnabled(value *bool) bool {
	return value == nil || *value
}
