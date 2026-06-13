package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Site is the checked-in description of one site: which node it runs on,
// which provider owns the node, and what converges onto it.
type Site struct {
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
		// name fails silently — address assigned by platform fallback, routes
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
	// Aisucks holds the per-site rendering values for the aisucks component
	// (concrete fields, not a generic values map: a typo'd key should fail
	// loudly at decode, not vanish into a map nobody reads).
	Aisucks struct {
		// Domain switches the service to ACME HTTPS; empty serves plain
		// HTTP on :80 (dev, or any site before DNS exists).
		Domain string `yaml:"domain"`
		// NtfyTopic routes this site's Gatus alerts to ntfy.sh/<topic>;
		// empty renders Gatus without alerting. Unguessable, but checked
		// in — rotate before the repo goes public.
		NtfyTopic string `yaml:"ntfyTopic"`
		// Watch adds cross-site probe URLs to this site's Gatus, so the
		// watchers watch each other once multiple sites exist.
		Watch []string `yaml:"watch"`
		// WatchPages adds cross-site page probes (status + charter marker).
		// Watch covers /healthz only — a site whose DB is healthy while its
		// page is broken would otherwise be seen by no one but itself.
		WatchPages []string `yaml:"watchPages"`
		// PodNetwork moves aisucks off hostNetwork behind the Gateway: pod
		// network, replicas 2, a Service the routes target. Requires
		// gateway.enabled — without Envoy on :443 nothing would answer.
		PodNetwork bool `yaml:"podNetwork"`
	} `yaml:"aisucks"`
	// Gateway holds the per-site values for the edge-gateway component
	// (src/infrastructure-components/gateway).
	Gateway struct {
		// Enabled converges the edge Gateway + routes onto this site,
		// putting Envoy on host :80/:443. The per-site conversion ratchet:
		// dev pilots, gamma repeats, prod converts last
		// (docs/architecture/gateway.md Phase 5).
		Enabled bool `yaml:"enabled"`
	} `yaml:"gateway"`
	// OCI holds the public registry/bootstrap placeholder surface. A non-empty
	// domain enables cert-manager, the guardian-oci placeholder, and a
	// Gateway-terminated HTTPS listener for that hostname.
	OCI struct {
		Domain string `yaml:"domain"`
	} `yaml:"oci"`
	// Clickhouse holds the per-site values for the clickhouse component
	// (src/infrastructure-components/clickhouse) — the observability ledger.
	Clickhouse struct {
		// Enabled converges the per-site ClickHouse ledger AND tees the
		// otel-collector's logs pipeline (filelog + k8sobjects Events) into
		// it; off, the collector renders byte-identical to the metrics-only
		// spine. The per-site ratchet: dev pilots, gamma repeats, prod last
		// — guardian up now configures the OpenBao path and waits for the
		// projected clickhouse-admin Secret before applying ClickHouse.
		Enabled bool `yaml:"enabled"`
	} `yaml:"clickhouse"`
	// Status holds the per-site values for the status-page component
	// (src/status).
	Status struct {
		// Domains the status pod's certmagic manages (TLS-ALPN-01 through
		// the Gateway's TLS passthrough). An empty list disables the
		// component: the manifest template guards on it and renders
		// nothing, so the site deploys no status pod (prod, until the
		// Gateway round exposes a status hostname there).
		Domains []string `yaml:"domains"`
		// Monitor adds the status hostnames to this site's blackbox probe
		// targets. Default OFF, and it MUST stay off until the hostnames
		// resolve and serve: vmalert's SiteProbeFailed pages on
		// probe_success == 0 sustained 2m, so flipping this pre-DNS is a
		// guaranteed page.
		Monitor bool `yaml:"monitor"`
	} `yaml:"status"`
}

// resolveSite resolves the site from an explicit positional arg, else the
// "site" key in the guardian config. The returned string is the path used.
func resolveSite(args []string) (*Site, string, error) {
	switch len(args) {
	case 0:
	case 1:
		site, err := loadSite(args[0])
		return site, args[0], err
	default:
		return nil, "", fmt.Errorf("%w: expected at most one site.yaml path", errUsage)
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, "", err
	}
	if cfg.Site == "" {
		return nil, "", fmt.Errorf("%w: no site configured: pass a site.yaml path or run: guardian config site <path>", errUsage)
	}
	site, err := loadSite(cfg.Site)
	return site, cfg.Site, err
}

func loadSite(path string) (*Site, error) {
	resolved := resolvePath(path)
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("site: %w", err)
	}
	var s Site
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("site %s: %w", resolved, err)
	}
	required := map[string]string{
		"cluster.name":           s.Cluster.Name,
		"cluster.endpoint":       s.Cluster.Endpoint,
		"node.address":           s.Node.Address,
		"node.hostname":          s.Node.Hostname,
		"node.gateway":           s.Node.Gateway,
		"node.interfaceMac":      s.Node.InterfaceMac,
		"node.installDiskSerial": s.Node.InstallDiskSerial,
		"node.zfsDiskSerial":     s.Node.ZFSDiskSerial,
		"talos.schematic":        s.Talos.Schematic,
	}
	for field, value := range required {
		if value == "" {
			return nil, fmt.Errorf("site %s: %s is required", resolved, field)
		}
	}
	if s.Node.PrefixLength < 1 || s.Node.PrefixLength > 32 {
		return nil, fmt.Errorf("site %s: node.prefixLength must be 1-32, got %d", resolved, s.Node.PrefixLength)
	}
	if len(s.Talos.Patches) == 0 {
		return nil, fmt.Errorf("site %s: talos.patches is required", resolved)
	}
	// Pod-network aisucks serves only through the edge Gateway's routes;
	// without gateway.enabled nothing answers on host :80/:443.
	if s.Aisucks.PodNetwork && !s.Gateway.Enabled {
		return nil, fmt.Errorf("site %s: aisucks.podNetwork requires gateway.enabled", resolved)
	}
	if s.OCI.Domain != "" && !s.Gateway.Enabled {
		return nil, fmt.Errorf("site %s: oci.domain requires gateway.enabled", resolved)
	}
	// Monitoring status hostnames with none declared would render a
	// blackbox job with an empty target list.
	if s.Status.Monitor && len(s.Status.Domains) == 0 {
		return nil, fmt.Errorf("site %s: status.monitor requires status.domains", resolved)
	}
	return &s, nil
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
