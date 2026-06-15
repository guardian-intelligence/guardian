package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Bootstrap is the checked-in pre-Kubernetes description of one site. It is
// intentionally limited to physical facts and Talos inputs that guardian must
// know before an API server exists.
type Bootstrap struct {
	Site    string `yaml:"site"`
	Cluster struct {
		Name     string `yaml:"name"`
		Endpoint string `yaml:"endpoint"`
	} `yaml:"cluster"`
	Node struct {
		Address  string `yaml:"address"`
		Hostname string `yaml:"hostname"`
		// Static addressing facts: the site has no DHCP, so both the kexec
		// maintenance kernel (ip= argument) and the installed machine config
		// derive network configuration from here.
		PrefixLength int    `yaml:"prefixLength"`
		Gateway      string `yaml:"gateway"`
		// The NIC is selected by MAC, not name: interface names are derived
		// per kernel policy and per board (eno1 vs enp1s0f1) and a dangling
		// name fails silently -- address assigned by platform fallback, routes
		// dropped, node network-dark. The MAC is a physical fact like a disk
		// serial, and `up` verifies it against the maintenance node's link
		// inventory the same way.
		InterfaceMac string `yaml:"interfaceMac"`
		// Disks are selected by serial, not device path: identical NVMes
		// re-enumerate across boots and a path-based install could land on
		// the ZFS disk and wipe the pool.
		InstallDiskSerial string `yaml:"installDiskSerial"`
		ZFSDiskSerial     string `yaml:"zfsDiskSerial"`
	} `yaml:"node"`
	Talos struct {
		Schematic string   `yaml:"schematic"`
		Patches   []string `yaml:"patches"`
	} `yaml:"talos"`
}

// Environment is the post-Kubernetes desired-state bag for one site. The
// source of truth is the site's Crossplane EnvironmentConfig.data document.
type Environment struct {
	Site struct {
		Name         string `yaml:"name"`
		ClusterName  string `yaml:"clusterName"`
		NodeHostname string `yaml:"nodeHostname"`
	} `yaml:"site"`
	Alerts struct {
		NtfyTopic string `yaml:"ntfyTopic"`
	} `yaml:"alerts"`
	Gateway struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"gateway"`
	Products struct {
		Aisucks struct {
			Domain     string   `yaml:"domain"`
			Watch      []string `yaml:"watch"`
			WatchPages []string `yaml:"watchPages"`
			PodNetwork bool     `yaml:"podNetwork"`
		} `yaml:"aisucks"`
		Company struct {
			Domain string `yaml:"domain"`
		} `yaml:"company"`
	} `yaml:"products"`
	Platform struct {
		OCI struct {
			Domain string `yaml:"domain"`
		} `yaml:"oci"`
		Clickhouse struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"clickhouse"`
		Status struct {
			Domains []string `yaml:"domains"`
			Monitor bool     `yaml:"monitor"`
		} `yaml:"status"`
	} `yaml:"platform"`
}

type environmentConfigMetadata struct {
	Name   string            `yaml:"name"`
	Labels map[string]string `yaml:"labels"`
}

// Site is the runtime view assembled from bootstrap facts plus the site's
// Crossplane environment bundle. Component templates still receive this
// strongly typed view while XRs take ownership incrementally.
type Site struct {
	Name string
	Bootstrap
	EnvironmentBundle struct {
		Path string
		Raw  []byte
	}
	Aisucks struct {
		Domain     string
		NtfyTopic  string
		Watch      []string
		WatchPages []string
		PodNetwork bool
	}
	Company struct {
		Domain string
	}
	Gateway struct {
		Enabled bool
	}
	OCI struct {
		Domain string
	}
	Clickhouse struct {
		Enabled bool
	}
	Status struct {
		Domains []string
		Monitor bool
	}
}

// resolveSite resolves the site from an explicit bootstrap path, else the
// configured bootstrap path. The returned string is the bootstrap path used.
func resolveSite(args []string) (*Site, string, error) {
	switch len(args) {
	case 0:
	case 1:
		site, err := loadSite(args[0])
		return site, args[0], err
	default:
		return nil, "", fmt.Errorf("%w: expected at most one bootstrap.yaml path", errUsage)
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, "", err
	}
	if cfg.Bootstrap == "" {
		return nil, "", fmt.Errorf("%w: no bootstrap configured: pass a bootstrap.yaml path or run: guardian config bootstrap <path>", errUsage)
	}
	site, err := loadSite(cfg.Bootstrap)
	return site, cfg.Bootstrap, err
}

func loadSite(path string) (*Site, error) {
	bootstrap, resolved, err := loadBootstrap(path)
	if err != nil {
		return nil, err
	}
	envPath := environmentPathForSite(bootstrap.Site)
	env, envMeta, envRaw, envResolved, err := loadEnvironment(envPath)
	if err != nil {
		return nil, err
	}
	s := &Site{
		Name:      bootstrap.Site,
		Bootstrap: *bootstrap,
	}
	s.EnvironmentBundle.Path = envResolved
	s.EnvironmentBundle.Raw = envRaw
	s.Aisucks.Domain = env.Products.Aisucks.Domain
	s.Aisucks.NtfyTopic = env.Alerts.NtfyTopic
	s.Aisucks.Watch = env.Products.Aisucks.Watch
	s.Aisucks.WatchPages = env.Products.Aisucks.WatchPages
	s.Aisucks.PodNetwork = env.Products.Aisucks.PodNetwork
	s.Company.Domain = env.Products.Company.Domain
	s.Gateway.Enabled = env.Gateway.Enabled
	s.OCI.Domain = env.Platform.OCI.Domain
	s.Clickhouse.Enabled = env.Platform.Clickhouse.Enabled
	s.Status.Domains = env.Platform.Status.Domains
	s.Status.Monitor = env.Platform.Status.Monitor
	if err := validateSite(s, resolved, envResolved, env, envMeta); err != nil {
		return nil, err
	}
	return s, nil
}

func loadBootstrap(path string) (*Bootstrap, string, error) {
	resolved := resolvePath(path)
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("bootstrap: %w", err)
	}
	var b Bootstrap
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&b); err != nil {
		return nil, "", fmt.Errorf("bootstrap %s: %w", resolved, err)
	}
	required := map[string]string{
		"site":                   b.Site,
		"cluster.name":           b.Cluster.Name,
		"cluster.endpoint":       b.Cluster.Endpoint,
		"node.address":           b.Node.Address,
		"node.hostname":          b.Node.Hostname,
		"node.gateway":           b.Node.Gateway,
		"node.interfaceMac":      b.Node.InterfaceMac,
		"node.installDiskSerial": b.Node.InstallDiskSerial,
		"node.zfsDiskSerial":     b.Node.ZFSDiskSerial,
		"talos.schematic":        b.Talos.Schematic,
	}
	for field, value := range required {
		if value == "" {
			return nil, "", fmt.Errorf("bootstrap %s: %s is required", resolved, field)
		}
	}
	if b.Node.PrefixLength < 1 || b.Node.PrefixLength > 32 {
		return nil, "", fmt.Errorf("bootstrap %s: node.prefixLength must be 1-32, got %d", resolved, b.Node.PrefixLength)
	}
	if len(b.Talos.Patches) == 0 {
		return nil, "", fmt.Errorf("bootstrap %s: talos.patches is required", resolved)
	}
	return &b, resolved, nil
}

func loadEnvironment(path string) (*Environment, *environmentConfigMetadata, []byte, string, error) {
	resolved, err := resolveRepoInputPath(path)
	if err != nil {
		return nil, nil, nil, "", err
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("environment: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var doc struct {
			Kind     string                    `yaml:"kind"`
			Metadata environmentConfigMetadata `yaml:"metadata"`
			Data     any                       `yaml:"data"`
		}
		if err := dec.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return nil, nil, nil, "", fmt.Errorf("environment %s: %w", resolved, err)
		}
		if doc.Kind != "EnvironmentConfig" {
			continue
		}
		data, err := yaml.Marshal(doc.Data)
		if err != nil {
			return nil, nil, nil, "", fmt.Errorf("environment %s: marshal data: %w", resolved, err)
		}
		var env Environment
		dataDec := yaml.NewDecoder(bytes.NewReader(data))
		dataDec.KnownFields(true)
		if err := dataDec.Decode(&env); err != nil {
			return nil, nil, nil, "", fmt.Errorf("environment %s data: %w", resolved, err)
		}
		return &env, &doc.Metadata, raw, resolved, nil
	}
	return nil, nil, nil, "", fmt.Errorf("environment %s: EnvironmentConfig document is required", resolved)
}

func validateSite(s *Site, bootstrapPath, envPath string, env *Environment, envMeta *environmentConfigMetadata) error {
	if envMeta == nil {
		return fmt.Errorf("environment %s: EnvironmentConfig metadata is required", envPath)
	}
	if envMeta.Name != s.Cluster.Name {
		return fmt.Errorf("environment %s: metadata.name = %q, want %q from bootstrap %s", envPath, envMeta.Name, s.Cluster.Name, bootstrapPath)
	}
	if envMeta.Labels["guardian.dev/site"] != s.Name {
		return fmt.Errorf("environment %s: metadata.labels[guardian.dev/site] = %q, want %q from bootstrap %s", envPath, envMeta.Labels["guardian.dev/site"], s.Name, bootstrapPath)
	}
	if env.Site.Name != s.Name {
		return fmt.Errorf("environment %s: site.name = %q, want %q from bootstrap %s", envPath, env.Site.Name, s.Name, bootstrapPath)
	}
	if env.Site.ClusterName != "" && env.Site.ClusterName != s.Cluster.Name {
		return fmt.Errorf("environment %s: site.clusterName = %q, want %q from bootstrap %s", envPath, env.Site.ClusterName, s.Cluster.Name, bootstrapPath)
	}
	if env.Site.NodeHostname != "" && env.Site.NodeHostname != s.Node.Hostname {
		return fmt.Errorf("environment %s: site.nodeHostname = %q, want %q from bootstrap %s", envPath, env.Site.NodeHostname, s.Node.Hostname, bootstrapPath)
	}
	// Pod-network aisucks serves only through the edge Gateway's routes;
	// without gateway.enabled nothing answers on host :80/:443.
	if s.Aisucks.PodNetwork && !s.Gateway.Enabled {
		return fmt.Errorf("environment %s: products.aisucks.podNetwork requires gateway.enabled", envPath)
	}
	if s.Gateway.Enabled && s.Aisucks.Domain != "" && !s.Aisucks.PodNetwork {
		return fmt.Errorf("environment %s: gateway.enabled requires products.aisucks.podNetwork", envPath)
	}
	if s.OCI.Domain != "" && !s.Gateway.Enabled {
		return fmt.Errorf("environment %s: platform.oci.domain requires gateway.enabled", envPath)
	}
	if s.Company.Domain != "" && !s.Gateway.Enabled {
		return fmt.Errorf("environment %s: products.company.domain requires gateway.enabled", envPath)
	}
	// Monitoring status hostnames with none declared would render a
	// blackbox job with an empty target list.
	if s.Status.Monitor && len(s.Status.Domains) == 0 {
		return fmt.Errorf("environment %s: platform.status.monitor requires platform.status.domains", envPath)
	}
	return nil
}

func environmentPathForSite(site string) string {
	return filepath.ToSlash(filepath.Join("src", "crossplane", "environments", site, "environment.yaml"))
}

// resolvePath maps repo-root-relative paths to the invoking shell's cwd.
// `bazelisk run` executes binaries inside the runfiles tree;
// BUILD_WORKING_DIRECTORY carries the directory the user ran from.
func resolvePath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	if wd := os.Getenv("BUILD_WORKING_DIRECTORY"); wd != "" {
		return filepath.Join(wd, p)
	}
	return p
}

// stateDir holds per-cluster Talos secrets, machine configs, and kubeconfig.
// Secret material never lives in the repo.
func stateDir(clusterName string) (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("state dir: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "guardian", clusterName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("state dir: %w", err)
	}
	return dir, nil
}
