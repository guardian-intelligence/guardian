package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const talosKubeletStorageRoot = "/var/mnt"

// HostConfig is the checked-in pre-Kubernetes description of one physical
// host. It is intentionally limited to provider identity, physical facts, and
// Talos inputs that guardian must know before an API server exists.
type HostConfig struct {
	Host        string `yaml:"host"`
	Environment string `yaml:"environment"`
	Provider    struct {
		Name     string `yaml:"name"`
		ServerID string `yaml:"serverId"`
		Metro    string `yaml:"metro"`
		Plan     string `yaml:"plan"`
	} `yaml:"provider"`
	Cluster struct {
		Name     string `yaml:"name"`
		Endpoint string `yaml:"endpoint"`
	} `yaml:"cluster"`
	Node struct {
		Address  string `yaml:"address"`
		Hostname string `yaml:"hostname"`
		// Static addressing facts: the host has no DHCP, so both the kexec
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
	Storage HostStorage `yaml:"storage"`
	Talos   struct {
		Schematic string   `yaml:"schematic"`
		Patches   []string `yaml:"patches"`
	} `yaml:"talos"`
}

type HostStorage struct {
	Pools []HostStoragePool `yaml:"pools"`
}

type HostStoragePool struct {
	Name          string   `yaml:"name"`
	Type          string   `yaml:"type"`
	Role          string   `yaml:"role"`
	DeviceSerials []string `yaml:"deviceSerials"`
	WipePolicy    string   `yaml:"wipePolicy"`
	Mountpoint    string   `yaml:"mountpoint"`
}

// Environment is the post-Kubernetes desired-state bag assigned to a host. The
// source of truth is the Crossplane EnvironmentConfig.data document.
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
			Domain string `yaml:"domain"`
		} `yaml:"aisucks"`
	} `yaml:"products"`
	Platform struct {
		OCI struct {
			Domain string `yaml:"domain"`
		} `yaml:"oci"`
	} `yaml:"platform"`
}

type environmentConfigMetadata struct {
	Name   string            `yaml:"name"`
	Labels map[string]string `yaml:"labels"`
}

// Host is the runtime view assembled from physical host facts plus the
// environment bundle assigned to that host. Validation and Kustomize patches
// consume this strongly typed view while XRs own platform desired state.
type Host struct {
	Name string
	HostConfig
	EnvironmentBundle struct {
		Path string
		Raw  []byte
	}
	Aisucks struct {
		Domain     string
		NtfyTopic  string
		Watch      []string
		WatchPages []string
	}
	SLO struct {
		PublicHTTP *sloProfileSpec
	}
	Synthetic struct {
		PublicHTTPTargets []syntheticCheckTarget
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
	Storage struct {
		ProductPool HostStoragePool
	}
	StoragePlane *storagePlaneManifest
}

// resolveHost resolves the host from an explicit host.yaml path, else the
// configured host path. The returned string is the host path used.
func resolveHost(args []string) (*Host, string, error) {
	switch len(args) {
	case 0:
	case 1:
		host, err := loadHost(args[0])
		return host, args[0], err
	default:
		return nil, "", fmt.Errorf("%w: expected at most one host.yaml path", errUsage)
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, "", err
	}
	if cfg.Host == "" {
		return nil, "", fmt.Errorf("%w: no host configured: pass a host.yaml path or run: guardian config host <path>", errUsage)
	}
	host, err := loadHost(cfg.Host)
	return host, cfg.Host, err
}

func loadHost(path string) (*Host, error) {
	hostConfig, resolved, err := loadHostConfig(path)
	if err != nil {
		return nil, err
	}
	envPath := environmentPathForEnvironment(hostConfig.Environment)
	env, envMeta, envRaw, envResolved, err := loadEnvironment(envPath)
	if err != nil {
		return nil, err
	}
	host := &Host{
		Name:       hostConfig.Environment,
		HostConfig: *hostConfig,
	}
	productPool, err := productWorkloadStoragePool(*hostConfig)
	if err != nil {
		return nil, err
	}
	host.Storage.ProductPool = productPool
	host.EnvironmentBundle.Path = envResolved
	host.EnvironmentBundle.Raw = envRaw
	host.Aisucks.Domain = env.Products.Aisucks.Domain
	host.Aisucks.NtfyTopic = env.Alerts.NtfyTopic
	host.Gateway.Enabled = env.Gateway.Enabled
	host.OCI.Domain = env.Platform.OCI.Domain
	planes, err := storagePlanes(host)
	if err != nil {
		return nil, err
	}
	host.StoragePlane = &planes[0]
	observability, err := observabilityStacks(host)
	if err != nil {
		return nil, err
	}
	host.Clickhouse.Enabled = observability[0].Spec.Clickhouse.Enabled
	status, err := statusSurfaces(host)
	if err != nil {
		return nil, err
	}
	host.Status.Domains = append([]string(nil), status[0].Spec.Domains...)
	host.Status.Monitor = status[0].Spec.Monitor
	if err := applySLOAndSyntheticConfig(host); err != nil {
		return nil, err
	}
	if err := validateHostEnvironment(host, resolved, envResolved, env, envMeta); err != nil {
		return nil, err
	}
	directus, err := directusInstances(host)
	if err != nil {
		return nil, err
	}
	if _, err := ociRegistries(host); err != nil {
		return nil, err
	}
	_ = directus
	return host, nil
}

func loadHostConfig(path string) (*HostConfig, string, error) {
	resolved := resolvePath(path)
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("host: %w", err)
	}
	var h HostConfig
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&h); err != nil {
		return nil, "", fmt.Errorf("host %s: %w", resolved, err)
	}
	required := map[string]string{
		"host":                   h.Host,
		"environment":            h.Environment,
		"provider.name":          h.Provider.Name,
		"provider.serverId":      h.Provider.ServerID,
		"provider.metro":         h.Provider.Metro,
		"provider.plan":          h.Provider.Plan,
		"cluster.name":           h.Cluster.Name,
		"cluster.endpoint":       h.Cluster.Endpoint,
		"node.address":           h.Node.Address,
		"node.hostname":          h.Node.Hostname,
		"node.gateway":           h.Node.Gateway,
		"node.interfaceMac":      h.Node.InterfaceMac,
		"node.installDiskSerial": h.Node.InstallDiskSerial,
		"node.zfsDiskSerial":     h.Node.ZFSDiskSerial,
		"talos.schematic":        h.Talos.Schematic,
	}
	for field, value := range required {
		if value == "" {
			return nil, "", fmt.Errorf("host %s: %s is required", resolved, field)
		}
	}
	if h.Node.PrefixLength < 1 || h.Node.PrefixLength > 32 {
		return nil, "", fmt.Errorf("host %s: node.prefixLength must be 1-32, got %d", resolved, h.Node.PrefixLength)
	}
	if len(h.Talos.Patches) == 0 {
		return nil, "", fmt.Errorf("host %s: talos.patches is required", resolved)
	}
	if err := validateHostStorage(resolved, h); err != nil {
		return nil, "", err
	}
	return &h, resolved, nil
}

func validateHostStorage(path string, h HostConfig) error {
	if h.Node.InstallDiskSerial == h.Node.ZFSDiskSerial {
		return fmt.Errorf("host %s: node.installDiskSerial and node.zfsDiskSerial must be different", path)
	}
	if len(h.Storage.Pools) == 0 {
		return fmt.Errorf("host %s: storage.pools is required", path)
	}
	var productPools int
	for i, pool := range h.Storage.Pools {
		prefix := fmt.Sprintf("storage.pools[%d]", i)
		required := map[string]string{
			prefix + ".name":       pool.Name,
			prefix + ".type":       pool.Type,
			prefix + ".role":       pool.Role,
			prefix + ".wipePolicy": pool.WipePolicy,
			prefix + ".mountpoint": pool.Mountpoint,
		}
		for field, value := range required {
			if value == "" {
				return fmt.Errorf("host %s: %s is required", path, field)
			}
		}
		if pool.Type != "zfs" {
			return fmt.Errorf("host %s: %s.type must be zfs, got %q", path, prefix, pool.Type)
		}
		if pool.WipePolicy != "never" {
			return fmt.Errorf("host %s: %s.wipePolicy must be never, got %q", path, prefix, pool.WipePolicy)
		}
		if !filepath.IsAbs(pool.Mountpoint) {
			return fmt.Errorf("host %s: %s.mountpoint must be absolute, got %q", path, prefix, pool.Mountpoint)
		}
		if len(pool.DeviceSerials) == 0 {
			return fmt.Errorf("host %s: %s.deviceSerials is required", path, prefix)
		}
		seen := map[string]bool{}
		var hasZFSDisk bool
		for _, serial := range pool.DeviceSerials {
			if serial == "" {
				return fmt.Errorf("host %s: %s.deviceSerials cannot contain empty values", path, prefix)
			}
			if seen[serial] {
				return fmt.Errorf("host %s: %s.deviceSerials contains duplicate serial %q", path, prefix, serial)
			}
			seen[serial] = true
			if serial == h.Node.InstallDiskSerial {
				return fmt.Errorf("host %s: %s.deviceSerials must not include install disk serial %s", path, prefix, serial)
			}
			if serial == h.Node.ZFSDiskSerial {
				hasZFSDisk = true
			}
		}
		if pool.Role == "product-workloads" {
			productPools++
			if !hasZFSDisk {
				return fmt.Errorf("host %s: %s.deviceSerials must include node.zfsDiskSerial %s", path, prefix, h.Node.ZFSDiskSerial)
			}
			if !pathWithin(pool.Mountpoint, talosKubeletStorageRoot) {
				return fmt.Errorf("host %s: %s.mountpoint %q must be under %s so Talos kubelet can see local PV paths", path, prefix, pool.Mountpoint, talosKubeletStorageRoot)
			}
		}
	}
	if productPools != 1 {
		return fmt.Errorf("host %s: exactly one storage pool must have role product-workloads, found %d", path, productPools)
	}
	return nil
}

func productWorkloadStoragePool(h HostConfig) (HostStoragePool, error) {
	for _, pool := range h.Storage.Pools {
		if pool.Role == "product-workloads" {
			return pool, nil
		}
	}
	return HostStoragePool{}, fmt.Errorf("host %s: product-workloads storage pool is required", h.Host)
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

func validateHostEnvironment(h *Host, hostPath, envPath string, env *Environment, envMeta *environmentConfigMetadata) error {
	if envMeta == nil {
		return fmt.Errorf("environment %s: EnvironmentConfig metadata is required", envPath)
	}
	if envMeta.Name != h.Cluster.Name {
		return fmt.Errorf("environment %s: metadata.name = %q, want %q from host %s", envPath, envMeta.Name, h.Cluster.Name, hostPath)
	}
	if envMeta.Labels["guardian.dev/site"] != h.Name {
		return fmt.Errorf("environment %s: metadata.labels[guardian.dev/site] = %q, want %q from host %s", envPath, envMeta.Labels["guardian.dev/site"], h.Name, hostPath)
	}
	if env.Site.Name != h.Name {
		return fmt.Errorf("environment %s: site.name = %q, want %q from host %s", envPath, env.Site.Name, h.Name, hostPath)
	}
	if env.Site.ClusterName != "" && env.Site.ClusterName != h.Cluster.Name {
		return fmt.Errorf("environment %s: site.clusterName = %q, want %q from host %s", envPath, env.Site.ClusterName, h.Cluster.Name, hostPath)
	}
	if env.Site.NodeHostname != "" && env.Site.NodeHostname != h.Node.Hostname {
		return fmt.Errorf("environment %s: site.nodeHostname = %q, want %q from host %s", envPath, env.Site.NodeHostname, h.Node.Hostname, hostPath)
	}
	if h.Aisucks.Domain != "" && !h.Gateway.Enabled {
		return fmt.Errorf("environment %s: products.aisucks.domain requires gateway.enabled", envPath)
	}
	if h.OCI.Domain != "" && !h.Gateway.Enabled {
		return fmt.Errorf("environment %s: platform.oci.domain requires gateway.enabled", envPath)
	}
	// Monitoring status hostnames with none declared would render a
	// blackbox job with an empty target list.
	if h.Status.Monitor && len(h.Status.Domains) == 0 {
		return fmt.Errorf("environment %s: StatusSurface spec.monitor requires spec.domains", envPath)
	}
	return nil
}

func environmentPathForEnvironment(environment string) string {
	return filepath.ToSlash(filepath.Join("src", "environments", environment, "environment.yaml"))
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
