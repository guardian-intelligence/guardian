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
	"sort"
	"strings"

	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/load"
	"cuelang.org/go/cue/parser"
)

type Config struct {
	Cluster   ClusterSpec   `json:"cluster" yaml:"cluster" toml:"cluster"`
	Provider  ProviderSpec  `json:"provider" yaml:"provider" toml:"provider"`
	Node      NodeSpec      `json:"node" yaml:"node" toml:"node"`
	Talm      TalmSpec      `json:"talm" yaml:"talm" toml:"talm"`
	Cozystack CozystackSpec `json:"cozystack" yaml:"cozystack" toml:"cozystack"`
	Bootstrap BootstrapSpec `json:"bootstrap" yaml:"bootstrap" toml:"bootstrap"`
	Hello     HelloSpec     `json:"hello" yaml:"hello" toml:"hello"`
}

type ProviderSpec struct {
	Name            string `json:"name" yaml:"name" toml:"name"`
	ServerID        string `json:"serverId" yaml:"serverId" toml:"serverId"`
	TokenEnv        string `json:"tokenEnv" yaml:"tokenEnv" toml:"tokenEnv"`
	Reinstall       bool   `json:"reinstall" yaml:"reinstall" toml:"reinstall"`
	TalosSchematic  string `json:"talosSchematic" yaml:"talosSchematic" toml:"talosSchematic"`
	TalosVersion    string `json:"talosVersion" yaml:"talosVersion" toml:"talosVersion"`
	RefuseProdNames bool   `json:"refuseProdNames" yaml:"refuseProdNames" toml:"refuseProdNames"`
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
	Name              string `json:"name" yaml:"name" toml:"name"`
	Address           string `json:"address" yaml:"address" toml:"address"`
	Hostname          string `json:"hostname" yaml:"hostname" toml:"hostname"`
	InterfaceMAC      string `json:"interfaceMac" yaml:"interfaceMac" toml:"interfaceMac"`
	InstallDiskSerial string `json:"installDiskSerial" yaml:"installDiskSerial" toml:"installDiskSerial"`
	Role              string `json:"role" yaml:"role" toml:"role"`
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
	Variant            string   `json:"variant" yaml:"variant" toml:"variant"`
	PublishingHost     string   `json:"publishingHost" yaml:"publishingHost" toml:"publishingHost"`
	APIServerEndpoint  string   `json:"apiServerEndpoint" yaml:"apiServerEndpoint" toml:"apiServerEndpoint"`
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

type HelloSpec struct {
	Enabled   bool   `json:"enabled" yaml:"enabled" toml:"enabled"`
	Namespace string `json:"namespace" yaml:"namespace" toml:"namespace"`
}

type Loaded struct {
	Path      string
	Config    Config
	Canonical []byte
	Digest    string
}

func Load(path string) (*Loaded, error) {
	resolved, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	args, err := cueArgs(resolved)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	ctx := cuecontext.New()
	instances := load.Instances(args, &load.Config{Dir: filepath.Dir(resolved)})
	if len(instances) != 1 {
		return nil, fmt.Errorf("load %s: got %d CUE instances, want 1", path, len(instances))
	}
	if err := instances[0].Err; err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	value := ctx.BuildInstance(instances[0])
	if err := value.Err(); err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	var cfg Config
	if err := value.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
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

func cueArgs(resolved string) ([]string, error) {
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("entrypoint must be a CUE file, got directory")
	}
	if filepath.Ext(resolved) != ".cue" {
		return nil, fmt.Errorf("entrypoint must be a .cue file")
	}
	entrypoint, err := parser.ParseFile(resolved, nil)
	if err != nil {
		return nil, fmt.Errorf("parse entrypoint: %w", err)
	}
	pkg := entrypoint.PackageName()
	args := []string{filepath.Base(resolved)}
	if pkg == "" {
		return args, nil
	}
	entries, err := os.ReadDir(filepath.Dir(resolved))
	if err != nil {
		return nil, err
	}
	var siblings []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() ||
			name == filepath.Base(resolved) ||
			filepath.Ext(name) != ".cue" ||
			strings.HasPrefix(name, ".") ||
			strings.HasPrefix(name, "_") ||
			strings.HasSuffix(name, "_test.cue") {
			continue
		}
		file, err := parser.ParseFile(filepath.Join(filepath.Dir(resolved), name), nil)
		if err != nil {
			return nil, fmt.Errorf("parse sibling %s: %w", name, err)
		}
		if file.PackageName() == pkg {
			siblings = append(siblings, name)
		}
	}
	sort.Strings(siblings)
	return append(args, siblings...), nil
}

func normalize(cfg *Config) {
	if cfg.Provider.TokenEnv == "" {
		cfg.Provider.TokenEnv = "LATITUDE_API_KEY"
	}
	if cfg.Provider.Name == "" && (cfg.Provider.ServerID != "" || cfg.Provider.Reinstall) {
		cfg.Provider.Name = "latitude"
	}
	if cfg.Provider.TalosVersion == "" {
		cfg.Provider.TalosVersion = cfg.Talm.TalosVersion
	}
	if cfg.Provider.ServerID != "" {
		cfg.Provider.RefuseProdNames = true
	}
	if cfg.Talm.Preset == "" {
		cfg.Talm.Preset = "cozystack"
	}
	if cfg.Talm.Template == "" {
		cfg.Talm.Template = "templates/controlplane.yaml"
	}
	if cfg.Cozystack.Variant == "" {
		cfg.Cozystack.Variant = "isp-full"
	}
	if len(cfg.Cozystack.ExposedServices) == 0 {
		cfg.Cozystack.ExposedServices = []string{"dashboard", "api"}
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
	if cfg.Hello.Namespace == "" {
		cfg.Hello.Namespace = "guardian-hello"
	}
	if cfg.Bootstrap.TargetState == "" {
		cfg.Bootstrap.TargetState = "talos-maintenance"
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
	if cfg.Provider.ServerID != "" || cfg.Provider.Reinstall {
		require("provider.name", cfg.Provider.Name)
		require("provider.serverId", cfg.Provider.ServerID)
		require("provider.tokenEnv", cfg.Provider.TokenEnv)
		if cfg.Provider.Reinstall {
			require("provider.talosSchematic", cfg.Provider.TalosSchematic)
			require("provider.talosVersion", cfg.Provider.TalosVersion)
		}
	}
	require("node.name", cfg.Node.Name)
	require("node.address", cfg.Node.Address)
	require("node.hostname", cfg.Node.Hostname)
	require("node.interfaceMac", cfg.Node.InterfaceMAC)
	require("node.installDiskSerial", cfg.Node.InstallDiskSerial)
	require("talm.talosVersion", cfg.Talm.TalosVersion)
	require("talm.kubernetesVersion", cfg.Talm.KubernetesVersion)
	require("talm.installerImage", cfg.Talm.InstallerImage)
	require("cozystack.version", cfg.Cozystack.Version)
	require("cozystack.publishingHost", cfg.Cozystack.PublishingHost)
	require("cozystack.apiServerEndpoint", cfg.Cozystack.APIServerEndpoint)
	if len(missing) > 0 {
		return fmt.Errorf("config missing required fields: %s", strings.Join(missing, ", "))
	}
	for path, raw := range map[string]string{
		"cluster.endpoint":            cfg.Cluster.Endpoint,
		"cozystack.apiServerEndpoint": cfg.Cozystack.APIServerEndpoint,
	} {
		u, err := url.ParseRequestURI(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("%s must be an https URL, got %q", path, raw)
		}
	}
	if ip := net.ParseIP(cfg.Node.Address); ip == nil {
		return fmt.Errorf("node.address must be an IP address, got %q", cfg.Node.Address)
	}
	if _, _, err := net.ParseCIDR(cfg.Cluster.AdvertisedCIDR); err != nil {
		return fmt.Errorf("cluster.advertisedCIDR: %w", err)
	}
	if _, err := net.ParseMAC(cfg.Node.InterfaceMAC); err != nil {
		return fmt.Errorf("node.interfaceMac: %w", err)
	}
	if cfg.Talm.Preset != "cozystack" {
		return fmt.Errorf("talm.preset: got %q, want cozystack", cfg.Talm.Preset)
	}
	if cfg.Provider.Name != "" && cfg.Provider.Name != "latitude" {
		return fmt.Errorf("provider.name: got %q, want latitude", cfg.Provider.Name)
	}
	if cfg.Bootstrap.TargetState != "talos-maintenance" {
		return fmt.Errorf("bootstrap.targetState: got %q, want talos-maintenance", cfg.Bootstrap.TargetState)
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
