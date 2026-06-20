package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Cluster   ClusterSpec   `json:"cluster" yaml:"cluster" toml:"cluster"`
	Node      NodeSpec      `json:"node" yaml:"node" toml:"node"`
	Talm      TalmSpec      `json:"talm" yaml:"talm" toml:"talm"`
	Cozystack CozystackSpec `json:"cozystack" yaml:"cozystack" toml:"cozystack"`
	Bootstrap BootstrapSpec `json:"bootstrap" yaml:"bootstrap" toml:"bootstrap"`
}

type ClusterSpec struct {
	Name            string `json:"name" yaml:"name" toml:"name"`
	Endpoint        string `json:"endpoint" yaml:"endpoint" toml:"endpoint"`
	Domain          string `json:"domain" yaml:"domain" toml:"domain"`
	PodCIDR         string `json:"podCIDR" yaml:"podCIDR" toml:"podCIDR"`
	ServiceCIDR     string `json:"serviceCIDR" yaml:"serviceCIDR" toml:"serviceCIDR"`
	JoinCIDR        string `json:"joinCIDR" yaml:"joinCIDR" toml:"joinCIDR"`
	AdvertisedCIDR  string `json:"advertisedCIDR" yaml:"advertisedCIDR" toml:"advertisedCIDR"`
	APIServerDomain string `json:"apiServerDomain" yaml:"apiServerDomain" toml:"apiServerDomain"`
}

type NodeSpec struct {
	Name    string `json:"name" yaml:"name" toml:"name"`
	Address string `json:"address" yaml:"address" toml:"address"`
}

type TalmSpec struct {
	Preset            string `json:"preset" yaml:"preset" toml:"preset"`
	TalosVersion      string `json:"talosVersion" yaml:"talosVersion" toml:"talosVersion"`
	KubernetesVersion string `json:"kubernetesVersion" yaml:"kubernetesVersion" toml:"kubernetesVersion"`
	InstallerImage    string `json:"installerImage" yaml:"installerImage" toml:"installerImage"`
	Template          string `json:"template" yaml:"template" toml:"template"`
}

type CozystackSpec struct {
	Version            string   `json:"version" yaml:"version" toml:"version"`
	PlatformVariant    string   `json:"platformVariant" yaml:"platformVariant" toml:"platformVariant"`
	PublishingHost     string   `json:"publishingHost" yaml:"publishingHost" toml:"publishingHost"`
	ExposedServices    []string `json:"exposedServices" yaml:"exposedServices" toml:"exposedServices"`
	RemoveControlTaint bool     `json:"removeControlPlaneTaint" yaml:"removeControlPlaneTaint" toml:"removeControlPlaneTaint"`
}

type BootstrapSpec struct {
	Destructive        bool        `json:"destructive" yaml:"destructive" toml:"destructive"`
	RequireMaintenance bool        `json:"requireMaintenance" yaml:"requireMaintenance" toml:"requireMaintenance"`
	TargetState        string      `json:"targetState" yaml:"targetState" toml:"targetState"`
	Genesis            GenesisSpec `json:"genesis" yaml:"genesis" toml:"genesis"`
}

type GenesisSpec struct {
	AgeRecipients []string `json:"ageRecipients" yaml:"ageRecipients" toml:"ageRecipients"`
}

type Loaded struct {
	Path      string
	Config    Config
	Canonical []byte
	Digest    string
}

type hostDocument struct {
	Asset    string `json:"asset"`
	Provider struct {
		Name      string `json:"name"`
		ServerID  string `json:"serverID"`
		ProjectID string `json:"projectID"`
		Site      string `json:"site"`
		Plan      string `json:"plan"`
	} `json:"provider"`
	Network struct {
		IPv4 string `json:"ipv4"`
	} `json:"network"`
	Assignment struct {
		Cluster            string `json:"cluster"`
		Environment        string `json:"environment"`
		DestructiveAllowed bool   `json:"destructiveAllowed"`
		Prod               bool   `json:"prod"`
	} `json:"assignment"`
}

type clusterDocument struct {
	Name            string   `json:"name"`
	Domain          string   `json:"domain"`
	APIServerDomain string   `json:"apiServerDomain"`
	Members         []string `json:"members"`
	Environments    []string `json:"environments"`
	Network         struct {
		PodCIDR        string `json:"podCIDR"`
		ServiceCIDR    string `json:"serviceCIDR"`
		JoinCIDR       string `json:"joinCIDR"`
		AdvertisedCIDR string `json:"advertisedCIDR"`
	} `json:"network"`
	Talos struct {
		Version           string `json:"version"`
		TalmVersion       string `json:"talmVersion"`
		KubernetesVersion string `json:"kubernetesVersion"`
		InstallerImage    string `json:"installerImage"`
	} `json:"talos"`
	Cozystack struct {
		Version                 string   `json:"version"`
		PlatformVariant         string   `json:"platformVariant"`
		PublishingHost          string   `json:"publishingHost"`
		ExposedServices         []string `json:"exposedServices"`
		RemoveControlPlaneTaint bool     `json:"removeControlPlaneTaint"`
	} `json:"cozystack"`
	Bootstrap struct {
		Destructive        bool   `json:"destructive"`
		RequireMaintenance bool   `json:"requireMaintenance"`
		TargetState        string `json:"targetState"`
		Genesis            struct {
			AgeRecipients []string `json:"ageRecipients"`
		} `json:"genesis"`
	} `json:"bootstrap"`
}

type environmentDocument struct {
	Name      string `json:"name"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Domains   struct {
		Company string `json:"company"`
		AISucks string `json:"aisucks"`
		OCI     string `json:"oci"`
	} `json:"domains"`
}

func Load(hostPath string) (*Loaded, error) {
	resolved, err := filepath.Abs(hostPath)
	if err != nil {
		return nil, fmt.Errorf("resolve host path: %w", err)
	}
	host, err := loadJSONFile[hostDocument](resolved)
	if err != nil {
		return nil, fmt.Errorf("load host %s: %w", hostPath, err)
	}
	root, err := repoRoot(resolved)
	if err != nil {
		return nil, err
	}
	if err := validateHostSource(host); err != nil {
		return nil, err
	}
	clusterPath := filepath.Join(root, "src", "clusters", host.Assignment.Cluster, "cluster.json")
	cluster, err := loadJSONFile[clusterDocument](clusterPath)
	if err != nil {
		return nil, fmt.Errorf("load cluster %s: %w", host.Assignment.Cluster, err)
	}
	envPath := filepath.Join(root, "src", "environments", host.Assignment.Environment, "environment.json")
	env, err := loadJSONFile[environmentDocument](envPath)
	if err != nil {
		return nil, fmt.Errorf("load environment %s: %w", host.Assignment.Environment, err)
	}
	if err := validateSourceLinks(host, cluster, env); err != nil {
		return nil, err
	}
	cfg := assemble(host, cluster, env)
	normalize(&cfg)
	if err := validate(cfg); err != nil {
		return nil, err
	}
	canonical, digest, err := canonicalConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Loaded{Path: resolved, Config: cfg, Canonical: canonical, Digest: digest}, nil
}

func loadJSONFile[T any](resolved string) (T, error) {
	var out T
	info, err := os.Stat(resolved)
	if err != nil {
		return out, err
	}
	if info.IsDir() {
		return out, fmt.Errorf("entrypoint must be a JSON file, got directory")
	}
	if filepath.Ext(resolved) != ".json" {
		return out, fmt.Errorf("entrypoint must be a .json file")
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("parse JSON: %w", err)
	}
	return out, nil
}

func repoRoot(path string) (string, error) {
	dir := filepath.Dir(path)
	for {
		if _, err := os.Stat(filepath.Join(dir, "MODULE.bazel")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find repo root for %s", path)
		}
		dir = parent
	}
}

func validateHostSource(host hostDocument) error {
	var missing []string
	require := func(path, value string) {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, path)
		}
	}
	require("asset", host.Asset)
	require("network.ipv4", host.Network.IPv4)
	require("assignment.cluster", host.Assignment.Cluster)
	require("assignment.environment", host.Assignment.Environment)
	if len(missing) > 0 {
		return fmt.Errorf("host missing required fields: %s", strings.Join(missing, ", "))
	}
	if host.Provider.Name != "" && host.Provider.Name != "latitude" {
		return fmt.Errorf("provider.name: got %q, want latitude", host.Provider.Name)
	}
	return nil
}

func validateSourceLinks(host hostDocument, cluster clusterDocument, env environmentDocument) error {
	if cluster.Name != host.Assignment.Cluster {
		return fmt.Errorf("cluster name %q does not match host assignment %q", cluster.Name, host.Assignment.Cluster)
	}
	if env.Name != host.Assignment.Environment {
		return fmt.Errorf("environment name %q does not match host assignment %q", env.Name, host.Assignment.Environment)
	}
	if env.Cluster != cluster.Name {
		return fmt.Errorf("environment %q targets cluster %q, want %q", env.Name, env.Cluster, cluster.Name)
	}
	if !contains(cluster.Members, host.Asset) {
		return fmt.Errorf("cluster %q members do not include host asset %q", cluster.Name, host.Asset)
	}
	if !contains(cluster.Environments, env.Name) {
		return fmt.Errorf("cluster %q environments do not include %q", cluster.Name, env.Name)
	}
	return nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func assemble(host hostDocument, cluster clusterDocument, _ environmentDocument) Config {
	return Config{
		Cluster: ClusterSpec{
			Name:            cluster.Name,
			Endpoint:        fmt.Sprintf("https://%s:6443", host.Network.IPv4),
			Domain:          cluster.Domain,
			PodCIDR:         cluster.Network.PodCIDR,
			ServiceCIDR:     cluster.Network.ServiceCIDR,
			JoinCIDR:        cluster.Network.JoinCIDR,
			AdvertisedCIDR:  cluster.Network.AdvertisedCIDR,
			APIServerDomain: cluster.APIServerDomain,
		},
		Node: NodeSpec{
			Name:    host.Asset,
			Address: host.Network.IPv4,
		},
		Talm: TalmSpec{
			Preset:            "cozystack",
			TalosVersion:      cluster.Talos.TalmVersion,
			KubernetesVersion: cluster.Talos.KubernetesVersion,
			InstallerImage:    cluster.Talos.InstallerImage,
			Template:          "templates/controlplane.yaml",
		},
		Cozystack: CozystackSpec{
			Version:            cluster.Cozystack.Version,
			PlatformVariant:    cluster.Cozystack.PlatformVariant,
			PublishingHost:     cluster.Cozystack.PublishingHost,
			ExposedServices:    cluster.Cozystack.ExposedServices,
			RemoveControlTaint: cluster.Cozystack.RemoveControlPlaneTaint,
		},
		Bootstrap: BootstrapSpec{
			Destructive:        cluster.Bootstrap.Destructive && host.Assignment.DestructiveAllowed,
			RequireMaintenance: cluster.Bootstrap.RequireMaintenance,
			TargetState:        cluster.Bootstrap.TargetState,
			Genesis: GenesisSpec{
				AgeRecipients: cluster.Bootstrap.Genesis.AgeRecipients,
			},
		},
	}
}

func normalize(cfg *Config) {
	if cfg.Talm.Preset == "" {
		cfg.Talm.Preset = "cozystack"
	}
	if cfg.Talm.Template == "" {
		cfg.Talm.Template = "templates/controlplane.yaml"
	}
	if cfg.Cluster.PodCIDR == "" {
		cfg.Cluster.PodCIDR = "10.244.0.0/16"
	}
	if cfg.Cluster.ServiceCIDR == "" {
		cfg.Cluster.ServiceCIDR = "10.96.0.0/16"
	}
	if cfg.Cluster.JoinCIDR == "" {
		cfg.Cluster.JoinCIDR = "100.64.0.0/16"
	}
	if cfg.Cozystack.PlatformVariant == "" {
		cfg.Cozystack.PlatformVariant = "isp-full"
	}
	if cfg.Bootstrap.TargetState == "" {
		cfg.Bootstrap.TargetState = "stock-ubuntu"
	}
}

func validate(cfg Config) error {
	var missing []string
	require := func(path, value string) {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, path)
		}
	}
	require("cluster.name", cfg.Cluster.Name)
	require("cluster.endpoint", cfg.Cluster.Endpoint)
	require("cluster.domain", cfg.Cluster.Domain)
	require("cluster.advertisedCIDR", cfg.Cluster.AdvertisedCIDR)
	require("node.name", cfg.Node.Name)
	require("node.address", cfg.Node.Address)
	require("talm.talosVersion", cfg.Talm.TalosVersion)
	require("talm.kubernetesVersion", cfg.Talm.KubernetesVersion)
	require("talm.installerImage", cfg.Talm.InstallerImage)
	require("cozystack.version", cfg.Cozystack.Version)
	require("cozystack.platformVariant", cfg.Cozystack.PlatformVariant)
	if len(missing) > 0 {
		return fmt.Errorf("config missing required fields: %s", strings.Join(missing, ", "))
	}
	u, err := url.ParseRequestURI(cfg.Cluster.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("cluster.endpoint must be an https URL, got %q", cfg.Cluster.Endpoint)
	}
	if ip := net.ParseIP(cfg.Node.Address); ip == nil {
		return fmt.Errorf("node.address must be an IP address, got %q", cfg.Node.Address)
	}
	if _, _, err := net.ParseCIDR(cfg.Cluster.AdvertisedCIDR); err != nil {
		return fmt.Errorf("cluster.advertisedCIDR: %w", err)
	}
	if cfg.Talm.Preset != "cozystack" {
		return fmt.Errorf("talm.preset: got %q, want cozystack", cfg.Talm.Preset)
	}
	switch cfg.Cozystack.PlatformVariant {
	case "isp-full", "isp-hosted", "isp-full-generic":
	default:
		return fmt.Errorf("cozystack.platformVariant: got %q, want isp-full, isp-hosted, or isp-full-generic", cfg.Cozystack.PlatformVariant)
	}
	if cfg.Bootstrap.TargetState != "stock-ubuntu" {
		return fmt.Errorf("bootstrap.targetState: got %q, want stock-ubuntu", cfg.Bootstrap.TargetState)
	}
	return nil
}

func canonicalConfig(cfg Config) ([]byte, string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cfg); err != nil {
		return nil, "", fmt.Errorf("canonical config: %w", err)
	}
	raw := bytes.TrimSpace(buf.Bytes())
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}
