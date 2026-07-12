package tests

import (
	"fmt"
	"strings"
	"testing"
)

func TestCloudflareOriginTLSConformance(t *testing.T) {
	secretPath := runfilePath("src/infrastructure/base/secrets/cloudflare-origin-tls.yaml")
	secret := readText(t, secretPath)
	for _, want := range []string{
		"name: cloudflare-origin-tls",
		"namespace: tenant-root",
		"type: kubernetes.io/tls",
		"property: tls.crt",
		"property: tls.key",
	} {
		assertTextContains(t, secret, want, secretPath)
	}
	assertTextNotContains(t, secret, "PRIVATE KEY", secretPath)

	ingressPath := runfilePath("src/infrastructure/base/app-patches/ingress-origin-edge.yaml")
	ingress := readText(t, ingressPath)
	assertTextContains(t, ingress, "default-ssl-certificate: tenant-root/cloudflare-origin-tls", ingressPath)
	for _, want := range []string{
		"hostNetwork: true",
		"dnsPolicy: ClusterFirstWithHostNet",
		"path: /spec/template/spec/runtimeClassName",
		"value: guardian-system-ingress",
		"externalIPs: []",
	} {
		assertTextContains(t, ingress, want, ingressPath)
	}

	edgePolicyPath := runfilePath("src/infrastructure/bootstrap/guardian-mgmt-edge-policy/main.tf")
	edgePolicy := readText(t, edgePolicyPath)
	for _, want := range []string{
		`resource "cloudflare_zone_setting" "origin_ssl"`,
		`setting_id = "ssl"`,
		`value      = "strict"`,
	} {
		assertTextContains(t, edgePolicy, want, edgePolicyPath)
	}
}

func TestCloudflareOriginPullIsRequired(t *testing.T) {
	for _, stage := range []string{"beta", "gamma", "prod"} {
		path := runfilePath(fmt.Sprintf("src/infrastructure/deployments/company/%s/web.yaml", stage))
		raw := readText(t, path)
		assertTextContains(t, raw, `nginx.ingress.kubernetes.io/auth-tls-verify-client: "on"`, path)
		assertTextNotContains(t, raw, "optional_no_ca", path)
	}
}

func TestPlatformAgentIsReadOnlyWithMaintenanceExceptions(t *testing.T) {
	path := runfilePath("src/infrastructure/base/cozystack/platform-admins.yaml")
	raw := readText(t, path)

	admin := raw[strings.Index(raw, "# platform-admin"):strings.Index(raw, "kind: KeycloakRealmGroup")]
	assertTextContains(t, admin, "- cozystack-cluster-admin", path)

	agentStart := strings.Index(raw, "# platform-agent")
	agentEnd := strings.Index(raw[agentStart:], "kind: ClusterRole") + agentStart
	agent := raw[agentStart:agentEnd]
	assertTextContains(t, agent, "- guardian-platform-agent", path)
	assertTextNotContains(t, agent, "cozystack-cluster-admin", path)

	maintenanceStart := agentEnd
	maintenanceEnd := strings.Index(raw[maintenanceStart:], "kind: ClusterRoleBinding") + maintenanceStart
	maintenance := raw[maintenanceStart:maintenanceEnd]
	for _, want := range []string{"- pods", "- jobs", "- pods/portforward", "- delete", "- create"} {
		assertTextContains(t, maintenance, want, path)
	}
	for _, forbidden := range []string{"secrets", "pods/exec", "- update", "- patch", "- \"*\""} {
		assertTextNotContains(t, maintenance, forbidden, path)
	}

	for _, want := range []string{
		"name: guardian-platform-agent-cluster-view",
		"- nodes",
		"- persistentvolumes",
		"- customresourcedefinitions",
		"- clusterroles",
		"name: guardian-platform-agent-view",
		"name: view",
		"name: guardian-platform-agent-readonly",
		`request.userInfo.username.endsWith("#platform-agent")`,
		`"guardian-platform-agent" in request.userInfo.groups`,
		`!has(request.subResource)`,
		`request.subResource == "portforward"`,
		`request.resource.resource == "jobs"`,
	} {
		assertTextContains(t, raw, want, path)
	}
}

func TestTalosPublicEdgeIsCloudflareOnly(t *testing.T) {
	valuesPath := runfilePath("src/infrastructure/talm/values.yaml")
	values := readText(t, valuesPath)
	for _, cidr := range []string{
		"173.245.48.0/20",
		"103.21.244.0/22",
		"103.22.200.0/22",
		"103.31.4.0/22",
		"141.101.64.0/18",
		"108.162.192.0/18",
		"190.93.240.0/20",
		"188.114.96.0/20",
		"197.234.240.0/22",
		"198.41.128.0/17",
		"162.158.0.0/15",
		"104.16.0.0/13",
		"104.24.0.0/14",
		"172.64.0.0/13",
		"131.0.72.0/22",
		"2400:cb00::/32",
		"2606:4700::/32",
		"2803:f800::/32",
		"2405:b500::/32",
		"2405:8100::/32",
		"2a06:98c0::/29",
		"2c0f:f248::/32",
	} {
		assertTextContains(t, values, cidr, valuesPath)
	}

	templatePath := runfilePath("src/infrastructure/talm/templates/_helpers.tpl")
	template := readText(t, templatePath)
	assertTextContains(t, template, "range .Values.ingressFirewall.cloudflareEdgeSubnets", templatePath)
	publicEdge := template[strings.Index(template, "name: public-edge"):]
	assertTextNotContains(t, publicEdge, "0.0.0.0/0", templatePath)
	assertTextNotContains(t, publicEdge, "::/0", templatePath)

	for _, want := range []string{"148.113.198.223/32", "operator-talos-api", "operator-kubernetes-api"} {
		assertTextContains(t, values+template, want, "Talos operator access")
	}
}

func TestRootIngressHasNarrowPodSecurityExemption(t *testing.T) {
	valuesPath := runfilePath("src/infrastructure/talm/values.yaml")
	values := readText(t, valuesPath)
	templatePath := runfilePath("src/infrastructure/talm/templates/_helpers.tpl")
	template := readText(t, templatePath)
	runtimePath := runfilePath("src/infrastructure/base/app-patches/ingress-runtimeclass.yaml")
	runtime := readText(t, runtimePath)

	for _, want := range []string{
		"exemptRuntimeClasses:",
		"guardian-system-ingress",
		"runtimeClasses:",
		"toYaml .Values.podSecurity.exemptRuntimeClasses",
	} {
		assertTextContains(t, values+template, want, "Talos Pod Security configuration")
	}
	for _, want := range []string{
		"kind: RuntimeClass",
		"name: guardian-system-ingress",
		"handler: runc",
	} {
		assertTextContains(t, runtime, want, runtimePath)
	}
	assertTextNotContains(t, template, "namespaces:\n              - tenant-root", templatePath)
}

func TestRootIngressNetworkPolicyIsCloudflareOnly(t *testing.T) {
	path := runfilePath("src/infrastructure/base/app-patches/ingress-origin-networkpolicy.yaml")
	raw := readText(t, path)
	for _, want := range []string{
		"name: root-ingress-cloudflare-or-cluster",
		"app.kubernetes.io/name: ingress-nginx",
		"app.kubernetes.io/component: controller",
		"cidr: 173.245.48.0/20",
		"cidr: 2c0f:f248::/32",
		"cidr: 10.8.0.0/24",
		"cidr: 10.244.0.0/16",
		"cidr: 100.64.0.0/16",
		"port: 80",
		"port: 443",
	} {
		assertTextContains(t, raw, want, path)
	}
	assertTextNotContains(t, raw, "cidr: 0.0.0.0/0", path)
	assertTextNotContains(t, raw, "cidr: ::/0", path)
}
