package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadHostConfigWithClusterAndEnvironment(t *testing.T) {
	root := writeTestRepo(t)

	loaded, err := Load(filepath.Join(root, "src", "hosts", "ash-bm-001", "host.json"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Digest == "" {
		t.Fatal("digest is empty")
	}
	if loaded.Config.Cluster.Name != "guardian-nonprod" {
		t.Fatalf("cluster name = %q", loaded.Config.Cluster.Name)
	}
	if loaded.Config.Cluster.Endpoint != "https://206.223.228.101:6443" {
		t.Fatalf("cluster endpoint = %q", loaded.Config.Cluster.Endpoint)
	}
	if loaded.Config.Talm.TalosVersion != "v1.13" {
		t.Fatalf("talm talos version = %q", loaded.Config.Talm.TalosVersion)
	}
	if loaded.Config.Cozystack.Version != "1.4.1" {
		t.Fatalf("cozystack version = %q", loaded.Config.Cozystack.Version)
	}
	if loaded.Config.Node.Address != "206.223.228.101" {
		t.Fatalf("node address = %q", loaded.Config.Node.Address)
	}
	if loaded.Config.Cozystack.PlatformVariant != "isp-full" {
		t.Fatalf("cozystack platform variant = %q", loaded.Config.Cozystack.PlatformVariant)
	}
	if !loaded.Config.Bootstrap.Destructive {
		t.Fatalf("bootstrap destructive = false, want true")
	}
}

func TestLoadRejectsHostNotInClusterMembers(t *testing.T) {
	root := writeTestRepo(t)
	clusterPath := filepath.Join(root, "src", "clusters", "guardian-nonprod", "cluster.json")
	raw, err := os.ReadFile(clusterPath)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), `"members": ["ash-bm-001"]`, `"members": ["ash-bm-002"]`, 1))
	if err := os.WriteFile(clusterPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = Load(filepath.Join(root, "src", "hosts", "ash-bm-001", "host.json"))
	if err == nil {
		t.Fatal("Load succeeded, want member validation error")
	}
	if !strings.Contains(err.Error(), "members do not include") {
		t.Fatalf("error = %v, want member validation", err)
	}
}

func TestLoadRequiresHostEntrypoint(t *testing.T) {
	root := writeTestRepo(t)

	_, err := Load(filepath.Join(root, "src", "clusters", "guardian-nonprod", "cluster.json"))
	if err == nil {
		t.Fatal("Load succeeded, want host entrypoint error")
	}
	if !strings.Contains(err.Error(), "host missing required fields") {
		t.Fatalf("error = %v, want host validation", err)
	}
}

func writeTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "MODULE.bazel", "module(name = \"test\")\n")
	writeFile(t, root, "src/hosts/ash-bm-001/host.json", `{
  "asset": "ash-bm-001",
  "network": {
    "ipv4": "206.223.228.101"
  },
  "assignment": {
    "cluster": "guardian-nonprod",
    "environment": "dev",
    "destructiveAllowed": true
  }
}
`)
	writeFile(t, root, "src/clusters/guardian-nonprod/cluster.json", `{
  "name": "guardian-nonprod",
  "domain": "guardianintelligence.org",
  "apiServerDomain": "api.nonprod.guardianintelligence.org",
  "members": ["ash-bm-001"],
  "environments": ["dev", "gamma"],
  "network": {
    "podCIDR": "10.244.0.0/16",
    "serviceCIDR": "10.96.0.0/16",
    "joinCIDR": "100.64.0.0/16",
    "advertisedCIDR": "206.223.228.100/31"
  },
  "talos": {
    "version": "v1.13.4",
    "talmVersion": "v1.13",
    "kubernetesVersion": "1.36.1",
    "installerImage": "ghcr.io/cozystack/cozystack/talos:v1.13.0"
  },
  "cozystack": {
    "version": "1.4.1",
    "platformVariant": "isp-full",
    "publishingHost": "",
    "exposedServices": [],
    "removeControlPlaneTaint": true
  },
  "bootstrap": {
    "destructive": true,
    "requireMaintenance": true,
    "targetState": "stock-ubuntu",
    "genesis": {
      "ageRecipients": ["age1e95feklupyh40qa24vly650vg0qmljcsfhqd66fwhwa82j3uefnsxed3s8"]
    }
  }
}
`)
	writeFile(t, root, "src/environments/dev/environment.json", `{
  "name": "dev",
  "cluster": "guardian-nonprod",
  "namespace": "guardian-dev",
  "domains": {
    "company": "dev.guardianintelligence.org"
  }
}
`)
	return root
}

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
