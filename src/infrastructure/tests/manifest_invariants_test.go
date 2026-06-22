package tests

import (
	"bytes"
	"encoding/json"
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
	t.Run("cozystack platform package", testCozystackPlatformPackage)
	t.Run("environment tenants", testEnvironmentTenants)
	t.Run("layer two networking", testLayerTwoNetworking)
	t.Run("single default storage class", testSingleDefaultStorageClass)
	t.Run("backup classes", testBackupClasses)
	t.Run("postgres backup activation guard", testPostgresBackupActivationGuard)
	t.Run("root tenant core services", testRootTenantCoreServices)
	t.Run("environment tenant core services", testEnvironmentTenantCoreServices)
	t.Run("company site", testCompanySite)
	t.Run("company site source ownership", testCompanySiteSourceOwnership)
	t.Run("openbao", testOpenBao)
	t.Run("openbao opentofu bootstrap", testOpenBaoOpenTofuBootstrap)
	t.Run("openbao cnpg backup secret projection", testOpenBaoCNPGBackupSecretProjection)
	t.Run("openbao clickhouse backup secret projection", testOpenBaoClickHouseBackupSecretProjection)
	t.Run("flux handoff", testFluxHandoff)
}

func testCozystackPlatformPackage(t *testing.T) {
	topology := guardianMgmtTopologyFixture(t)
	docs := readManifests(t, "src/infrastructure/base/cozystack/platform.yaml")
	pkg := findObject(t, docs, "Package", "", "cozystack.cozystack-platform")

	assertString(t, pkg, "cozystack.io/v1alpha1", "apiVersion")
	assertString(t, pkg, "isp-full", "spec", "variant")
	assertStringSlice(t, pkg, []string{"cozystack.external-secrets-operator", "cozystack.velero"}, "spec", "components", "platform", "values", "bundles", "enabledPackages")
	assertString(t, pkg, "guardianintelligence.org", "spec", "components", "platform", "values", "publishing", "host")
	assertString(t, pkg, fmt.Sprintf("https://%s:6443", topology.Network.VLAN.APIVIP), "spec", "components", "platform", "values", "publishing", "apiServerEndpoint")
	assertStringSlice(t, pkg, topologyPublicIPs(topology), "spec", "components", "platform", "values", "publishing", "externalIPs")
	assertStringSlice(t, pkg, []string{"dashboard", "api"}, "spec", "components", "platform", "values", "publishing", "exposedServices")

	assertString(t, pkg, "10.244.0.0/16", "spec", "components", "platform", "values", "networking", "podCIDR")
	assertString(t, pkg, "10.244.0.1", "spec", "components", "platform", "values", "networking", "podGateway")
	assertString(t, pkg, "10.96.0.0/16", "spec", "components", "platform", "values", "networking", "serviceCIDR")
	assertString(t, pkg, "100.64.0.0/16", "spec", "components", "platform", "values", "networking", "joinCIDR")

	assertBool(t, pkg, true, "spec", "components", "platform", "values", "authentication", "oidc", "enabled")
	assertString(t, pkg, "http://keycloak-http.cozy-keycloak.svc:8080/realms/cozy", "spec", "components", "platform", "values", "authentication", "oidc", "keycloakInternalUrl")
	assertString(t, pkg, "Guardian", "spec", "components", "platform", "values", "branding", "titleText")
	assertString(t, pkg, "Guardian Intelligence", "spec", "components", "platform", "values", "branding", "footerText")
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

func testEnvironmentTenants(t *testing.T) {
	docs := readManifests(t, "src/infrastructure/base/tenants/environments.yaml")

	for _, env := range []string{"dev", "gamma", "prod"} {
		t.Run(env, func(t *testing.T) {
			tenant := findObject(t, docs, "Tenant", "tenant-root", env)
			assertString(t, tenant, "apps.cozystack.io/v1alpha1", "apiVersion")
			assertString(t, tenant, env+".gi.org", "spec", "host")
			assertBool(t, tenant, false, "spec", "etcd")
			assertBool(t, tenant, false, "spec", "ingress")
			assertBool(t, tenant, false, "spec", "monitoring")
			assertBool(t, tenant, false, "spec", "seaweedfs")
		})
	}
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

func testBackupClasses(t *testing.T) {
	postgresDocs := readManifests(t, "src/infrastructure/base/backup/postgres-cnpg.yaml")

	strategy := findObject(t, postgresDocs, "CNPG", "", "guardian-postgres-r2")
	assertString(t, strategy, "strategy.backups.cozystack.io/v1alpha1", "apiVersion")
	assertString(t, strategy, "{{ .Application.metadata.namespace }}-{{ .Application.metadata.name }}", "spec", "template", "serverName")
	assertString(t, strategy, "{{ .Application.spec.backup.destinationPath }}", "spec", "template", "barmanObjectStore", "destinationPath")
	assertString(t, strategy, "{{ .Application.spec.backup.endpointURL }}", "spec", "template", "barmanObjectStore", "endpointURL")
	assertString(t, strategy, "30d", "spec", "template", "barmanObjectStore", "retentionPolicy")
	assertString(t, strategy, "{{ .Application.metadata.name }}-cnpg-backup-creds", "spec", "template", "barmanObjectStore", "s3Credentials", "secretRef", "name")
	assertString(t, strategy, "gzip", "spec", "template", "barmanObjectStore", "data", "compression")
	assertString(t, strategy, "gzip", "spec", "template", "barmanObjectStore", "wal", "compression")

	class := findObject(t, postgresDocs, "BackupClass", "", "guardian-postgres-cnpg")
	assertString(t, class, "backups.cozystack.io/v1alpha1", "apiVersion")
	strategies := sliceAt(t, class, "spec", "strategies")
	if len(strategies) != 1 {
		t.Fatalf("spec.strategies has %d entries, want 1", len(strategies))
	}
	classStrategy := asManifest(t, strategies[0], "spec.strategies[0]")
	assertString(t, classStrategy, "apps.cozystack.io", "application", "apiGroup")
	assertString(t, classStrategy, "Postgres", "application", "kind")
	assertString(t, classStrategy, "strategy.backups.cozystack.io", "strategyRef", "apiGroup")
	assertString(t, classStrategy, "CNPG", "strategyRef", "kind")
	assertString(t, classStrategy, "guardian-postgres-r2", "strategyRef", "name")

	clickhouseDocs := readManifests(t, "src/infrastructure/base/backup/clickhouse-altinity.yaml")
	altinity := findObject(t, clickhouseDocs, "Altinity", "", "guardian-clickhouse-altinity")
	assertString(t, altinity, "strategy.backups.cozystack.io/v1alpha1", "apiVersion")
	assertInt(t, altinity, 1800, "spec", "template", "spec", "activeDeadlineSeconds")
	assertString(t, altinity, "Never", "spec", "template", "spec", "restartPolicy")
	containers := sliceAt(t, altinity, "spec", "template", "spec", "containers")
	if len(containers) != 1 {
		t.Fatalf("Altinity strategy containers has %d entries, want 1", len(containers))
	}
	container := asManifest(t, containers[0], "spec.template.spec.containers[0]")
	assertString(t, container, "clickhouse-backup-client", "name")
	assertString(t, container, "docker.io/library/python@sha256:c25cd44f45df1279a2cba589e67dfcd9db04647ea483b117a7de8b1a99bdfb23", "image")
	assertStringSlice(t, container, []string{"python3", "-c"}, "command")
	env := sliceAt(t, container, "env")
	assertEnvValue(t, env, "MODE", "{{ .Mode }}")
	assertEnvValue(t, env, "RELEASE_NAME", "{{ .Release.Name }}")
	assertEnvSecretRef(t, env, "API_USERNAME", "clickhouse-{{ .Release.Name }}-backup-api-auth", "username")
	assertEnvSecretRef(t, env, "API_PASSWORD", "clickhouse-{{ .Release.Name }}-backup-api-auth", "password")
	args := sliceAt(t, container, "args")
	if len(args) != 1 {
		t.Fatalf("Altinity strategy args has %d entries, want 1", len(args))
	}
	script, ok := args[0].(string)
	if !ok {
		t.Fatalf("Altinity strategy args[0] = %T(%#v), want string", args[0], args[0])
	}
	assertTextContains(t, script, "urllib.request", "Altinity strategy script")
	if strings.Contains(script, "apk add") || strings.Contains(script, "curl ") || strings.Contains(script, "jq") {
		t.Fatalf("Altinity strategy script installs or shells to unpinned runtime tools: %s", script)
	}
	assertBool(t, container, false, "securityContext", "allowPrivilegeEscalation")
	assertBool(t, container, true, "securityContext", "readOnlyRootFilesystem")
	assertBool(t, container, true, "securityContext", "runAsNonRoot")
	assertInt(t, container, 65532, "securityContext", "runAsUser")
	assertInt(t, container, 65532, "securityContext", "runAsGroup")
	assertString(t, container, "RuntimeDefault", "securityContext", "seccompProfile", "type")

	clickhouseClass := findObject(t, clickhouseDocs, "BackupClass", "", "guardian-clickhouse-altinity")
	assertString(t, clickhouseClass, "backups.cozystack.io/v1alpha1", "apiVersion")
	clickhouseStrategies := sliceAt(t, clickhouseClass, "spec", "strategies")
	if len(clickhouseStrategies) != 1 {
		t.Fatalf("ClickHouse BackupClass spec.strategies has %d entries, want 1", len(clickhouseStrategies))
	}
	clickhouseClassStrategy := asManifest(t, clickhouseStrategies[0], "spec.strategies[0]")
	assertString(t, clickhouseClassStrategy, "apps.cozystack.io", "application", "apiGroup")
	assertString(t, clickhouseClassStrategy, "ClickHouse", "application", "kind")
	assertString(t, clickhouseClassStrategy, "strategy.backups.cozystack.io", "strategyRef", "apiGroup")
	assertString(t, clickhouseClassStrategy, "Altinity", "strategyRef", "kind")
	assertString(t, clickhouseClassStrategy, "guardian-clickhouse-altinity", "strategyRef", "name")

	for _, docs := range [][]manifest{postgresDocs, clickhouseDocs} {
		assertNoKind(t, docs, "Plan")
		assertNoKind(t, docs, "BackupJob")
	}
}

func testPostgresBackupActivationGuard(t *testing.T) {
	for _, tc := range []struct {
		name      string
		manifest  string
		namespace string
	}{
		{name: "root", manifest: "src/infrastructure/base/apps/core-services.yaml", namespace: "tenant-root"},
		{name: "dev", manifest: "src/infrastructure/environments/dev/core-services.yaml", namespace: "tenant-dev"},
		{name: "gamma", manifest: "src/infrastructure/environments/gamma/core-services.yaml", namespace: "tenant-gamma"},
		{name: "prod", manifest: "src/infrastructure/environments/prod/core-services.yaml", namespace: "tenant-prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := readManifests(t, tc.manifest)
			app := findObject(t, docs, "Postgres", tc.namespace, "guardian")
			plans := findObjects(t, docs, "Plan", tc.namespace, "guardian-postgres-daily")
			backupValue := valueAt(app, "spec", "backup")
			if backupValue == nil {
				if len(plans) != 0 {
					t.Fatalf("found guardian-postgres-daily Plan without spec.backup on Postgres tenant %s", tc.namespace)
				}
				return
			}

			backup := asManifest(t, backupValue, "Postgres spec.backup")
			assertBool(t, backup, true, "enabled")
			destinationPath := stringAt(backup, "destinationPath")
			endpointURL := stringAt(backup, "endpointURL")
			assertConcreteBackupCoordinate(t, destinationPath, "destinationPath", "s3://")
			assertConcreteBackupCoordinate(t, endpointURL, "endpointURL", "https://")

			if len(plans) != 1 {
				t.Fatalf("found %d guardian-postgres-daily Plans, want exactly 1 when Postgres backup is enabled", len(plans))
			}
			plan := plans[0]
			assertString(t, plan, "backups.cozystack.io/v1alpha1", "apiVersion")
			assertString(t, plan, "apps.cozystack.io", "spec", "applicationRef", "apiGroup")
			assertString(t, plan, "Postgres", "spec", "applicationRef", "kind")
			assertString(t, plan, "guardian", "spec", "applicationRef", "name")
			assertString(t, plan, "guardian-postgres-cnpg", "spec", "backupClassName")
			assertString(t, plan, "cron", "spec", "schedule", "type")
			assertConcreteBackupSchedule(t, stringAt(plan, "spec", "schedule", "cron"))
		})
	}
}

func testRootTenantCoreServices(t *testing.T) {
	docs := readManifests(t, "src/infrastructure/base/apps/core-services.yaml")

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
	assertApp(t, docs, appExpectation{
		kind:               "ClickHouse",
		namespace:          "tenant-root",
		storageClass:       "replicated",
		topReplicas:        3,
		backupSecretName:   "guardian-clickhouse-backup-creds",
		backupPlanName:     "guardian-clickhouse-daily",
		backupPlanSchedule: "17 1 * * *",
		nestedReplicas: map[string]int{
			"clickhouseKeeper": 3,
		},
	})
}

func testEnvironmentTenantCoreServices(t *testing.T) {
	for _, env := range []string{"dev", "gamma", "prod"} {
		t.Run(env, func(t *testing.T) {
			docs := readManifests(t, "src/infrastructure/environments/"+env+"/core-services.yaml")
			namespace := "tenant-" + env

			assertApp(t, docs, appExpectation{
				kind:            "Postgres",
				namespace:       namespace,
				storageClass:    "replicated",
				topReplicas:     3,
				noExternalDB:    true,
				postgresVersion: "v18",
			})
			assertApp(t, docs, appExpectation{
				kind:         "Harbor",
				namespace:    namespace,
				host:         "harbor." + env + ".gi.org",
				storageClass: "replicated",
				nestedReplicas: map[string]int{
					"database": 3,
					"redis":    3,
				},
			})
			assertApp(t, docs, appExpectation{
				kind:               "ClickHouse",
				namespace:          namespace,
				storageClass:       "replicated",
				topReplicas:        3,
				backupSecretName:   "guardian-clickhouse-backup-creds",
				backupPlanName:     "guardian-clickhouse-daily",
				backupPlanSchedule: map[string]string{"dev": "23 1 * * *", "gamma": "29 1 * * *", "prod": "41 1 * * *"}[env],
				nestedReplicas: map[string]int{
					"clickhouseKeeper": 3,
				},
			})
		})
	}
}

func testCompanySite(t *testing.T) {
	healthz := readRunfile(t, "src/products/company/web/src/routes/healthz.tsx")
	if !bytes.Contains(healthz, []byte(`GET: () => new Response("ok\n", { status: 200, headers })`)) {
		t.Fatalf("company-site healthz route does not return ok")
	}
	livez := readRunfile(t, "src/products/company/web/src/routes/livez.tsx")
	if !bytes.Contains(livez, []byte(`GET: () => new Response("ok\n", { status: 200, headers })`)) {
		t.Fatalf("company-site livez route does not return ok")
	}

	metrics := readRunfile(t, "src/products/company/web/src/routes/metrics.tsx")
	if !bytes.Contains(metrics, []byte(`company_site_build_info{app="company-site",runtime="tanstack-start"} 1`)) {
		t.Fatalf("company-site metrics endpoint does not expose build info: %q", metrics)
	}

	otelForwarder := string(readRunfile(t, "src/products/company/web/src/routes/api/otel/v1/traces.tsx"))
	assertTextContains(t, otelForwarder, "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "company-site OTLP forwarder")
	assertTextContains(t, otelForwarder, "OTEL_EXPORTER_OTLP_ENDPOINT", "company-site OTLP forwarder")
	assertTextContains(t, otelForwarder, "otel exporter endpoint not configured", "company-site OTLP forwarder")
	assertTextNotContains(t, otelForwarder, "127.0.0.1:4318", "company-site OTLP forwarder")

	landing := readRunfile(t, "src/products/company/web/src/routes/_workshop/index.tsx")
	if !bytes.Contains(landing, []byte("Guardian")) ||
		!bytes.Contains(landing, []byte("FirstLight")) {
		t.Fatalf("company-site landing route does not contain the expected TanStack app")
	}

	image := "harbor.guardianintelligence.org/guardian/company-site@" + readCompanySiteImageDigest(t)

	for _, env := range []struct {
		name      string
		namespace string
		host      string
	}{
		{name: "dev", namespace: "tenant-dev", host: "dev.gi.org"},
		{name: "gamma", namespace: "tenant-gamma", host: "gamma.gi.org"},
		{name: "prod", namespace: "tenant-prod", host: "guardianintelligence.org"},
	} {
		t.Run(env.name, func(t *testing.T) {
			docs := readManifests(t, "src/infrastructure/environments/"+env.name+"/company-site.yaml")

			deploy := findObject(t, docs, "Deployment", env.namespace, "company-site")
			assertString(t, deploy, "apps/v1", "apiVersion")
			assertInt(t, deploy, 3, "spec", "replicas")
			assertString(t, deploy, "RollingUpdate", "spec", "strategy", "type")
			assertInt(t, deploy, 0, "spec", "strategy", "rollingUpdate", "maxUnavailable")
			assertInt(t, deploy, 1, "spec", "strategy", "rollingUpdate", "maxSurge")
			assertString(t, deploy, "company-site", "spec", "selector", "matchLabels", "app.kubernetes.io/name")
			assertString(t, deploy, env.name, "spec", "selector", "matchLabels", "guardian.dev/stage")
			assertString(t, deploy, "RuntimeDefault", "spec", "template", "spec", "securityContext", "seccompProfile", "type")

			spread := sliceAt(t, deploy, "spec", "template", "spec", "topologySpreadConstraints")
			if len(spread) != 1 {
				t.Fatalf("topologySpreadConstraints has %d entries, want 1", len(spread))
			}
			assertString(t, asManifest(t, spread[0], "topologySpreadConstraints[0]"), "kubernetes.io/hostname", "topologyKey")
			assertString(t, asManifest(t, spread[0], "topologySpreadConstraints[0]"), "DoNotSchedule", "whenUnsatisfiable")

			containers := sliceAt(t, deploy, "spec", "template", "spec", "containers")
			if len(containers) != 1 {
				t.Fatalf("containers has %d entries, want 1", len(containers))
			}
			container := asManifest(t, containers[0], "containers[0]")
			assertString(t, container, "company-site", "name")
			assertString(t, container, image, "image")
			assertString(t, container, "IfNotPresent", "imagePullPolicy")
			assertBool(t, container, false, "securityContext", "allowPrivilegeEscalation")
			assertBool(t, container, true, "securityContext", "runAsNonRoot")
			assertInt(t, container, 65532, "securityContext", "runAsUser")
			assertBool(t, container, true, "securityContext", "readOnlyRootFilesystem")
			assertEnvValue(t, sliceAt(t, container, "env"), "TMPDIR", "/tmp")
			volumeMounts := sliceAt(t, container, "volumeMounts")
			if len(volumeMounts) != 1 {
				t.Fatalf("company-site volumeMounts has %d entries, want 1", len(volumeMounts))
			}
			tmpMount := asManifest(t, volumeMounts[0], "volumeMounts[0]")
			assertString(t, tmpMount, "tmp", "name")
			assertString(t, tmpMount, "/tmp", "mountPath")
			assertString(t, container, "/healthz", "readinessProbe", "httpGet", "path")
			assertString(t, container, "/livez", "livenessProbe", "httpGet", "path")

			volumes := sliceAt(t, deploy, "spec", "template", "spec", "volumes")
			if len(volumes) != 1 {
				t.Fatalf("company-site volumes has %d entries, want 1", len(volumes))
			}
			tmpVolume := asManifest(t, volumes[0], "spec.template.spec.volumes[0]")
			assertString(t, tmpVolume, "tmp", "name")
			if valueAt(tmpVolume, "emptyDir") == nil {
				t.Fatalf("company-site tmp volume must use emptyDir")
			}

			service := findObject(t, docs, "Service", env.namespace, "company-site")
			assertString(t, service, "v1", "apiVersion")
			assertString(t, service, "company-site", "spec", "selector", "app.kubernetes.io/name")
			assertString(t, service, env.name, "spec", "selector", "guardian.dev/stage")
			servicePorts := sliceAt(t, service, "spec", "ports")
			if len(servicePorts) != 1 {
				t.Fatalf("service ports has %d entries, want 1", len(servicePorts))
			}
			assertInt(t, asManifest(t, servicePorts[0], "spec.ports[0]"), 80, "port")
			assertString(t, asManifest(t, servicePorts[0], "spec.ports[0]"), "http", "targetPort")

			policy := findObject(t, docs, "NetworkPolicy", env.namespace, "company-site-ingress")
			assertString(t, policy, "networking.k8s.io/v1", "apiVersion")
			assertString(t, policy, "company-site", "spec", "podSelector", "matchLabels", "app.kubernetes.io/name")
			assertString(t, policy, env.name, "spec", "podSelector", "matchLabels", "guardian.dev/stage")
			assertStringSlice(t, policy, []string{"Ingress"}, "spec", "policyTypes")
			if valueAt(policy, "spec", "egress") != nil {
				t.Fatalf("company-site NetworkPolicy must not restrict egress")
			}
			ingressRules := sliceAt(t, policy, "spec", "ingress")
			if len(ingressRules) != 1 {
				t.Fatalf("company-site NetworkPolicy ingress has %d entries, want 1", len(ingressRules))
			}
			ingressRule := asManifest(t, ingressRules[0], "spec.ingress[0]")
			from := sliceAt(t, ingressRule, "from")
			if len(from) != 1 {
				t.Fatalf("company-site NetworkPolicy ingress[0].from has %d entries, want 1", len(from))
			}
			source := asManifest(t, from[0], "spec.ingress[0].from[0]")
			assertString(t, source, "tenant-root", "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name")
			assertString(t, source, "ingress-nginx", "podSelector", "matchLabels", "app.kubernetes.io/name")
			assertString(t, source, "ingress-nginx-system", "podSelector", "matchLabels", "app.kubernetes.io/instance")
			ports := sliceAt(t, ingressRule, "ports")
			if len(ports) != 1 {
				t.Fatalf("company-site NetworkPolicy ingress[0].ports has %d entries, want 1", len(ports))
			}
			networkPort := asManifest(t, ports[0], "spec.ingress[0].ports[0]")
			assertString(t, networkPort, "TCP", "protocol")
			assertInt(t, networkPort, 8080, "port")

			pdb := findObject(t, docs, "PodDisruptionBudget", env.namespace, "company-site")
			assertString(t, pdb, "policy/v1", "apiVersion")
			assertInt(t, pdb, 2, "spec", "minAvailable")
			assertString(t, pdb, "company-site", "spec", "selector", "matchLabels", "app.kubernetes.io/name")
			assertString(t, pdb, env.name, "spec", "selector", "matchLabels", "guardian.dev/stage")

			ingress := findObject(t, docs, "Ingress", env.namespace, "company-site")
			assertString(t, ingress, "networking.k8s.io/v1", "apiVersion")
			assertString(t, ingress, "tenant-root", "metadata", "annotations", "acme.cert-manager.io/http01-ingress-ingressclassname")
			assertString(t, ingress, "letsencrypt-prod", "metadata", "annotations", "cert-manager.io/cluster-issuer")
			assertString(t, ingress, "tenant-root", "spec", "ingressClassName")
			assertIngressHost(t, ingress, env.host)
		})
	}
}

func testCompanySiteSourceOwnership(t *testing.T) {
	deps := string(readRunfile(t, "src/infrastructure/tests/company_site_dependency_closure"))
	assertTextContains(t, deps, "//src/products/company/site:image", "company-site dependency closure")
	assertTextContains(t, deps, "//src/products/company/web:image", "company-site dependency closure")
	assertTextNotContains(t, deps, "src-old", "company-site dependency closure")
	assertTextNotContains(t, deps, "//src-old", "company-site dependency closure")

	bazelignore := string(readRunfile(t, ".bazelignore"))
	assertTextContains(t, bazelignore, "src-old", ".bazelignore")
}

func readCompanySiteImageDigest(t *testing.T) string {
	t.Helper()

	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	data := readRunfile(t, "src/products/company/web/image/index.json")
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatalf("parse company-site OCI index: %v", err)
	}
	if len(index.Manifests) != 1 {
		t.Fatalf("company-site OCI index has %d manifests, want 1", len(index.Manifests))
	}
	return index.Manifests[0].Digest
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

	esoPolicy := findObject(t, policies, "CiliumNetworkPolicy", "tenant-root", "allow-external-secrets-to-openbao")
	assertString(t, esoPolicy, "cilium.io/v2", "apiVersion")
	assertString(t, esoPolicy, "openbao", "spec", "endpointSelector", "matchLabels", "app.kubernetes.io/name")

	ingress := sliceAt(t, esoPolicy, "spec", "ingress")
	if len(ingress) != 1 {
		t.Fatalf("allow-external-secrets-to-openbao spec.ingress has %d entries, want 1", len(ingress))
	}
	fromEndpoints := sliceAt(t, asManifest(t, ingress[0], "spec.ingress[0]"), "fromEndpoints")
	if len(fromEndpoints) != 1 {
		t.Fatalf("allow-external-secrets-to-openbao spec.ingress[0].fromEndpoints has %d entries, want 1", len(fromEndpoints))
	}
	source := asManifest(t, fromEndpoints[0], "spec.ingress[0].fromEndpoints[0]")
	assertString(t, source, "cozy-external-secrets-operator", "matchLabels", "k8s:io.kubernetes.pod.namespace")
	assertString(t, source, "external-secrets", "matchLabels", "app.kubernetes.io/name")
	assertString(t, source, "external-secrets-operator", "matchLabels", "app.kubernetes.io/instance")

	ingressToPorts := sliceAt(t, asManifest(t, ingress[0], "spec.ingress[0]"), "toPorts")
	if len(ingressToPorts) != 1 {
		t.Fatalf("allow-external-secrets-to-openbao spec.ingress[0].toPorts has %d entries, want 1", len(ingressToPorts))
	}
	ingressPorts := sliceAt(t, asManifest(t, ingressToPorts[0], "spec.ingress[0].toPorts[0]"), "ports")
	if len(ingressPorts) != 1 {
		t.Fatalf("allow-external-secrets-to-openbao spec.ingress[0].toPorts[0].ports has %d entries, want 1", len(ingressPorts))
	}
	ingressPort := asManifest(t, ingressPorts[0], "spec.ingress[0].toPorts[0].ports[0]")
	assertString(t, ingressPort, "8200", "port")
	assertString(t, ingressPort, "TCP", "protocol")
}

func testOpenBaoOpenTofuBootstrap(t *testing.T) {
	const versionsPath = "src/infrastructure/bootstrap/guardian-mgmt-openbao/versions.tf"
	versionsBytes := readRunfile(t, versionsPath)
	versions := string(versionsBytes)
	mainTF := string(readRunfile(t, "src/infrastructure/bootstrap/guardian-mgmt-openbao/main.tf"))
	lock := string(readRunfile(t, "src/infrastructure/bootstrap/guardian-mgmt-openbao/.terraform.lock.hcl"))

	assertTextContains(t, versions, `source  = "hashicorp/vault"`, "guardian-mgmt-openbao versions.tf")
	assertTextContains(t, versions, `version = "= 4.4.0"`, "guardian-mgmt-openbao versions.tf")
	backendAttrs := hclBackendAttrs(t, versionsBytes, versionsPath, "s3")
	assertHCLStringAttribute(t, backendAttrs, "key", "opentofu/guardian-mgmt-openbao.tfstate", "guardian-mgmt-openbao backend.s3")
	assertTextContains(t, hclExpressionSource(versionsBytes, hclAttr(t, backendAttrs, "endpoint", "guardian-mgmt-openbao backend.s3").Expr), "var.cloudflare_account_id", "guardian-mgmt-openbao backend.s3 endpoint")
	assertTextContains(t, hclExpressionSource(versionsBytes, hclAttr(t, backendAttrs, "endpoint", "guardian-mgmt-openbao backend.s3").Expr), ".r2.cloudflarestorage.com", "guardian-mgmt-openbao backend.s3 endpoint")
	assertTextContains(t, lock, `provider "registry.opentofu.org/hashicorp/vault"`, "guardian-mgmt-openbao lock")
	assertTextContains(t, lock, `version     = "4.4.0"`, "guardian-mgmt-openbao lock")

	assertTextContains(t, mainTF, `resource "vault_mount" "kv"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `type        = "kv-v2"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `resource "vault_auth_backend" "kubernetes"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `type        = "kubernetes"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `resource "vault_kubernetes_auth_backend_config" "guardian_mgmt"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `kubernetes_host        = "https://kubernetes.default.svc:443"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `disable_iss_validation = true`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `resource "vault_policy" "secret_projection"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `resource "vault_kubernetes_auth_backend_role" "secret_projection"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `audience                         = "openbao"`, "guardian-mgmt-openbao main.tf")
	assertTextContains(t, mainTF, `token_no_default_policy          = true`, "guardian-mgmt-openbao main.tf")
	assertTextNotContains(t, mainTF, "vault_kv_secret", "guardian-mgmt-openbao main.tf")
	assertTextNotContains(t, mainTF, "vault_generic_secret", "guardian-mgmt-openbao main.tf")

	for _, tc := range []struct {
		role           string
		namespace      string
		serviceAccount string
		path           string
	}{
		{
			role:           "tenant-root-cnpg-backup",
			namespace:      "tenant-root",
			serviceAccount: "guardian-external-secrets",
			path:           "guardian/guardian-mgmt/tenant-root/postgres/guardian/cnpg-backup",
		},
		{
			role:           "tenant-dev-cnpg-backup",
			namespace:      "tenant-dev",
			serviceAccount: "guardian-external-secrets",
			path:           "guardian/guardian-mgmt/tenant-dev/postgres/guardian/cnpg-backup",
		},
		{
			role:           "tenant-gamma-cnpg-backup",
			namespace:      "tenant-gamma",
			serviceAccount: "guardian-external-secrets",
			path:           "guardian/guardian-mgmt/tenant-gamma/postgres/guardian/cnpg-backup",
		},
		{
			role:           "tenant-prod-cnpg-backup",
			namespace:      "tenant-prod",
			serviceAccount: "guardian-external-secrets",
			path:           "guardian/guardian-mgmt/tenant-prod/postgres/guardian/cnpg-backup",
		},
		{
			role:           "tenant-root-clickhouse-backup",
			namespace:      "tenant-root",
			serviceAccount: "guardian-clickhouse-external-secrets",
			path:           "guardian/guardian-mgmt/tenant-root/clickhouse/guardian/backup",
		},
		{
			role:           "tenant-dev-clickhouse-backup",
			namespace:      "tenant-dev",
			serviceAccount: "guardian-clickhouse-external-secrets",
			path:           "guardian/guardian-mgmt/tenant-dev/clickhouse/guardian/backup",
		},
		{
			role:           "tenant-gamma-clickhouse-backup",
			namespace:      "tenant-gamma",
			serviceAccount: "guardian-clickhouse-external-secrets",
			path:           "guardian/guardian-mgmt/tenant-gamma/clickhouse/guardian/backup",
		},
		{
			role:           "tenant-prod-clickhouse-backup",
			namespace:      "tenant-prod",
			serviceAccount: "guardian-clickhouse-external-secrets",
			path:           "guardian/guardian-mgmt/tenant-prod/clickhouse/guardian/backup",
		},
	} {
		t.Run(tc.role, func(t *testing.T) {
			assertTextContains(t, mainTF, tc.role+" = {", "guardian-mgmt-openbao main.tf")
			assertTextContains(t, mainTF, `service_account = "`+tc.serviceAccount+`"`, "guardian-mgmt-openbao main.tf")
			assertTextContains(t, mainTF, `namespace       = "`+tc.namespace+`"`, "guardian-mgmt-openbao main.tf")
			assertTextContains(t, mainTF, `path            = "`+tc.path+`"`, "guardian-mgmt-openbao main.tf")
		})
	}
}

func testOpenBaoCNPGBackupSecretProjection(t *testing.T) {
	cases := []struct {
		name       string
		manifest   string
		namespace  string
		role       string
		remotePath string
	}{
		{
			name:       "root",
			manifest:   "src/infrastructure/base/secrets/cnpg-backup-secrets.yaml",
			namespace:  "tenant-root",
			role:       "tenant-root-cnpg-backup",
			remotePath: "guardian/guardian-mgmt/tenant-root/postgres/guardian/cnpg-backup",
		},
		{
			name:       "dev",
			manifest:   "src/infrastructure/environments/dev/secrets.yaml",
			namespace:  "tenant-dev",
			role:       "tenant-dev-cnpg-backup",
			remotePath: "guardian/guardian-mgmt/tenant-dev/postgres/guardian/cnpg-backup",
		},
		{
			name:       "gamma",
			manifest:   "src/infrastructure/environments/gamma/secrets.yaml",
			namespace:  "tenant-gamma",
			role:       "tenant-gamma-cnpg-backup",
			remotePath: "guardian/guardian-mgmt/tenant-gamma/postgres/guardian/cnpg-backup",
		},
		{
			name:       "prod",
			manifest:   "src/infrastructure/environments/prod/secrets.yaml",
			namespace:  "tenant-prod",
			role:       "tenant-prod-cnpg-backup",
			remotePath: "guardian/guardian-mgmt/tenant-prod/postgres/guardian/cnpg-backup",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docs := readManifests(t, tc.manifest)
			assertCNPGBackupSecretProjection(t, docs, tc.namespace, tc.role, tc.remotePath)
		})
	}
}

func assertCNPGBackupSecretProjection(t *testing.T, docs []manifest, namespace, role, remotePath string) {
	t.Helper()

	sa := findObject(t, docs, "ServiceAccount", namespace, "guardian-external-secrets")
	assertString(t, sa, "v1", "apiVersion")
	assertString(t, sa, "guardian", "metadata", "labels", "app.kubernetes.io/part-of")
	assertString(t, sa, "cnpg-backup", "metadata", "labels", "guardian.dev/secret-scope")

	store := findObject(t, docs, "SecretStore", namespace, "openbao")
	assertString(t, store, "external-secrets.io/v1beta1", "apiVersion")
	assertString(t, store, "http://openbao-guardian.tenant-root.svc:8200", "spec", "provider", "vault", "server")
	assertString(t, store, "kv", "spec", "provider", "vault", "path")
	assertString(t, store, "v2", "spec", "provider", "vault", "version")
	assertString(t, store, "kubernetes", "spec", "provider", "vault", "auth", "kubernetes", "mountPath")
	assertString(t, store, role, "spec", "provider", "vault", "auth", "kubernetes", "role")
	assertString(t, store, "guardian-external-secrets", "spec", "provider", "vault", "auth", "kubernetes", "serviceAccountRef", "name")
	assertStringSlice(t, store, []string{"openbao"}, "spec", "provider", "vault", "auth", "kubernetes", "serviceAccountRef", "audiences")

	externalSecret := findObject(t, docs, "ExternalSecret", namespace, "guardian-cnpg-backup-creds")
	assertString(t, externalSecret, "external-secrets.io/v1beta1", "apiVersion")
	assertString(t, externalSecret, "1h", "spec", "refreshInterval")
	assertString(t, externalSecret, "openbao", "spec", "secretStoreRef", "name")
	assertString(t, externalSecret, "SecretStore", "spec", "secretStoreRef", "kind")
	assertString(t, externalSecret, "guardian-cnpg-backup-creds", "spec", "target", "name")
	assertString(t, externalSecret, "Owner", "spec", "target", "creationPolicy")
	assertString(t, externalSecret, "Opaque", "spec", "target", "template", "type")

	data := sliceAt(t, externalSecret, "spec", "data")
	if len(data) != 2 {
		t.Fatalf("ExternalSecret spec.data has %d entries, want 2", len(data))
	}
	assertExternalSecretData(t, data, "AWS_ACCESS_KEY_ID", remotePath, "AWS_ACCESS_KEY_ID")
	assertExternalSecretData(t, data, "AWS_SECRET_ACCESS_KEY", remotePath, "AWS_SECRET_ACCESS_KEY")
}

func assertExternalSecretData(t *testing.T, entries []any, secretKey, remotePath, property string) {
	t.Helper()

	for _, entry := range entries {
		doc := asManifest(t, entry, "spec.data[]")
		if stringAt(doc, "secretKey") != secretKey {
			continue
		}
		assertString(t, doc, remotePath, "remoteRef", "key")
		assertString(t, doc, property, "remoteRef", "property")
		return
	}
	t.Fatalf("ExternalSecret spec.data is missing secretKey %q", secretKey)
}

func testOpenBaoClickHouseBackupSecretProjection(t *testing.T) {
	cases := []struct {
		name       string
		manifest   string
		namespace  string
		role       string
		remotePath string
	}{
		{
			name:       "root",
			manifest:   "src/infrastructure/base/secrets/clickhouse-backup-secrets.yaml",
			namespace:  "tenant-root",
			role:       "tenant-root-clickhouse-backup",
			remotePath: "guardian/guardian-mgmt/tenant-root/clickhouse/guardian/backup",
		},
		{
			name:       "dev",
			manifest:   "src/infrastructure/environments/dev/secrets.yaml",
			namespace:  "tenant-dev",
			role:       "tenant-dev-clickhouse-backup",
			remotePath: "guardian/guardian-mgmt/tenant-dev/clickhouse/guardian/backup",
		},
		{
			name:       "gamma",
			manifest:   "src/infrastructure/environments/gamma/secrets.yaml",
			namespace:  "tenant-gamma",
			role:       "tenant-gamma-clickhouse-backup",
			remotePath: "guardian/guardian-mgmt/tenant-gamma/clickhouse/guardian/backup",
		},
		{
			name:       "prod",
			manifest:   "src/infrastructure/environments/prod/secrets.yaml",
			namespace:  "tenant-prod",
			role:       "tenant-prod-clickhouse-backup",
			remotePath: "guardian/guardian-mgmt/tenant-prod/clickhouse/guardian/backup",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docs := readManifests(t, tc.manifest)
			assertClickHouseBackupSecretProjection(t, docs, tc.namespace, tc.role, tc.remotePath)
		})
	}
}

func assertClickHouseBackupSecretProjection(t *testing.T, docs []manifest, namespace, role, remotePath string) {
	t.Helper()

	sa := findObject(t, docs, "ServiceAccount", namespace, "guardian-clickhouse-external-secrets")
	assertString(t, sa, "v1", "apiVersion")
	assertString(t, sa, "guardian", "metadata", "labels", "app.kubernetes.io/part-of")
	assertString(t, sa, "clickhouse-backup", "metadata", "labels", "guardian.dev/secret-scope")

	store := findObject(t, docs, "SecretStore", namespace, "openbao-clickhouse-backup")
	assertString(t, store, "external-secrets.io/v1beta1", "apiVersion")
	assertString(t, store, "http://openbao-guardian.tenant-root.svc:8200", "spec", "provider", "vault", "server")
	assertString(t, store, "kv", "spec", "provider", "vault", "path")
	assertString(t, store, "v2", "spec", "provider", "vault", "version")
	assertString(t, store, "kubernetes", "spec", "provider", "vault", "auth", "kubernetes", "mountPath")
	assertString(t, store, role, "spec", "provider", "vault", "auth", "kubernetes", "role")
	assertString(t, store, "guardian-clickhouse-external-secrets", "spec", "provider", "vault", "auth", "kubernetes", "serviceAccountRef", "name")
	assertStringSlice(t, store, []string{"openbao"}, "spec", "provider", "vault", "auth", "kubernetes", "serviceAccountRef", "audiences")

	externalSecret := findObject(t, docs, "ExternalSecret", namespace, "guardian-clickhouse-backup-creds")
	assertString(t, externalSecret, "external-secrets.io/v1beta1", "apiVersion")
	assertString(t, externalSecret, "1h", "spec", "refreshInterval")
	assertString(t, externalSecret, "openbao-clickhouse-backup", "spec", "secretStoreRef", "name")
	assertString(t, externalSecret, "SecretStore", "spec", "secretStoreRef", "kind")
	assertString(t, externalSecret, "guardian-clickhouse-backup-creds", "spec", "target", "name")
	assertString(t, externalSecret, "Owner", "spec", "target", "creationPolicy")
	assertString(t, externalSecret, "Opaque", "spec", "target", "template", "type")

	data := sliceAt(t, externalSecret, "spec", "data")
	if len(data) != 5 {
		t.Fatalf("ExternalSecret spec.data has %d entries, want 5", len(data))
	}
	for _, key := range []string{"bucketName", "endpoint", "region", "accessKey", "secretKey"} {
		assertExternalSecretData(t, data, key, remotePath, key)
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
	assertBool(t, base, false, "spec", "prune")
	assertBool(t, base, false, "spec", "wait")

	apps := findObject(t, docs, "Kustomization", "cozy-fluxcd", "guardian-mgmt-tenant-apps")
	assertString(t, apps, "./src/infrastructure/environments", "spec", "path")
	assertString(t, apps, "GitRepository", "spec", "sourceRef", "kind")
	assertString(t, apps, "guardian", "spec", "sourceRef", "name")
	assertBool(t, apps, false, "spec", "prune")
	assertBool(t, apps, false, "spec", "wait")

	deps := sliceAt(t, apps, "spec", "dependsOn")
	if len(deps) != 1 || stringAt(asManifest(t, deps[0], "spec.dependsOn[0]"), "name") != "guardian-mgmt-base" {
		t.Fatalf("guardian-mgmt-tenant-apps dependsOn = %#v, want only guardian-mgmt-base", deps)
	}
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
	backupSecretName   string
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
	if want.backupSecretName != "" {
		assertBool(t, app, true, "spec", "backup", "enabled")
		assertString(t, app, "", "spec", "backup", "schedule")
		assertString(t, app, want.backupSecretName, "spec", "backup", "s3CredentialsSecret", "name")

		plan := findObject(t, docs, "Plan", want.namespace, want.backupPlanName)
		assertString(t, plan, "backups.cozystack.io/v1alpha1", "apiVersion")
		assertString(t, plan, "apps.cozystack.io", "spec", "applicationRef", "apiGroup")
		assertString(t, plan, want.kind, "spec", "applicationRef", "kind")
		assertString(t, plan, "guardian", "spec", "applicationRef", "name")
		assertString(t, plan, "guardian-clickhouse-altinity", "spec", "backupClassName")
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

func topologyPublicIPs(topology guardianMgmtTopology) []string {
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

	for _, value := range values {
		if got, ok := value.(string); ok && got == want {
			return
		}
	}
	t.Fatalf("%s = %#v, want it to contain %q", label, values, want)
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

func assertIngressHost(t *testing.T, ingress manifest, host string) {
	t.Helper()

	tls := sliceAt(t, ingress, "spec", "tls")
	if len(tls) != 1 {
		t.Fatalf("spec.tls has %d entries, want 1", len(tls))
	}
	assertStringSlice(t, asManifest(t, tls[0], "spec.tls[0]"), []string{host}, "hosts")
	assertString(t, asManifest(t, tls[0], "spec.tls[0]"), "company-site-tls", "secretName")

	rules := sliceAt(t, ingress, "spec", "rules")
	if len(rules) != 1 {
		t.Fatalf("spec.rules has %d entries, want 1", len(rules))
	}
	rule := asManifest(t, rules[0], "spec.rules[0]")
	assertString(t, rule, host, "host")

	paths := sliceAt(t, rule, "http", "paths")
	if len(paths) != 1 {
		t.Fatalf("spec.rules[0].http.paths has %d entries, want 1", len(paths))
	}
	path := asManifest(t, paths[0], "spec.rules[0].http.paths[0]")
	assertString(t, path, "/", "path")
	assertString(t, path, "Prefix", "pathType")
	assertString(t, path, "company-site", "backend", "service", "name")
	assertInt(t, path, 80, "backend", "service", "port", "number")
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
