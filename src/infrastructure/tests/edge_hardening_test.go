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
	for _, stage := range []string{"prod"} {
		path := runfilePath(fmt.Sprintf("src/infrastructure/deployments/company/%s/web.yaml", stage))
		raw := readText(t, path)
		assertTextContains(t, raw, `nginx.ingress.kubernetes.io/auth-tls-verify-client: "on"`, path)
		assertTextNotContains(t, raw, "optional_no_ca", path)
	}
}

// personaSpec declares one rung of the persona ladder
// (src/infrastructure/base/cozystack/platform-admins.yaml). The ladder is
// meant to grow: adding a rung is a manifest block plus an entry here, and
// this test is what makes the pair inseparable. Everything it asserts is a
// property a hand-written persona could get quietly wrong — a user left in two
// groups, a write persona handed offline_access and therefore able to refresh
// unattended, a policy that matches on the group but not the username and so
// fails open when a token arrives without a groups claim.
type personaSpec struct {
	name         string
	username     string
	unattended   bool
	clusterRoles []string
	hasPolicy    bool
}

var personaLadder = []personaSpec{
	{
		name:         "read",
		username:     "platform-agent",
		unattended:   true,
		clusterRoles: []string{"view", "guardian-persona-cluster-view", "guardian-persona-portforward"},
		hasPolicy:    true,
	},
	{
		name:     "write-basic",
		username: "platform-write-basic",
		clusterRoles: []string{
			"view",
			"guardian-persona-cluster-view",
			"guardian-persona-portforward",
			"guardian-persona-maintenance",
			"guardian-persona-secrets-writer-token",
		},
		hasPolicy: true,
	},
	{
		name:         "write-all",
		username:     "platform-write-all",
		clusterRoles: []string{"cluster-admin"},
		hasPolicy:    false,
	},
}

func TestPersonaLadderShape(t *testing.T) {
	path := runfilePath("src/infrastructure/base/cozystack/platform-admins.yaml")
	raw := readText(t, path)
	docs := yamlDocs(t, path)

	declaredGroups := map[string]bool{}
	for _, doc := range docs {
		if stringValue(doc["kind"]) == "KeycloakRealmGroup" {
			declaredGroups[stringValue(mapValue(doc["spec"])["name"])] = true
		}
	}

	for _, persona := range personaLadder {
		group := "guardian-persona-" + persona.name
		t.Run(persona.name, func(t *testing.T) {
			if !declaredGroups[group] {
				t.Fatalf("persona %s has no KeycloakRealmGroup %s", persona.name, group)
			}

			var user map[string]interface{}
			for _, doc := range docs {
				if stringValue(doc["kind"]) != "KeycloakRealmUser" {
					continue
				}
				if stringValue(mapValue(doc["spec"])["username"]) == persona.username {
					user = mapValue(doc["spec"])
				}
			}
			if user == nil {
				t.Fatalf("persona %s has no KeycloakRealmUser %s", persona.name, persona.username)
			}

			groups := sliceValue(user["groups"])
			if len(groups) != 1 || stringValue(groups[0]) != group {
				t.Fatalf("persona user %s is in groups %v, want exactly [%q]; a user in two rungs carries the union of both",
					persona.username, groups, group)
			}

			offline := false
			for _, role := range sliceValue(user["roles"]) {
				if stringValue(role) == "offline_access" {
					offline = true
				}
			}
			if offline != persona.unattended {
				t.Fatalf("persona user %s offline_access = %v, want %v; offline_access is what lets a session refresh without a human, so only unattended rungs may hold it",
					persona.username, offline, persona.unattended)
			}

			bound := map[string]bool{}
			for _, doc := range docs {
				if stringValue(doc["kind"]) != "ClusterRoleBinding" {
					continue
				}
				for _, subject := range sliceValue(doc["subjects"]) {
					s := mapValue(subject)
					if stringValue(s["kind"]) == "Group" && stringValue(s["name"]) == group {
						bound[stringValue(mapValue(doc["roleRef"])["name"])] = true
					}
				}
			}
			want := map[string]bool{}
			for _, role := range persona.clusterRoles {
				want[role] = true
			}
			for role := range bound {
				if !want[role] {
					t.Fatalf("persona %s is bound to unexpected ClusterRole %s", persona.name, role)
				}
			}
			for role := range want {
				if !bound[role] {
					t.Fatalf("persona %s is missing a binding to ClusterRole %s", persona.name, role)
				}
			}

			if !persona.hasPolicy {
				return
			}
			policy := findDoc(t, docs, "ValidatingAdmissionPolicy", group)
			spec := mapValue(policy["spec"])
			if stringValue(spec["failurePolicy"]) != "Fail" {
				t.Fatalf("persona policy %s must be failurePolicy Fail; a policy that fails open is not a boundary", group)
			}
			conditions := sliceValue(spec["matchConditions"])
			if len(conditions) != 1 {
				t.Fatalf("persona policy %s has %d matchConditions, want exactly 1", group, len(conditions))
			}
			expr := stringValue(mapValue(conditions[0])["expression"])
			if !strings.Contains(expr, `"`+group+`" in request.userInfo.groups`) {
				t.Fatalf("persona policy %s does not match its group %s", group, group)
			}
			if !strings.Contains(expr, `endsWith("#`+persona.username+`")`) {
				t.Fatalf("persona policy %s has no username fallback for %s; a token without a groups claim would go unmatched and unrestricted",
					group, persona.username)
			}
		})
	}

	// Only the human operator's identity may carry cluster-admin through the
	// Cozystack group; a persona reaching it would silently outrank its rung.
	for _, doc := range docs {
		if stringValue(doc["kind"]) != "KeycloakRealmUser" {
			continue
		}
		spec := mapValue(doc["spec"])
		username := stringValue(spec["username"])
		for _, g := range sliceValue(spec["groups"]) {
			if stringValue(g) == "cozystack-cluster-admin" && username != "platform-admin" {
				t.Fatalf("user %s is in cozystack-cluster-admin; only platform-admin may be", username)
			}
		}
	}

	// The token-mint lane must stay pinned to the per-namespace secrets-writer
	// SAs (write-only OpenBao roles): an unpinned serviceaccounts/token grant
	// would let a persona mint any SA's token and read Secrets through it, and
	// no ClusterRole here may grant the secrets resource at all.
	tokenRole := findDoc(t, docs, "ClusterRole", "guardian-persona-secrets-writer-token")
	tokenRules := sliceValue(tokenRole["rules"])
	if len(tokenRules) != 1 {
		t.Fatalf("guardian-persona-secrets-writer-token has %d rules, want exactly 1", len(tokenRules))
	}
	tokenRule := mapValue(tokenRules[0])
	for field, want := range map[string]string{
		"apiGroups":     "",
		"resources":     "serviceaccounts/token",
		"resourceNames": "secrets-writer",
		"verbs":         "create",
	} {
		values := sliceValue(tokenRule[field])
		if len(values) != 1 || stringValue(values[0]) != want {
			t.Fatalf("guardian-persona-secrets-writer-token %s = %v, want exactly [%q]", field, values, want)
		}
	}
	for _, doc := range docs {
		if stringValue(doc["kind"]) != "ClusterRole" {
			continue
		}
		name := stringValue(mapValue(doc["metadata"])["name"])
		for _, item := range sliceValue(doc["rules"]) {
			rule := mapValue(item)
			for _, resource := range sliceValue(rule["resources"]) {
				switch stringValue(resource) {
				case "secrets":
					t.Fatalf("ClusterRole %s grants secrets; no persona may read a secret value", name)
				case "serviceaccounts/token":
					if name != "guardian-persona-secrets-writer-token" {
						t.Fatalf("ClusterRole %s grants serviceaccounts/token outside the pinned token-mint lane", name)
					}
				}
			}
		}
	}

	// Every persona password must be born in the cluster except the read
	// persona's, which predates the ladder and still resolves through OpenBao.
	assertTextContains(t, raw, "kind: Password", path)
	assertTextContains(t, raw, "refreshPolicy: CreatedOnce", path)

	analyticsPath := runfilePath("src/infrastructure/deployments/analytics/system/secrets.yaml")
	analyticsDocs := yamlDocs(t, analyticsPath)
	analyticsRole := findDoc(t, analyticsDocs, "Role", "guardian-persona-analytics-read")
	for _, item := range sliceValue(analyticsRole["rules"]) {
		rule := mapValue(item)
		verbs := sliceValue(rule["verbs"])
		if len(verbs) != 1 || stringValue(verbs[0]) != "get" {
			t.Fatalf("analytics read carve-out grants verbs %v, want exactly [\"get\"]", verbs)
		}
		if len(sliceValue(rule["resourceNames"])) == 0 {
			t.Fatal("analytics read carve-out has no resourceNames; the exception must stay pinned to named Secrets")
		}
	}
	analyticsBinding := findDoc(t, analyticsDocs, "RoleBinding", "guardian-persona-analytics-read")
	assertNestedString(t, analyticsBinding, "guardian-persona-analytics-read", "roleRef", "name")
	for _, subject := range sliceValue(analyticsBinding["subjects"]) {
		name := stringValue(mapValue(subject)["name"])
		if !declaredGroups[name] {
			t.Fatalf("analytics read carve-out is bound to %s, which is not a declared persona group", name)
		}
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
