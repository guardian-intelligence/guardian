package tests

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"gopkg.in/yaml.v3"
)

type manifest map[string]any

type guardianMgmtTopology struct {
	Cluster string `json:"cluster"`
	Network struct {
		VLAN struct {
			ID          string `json:"id"`
			VID         int    `json:"vid"`
			Description string `json:"description"`
			Subnet      string `json:"subnet"`
			VLANMTU     int    `json:"vlan_mtu"`
			PodMTU      int    `json:"pod_mtu"`
			APIVIP      string `json:"api_vip"`
			VIPLink     string `json:"vip_link"`
			MetalLBPool string `json:"metallb_pool"`
		} `json:"vlan"`
	} `json:"network"`
	Nodes []guardianMgmtNode `json:"nodes"`
}

type guardianMgmtNode struct {
	Name        string `json:"name"`
	ServerID    string `json:"server_id"`
	Hostname    string `json:"hostname"`
	PublicIPv4  string `json:"public_ipv4"`
	PrivateIPv4 string `json:"private_ipv4"`
}

func TestManifestInvariants(t *testing.T) {
	t.Run("guardian mgmt topology alignment", testGuardianMgmtTopologyAlignment)
	t.Run("guardian mgmt dns bootstrap", testGuardianMgmtDNSBootstrap)
	t.Run("talm install disk selectors", testTalmInstallDiskSelectors)
	t.Run("cozystack platform package", testCozystackPlatformPackage)
	t.Run("layer two networking", testLayerTwoNetworking)
	t.Run("single default storage class", testSingleDefaultStorageClass)
	t.Run("linstor data pools", testLINSTORDataPools)
	t.Run("cozystack system bucket backups", testCozystackSystemBucketBackups)
	t.Run("cozystack platform patches", testCozystackPlatformPatches)
	t.Run("cozystack app patches", testCozystackAppPatches)
	t.Run("external dns", testExternalDNS)
	t.Run("edge health targets", testEdgeHealthTargets)
	t.Run("company site prod deployment", testCompanySiteProdDeployment)
	t.Run("root tenant core services", testRootTenantCoreServices)
	t.Run("observability", testObservability)
	t.Run("openbao", testOpenBao)
	t.Run("openbao opentofu bootstrap", testOpenBaoOpenTofuBootstrap)
	t.Run("flux handoff", testFluxHandoff)
}

func testTalmInstallDiskSelectors(t *testing.T) {
	systemDisks := map[string]string{
		"ash-earth": "362510FCEFB8",
		"ash-wind":  "352410A4E051",
		"ash-water": "362510FE3218",
	}
	dataDisks := map[string]string{
		"ash-earth": "362510FD7C47",
		"ash-wind":  "352410A4E0A6",
		"ash-water": "362510FE3204",
	}

	for node, systemSerial := range systemDisks {
		t.Run(node, func(t *testing.T) {
			rel := "src/infrastructure/talm/nodes/" + node + ".yaml"
			text := string(readRunfile(t, rel))
			assertTextNotContains(t, text, "disk: /dev/nvme", rel)

			docs := readManifests(t, rel)
			if len(docs) == 0 {
				t.Fatalf("%s has no YAML documents", rel)
			}
			install := valueAt(docs[0], "machine", "install")
			if install == nil {
				t.Fatalf("%s first document is missing machine.install", rel)
			}
			installMap := asManifest(t, install, rel+" machine.install")
			assertString(t, installMap, systemSerial, "diskSelector", "serial")
			assertString(t, installMap, "ghcr.io/cozystack/cozystack/talos:v1.13.0", "image")
			if disk := stringAt(installMap, "disk"); disk != "" {
				t.Fatalf("%s machine.install.disk = %q, want diskSelector only", rel, disk)
			}
			if systemSerial == dataDisks[node] {
				t.Fatalf("%s system disk serial must differ from LINSTOR data disk serial %s", node, dataDisks[node])
			}
		})
	}
}

func testCozystackPlatformPackage(t *testing.T) {
	topology := guardianMgmtTopologyFixture(t)
	docs := readManifests(t, "src/infrastructure/base/cozystack/platform.yaml")
	pkg := findObject(t, docs, "Package", "", "cozystack.cozystack-platform")

	assertString(t, pkg, "cozystack.io/v1alpha1", "apiVersion")
	assertString(t, pkg, "isp-full", "spec", "variant")
	if valueAt(pkg, "spec", "components", "platform", "values", "bundles", "enabledPackages") != nil {
		t.Fatalf("cozystack platform package must not carry legacy enabledPackages overrides")
	}
	assertStringSlice(t, pkg, []string{"cozystack.backupstrategy-controller"}, "spec", "components", "platform", "values", "bundles", "disabledPackages")
	assertString(t, pkg, "guardianintelligence.org", "spec", "components", "platform", "values", "publishing", "host")
	assertString(t, pkg, fmt.Sprintf("https://%s:6443", topology.Network.VLAN.APIVIP), "spec", "components", "platform", "values", "publishing", "apiServerEndpoint")
	assertStringSlice(t, pkg, topologyPublicIPs(t, topology), "spec", "components", "platform", "values", "publishing", "externalIPs")
	assertStringSlice(t, pkg, []string{"dashboard", "api"}, "spec", "components", "platform", "values", "publishing", "exposedServices")

	assertString(t, pkg, "10.244.0.0/16", "spec", "components", "platform", "values", "networking", "podCIDR")
	assertString(t, pkg, "10.244.0.1", "spec", "components", "platform", "values", "networking", "podGateway")
	assertString(t, pkg, "10.96.0.0/16", "spec", "components", "platform", "values", "networking", "serviceCIDR")
	assertString(t, pkg, "100.64.0.0/16", "spec", "components", "platform", "values", "networking", "joinCIDR")

	assertBool(t, pkg, true, "spec", "components", "platform", "values", "authentication", "oidc", "enabled")
	assertString(t, pkg, "http://keycloak-http.cozy-keycloak.svc:8080/realms/cozy", "spec", "components", "platform", "values", "authentication", "oidc", "keycloakInternalUrl")
	assertString(t, pkg, "Guardian", "spec", "components", "platform", "values", "branding", "titleText")
	assertString(t, pkg, "Guardian Intelligence", "spec", "components", "platform", "values", "branding", "footerText")

	docs = readManifests(t, "src/infrastructure/base/cozystack/backupstrategy-controller.yaml")
	pkg = findObject(t, docs, "Package", "", "cozystack.backupstrategy-controller")
	assertString(t, pkg, "cozystack.io/v1alpha1", "apiVersion")
	assertString(t, pkg, "default", "spec", "variant")
	assertString(t, pkg, "https://s3.guardianintelligence.org", "spec", "components", "backupstrategy-controller", "values", "backupStorage", "endpoint")
}

func testGuardianMgmtTopologyAlignment(t *testing.T) {
	topology := guardianMgmtTopologyFixture(t)
	if topology.Cluster != "guardian-mgmt" {
		t.Fatalf("topology cluster = %q, want guardian-mgmt", topology.Cluster)
	}
	if len(topology.Nodes) != 3 {
		t.Fatalf("topology nodes = %d, want 3", len(topology.Nodes))
	}
	assertUniqueTopologyValues(t, topology)

	values := readYAMLMap(t, "src/infrastructure/talm/values.yaml")
	assertString(t, values, fmt.Sprintf("https://%s:6443", topology.Network.VLAN.APIVIP), "endpoint")
	assertString(t, values, topology.Network.VLAN.APIVIP, "floatingIP")
	assertString(t, values, topology.Network.VLAN.VIPLink, "vipLink")
	assertStringSlice(t, values, []string{topology.Network.VLAN.Subnet}, "advertisedSubnets")

	certSANs := sliceAt(t, values, "certSANs")
	assertContainsString(t, certSANs, topology.Network.VLAN.APIVIP, "certSANs")
	for _, node := range topology.Nodes {
		assertContainsString(t, certSANs, node.PublicIPv4, "certSANs")
	}

	imports := string(readRunfile(t, "src/infrastructure/bootstrap/guardian-mgmt/imports.tf"))
	assertTextContains(t, imports, `to = latitudesh_virtual_network.management`, "imports.tf")
	assertTextContains(t, imports, `id = "`+topology.Network.VLAN.ID+`"`, "imports.tf")
	for _, node := range topology.Nodes {
		assertTextContains(t, imports, `to = latitudesh_server.control_plane["`+node.Name+`"]`, "imports.tf")
		assertTextContains(t, imports, `id = "`+node.ServerID+`"`, "imports.tf")
	}

	mainTF := string(readRunfile(t, "src/infrastructure/bootstrap/guardian-mgmt/main.tf"))
	assertTextNotContains(t, mainTF, "jsondecode", "main.tf")
	assertTextNotContains(t, mainTF, "guardian-mgmt.json", "main.tf")
	assertTextContains(t, mainTF, `project_id = "proj_R82A0yqmd06mM"`, "main.tf")
	assertTextContains(t, mainTF, `site       = "ASH"`, "main.tf")
	assertTextContains(t, mainTF, `id           = "`+topology.Network.VLAN.ID+`"`, "main.tf")
	assertTextContains(t, mainTF, fmt.Sprintf(`vid          = %d`, topology.Network.VLAN.VID), "main.tf")
	assertTextContains(t, mainTF, `subnet       = "`+topology.Network.VLAN.Subnet+`"`, "main.tf")
	assertTextContains(t, mainTF, fmt.Sprintf(`vlan_mtu     = %d`, topology.Network.VLAN.VLANMTU), "main.tf")
	assertTextContains(t, mainTF, fmt.Sprintf(`pod_mtu      = %d`, topology.Network.VLAN.PodMTU), "main.tf")
	assertTextContains(t, mainTF, `api_vip      = "`+topology.Network.VLAN.APIVIP+`"`, "main.tf")
	assertTextContains(t, mainTF, `vip_link     = "`+topology.Network.VLAN.VIPLink+`"`, "main.tf")
	assertTextContains(t, mainTF, `metallb_pool = "`+topology.Network.VLAN.MetalLBPool+`"`, "main.tf")
	for _, node := range topology.Nodes {
		assertTextContains(t, mainTF, node.Name+" = {", "main.tf")
		assertTextContains(t, mainTF, `server_id    = "`+node.ServerID+`"`, "main.tf")
		assertTextContains(t, mainTF, `hostname     = "`+node.Hostname+`"`, "main.tf")
		assertTextContains(t, mainTF, `public_ipv4  = "`+node.PublicIPv4+`"`, "main.tf")
		assertTextContains(t, mainTF, `private_ipv4 = "`+node.PrivateIPv4+`"`, "main.tf")
	}
	assertTextContains(t, mainTF, `resource "latitudesh_vlan_assignment" "control_plane"`, "main.tf")
	assertTextContains(t, mainTF, `for_each = local.control_plane_nodes`, "main.tf")
	assertTextContains(t, mainTF, `latitudesh_virtual_network.management.vid == local.vlan.vid`, "main.tf")
	assertTextContains(t, mainTF, `latitudesh_server.control_plane[name].primary_ipv4 == node.public_ipv4`, "main.tf")

	versionsPath := "src/infrastructure/bootstrap/guardian-mgmt/versions.tf"
	assertPartialS3Backend(t, readRunfile(t, versionsPath), versionsPath, "opentofu/guardian-mgmt.tfstate", "guardian-mgmt backend.s3")

	outputsTF := string(readRunfile(t, "src/infrastructure/bootstrap/guardian-mgmt/outputs.tf"))
	assertTextContains(t, outputsTF, `output "management_vlan"`, "outputs.tf")
	assertTextContains(t, outputsTF, `api_server_endpoint = "https://${local.vlan.api_vip}:6443"`, "outputs.tf")
	assertTextContains(t, outputsTF, `metallb_pool        = local.vlan.metallb_pool`, "outputs.tf")
	assertTextContains(t, outputsTF, `output "control_plane_nodes"`, "outputs.tf")
	assertTextContains(t, outputsTF, `for name, node in local.control_plane_nodes`, "outputs.tf")
	assertTextContains(t, outputsTF, `server_id    = latitudesh_server.control_plane[name].id`, "outputs.tf")
	assertTextContains(t, outputsTF, `hostname     = latitudesh_server.control_plane[name].hostname`, "outputs.tf")
	assertTextContains(t, outputsTF, `public_ipv4  = latitudesh_server.control_plane[name].primary_ipv4`, "outputs.tf")
	assertTextContains(t, outputsTF, `private_ipv4 = node.private_ipv4`, "outputs.tf")
}

func testGuardianMgmtDNSBootstrap(t *testing.T) {
	const versionsPath = "src/infrastructure/bootstrap/guardian-mgmt-dns/versions.tf"
	versionsBytes := readRunfile(t, versionsPath)
	versions := string(versionsBytes)
	mainTF := string(readRunfile(t, "src/infrastructure/bootstrap/guardian-mgmt-dns/main.tf"))
	variablesTF := string(readRunfile(t, "src/infrastructure/bootstrap/guardian-mgmt-dns/variables.tf"))
	outputsTF := string(readRunfile(t, "src/infrastructure/bootstrap/guardian-mgmt-dns/outputs.tf"))
	lock := string(readRunfile(t, "src/infrastructure/bootstrap/guardian-mgmt-dns/.terraform.lock.hcl"))

	assertTextNotContains(t, versions, `source  = "hashicorp/aws"`, "guardian-mgmt-dns versions.tf")
	assertTextNotContains(t, lock, `provider "registry.opentofu.org/hashicorp/aws"`, "guardian-mgmt-dns lock")
	assertTextContains(t, versions, `source  = "cloudflare/cloudflare"`, "guardian-mgmt-dns versions.tf")
	assertTextContains(t, versions, `version = "= 4.52.5"`, "guardian-mgmt-dns versions.tf")
	assertPartialS3Backend(t, versionsBytes, versionsPath, "opentofu/guardian-mgmt-dns.tfstate", "guardian-mgmt-dns backend.s3")

	assertTextNotContains(t, variablesTF, `variable "aws_region"`, "guardian-mgmt-dns variables.tf")
	assertTextContains(t, variablesTF, `variable "cloudflare_account_id"`, "guardian-mgmt-dns variables.tf")
	assertTextContains(t, variablesTF, `variable "cloudflare_lb_monitor_interval_seconds"`, "guardian-mgmt-dns variables.tf")
	assertTextContains(t, variablesTF, `variable "cloudflare_lb_check_regions"`, "guardian-mgmt-dns variables.tf")
	assertTextNotContains(t, mainTF, `data "terraform_remote_state"`, "guardian-mgmt-dns main.tf")
	assertTextContains(t, mainTF, `public_ingress_origin_names = [`, "guardian-mgmt-dns main.tf")
	assertTextContains(t, mainTF, `public_ingress_origins = {`, "guardian-mgmt-dns main.tf")
	assertTextContains(t, mainTF, `cloudflare_load_balancer_monitor`, "guardian-mgmt-dns main.tf")
	assertTextContains(t, mainTF, `cloudflare_load_balancer_pool`, "guardian-mgmt-dns main.tf")
	assertTextContains(t, mainTF, `cloudflare_load_balancer`, "guardian-mgmt-dns main.tf")
	assertTextContains(t, mainTF, `origin_steering {`, "guardian-mgmt-dns main.tf")
	assertTextContains(t, mainTF, `policy = "random"`, "guardian-mgmt-dns main.tf")
	assertTextContains(t, mainTF, `data "cloudflare_zone" "guardianintelligence_org"`, "guardian-mgmt-dns main.tf")
	assertTextContains(t, mainTF, `account_id = var.cloudflare_account_id`, "guardian-mgmt-dns main.tf")
	assertTextNotContains(t, mainTF, `resource "aws_route53_record"`, "guardian-mgmt-dns main.tf")
	assertTextNotContains(t, mainTF, `resource "cloudflare_record"`, "guardian-mgmt-dns main.tf")
	assertTextContains(t, mainTF, `external_dns_owner_id = "guardian-mgmt-ash"`, "guardian-mgmt-dns main.tf")

	for _, host := range []string{
		`"*.guardianintelligence.org"`,
		`"guardianintelligence.org"`,
		`"api.guardianintelligence.org"`,
		`"alerta.guardianintelligence.org"`,
		`"dashboard.guardianintelligence.org"`,
		`"keycloak.guardianintelligence.org"`,
		`"grafana.guardianintelligence.org"`,
		`"harbor.guardianintelligence.org"`,
		`"s3.guardianintelligence.org"`,
	} {
		assertTextContains(t, mainTF, host, "guardian-mgmt-dns main.tf")
	}
	recordDefinitions := strings.Split(mainTF, `check "no_legacy_verself_records"`)[0]
	assertTextNotContains(t, recordDefinitions, "206.223.228.99", "guardian-mgmt-dns record definitions")
	assertTextNotContains(t, recordDefinitions, "67.213.115.113", "guardian-mgmt-dns record definitions")
	assertTextContains(t, mainTF, `check "cloudflare_load_balancer_hostnames"`, "guardian-mgmt-dns main.tf")
	assertTextContains(t, mainTF, `check "cloudflare_load_balancer_origins"`, "guardian-mgmt-dns main.tf")

	assertTextContains(t, outputsTF, `output "cloudflare_zone_id"`, "guardian-mgmt-dns outputs.tf")
	assertTextContains(t, outputsTF, `output "external_dns_owner_id"`, "guardian-mgmt-dns outputs.tf")
	assertTextContains(t, outputsTF, `output "cloudflare_load_balancer_hostnames"`, "guardian-mgmt-dns outputs.tf")
	assertTextContains(t, outputsTF, `output "cloudflare_load_balancer_pool_id"`, "guardian-mgmt-dns outputs.tf")
	assertTextContains(t, outputsTF, `output "public_ingress_ipv4s"`, "guardian-mgmt-dns outputs.tf")
}

func testLayerTwoNetworking(t *testing.T) {
	topology := guardianMgmtTopologyFixture(t)
	metallb := readManifests(t, "src/infrastructure/base/networking/metallb.yaml")
	pool := findObject(t, metallb, "IPAddressPool", "cozy-metallb", "cozystack")
	assertString(t, pool, "metallb.io/v1beta1", "apiVersion")
	assertStringSlice(t, pool, []string{topology.Network.VLAN.MetalLBPool}, "spec", "addresses")
	assertBool(t, pool, true, "spec", "autoAssign")
	assertBool(t, pool, false, "spec", "avoidBuggyIPs")

	ad := findObject(t, metallb, "L2Advertisement", "cozy-metallb", "cozystack")
	assertString(t, ad, "metallb.io/v1beta1", "apiVersion")
	assertStringSlice(t, ad, []string{"cozystack"}, "spec", "ipAddressPools")

	subnets := readManifests(t, "src/infrastructure/base/networking/subnet-mtu.yaml")
	ovnDefault := findObject(t, subnets, "Subnet", "", "ovn-default")
	assertString(t, ovnDefault, "kubeovn.io/v1", "apiVersion")
	assertBool(t, ovnDefault, true, "spec", "default")
	assertString(t, ovnDefault, "10.244.0.0/16", "spec", "cidrBlock")
	assertString(t, ovnDefault, "10.244.0.1", "spec", "gateway")
	assertString(t, ovnDefault, "distributed", "spec", "gatewayType")
	assertBool(t, ovnDefault, true, "spec", "natOutgoing")
	assertInt(t, ovnDefault, topology.Network.VLAN.PodMTU, "spec", "mtu")

	join := findObject(t, subnets, "Subnet", "", "join")
	assertString(t, join, "kubeovn.io/v1", "apiVersion")
	assertBool(t, join, false, "spec", "default")
	assertString(t, join, "100.64.0.0/16", "spec", "cidrBlock")
	assertString(t, join, "100.64.0.1", "spec", "gateway")
	assertBool(t, join, false, "spec", "natOutgoing")
	assertInt(t, join, topology.Network.VLAN.PodMTU, "spec", "mtu")
}

func testSingleDefaultStorageClass(t *testing.T) {
	docs := readManifests(t, "src/infrastructure/base/storage/storageclasses.yaml")

	var defaults []manifest
	for _, doc := range docs {
		if stringAt(doc, "kind") != "StorageClass" {
			continue
		}
		if stringAt(doc, "metadata", "annotations", "storageclass.kubernetes.io/is-default-class") == "true" {
			defaults = append(defaults, doc)
		}
	}

	if len(defaults) != 1 {
		t.Fatalf("expected exactly one default StorageClass, got %d", len(defaults))
	}

	replicated := defaults[0]
	assertString(t, replicated, "replicated", "metadata", "name")
	assertString(t, replicated, "linstor.csi.linbit.com", "provisioner")
	assertString(t, replicated, "data", "parameters", "linstor.csi.linbit.com/storagePool")
	assertString(t, replicated, "drbd storage", "parameters", "linstor.csi.linbit.com/layerList")
	assertString(t, replicated, "3", "parameters", "linstor.csi.linbit.com/autoPlace")
	assertString(t, replicated, "Immediate", "volumeBindingMode")
}

func testLINSTORDataPools(t *testing.T) {
	kustomization := readYAMLMap(t, "src/infrastructure/base/kustomization.yaml")
	resources := sliceAt(t, kustomization, "resources")
	if containsString(resources, "storage/linstor-data-pools.yaml") {
		t.Fatalf("base kustomization must not include storage/linstor-data-pools.yaml; storage is reconciled by guardian-mgmt-storage")
	}

	storageKustomization := readYAMLMap(t, "src/infrastructure/base/storage/kustomization.yaml")
	storageResources := sliceAt(t, storageKustomization, "resources")
	assertContainsString(t, storageResources, "linstor-data-pools.yaml", "storage kustomization resources")
	assertContainsString(t, storageResources, "storageclasses.yaml", "storage kustomization resources")

	docs := readManifests(t, "src/infrastructure/base/storage/linstor-data-pools.yaml")
	wantDevices := map[string]string{
		"ash-earth": "/dev/disk/by-id/nvme-MTFDKCC960TGP-1BK1JABYY_362510FD7C47",
		"ash-wind":  "/dev/disk/by-id/nvme-MTFDKCC960TGP-1BK1JABYY_352410A4E0A6",
		"ash-water": "/dev/disk/by-id/nvme-MTFDKCC960TGP-1BK1JABYY_362510FE3204",
	}
	if len(docs) != len(wantDevices) {
		t.Fatalf("LINSTOR data pool docs = %d, want %d", len(docs), len(wantDevices))
	}

	for node, device := range wantDevices {
		cfg := findObject(t, docs, "LinstorSatelliteConfiguration", "cozy-linstor", "guardian-data-pool-"+node)
		assertString(t, cfg, "piraeus.io/v1", "apiVersion")
		assertString(t, cfg, node, "spec", "nodeSelector", "kubernetes.io/hostname")

		pools := sliceAt(t, cfg, "spec", "storagePools")
		if len(pools) != 1 {
			t.Fatalf("%s storagePools = %d, want 1", node, len(pools))
		}
		pool := asManifest(t, pools[0], node+" storagePools[0]")
		assertString(t, pool, "data", "name")
		assertString(t, pool, "data", "lvmPool", "volumeGroup")
		assertStringSlice(t, pool, []string{device}, "source", "hostDevices")
	}
}

func testCozystackSystemBucketBackups(t *testing.T) {
	for _, rel := range []string{
		"src/infrastructure/base/kustomization.yaml",
		"src/infrastructure/base/apps/core-services.yaml",
	} {
		text := string(readRunfile(t, rel))
		for _, forbidden := range []string{
			"backup/",
			"destinationPath:",
			"endpointURL:",
			"s3CredentialsSecret:",
			"ExternalSecret",
			"SecretStore",
		} {
			assertTextNotContains(t, text, forbidden, rel)
		}
	}

	docs := readManifests(t, "src/infrastructure/base/apps/core-services.yaml")
	assertSystemBucketBackup(t, docs, "Postgres", "tenant-root", "guardian-postgres-daily", "7 1 * * *")
	assertSystemBucketBackup(t, docs, "ClickHouse", "tenant-root", "guardian-clickhouse-daily", "17 1 * * *")
}

func testCozystackPlatformPatches(t *testing.T) {
	docs := readManifests(t, "src/infrastructure/base/platform-patches/cozystack-networking-hubble.yaml")
	pkg := findObject(t, docs, "Package", "", "cozystack.networking")
	assertString(t, pkg, "cozystack.io/v1alpha1", "apiVersion")
	assertBool(t, pkg, true, "spec", "components", "cilium", "values", "cilium", "hubble", "enabled")
	assertString(t, pkg, "cozy.local", "spec", "components", "cilium", "values", "cilium", "hubble", "peerService", "clusterDomain")
	assertBool(t, pkg, true, "spec", "components", "cilium", "values", "cilium", "hubble", "relay", "enabled")
	assertBool(t, pkg, true, "spec", "components", "cilium", "values", "cilium", "hubble", "relay", "rollOutPods")
	assertBool(t, pkg, true, "spec", "components", "cilium", "values", "cilium", "hubble", "ui", "enabled")
	assertBool(t, pkg, true, "spec", "components", "cilium", "values", "cilium", "hubble", "ui", "rollOutPods")
	assertBool(t, pkg, true, "spec", "components", "cilium", "values", "cilium", "hubble", "metrics", "serviceMonitor", "enabled")
	assertBool(t, pkg, true, "spec", "components", "cilium", "values", "cilium", "hubble", "relay", "prometheus", "serviceMonitor", "enabled")
	assertString(t, pkg, "50m", "spec", "components", "cilium", "values", "cilium", "hubble", "relay", "resources", "requests", "cpu")
	assertString(t, pkg, "256Mi", "spec", "components", "cilium", "values", "cilium", "hubble", "relay", "resources", "limits", "memory")

	metrics := sliceAt(t, pkg, "spec", "components", "cilium", "values", "cilium", "hubble", "metrics", "enabled")
	if len(metrics) != 7 {
		t.Fatalf("hubble metrics enabled = %d entries, want 7", len(metrics))
	}
	for _, want := range []string{"dns", "drop", "tcp", "flow", "port-distribution", "icmp", "httpV2:exemplars=true;labelsContext=source_ip,source_namespace,source_workload,destination_ip,destination_namespace,destination_workload,traffic_direction"} {
		found := false
		for _, got := range metrics {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("hubble metrics enabled missing %q: %#v", want, metrics)
		}
	}

	fluxDocs := readManifests(t, "src/infrastructure/base/flux/sync.yaml")
	kustomization := findObject(t, fluxDocs, "Kustomization", "cozy-fluxcd", "guardian-mgmt-platform-patches")
	assertString(t, kustomization, "kustomize.toolkit.fluxcd.io/v1", "apiVersion")
	assertString(t, kustomization, "./src/infrastructure/base/platform-patches", "spec", "path")
	assertBool(t, kustomization, false, "spec", "prune")
	assertBool(t, kustomization, false, "spec", "wait")
	dependsOn := valueAt(kustomization, "spec", "dependsOn")
	deps, ok := dependsOn.([]any)
	if !ok || len(deps) != 1 {
		t.Fatalf("guardian-mgmt-platform-patches dependsOn = %#v, want one dependency", dependsOn)
	}
	dep := asManifest(t, deps[0], "guardian-mgmt-platform-patches dependsOn[0]")
	assertString(t, dep, "guardian-mgmt-platform", "name")
}

func testCozystackAppPatches(t *testing.T) {
	kustomization := readYAMLMap(t, "src/infrastructure/base/app-patches/kustomization.yaml")
	assertContainsString(t, sliceAt(t, kustomization, "resources"), "clickhouse-system-bucket-endpoint.yaml", "app patches kustomization resources")
	assertContainsString(t, sliceAt(t, kustomization, "resources"), "ingress-origin-edge.yaml", "app patches kustomization resources")

	docs := readManifests(t, "src/infrastructure/base/app-patches/clickhouse-system-bucket-endpoint.yaml")
	hr := findObject(t, docs, "HelmRelease", "tenant-root", "clickhouse-guardian")
	assertString(t, hr, "helm.toolkit.fluxcd.io/v2", "apiVersion")

	postRenderers := valueAt(hr, "spec", "postRenderers")
	list, ok := postRenderers.([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("clickhouse HelmRelease patch postRenderers = %#v, want one postRenderer", postRenderers)
	}
	renderer := asManifest(t, list[0], "clickhouse postRenderer")
	patches := valueAt(renderer, "kustomize", "patches")
	patchList, ok := patches.([]any)
	if !ok || len(patchList) != 1 {
		t.Fatalf("clickhouse postRenderer patches = %#v, want one patch", patches)
	}
	patch := asManifest(t, patchList[0], "clickhouse postRenderer patch")
	assertString(t, patch, "clickhouse.altinity.com", "target", "group")
	assertString(t, patch, "v1", "target", "version")
	assertString(t, patch, "ClickHouseInstallation", "target", "kind")
	assertString(t, patch, "clickhouse-guardian", "target", "name")

	patchText := stringAt(patch, "patch")
	for _, want := range []string{
		"path: /spec/templates/podTemplates/0/spec/containers/1/env/12/valueFrom",
		"path: /spec/templates/podTemplates/0/spec/containers/1/env/12/value",
		"value: https://s3.guardianintelligence.org",
	} {
		assertTextContains(t, patchText, want, "clickhouse postRenderer patch")
	}

	ingressDocs := readManifests(t, "src/infrastructure/base/app-patches/ingress-origin-edge.yaml")
	ingressHR := findObject(t, ingressDocs, "HelmRelease", "tenant-root", "ingress-nginx-system")
	assertString(t, ingressHR, "helm.toolkit.fluxcd.io/v2", "apiVersion")
	assertString(t, ingressHR, "false", "spec", "values", "ingress-nginx", "controller", "config", "use-http2")
	assertString(t, ingressHR, "Cluster", "spec", "values", "ingress-nginx", "controller", "service", "externalTrafficPolicy")
	assertString(t, ingressHR, "RollingUpdate", "spec", "values", "ingress-nginx", "controller", "updateStrategy", "type")
	assertInt(t, ingressHR, 0, "spec", "values", "ingress-nginx", "controller", "updateStrategy", "rollingUpdate", "maxSurge")
	assertInt(t, ingressHR, 1, "spec", "values", "ingress-nginx", "controller", "updateStrategy", "rollingUpdate", "maxUnavailable")
	requiredAntiAffinity := valueAt(ingressHR, "spec", "values", "ingress-nginx", "controller", "affinity", "podAntiAffinity", "requiredDuringSchedulingIgnoredDuringExecution")
	requiredTerms, ok := requiredAntiAffinity.([]any)
	if !ok || len(requiredTerms) != 1 {
		t.Fatalf("ingress required pod anti-affinity = %#v, want one term", requiredAntiAffinity)
	}
	requiredTerm := asManifest(t, requiredTerms[0], "ingress required pod anti-affinity")
	assertString(t, requiredTerm, "kubernetes.io/hostname", "topologyKey")

	fluxDocs := readManifests(t, "src/infrastructure/base/flux/sync.yaml")
	fluxKustomization := findObject(t, fluxDocs, "Kustomization", "cozy-fluxcd", "guardian-mgmt-app-patches")
	assertString(t, fluxKustomization, "kustomize.toolkit.fluxcd.io/v1", "apiVersion")
	assertString(t, fluxKustomization, "./src/infrastructure/base/app-patches", "spec", "path")
	assertBool(t, fluxKustomization, false, "spec", "prune")
	assertBool(t, fluxKustomization, false, "spec", "wait")
	dependsOn := valueAt(fluxKustomization, "spec", "dependsOn")
	deps, ok := dependsOn.([]any)
	if !ok || len(deps) != 1 {
		t.Fatalf("guardian-mgmt-app-patches dependsOn = %#v, want one dependency", dependsOn)
	}
	dep := asManifest(t, deps[0], "guardian-mgmt-app-patches dependsOn[0]")
	assertString(t, dep, "guardian-mgmt-base", "name")
}

func testExternalDNS(t *testing.T) {
	base := readYAMLMap(t, "src/infrastructure/base/kustomization.yaml")
	baseResources := sliceAt(t, base, "resources")
	if containsString(baseResources, "dns") {
		t.Fatalf("base kustomization must not include dns directly; Flux applies the DNS controller in an ordered slice")
	}
	assertContainsString(t, baseResources, "flux/sync.yaml", "base kustomization resources")

	kustomization := readYAMLMap(t, "src/infrastructure/base/dns/kustomization.yaml")
	for _, resource := range []string{
		"namespace.yaml",
		"secrets.yaml",
		"external-dns.yaml",
		"networkpolicy.yaml",
	} {
		assertContainsString(t, sliceAt(t, kustomization, "resources"), resource, "external-dns kustomization resources")
	}

	namespace := findObject(t, readManifests(t, "src/infrastructure/base/dns/namespace.yaml"), "Namespace", "", "external-dns")
	assertString(t, namespace, "restricted", "metadata", "labels", "pod-security.kubernetes.io/enforce")
	assertString(t, namespace, "restricted", "metadata", "labels", "pod-security.kubernetes.io/audit")
	assertString(t, namespace, "restricted", "metadata", "labels", "pod-security.kubernetes.io/warn")

	secretDocs := readManifests(t, "src/infrastructure/base/dns/secrets.yaml")
	serviceAccount := findObject(t, secretDocs, "ServiceAccount", "external-dns", "external-dns-secrets")
	assertBool(t, serviceAccount, false, "automountServiceAccountToken")

	store := findObject(t, secretDocs, "SecretStore", "external-dns", "openbao")
	assertString(t, store, "http://openbao-guardian.tenant-root.svc:8200", "spec", "provider", "vault", "server")
	assertString(t, store, "kv", "spec", "provider", "vault", "path")
	assertString(t, store, "v2", "spec", "provider", "vault", "version")
	assertString(t, store, "kubernetes", "spec", "provider", "vault", "auth", "kubernetes", "mountPath")
	assertString(t, store, "tenant-root-external-dns", "spec", "provider", "vault", "auth", "kubernetes", "role")
	assertString(t, store, "external-dns-secrets", "spec", "provider", "vault", "auth", "kubernetes", "serviceAccountRef", "name")
	assertStringSlice(t, store, []string{"openbao"}, "spec", "provider", "vault", "auth", "kubernetes", "serviceAccountRef", "audiences")

	externalSecret := findObject(t, secretDocs, "ExternalSecret", "external-dns", "cloudflare-external-dns")
	assertString(t, externalSecret, "1h", "spec", "refreshInterval")
	assertString(t, externalSecret, "SecretStore", "spec", "secretStoreRef", "kind")
	assertString(t, externalSecret, "openbao", "spec", "secretStoreRef", "name")
	assertString(t, externalSecret, "cloudflare-external-dns", "spec", "target", "name")
	assertString(t, externalSecret, "Owner", "spec", "target", "creationPolicy")
	assertString(t, externalSecret, "Retain", "spec", "target", "deletionPolicy")
	data := sliceAt(t, externalSecret, "spec", "data")
	if len(data) != 1 {
		t.Fatalf("external-dns ExternalSecret data = %d entries, want 1", len(data))
	}
	secretRef := asManifest(t, data[0], "external-dns ExternalSecret data[0]")
	assertString(t, secretRef, "CF_API_TOKEN", "secretKey")
	assertString(t, secretRef, "guardian/guardian-mgmt/tenant-root/dns/external-dns", "remoteRef", "key")
	assertString(t, secretRef, "CF_API_TOKEN", "remoteRef", "property")

	externalDNSDocs := readManifests(t, "src/infrastructure/base/dns/external-dns.yaml")
	repo := findObject(t, externalDNSDocs, "HelmRepository", "external-dns", "external-dns")
	assertString(t, repo, "https://kubernetes-sigs.github.io/external-dns/", "spec", "url")
	assertString(t, repo, "1h", "spec", "interval")

	release := findObject(t, externalDNSDocs, "HelmRelease", "external-dns", "external-dns")
	assertString(t, release, "helm.toolkit.fluxcd.io/v2", "apiVersion")
	assertString(t, release, "external-dns", "spec", "chart", "spec", "chart")
	assertString(t, release, "1.21.1", "spec", "chart", "spec", "version")
	assertString(t, release, "HelmRepository", "spec", "chart", "spec", "sourceRef", "kind")
	assertString(t, release, "external-dns", "spec", "chart", "spec", "sourceRef", "name")
	assertString(t, release, "external-dns", "spec", "chart", "spec", "sourceRef", "namespace")
	assertString(t, release, "Create", "spec", "install", "crds")
	assertString(t, release, "CreateReplace", "spec", "upgrade", "crds")
	assertString(t, release, "cloudflare", "spec", "values", "provider", "name")
	assertStringSlice(t, release, []string{"crd"}, "spec", "values", "sources")
	assertStringSlice(t, release, []string{"A"}, "spec", "values", "managedRecordTypes")
	assertString(t, release, "sync", "spec", "values", "policy")
	assertString(t, release, "txt", "spec", "values", "registry")
	assertString(t, release, "guardian-mgmt-ash", "spec", "values", "txtOwnerId")
	assertString(t, release, "external-dns-", "spec", "values", "txtPrefix")
	assertStringSlice(t, release, []string{"guardianintelligence.org"}, "spec", "values", "domainFilters")
	assertBool(t, release, true, "spec", "values", "triggerLoopOnEvent")
	assertString(t, release, "json", "spec", "values", "logFormat")
	assertString(t, release, "c952fb5989d232593ec9cca71030cb58", "spec", "values", "extraArgs", "zone-id-filter")
	assertString(t, release, "5000", "spec", "values", "extraArgs", "cloudflare-dns-records-per-page")
	assertString(t, release, "guardian-mgmt-ash external-dns", "spec", "values", "extraArgs", "cloudflare-record-comment")
	assertString(t, release, "25m", "spec", "values", "resources", "requests", "cpu")
	assertString(t, release, "64Mi", "spec", "values", "resources", "requests", "memory")
	assertString(t, release, "250m", "spec", "values", "resources", "limits", "cpu")
	assertString(t, release, "256Mi", "spec", "values", "resources", "limits", "memory")
	assertBool(t, release, true, "spec", "values", "serviceMonitor", "enabled")
	assertEnvSecretRef(t, sliceAt(t, release, "spec", "values", "env"), "CF_API_TOKEN", "cloudflare-external-dns", "CF_API_TOKEN")

	policy := findObject(t, readManifests(t, "src/infrastructure/base/dns/networkpolicy.yaml"), "CiliumNetworkPolicy", "external-dns", "external-dns-egress")
	assertString(t, policy, "cilium.io/v2", "apiVersion")
	assertString(t, policy, "external-dns", "spec", "endpointSelector", "matchLabels", "app.kubernetes.io/name")
	egress := sliceAt(t, policy, "spec", "egress")
	if len(egress) != 3 {
		t.Fatalf("external-dns egress policy has %d rules, want 3", len(egress))
	}
	assertStringSlice(t, asManifest(t, egress[0], "external-dns egress[0]"), []string{"kube-apiserver"}, "toEntities")
	dnsRule := asManifest(t, egress[1], "external-dns egress[1]")
	dnsTargets := sliceAt(t, dnsRule, "toEndpoints")
	if len(dnsTargets) != 1 {
		t.Fatalf("external-dns DNS egress targets = %d, want 1", len(dnsTargets))
	}
	dnsTarget := asManifest(t, dnsTargets[0], "external-dns DNS egress target")
	assertString(t, dnsTarget, "kube-system", "matchLabels", "k8s:io.kubernetes.pod.namespace")
	assertString(t, dnsTarget, "kube-dns", "matchLabels", "k8s:k8s-app")
	dnsToPorts := sliceAt(t, dnsRule, "toPorts")
	if len(dnsToPorts) != 1 {
		t.Fatalf("external-dns DNS egress toPorts = %d, want 1", len(dnsToPorts))
	}
	dnsPortRule := asManifest(t, dnsToPorts[0], "external-dns DNS egress toPorts[0]")
	dnsPorts := sliceAt(t, dnsPortRule, "ports")
	if len(dnsPorts) != 2 {
		t.Fatalf("external-dns DNS egress ports = %d, want 2", len(dnsPorts))
	}
	httpsRule := asManifest(t, egress[2], "external-dns egress[2]")
	assertStringSlice(t, httpsRule, []string{"world"}, "toEntities")
	httpsToPorts := sliceAt(t, httpsRule, "toPorts")
	if len(httpsToPorts) != 1 {
		t.Fatalf("external-dns HTTPS egress toPorts = %d, want 1", len(httpsToPorts))
	}
	httpsPorts := sliceAt(t, asManifest(t, httpsToPorts[0], "external-dns HTTPS egress toPorts[0]"), "ports")
	if len(httpsPorts) != 1 {
		t.Fatalf("external-dns HTTPS egress ports = %d, want 1", len(httpsPorts))
	}
	httpsPort := asManifest(t, httpsPorts[0], "external-dns HTTPS egress port")
	assertString(t, httpsPort, "443", "port")
	assertString(t, httpsPort, "TCP", "protocol")
}

func testEdgeHealthTargets(t *testing.T) {
	type fileSDGroup struct {
		Targets []string          `yaml:"targets"`
		Labels  map[string]string `yaml:"labels"`
	}

	var groups []fileSDGroup
	if err := yaml.Unmarshal(readRunfile(t, "src/infrastructure/edge/http-targets.file_sd.yaml"), &groups); err != nil {
		t.Fatal(err)
	}
	for _, group := range groups {
		for _, target := range group.Targets {
			if target != "https://guardianintelligence.org/" {
				continue
			}
			if group.Labels["guardian_surface"] != "company-site" {
				t.Fatalf("guardianintelligence.org surface = %q, want company-site", group.Labels["guardian_surface"])
			}
			if group.Labels["guardian_stage"] != "prod" {
				t.Fatalf("guardianintelligence.org stage = %q, want prod", group.Labels["guardian_stage"])
			}
			if group.Labels["guardian_expected_statuses"] != "2xx" {
				t.Fatalf("guardianintelligence.org expected statuses = %q, want 2xx", group.Labels["guardian_expected_statuses"])
			}
			return
		}
	}
	t.Fatal("edge health targets must include https://guardianintelligence.org/")
}

func assertSystemBucketBackup(t *testing.T, docs []manifest, kind, namespace, planName, schedule string) {
	t.Helper()

	app := findObject(t, docs, kind, namespace, "guardian")
	backupValue := valueAt(app, "spec", "backup")
	if backupValue == nil {
		t.Fatalf("%s tenant %s is missing spec.backup", kind, namespace)
	}
	backup := asManifest(t, backupValue, kind+" spec.backup")
	assertBool(t, backup, true, "enabled")
	assertString(t, backup, "", "schedule")
	assertBool(t, backup, true, "useSystemBucket")
	if kind == "Postgres" {
		assertString(t, backup, "30d", "retentionPolicy")
	}
	for _, field := range []string{"destinationPath", "endpointURL", "s3CredentialsSecret"} {
		if valueAt(backup, field) != nil {
			t.Fatalf("%s %s backup must not set legacy %s", namespace, kind, field)
		}
	}

	plans := findObjects(t, docs, "Plan", namespace, planName)
	if len(plans) != 1 {
		t.Fatalf("found %d %s Plans, want exactly 1 when %s backup is enabled", len(plans), planName, kind)
	}
	plan := plans[0]
	assertString(t, plan, "backups.cozystack.io/v1alpha1", "apiVersion")
	assertString(t, plan, "apps.cozystack.io", "spec", "applicationRef", "apiGroup")
	assertString(t, plan, kind, "spec", "applicationRef", "kind")
	assertString(t, plan, "guardian", "spec", "applicationRef", "name")
	assertString(t, plan, "cozy-default", "spec", "backupClassName")
	assertString(t, plan, "cron", "spec", "schedule", "type")
	assertConcreteBackupSchedule(t, stringAt(plan, "spec", "schedule", "cron"))
	assertString(t, plan, schedule, "spec", "schedule", "cron")
}

func assertHarborRWORolloutStrategy(t *testing.T, rel, namespace string) {
	t.Helper()

	hr := findObject(t, readManifests(t, rel), "HelmRelease", namespace, "harbor-guardian-system")
	assertString(t, hr, "helm.toolkit.fluxcd.io/v2", "apiVersion")
	assertString(t, hr, "Recreate", "spec", "values", "harbor", "updateStrategy", "type")
}

func assertMonitoring(t *testing.T, rel, namespace, host string, highCapacity bool, labelKey, labelValue string) {
	t.Helper()

	docs := readManifests(t, rel)
	monitoring := findObject(t, docs, "Monitoring", namespace, "monitoring")
	assertString(t, monitoring, "apps.cozystack.io/v1alpha1", "apiVersion")
	assertString(t, monitoring, host, "spec", "host")

	shortSize := "3Gi"
	longSize := "5Gi"
	logRetention := "3"
	logSize := "3Gi"
	dbSize := "1Gi"
	alertaStorage := "1Gi"
	cpuRequest := "25m"
	cpuLimit := "250m"
	if highCapacity {
		shortSize = "5Gi"
		longSize = "10Gi"
		logSize = "5Gi"
		dbSize = "2Gi"
		alertaStorage = "2Gi"
		cpuRequest = "50m"
		cpuLimit = "500m"
	}

	metricsStorages := sliceAt(t, monitoring, "spec", "metricsStorages")
	if len(metricsStorages) != 2 {
		t.Fatalf("%s Monitoring metricsStorages = %d, want 2", namespace, len(metricsStorages))
	}
	assertMetricsStorage(t, asManifest(t, metricsStorages[0], namespace+" metricsStorages[0]"), "shortterm", "3d", "15s", shortSize, cpuRequest, cpuLimit)
	assertMetricsStorage(t, asManifest(t, metricsStorages[1], namespace+" metricsStorages[1]"), "longterm", map[bool]string{true: "14d", false: "7d"}[highCapacity], "5m", longSize, cpuRequest, cpuLimit)

	logsStorages := sliceAt(t, monitoring, "spec", "logsStorages")
	if len(logsStorages) != 1 {
		t.Fatalf("%s Monitoring logsStorages = %d, want 1", namespace, len(logsStorages))
	}
	logs := asManifest(t, logsStorages[0], namespace+" logsStorages[0]")
	assertString(t, logs, "generic", "name")
	assertString(t, logs, logRetention, "retentionPeriod")
	assertString(t, logs, logSize, "storage")
	assertString(t, logs, "replicated", "storageClassName")

	assertString(t, monitoring, dbSize, "spec", "grafana", "db", "size")
	assertString(t, monitoring, "128Mi", "spec", "grafana", "resources", "requests", "memory")
	assertString(t, monitoring, cpuRequest, "spec", "grafana", "resources", "requests", "cpu")
	assertString(t, monitoring, "512Mi", "spec", "grafana", "resources", "limits", "memory")
	assertString(t, monitoring, cpuLimit, "spec", "grafana", "resources", "limits", "cpu")

	assertString(t, monitoring, alertaStorage, "spec", "alerta", "storage")
	assertString(t, monitoring, "replicated", "spec", "alerta", "storageClassName")
	assertString(t, monitoring, "25m", "spec", "alerta", "resources", "requests", "cpu")
	assertString(t, monitoring, "128Mi", "spec", "alerta", "resources", "requests", "memory")
	assertString(t, monitoring, "250m", "spec", "alerta", "resources", "limits", "cpu")
	assertString(t, monitoring, "512Mi", "spec", "alerta", "resources", "limits", "memory")

	assertString(t, monitoring, "guardian-mgmt", "spec", "vmagent", "externalLabels", "cluster")
	assertString(t, monitoring, labelValue, "spec", "vmagent", "externalLabels", labelKey)
	assertStringSlice(t, monitoring, []string{
		"http://vminsert-shortterm:8480/insert/0/prometheus",
		"http://vminsert-longterm:8480/insert/0/prometheus",
	}, "spec", "vmagent", "remoteWrite", "urls")
}

func assertMetricsStorage(t *testing.T, storage manifest, name, retention, deduplication, size, cpuRequest, cpuLimit string) {
	t.Helper()

	assertString(t, storage, name, "name")
	assertString(t, storage, retention, "retentionPeriod")
	assertString(t, storage, deduplication, "deduplicationInterval")
	assertString(t, storage, size, "storage")
	assertString(t, storage, "replicated", "storageClassName")
	for _, component := range []string{"vminsert", "vmselect", "vmstorage"} {
		assertString(t, storage, cpuRequest, component, "minAllowed", "cpu")
		assertString(t, storage, cpuLimit, component, "maxAllowed", "cpu")
		if component == "vmstorage" {
			assertString(t, storage, "256Mi", component, "minAllowed", "memory")
			assertString(t, storage, "1Gi", component, "maxAllowed", "memory")
			continue
		}
		assertString(t, storage, "128Mi", component, "minAllowed", "memory")
		assertString(t, storage, "512Mi", component, "maxAllowed", "memory")
	}
}

func testRootTenantCoreServices(t *testing.T) {
	docs := readManifests(t, "src/infrastructure/base/apps/core-services.yaml")

	ingress := findObject(t, docs, "Ingress", "tenant-root", "ingress")
	assertString(t, ingress, "apps.cozystack.io/v1alpha1", "apiVersion")
	assertInt(t, ingress, 3, "spec", "replicas")
	assertStringSlice(t, ingress, []string{}, "spec", "whitelist")
	assertBool(t, ingress, true, "spec", "cloudflareProxy")

	seaweedfs := findObject(t, docs, "SeaweedFS", "tenant-root", "seaweedfs")
	assertString(t, seaweedfs, "apps.cozystack.io/v1alpha1", "apiVersion")
	assertString(t, seaweedfs, "s3.guardianintelligence.org", "spec", "host")
	assertString(t, seaweedfs, "Simple", "spec", "topology")
	assertInt(t, seaweedfs, 3, "spec", "replicationFactor")
	assertInt(t, seaweedfs, 3, "spec", "db", "replicas")
	assertString(t, seaweedfs, "10Gi", "spec", "db", "size")
	assertString(t, seaweedfs, "replicated", "spec", "db", "storageClass")
	assertInt(t, seaweedfs, 3, "spec", "master", "replicas")
	assertInt(t, seaweedfs, 3, "spec", "filer", "replicas")
	assertInt(t, seaweedfs, 3, "spec", "volume", "replicas")
	assertString(t, seaweedfs, "20Gi", "spec", "volume", "size")
	assertString(t, seaweedfs, "replicated", "spec", "volume", "storageClass")
	assertInt(t, seaweedfs, 3, "spec", "s3", "replicas")

	seaweedfsS3Service := findObject(t, readManifests(t, "src/infrastructure/base/apps/seaweedfs-s3-ingress-service.yaml"), "Service", "tenant-root", "seaweedfs-system-s3")
	assertString(t, seaweedfsS3Service, "v1", "apiVersion")
	assertString(t, seaweedfsS3Service, "seaweedfs", "metadata", "labels", "app.kubernetes.io/name")
	assertString(t, seaweedfsS3Service, "seaweedfs-system", "metadata", "labels", "app.kubernetes.io/instance")
	assertString(t, seaweedfsS3Service, "s3", "metadata", "labels", "app.kubernetes.io/component")
	assertString(t, seaweedfsS3Service, "ClusterIP", "spec", "type")
	assertString(t, seaweedfsS3Service, "Cluster", "spec", "internalTrafficPolicy")
	assertString(t, seaweedfsS3Service, "PreferClose", "spec", "trafficDistribution")
	assertString(t, seaweedfsS3Service, "seaweedfs", "spec", "selector", "app.kubernetes.io/name")
	assertString(t, seaweedfsS3Service, "seaweedfs-system", "spec", "selector", "app.kubernetes.io/instance")
	assertString(t, seaweedfsS3Service, "s3", "spec", "selector", "app.kubernetes.io/component")
	assertServicePort(t, seaweedfsS3Service, "swfs-s3", 8333)
	assertServicePort(t, seaweedfsS3Service, "metrics", 9327)

	assertApp(t, docs, appExpectation{
		kind:            "Postgres",
		namespace:       "tenant-root",
		storageClass:    "replicated",
		topReplicas:     3,
		noExternalDB:    true,
		postgresVersion: "v18",
	})
	assertApp(t, docs, appExpectation{
		kind:         "Harbor",
		namespace:    "tenant-root",
		host:         "harbor.guardianintelligence.org",
		storageClass: "replicated",
		nestedReplicas: map[string]int{
			"database": 3,
			"redis":    3,
		},
	})
	assertHarborRWORolloutStrategy(t, "src/infrastructure/base/apps/harbor-rwo-rollout-strategy.yaml", "tenant-root")
	assertApp(t, docs, appExpectation{
		kind:               "ClickHouse",
		namespace:          "tenant-root",
		storageClass:       "replicated",
		topReplicas:        3,
		backupPlanName:     "guardian-clickhouse-daily",
		backupPlanSchedule: "17 1 * * *",
		nestedReplicas: map[string]int{
			"clickhouseKeeper": 3,
		},
	})
}

func testObservability(t *testing.T) {
	assertMonitoring(t, "src/infrastructure/base/apps/observability.yaml", "tenant-root", "guardianintelligence.org", true, "guardian_tenant", "root")

	docs := readManifests(t, "src/infrastructure/base/platform-patches/cozystack-monitoring-agents-vmagent.yaml")
	pkg := findObject(t, docs, "Package", "", "cozystack.monitoring-agents")
	assertString(t, pkg, "64MB", "spec", "components", "monitoring-agents", "values", "vmagent", "extraArgs", "promscrape.maxScrapeSize")
}

func testOpenBao(t *testing.T) {
	docs := readManifests(t, "src/infrastructure/base/openbao/openbao.yaml")
	bao := findObject(t, docs, "OpenBAO", "tenant-root", "guardian")

	assertString(t, bao, "apps.cozystack.io/v1alpha1", "apiVersion")
	assertInt(t, bao, 3, "spec", "replicas")
	assertString(t, bao, "local-retain", "spec", "storageClass")
	assertString(t, bao, "10Gi", "spec", "size")
	assertBool(t, bao, true, "spec", "ui")
	assertBool(t, bao, false, "spec", "external")
	assertString(t, bao, "1", "spec", "resources", "cpu")
	assertString(t, bao, "2Gi", "spec", "resources", "memory")

	policies := readManifests(t, "src/infrastructure/base/openbao/networkpolicy.yaml")
	policy := findObject(t, policies, "CiliumNetworkPolicy", "tenant-root", "allow-openbao-to-apiserver")
	assertString(t, policy, "cilium.io/v2", "apiVersion")
	assertString(t, policy, "openbao", "spec", "endpointSelector", "matchLabels", "app.kubernetes.io/name")

	egress := sliceAt(t, policy, "spec", "egress")
	if len(egress) != 2 {
		t.Fatalf("spec.egress has %d entries, want 2", len(egress))
	}
	assertStringSlice(t, asManifest(t, egress[0], "spec.egress[0]"), []string{"kube-apiserver"}, "toEntities")

	toPorts := sliceAt(t, asManifest(t, egress[1], "spec.egress[1]"), "toPorts")
	if len(toPorts) != 1 {
		t.Fatalf("spec.egress[1].toPorts has %d entries, want 1", len(toPorts))
	}
	ports := sliceAt(t, asManifest(t, toPorts[0], "spec.egress[1].toPorts[0]"), "ports")
	if len(ports) != 1 {
		t.Fatalf("spec.egress[1].toPorts[0].ports has %d entries, want 1", len(ports))
	}
	port := asManifest(t, ports[0], "spec.egress[1].toPorts[0].ports[0]")
	assertString(t, port, "6443", "port")
	assertString(t, port, "TCP", "protocol")
	assertNoObject(t, policies, "CiliumNetworkPolicy", "tenant-root", "allow-external-secrets-to-openbao")
}

func testOpenBaoOpenTofuBootstrap(t *testing.T) {
	const versionsPath = "src/infrastructure/bootstrap/guardian-mgmt-openbao/versions.tf"
	versionsBytes := readRunfile(t, versionsPath)
	versions := string(versionsBytes)
	mainTF := string(readRunfile(t, "src/infrastructure/bootstrap/guardian-mgmt-openbao/main.tf"))
	lock := string(readRunfile(t, "src/infrastructure/bootstrap/guardian-mgmt-openbao/.terraform.lock.hcl"))

	assertTextContains(t, versions, `source  = "hashicorp/vault"`, "guardian-mgmt-openbao versions.tf")
	assertTextContains(t, versions, `version = "= 4.4.0"`, "guardian-mgmt-openbao versions.tf")
	assertPartialS3Backend(t, versionsBytes, versionsPath, "opentofu/guardian-mgmt-openbao.tfstate", "guardian-mgmt-openbao backend.s3")
	backendConfig := string(readRunfile(t, "src/infrastructure/bootstrap/backend.tfvars"))
	assertTextContains(t, backendConfig, `cloudflare_account_id = "c3eaeffaadf7d4847684d4775c16d598"`, "backend.tfvars")
	assertTextContains(t, lock, `provider "registry.opentofu.org/hashicorp/vault"`, "guardian-mgmt-openbao lock")
	assertTextContains(t, lock, `version     = "4.4.0"`, "guardian-mgmt-openbao lock")

	assertTextContains(t, mainTF, `resource "vault_mount" "kv"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `type        = "kv-v2"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `resource "vault_auth_backend" "kubernetes"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `type        = "kubernetes"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `resource "vault_kubernetes_auth_backend_config" "guardian_mgmt"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `kubernetes_host        = "https://kubernetes.default.svc:443"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `disable_iss_validation = true`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `external_dns_secret   = "guardian/guardian-mgmt/tenant-root/dns/external-dns"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `resource "vault_policy" "external_dns"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `name = "tenant-root-external-dns"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `path "${local.kv_mount}/data/${local.external_dns_secret}"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `resource "vault_kubernetes_auth_backend_role" "external_dns"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `role_name                        = "tenant-root-external-dns"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `bound_service_account_names      = ["external-dns-secrets"]`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `bound_service_account_namespaces = ["external-dns"]`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `audience                         = "openbao"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `token_policies                   = [vault_policy.external_dns.name]`, "guardian-mgmt-openbao main.tf")
	assertTextNotContains(t, mainTF, `resource "vault_policy" "secret_projection"`, "guardian-mgmt-openbao main.tf")
	assertTextNotContains(t, mainTF, `resource "vault_kubernetes_auth_backend_role" "secret_projection"`, "guardian-mgmt-openbao main.tf")
	assertTextNotContains(t, mainTF, `auth/token/lookup-self`, "guardian-mgmt-openbao main.tf")
	assertTextNotContains(t, mainTF, "vault_kv_secret", "guardian-mgmt-openbao main.tf")
	assertTextNotContains(t, mainTF, "vault_generic_secret", "guardian-mgmt-openbao main.tf")

	for _, forbidden := range []string{
		"secret_projection",
		"vault_kv_secret",
		"vault_generic_secret",
	} {
		assertTextNotContains(t, mainTF, forbidden, "guardian-mgmt-openbao main.tf")
	}
}

func testFluxHandoff(t *testing.T) {
	docs := readManifests(t, "src/infrastructure/base/flux/sync.yaml")

	repo := findObject(t, docs, "GitRepository", "cozy-fluxcd", "guardian")
	assertString(t, repo, "https://github.com/guardian-intelligence/guardian", "spec", "url")
	assertString(t, repo, "main", "spec", "ref", "branch")

	base := findObject(t, docs, "Kustomization", "cozy-fluxcd", "guardian-mgmt-base")
	assertString(t, base, "./src/infrastructure/base", "spec", "path")
	assertString(t, base, "GitRepository", "spec", "sourceRef", "kind")
	assertString(t, base, "guardian", "spec", "sourceRef", "name")
	assertBool(t, base, true, "spec", "prune")
	assertBool(t, base, false, "spec", "wait")

	platform := findObject(t, docs, "Kustomization", "cozy-fluxcd", "guardian-mgmt-platform")
	assertString(t, platform, "./src/infrastructure/base/cozystack", "spec", "path")
	assertString(t, platform, "GitRepository", "spec", "sourceRef", "kind")
	assertString(t, platform, "guardian", "spec", "sourceRef", "name")
	assertBool(t, platform, false, "spec", "prune")
	assertBool(t, platform, false, "spec", "wait")

	platformPatches := findObject(t, docs, "Kustomization", "cozy-fluxcd", "guardian-mgmt-platform-patches")
	assertString(t, platformPatches, "./src/infrastructure/base/platform-patches", "spec", "path")
	assertString(t, platformPatches, "GitRepository", "spec", "sourceRef", "kind")
	assertString(t, platformPatches, "guardian", "spec", "sourceRef", "name")
	assertBool(t, platformPatches, false, "spec", "prune")
	assertBool(t, platformPatches, false, "spec", "wait")

	storage := findObject(t, docs, "Kustomization", "cozy-fluxcd", "guardian-mgmt-storage")
	assertString(t, storage, "./src/infrastructure/base/storage", "spec", "path")
	assertString(t, storage, "GitRepository", "spec", "sourceRef", "kind")
	assertString(t, storage, "guardian", "spec", "sourceRef", "name")
	assertBool(t, storage, false, "spec", "prune")
	assertBool(t, storage, false, "spec", "wait")

	storageDeps := sliceAt(t, storage, "spec", "dependsOn")
	if len(storageDeps) != 1 || stringAt(asManifest(t, storageDeps[0], "storage spec.dependsOn[0]"), "name") != "guardian-mgmt-platform" {
		t.Fatalf("guardian-mgmt-storage dependsOn = %#v, want only guardian-mgmt-platform", storageDeps)
	}

	platformPatchDeps := sliceAt(t, platformPatches, "spec", "dependsOn")
	if len(platformPatchDeps) != 1 || stringAt(asManifest(t, platformPatchDeps[0], "platform patches spec.dependsOn[0]"), "name") != "guardian-mgmt-platform" {
		t.Fatalf("guardian-mgmt-platform-patches dependsOn = %#v, want only guardian-mgmt-platform", platformPatchDeps)
	}

	baseDeps := sliceAt(t, base, "spec", "dependsOn")
	if len(baseDeps) != 2 ||
		stringAt(asManifest(t, baseDeps[0], "base spec.dependsOn[0]"), "name") != "guardian-mgmt-platform" ||
		stringAt(asManifest(t, baseDeps[1], "base spec.dependsOn[1]"), "name") != "guardian-mgmt-storage" {
		t.Fatalf("guardian-mgmt-base dependsOn = %#v, want guardian-mgmt-platform then guardian-mgmt-storage", baseDeps)
	}

	dnsController := findObject(t, docs, "Kustomization", "cozy-fluxcd", "guardian-mgmt-dns-controller")
	assertString(t, dnsController, "./src/infrastructure/base/dns", "spec", "path")
	assertString(t, dnsController, "GitRepository", "spec", "sourceRef", "kind")
	assertString(t, dnsController, "guardian", "spec", "sourceRef", "name")
	assertBool(t, dnsController, true, "spec", "prune")
	assertBool(t, dnsController, true, "spec", "wait")
	dnsControllerDeps := sliceAt(t, dnsController, "spec", "dependsOn")
	if len(dnsControllerDeps) != 1 || stringAt(asManifest(t, dnsControllerDeps[0], "dns controller spec.dependsOn[0]"), "name") != "guardian-mgmt-base" {
		t.Fatalf("guardian-mgmt-dns-controller dependsOn = %#v, want only guardian-mgmt-base", dnsControllerDeps)
	}

	companyProd := findObject(t, docs, "Kustomization", "cozy-fluxcd", "guardian-company-prod")
	assertString(t, companyProd, "./src/infrastructure/deployments/company/prod", "spec", "path")
	assertString(t, companyProd, "GitRepository", "spec", "sourceRef", "kind")
	assertString(t, companyProd, "guardian", "spec", "sourceRef", "name")
	assertBool(t, companyProd, true, "spec", "prune")
	assertBool(t, companyProd, true, "spec", "wait")
	companyProdDeps := sliceAt(t, companyProd, "spec", "dependsOn")
	if len(companyProdDeps) != 1 || stringAt(asManifest(t, companyProdDeps[0], "company prod spec.dependsOn[0]"), "name") != "guardian-mgmt-base" {
		t.Fatalf("guardian-company-prod dependsOn = %#v, want only guardian-mgmt-base", companyProdDeps)
	}
}

func testCompanySiteProdDeployment(t *testing.T) {
	docs := readManifests(t, "src/infrastructure/deployments/company/prod/web.yaml")

	deployment := findObject(t, docs, "Deployment", "tenant-prod", "company-site")
	assertInt(t, deployment, 3, "spec", "replicas")
	assertInt(t, deployment, 3, "spec", "revisionHistoryLimit")
	assertString(t, deployment, "RollingUpdate", "spec", "strategy", "type")
	assertInt(t, deployment, 0, "spec", "strategy", "rollingUpdate", "maxUnavailable")
	assertInt(t, deployment, 1, "spec", "strategy", "rollingUpdate", "maxSurge")
	assertString(t, deployment, "company-site", "metadata", "labels", "app.kubernetes.io/name")
	assertString(t, deployment, "company", "metadata", "labels", "guardian.dev/product")
	assertString(t, deployment, "prod", "metadata", "labels", "guardian.dev/stage")
	assertBool(t, deployment, false, "spec", "template", "spec", "automountServiceAccountToken")

	containers := sliceAt(t, deployment, "spec", "template", "spec", "containers")
	if len(containers) != 1 {
		t.Fatalf("company-site containers = %d, want 1", len(containers))
	}
	container := asManifest(t, containers[0], "company-site container")
	assertString(t, container, "web", "name")
	assertString(t, container, "harbor.guardianintelligence.org/guardian/company-site@sha256:d23f3f74fb61c45721367af4906df95d09f493a13b4fe1e3fc9cf5f9e06843bf", "image")
	assertString(t, container, "IfNotPresent", "imagePullPolicy")
	assertString(t, container, "50m", "resources", "requests", "cpu")
	assertString(t, container, "128Mi", "resources", "requests", "memory")
	assertString(t, container, "500m", "resources", "limits", "cpu")
	assertString(t, container, "512Mi", "resources", "limits", "memory")
	assertString(t, container, "/healthz", "readinessProbe", "httpGet", "path")
	assertString(t, container, "http", "readinessProbe", "httpGet", "port")
	assertString(t, container, "/livez", "livenessProbe", "httpGet", "path")
	assertString(t, container, "http", "livenessProbe", "httpGet", "port")

	service := findObject(t, docs, "Service", "tenant-prod", "company-site")
	assertString(t, service, "ClusterIP", "spec", "type")
	assertString(t, service, "Cluster", "spec", "internalTrafficPolicy")
	assertString(t, service, "company-site", "spec", "selector", "app.kubernetes.io/name")
	assertString(t, service, "prod", "spec", "selector", "guardian.dev/stage")
	ports := sliceAt(t, service, "spec", "ports")
	if len(ports) != 1 {
		t.Fatalf("company-site service ports = %d, want 1", len(ports))
	}
	port := asManifest(t, ports[0], "company-site service port")
	assertString(t, port, "http", "name")
	assertInt(t, port, 80, "port")
	assertString(t, port, "http", "targetPort")
	assertString(t, port, "TCP", "protocol")

	pdb := findObject(t, docs, "PodDisruptionBudget", "tenant-prod", "company-site")
	assertInt(t, pdb, 2, "spec", "minAvailable")
	assertString(t, pdb, "company-site", "spec", "selector", "matchLabels", "app.kubernetes.io/name")
	assertString(t, pdb, "prod", "spec", "selector", "matchLabels", "guardian.dev/stage")

	ingress := findObject(t, docs, "Ingress", "tenant-prod", "company-site")
	assertString(t, ingress, "tenant-root", "spec", "ingressClassName")
	rules := sliceAt(t, ingress, "spec", "rules")
	if len(rules) != 1 {
		t.Fatalf("company-site ingress rules = %d, want 1", len(rules))
	}
	rule := asManifest(t, rules[0], "company-site ingress rule")
	assertString(t, rule, "guardianintelligence.org", "host")
	paths := sliceAt(t, rule, "http", "paths")
	if len(paths) != 1 {
		t.Fatalf("company-site ingress paths = %d, want 1", len(paths))
	}
	path := asManifest(t, paths[0], "company-site ingress path")
	assertString(t, path, "/", "path")
	assertString(t, path, "Prefix", "pathType")
	assertString(t, path, "company-site", "backend", "service", "name")
	assertInt(t, path, 80, "backend", "service", "port", "number")
}

type appExpectation struct {
	kind               string
	namespace          string
	host               string
	storageClass       string
	topReplicas        int
	nestedReplicas     map[string]int
	noExternalDB       bool
	postgresVersion    string
	backupPlanName     string
	backupPlanSchedule string
}

func assertApp(t *testing.T, docs []manifest, want appExpectation) {
	t.Helper()

	app := findObject(t, docs, want.kind, want.namespace, "guardian")
	assertString(t, app, "apps.cozystack.io/v1alpha1", "apiVersion")
	assertString(t, app, want.storageClass, "spec", "storageClass")

	if want.host != "" {
		assertString(t, app, want.host, "spec", "host")
	}
	if want.topReplicas != 0 {
		assertInt(t, app, want.topReplicas, "spec", "replicas")
	}
	for field, replicas := range want.nestedReplicas {
		assertInt(t, app, replicas, "spec", field, "replicas")
	}
	if want.noExternalDB {
		assertBool(t, app, false, "spec", "external")
	}
	if want.postgresVersion != "" {
		assertString(t, app, want.postgresVersion, "spec", "version")
	}
	if want.backupPlanName != "" {
		assertBool(t, app, true, "spec", "backup", "enabled")
		assertString(t, app, "", "spec", "backup", "schedule")
		assertBool(t, app, true, "spec", "backup", "useSystemBucket")
		if valueAt(app, "spec", "backup", "s3CredentialsSecret") != nil {
			t.Fatalf("%s/%s must not set legacy backup s3CredentialsSecret", want.namespace, want.kind)
		}

		plan := findObject(t, docs, "Plan", want.namespace, want.backupPlanName)
		assertString(t, plan, "backups.cozystack.io/v1alpha1", "apiVersion")
		assertString(t, plan, "apps.cozystack.io", "spec", "applicationRef", "apiGroup")
		assertString(t, plan, want.kind, "spec", "applicationRef", "kind")
		assertString(t, plan, "guardian", "spec", "applicationRef", "name")
		assertString(t, plan, "cozy-default", "spec", "backupClassName")
		assertString(t, plan, "cron", "spec", "schedule", "type")
		assertString(t, plan, want.backupPlanSchedule, "spec", "schedule", "cron")
	}
}

func guardianMgmtTopologyFixture(t *testing.T) guardianMgmtTopology {
	t.Helper()

	const mainTF = "src/infrastructure/bootstrap/guardian-mgmt/main.tf"
	file, diags := hclsyntax.ParseConfig(readRunfile(t, mainTF), mainTF, hcl.InitialPos)
	assertHCLDiags(t, diags, mainTF)
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		t.Fatalf("%s parsed body = %T, want *hclsyntax.Body", mainTF, file.Body)
	}

	var locals *hclsyntax.Block
	for _, block := range body.Blocks {
		if block.Type != "locals" {
			continue
		}
		if locals != nil {
			t.Fatalf("%s has more than one locals block", mainTF)
		}
		locals = block
	}
	if locals == nil {
		t.Fatalf("%s is missing locals block", mainTF)
	}

	attrs := locals.Body.Attributes
	vlan := evalObjectExpr(t, hclAttr(t, attrs, "vlan", "locals").Expr, "local.vlan")
	nodes := objectExpr(t, hclAttr(t, attrs, "control_plane_nodes", "locals").Expr, "local.control_plane_nodes")

	var topology guardianMgmtTopology
	topology.Cluster = "guardian-mgmt"
	topology.Network.VLAN.ID = ctyStringField(t, vlan, "id", "local.vlan")
	topology.Network.VLAN.VID = ctyIntField(t, vlan, "vid", "local.vlan")
	topology.Network.VLAN.Description = ctyStringField(t, vlan, "description", "local.vlan")
	topology.Network.VLAN.Subnet = ctyStringField(t, vlan, "subnet", "local.vlan")
	topology.Network.VLAN.VLANMTU = ctyIntField(t, vlan, "vlan_mtu", "local.vlan")
	topology.Network.VLAN.PodMTU = ctyIntField(t, vlan, "pod_mtu", "local.vlan")
	topology.Network.VLAN.APIVIP = ctyStringField(t, vlan, "api_vip", "local.vlan")
	topology.Network.VLAN.VIPLink = ctyStringField(t, vlan, "vip_link", "local.vlan")
	topology.Network.VLAN.MetalLBPool = ctyStringField(t, vlan, "metallb_pool", "local.vlan")

	for _, item := range nodes.Items {
		key := ctyString(t, evalHCLExpr(t, item.KeyExpr, "local.control_plane_nodes key"), "local.control_plane_nodes key")
		nodeFields := evalObjectExpr(t, item.ValueExpr, "local.control_plane_nodes."+key)
		node := guardianMgmtNode{
			Name:        ctyStringField(t, nodeFields, "name", "local.control_plane_nodes."+key),
			ServerID:    ctyStringField(t, nodeFields, "server_id", "local.control_plane_nodes."+key),
			Hostname:    ctyStringField(t, nodeFields, "hostname", "local.control_plane_nodes."+key),
			PublicIPv4:  ctyStringField(t, nodeFields, "public_ipv4", "local.control_plane_nodes."+key),
			PrivateIPv4: ctyStringField(t, nodeFields, "private_ipv4", "local.control_plane_nodes."+key),
		}
		if node.Name != key {
			t.Fatalf("local.control_plane_nodes.%s.name = %q, want it to match the OpenTofu map key", key, node.Name)
		}
		topology.Nodes = append(topology.Nodes, node)
	}
	return topology
}

func assertHCLDiags(t *testing.T, diags hcl.Diagnostics, label string) {
	t.Helper()
	if diags.HasErrors() {
		t.Fatalf("%s HCL diagnostics: %s", label, diags.Error())
	}
}

func hclAttr(t *testing.T, attrs hclsyntax.Attributes, name, label string) *hclsyntax.Attribute {
	t.Helper()
	attr, ok := attrs[name]
	if !ok {
		t.Fatalf("%s is missing %q", label, name)
	}
	return attr
}

func hclBackendAttrs(t *testing.T, source []byte, path, backendType string) hclsyntax.Attributes {
	t.Helper()

	file, diags := hclsyntax.ParseConfig(source, path, hcl.InitialPos)
	assertHCLDiags(t, diags, path)
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		t.Fatalf("%s parsed body = %T, want *hclsyntax.Body", path, file.Body)
	}

	for _, block := range body.Blocks {
		if block.Type != "terraform" {
			continue
		}
		for _, nested := range block.Body.Blocks {
			if nested.Type == "backend" && len(nested.Labels) == 1 && nested.Labels[0] == backendType {
				return nested.Body.Attributes
			}
		}
	}
	t.Fatalf("%s is missing terraform backend %q", path, backendType)
	return nil
}

func assertPartialS3Backend(t *testing.T, source []byte, path, wantKey, label string) {
	t.Helper()

	attrs := hclBackendAttrs(t, source, path, "s3")
	assertHCLStringAttribute(t, attrs, "bucket", "guardian-vault", label)
	assertHCLStringAttribute(t, attrs, "key", wantKey, label)
	assertHCLStringAttribute(t, attrs, "region", "auto", label)
	if _, ok := attrs["endpoint"]; ok {
		t.Fatalf("%s must not set endpoint in HCL; pass it with -backend-config during init", label)
	}
}

func assertHCLStringAttribute(t *testing.T, attrs hclsyntax.Attributes, name, want, label string) {
	t.Helper()

	got := ctyString(t, evalHCLExpr(t, hclAttr(t, attrs, name, label).Expr, label+"."+name), label+"."+name)
	if got != want {
		t.Fatalf("%s.%s = %q, want %q", label, name, got, want)
	}
}

func hclExpressionSource(source []byte, expr hcl.Expression) string {
	return string(expr.Range().SliceBytes(source))
}

func cloudflareR2Endpoint(t *testing.T) string {
	t.Helper()

	const path = "src/infrastructure/bootstrap/backend.tfvars"
	file, diags := hclsyntax.ParseConfig(readRunfile(t, path), path, hcl.InitialPos)
	assertHCLDiags(t, diags, path)
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		t.Fatalf("%s parsed body = %T, want *hclsyntax.Body", path, file.Body)
	}

	accountID := ctyString(t, evalHCLExpr(t, hclAttr(t, body.Attributes, "cloudflare_account_id", path).Expr, path+".cloudflare_account_id"), path+".cloudflare_account_id")
	return "https://" + accountID + ".r2.cloudflarestorage.com"
}

func objectExpr(t *testing.T, expr hcl.Expression, label string) *hclsyntax.ObjectConsExpr {
	t.Helper()
	obj, ok := expr.(*hclsyntax.ObjectConsExpr)
	if !ok {
		t.Fatalf("%s = %T, want HCL object constructor", label, expr)
	}
	return obj
}

func evalObjectExpr(t *testing.T, expr hcl.Expression, label string) map[string]cty.Value {
	t.Helper()
	value := evalHCLExpr(t, expr, label)
	if !value.CanIterateElements() {
		t.Fatalf("%s = %s, want object value", label, value.GoString())
	}
	return value.AsValueMap()
}

func evalHCLExpr(t *testing.T, expr hcl.Expression, label string) cty.Value {
	t.Helper()
	value, diags := expr.Value(nil)
	assertHCLDiags(t, diags, label)
	return value
}

func ctyStringField(t *testing.T, values map[string]cty.Value, field, label string) string {
	t.Helper()
	value, ok := values[field]
	if !ok {
		t.Fatalf("%s is missing %q", label, field)
	}
	return ctyString(t, value, label+"."+field)
}

func ctyString(t *testing.T, value cty.Value, label string) string {
	t.Helper()
	if value.Type() != cty.String {
		t.Fatalf("%s = %s, want string", label, value.GoString())
	}
	return value.AsString()
}

func ctyIntField(t *testing.T, values map[string]cty.Value, field, label string) int {
	t.Helper()
	value, ok := values[field]
	if !ok {
		t.Fatalf("%s is missing %q", label, field)
	}
	if value.Type() != cty.Number {
		t.Fatalf("%s.%s = %s, want number", label, field, value.GoString())
	}
	got, accuracy := value.AsBigFloat().Int64()
	if accuracy != big.Exact {
		t.Fatalf("%s.%s = %s, want exact integer", label, field, value.GoString())
	}
	return int(got)
}

func topologyNodePublicIP(t *testing.T, topology guardianMgmtTopology, name string) string {
	t.Helper()
	for _, node := range topology.Nodes {
		if node.Name == name {
			return node.PublicIPv4
		}
	}
	t.Fatalf("topology node %q not found", name)
	return ""
}

func topologyPublicIPs(t *testing.T, topology guardianMgmtTopology) []string {
	t.Helper()
	out := make([]string, 0, len(topology.Nodes))
	for _, node := range topology.Nodes {
		out = append(out, node.PublicIPv4)
	}
	return out
}

func assertUniqueTopologyValues(t *testing.T, topology guardianMgmtTopology) {
	t.Helper()

	seenNames := map[string]bool{}
	seenServerIDs := map[string]bool{}
	seenPublicIPs := map[string]bool{}
	seenPrivateIPs := map[string]bool{}
	for _, node := range topology.Nodes {
		assertUniqueValue(t, seenNames, "node name", node.Name)
		assertUniqueValue(t, seenServerIDs, "server ID", node.ServerID)
		assertUniqueValue(t, seenPublicIPs, "public IPv4", node.PublicIPv4)
		assertUniqueValue(t, seenPrivateIPs, "private IPv4", node.PrivateIPv4)
	}
}

func assertUniqueValue(t *testing.T, seen map[string]bool, label, value string) {
	t.Helper()

	if value == "" {
		t.Fatalf("%s is empty", label)
	}
	if seen[value] {
		t.Fatalf("duplicate %s %q", label, value)
	}
	seen[value] = true
}

func readYAMLMap(t *testing.T, rel string) manifest {
	t.Helper()

	var doc manifest
	if err := yaml.Unmarshal(readRunfile(t, rel), &doc); err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	return doc
}

func readManifests(t *testing.T, rel string) []manifest {
	t.Helper()

	data := readRunfile(t, rel)

	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs []manifest
	for {
		var doc manifest
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		if len(doc) == 0 {
			continue
		}
		docs = append(docs, doc)
	}
	return docs
}

func readRunfile(t *testing.T, rel string) []byte {
	t.Helper()

	path := runfilePath(rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func assertRunfileContent(t *testing.T, rel, want string) {
	t.Helper()
	if got := string(readRunfile(t, rel)); got != want {
		t.Fatalf("%s = %q, want %q", rel, got, want)
	}
}

func runfilePath(rel string) string {
	if testSrcdir, workspace := os.Getenv("TEST_SRCDIR"), os.Getenv("TEST_WORKSPACE"); testSrcdir != "" && workspace != "" {
		return filepath.Join(testSrcdir, workspace, rel)
	}
	return rel
}

func findObject(t *testing.T, docs []manifest, kind, namespace, name string) manifest {
	t.Helper()

	matches := findObjects(t, docs, kind, namespace, name)
	if len(matches) != 1 {
		t.Fatalf("expected one %s %s/%s, got %d", kind, namespace, name, len(matches))
	}
	return matches[0]
}

func assertNoObject(t *testing.T, docs []manifest, kind, namespace, name string) {
	t.Helper()

	matches := findObjects(t, docs, kind, namespace, name)
	if len(matches) != 0 {
		t.Fatalf("expected no %s %s/%s, got %d", kind, namespace, name, len(matches))
	}
}

func findObjects(t *testing.T, docs []manifest, kind, namespace, name string) []manifest {
	t.Helper()

	var matches []manifest
	for _, doc := range docs {
		if stringAt(doc, "kind") == kind &&
			stringAt(doc, "metadata", "namespace") == namespace &&
			stringAt(doc, "metadata", "name") == name {
			matches = append(matches, doc)
		}
	}
	return matches
}

func assertNoKind(t *testing.T, docs []manifest, kind string) {
	t.Helper()

	for _, doc := range docs {
		if stringAt(doc, "kind") == kind {
			t.Fatalf("found unexpected %s %s/%s", kind, stringAt(doc, "metadata", "namespace"), stringAt(doc, "metadata", "name"))
		}
	}
}

func assertString(t *testing.T, doc manifest, want string, path ...string) {
	t.Helper()
	if got := stringAt(doc, path...); got != want {
		t.Fatalf("%s = %q, want %q", dotPath(path), got, want)
	}
}

func assertInt(t *testing.T, doc manifest, want int, path ...string) {
	t.Helper()
	got, ok := valueAt(doc, path...).(int)
	if !ok {
		t.Fatalf("%s = %T(%#v), want int %d", dotPath(path), valueAt(doc, path...), valueAt(doc, path...), want)
	}
	if got != want {
		t.Fatalf("%s = %d, want %d", dotPath(path), got, want)
	}
}

func assertBool(t *testing.T, doc manifest, want bool, path ...string) {
	t.Helper()
	got, ok := valueAt(doc, path...).(bool)
	if !ok {
		t.Fatalf("%s = %T(%#v), want bool %v", dotPath(path), valueAt(doc, path...), valueAt(doc, path...), want)
	}
	if got != want {
		t.Fatalf("%s = %v, want %v", dotPath(path), got, want)
	}
}

func assertStringSlice(t *testing.T, doc manifest, want []string, path ...string) {
	t.Helper()

	gotValues := sliceAt(t, doc, path...)
	if len(gotValues) != len(want) {
		t.Fatalf("%s = %#v, want %#v", dotPath(path), gotValues, want)
	}
	for i, value := range gotValues {
		got, ok := value.(string)
		if !ok {
			t.Fatalf("%s[%d] = %T(%#v), want string %q", dotPath(path), i, value, value, want[i])
		}
		if got != want[i] {
			t.Fatalf("%s[%d] = %q, want %q", dotPath(path), i, got, want[i])
		}
	}
}

func assertEnvValue(t *testing.T, env []any, name, want string) {
	t.Helper()

	entry := findEnv(t, env, name)
	assertString(t, entry, want, "value")
}

func assertEnvSecretRef(t *testing.T, env []any, name, secretName, key string) {
	t.Helper()

	entry := findEnv(t, env, name)
	assertString(t, entry, secretName, "valueFrom", "secretKeyRef", "name")
	assertString(t, entry, key, "valueFrom", "secretKeyRef", "key")
}

func findEnv(t *testing.T, env []any, name string) manifest {
	t.Helper()

	for _, value := range env {
		entry := asManifest(t, value, "env[]")
		if stringAt(entry, "name") == name {
			return entry
		}
	}
	t.Fatalf("env is missing %q", name)
	return nil
}

func assertContainsString(t *testing.T, values []any, want, label string) {
	t.Helper()

	if containsString(values, want) {
		return
	}
	t.Fatalf("%s = %#v, want it to contain %q", label, values, want)
}

func containsString(values []any, want string) bool {
	for _, value := range values {
		if got, ok := value.(string); ok && got == want {
			return true
		}
	}
	return false
}

func assertTextContains(t *testing.T, text, needle, label string) {
	t.Helper()

	if !strings.Contains(text, needle) {
		t.Fatalf("%s does not contain %q", label, needle)
	}
}

func assertTextNotContains(t *testing.T, text, needle, label string) {
	t.Helper()

	if strings.Contains(text, needle) {
		t.Fatalf("%s contains %q", label, needle)
	}
}

func assertConcreteBackupCoordinate(t *testing.T, value, label, prefix string) {
	t.Helper()

	if value == "" {
		t.Fatalf("Postgres backup %s is empty", label)
	}
	if !strings.HasPrefix(value, prefix) {
		t.Fatalf("Postgres backup %s = %q, want prefix %q", label, value, prefix)
	}
	assertNoTemplateOrPlaceholder(t, value, "Postgres backup "+label)
}

func assertConcreteBackupSchedule(t *testing.T, value string) {
	t.Helper()

	if value == "" {
		t.Fatalf("Postgres backup Plan schedule.cron is empty")
	}
	assertNoTemplateOrPlaceholder(t, value, "Postgres backup Plan schedule.cron")
}

func assertNoTemplateOrPlaceholder(t *testing.T, value, label string) {
	t.Helper()

	for _, bad := range []string{"{{", "}}", "TODO", "todo", "placeholder", "example", "DELETE_ME"} {
		if strings.Contains(value, bad) {
			t.Fatalf("%s = %q contains non-production marker %q", label, value, bad)
		}
	}
}

func assertServicePort(t *testing.T, service manifest, name string, port int) {
	t.Helper()

	for _, value := range sliceAt(t, service, "spec", "ports") {
		entry := asManifest(t, value, "spec.ports[]")
		if stringAt(entry, "name") == name {
			assertInt(t, entry, port, "port")
			assertInt(t, entry, port, "targetPort")
			assertString(t, entry, "TCP", "protocol")
			return
		}
	}
	t.Fatalf("service %s/%s is missing port %q", stringAt(service, "metadata", "namespace"), stringAt(service, "metadata", "name"), name)
}

func stringAt(doc manifest, path ...string) string {
	got, _ := valueAt(doc, path...).(string)
	return got
}

func sliceAt(t *testing.T, doc manifest, path ...string) []any {
	t.Helper()
	got, ok := valueAt(doc, path...).([]any)
	if !ok {
		t.Fatalf("%s = %T(%#v), want sequence", dotPath(path), valueAt(doc, path...), valueAt(doc, path...))
	}
	return got
}

func valueAt(doc manifest, path ...string) any {
	var cur any = doc
	for _, field := range path {
		next, ok := asMap(cur)[field]
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

func asManifest(t *testing.T, value any, label string) manifest {
	t.Helper()
	m := asMap(value)
	if m == nil {
		t.Fatalf("%s = %T(%#v), want mapping", label, value, value)
	}
	return m
}

func asMap(value any) manifest {
	switch typed := value.(type) {
	case manifest:
		return typed
	case map[string]any:
		return manifest(typed)
	default:
		return nil
	}
}

func dotPath(path []string) string {
	if len(path) == 0 {
		return "<root>"
	}
	out := path[0]
	for _, elem := range path[1:] {
		out += "." + elem
	}
	return out
}
