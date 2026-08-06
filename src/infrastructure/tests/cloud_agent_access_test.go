package tests

import (
	"strings"
	"testing"
)

func TestCloudAgentDeliveryReadBoundary(t *testing.T) {
	path := runfilePath("src/infrastructure/base/cozystack-identities/cloud-agents.yaml")
	raw := readText(t, path)
	docs := yamlDocs(t, path)

	for _, provider := range []string{"cursor", "devin"} {
		name := "guardian-cloud-agent-" + provider
		sa := findDoc(t, docs, "ServiceAccount", name)
		if enabled, ok := sa["automountServiceAccountToken"].(bool); !ok || enabled {
			t.Fatalf("ServiceAccount %s automountServiceAccountToken = %v, want false", name, sa["automountServiceAccountToken"])
		}
		assertTextContains(t, raw, "system:serviceaccount:tenant-root:"+name, path)
	}

	role := findDoc(t, docs, "ClusterRole", "guardian-persona-delivery-read")
	for _, item := range sliceValue(role["rules"]) {
		rule := mapValue(item)
		for _, verb := range sliceValue(rule["verbs"]) {
			got := stringValue(verb)
			if got != "get" && got != "list" && got != "watch" {
				t.Fatalf("delivery-read grants verb %q; want only get/list/watch", got)
			}
		}
		for _, resource := range sliceValue(rule["resources"]) {
			switch stringValue(resource) {
			case "secrets", "serviceaccounts/token", "pods/exec", "pods/attach", "pods/portforward":
				t.Fatalf("delivery-read grants forbidden resource %q", stringValue(resource))
			}
		}
	}

	minter := findDoc(t, docs, "ClusterRole", "guardian-persona-cloud-agent-token")
	rules := sliceValue(minter["rules"])
	if len(rules) != 1 {
		t.Fatalf("cloud-agent token minter has %d rules, want 1", len(rules))
	}
	rule := mapValue(rules[0])
	resources := sliceValue(rule["resources"])
	if len(resources) != 1 || stringValue(resources[0]) != "serviceaccounts/token" {
		t.Fatalf("cloud-agent token minter resources = %v", resources)
	}
	names := sliceValue(rule["resourceNames"])
	if len(names) != 2 || stringValue(names[0]) != "guardian-cloud-agent-cursor" || stringValue(names[1]) != "guardian-cloud-agent-devin" {
		t.Fatalf("cloud-agent token minter resourceNames = %v", names)
	}

	binding := findDoc(t, docs, "ClusterRoleBinding", "guardian-persona-cloud-agent-token")
	boundGroups := map[string]bool{}
	for _, item := range sliceValue(binding["subjects"]) {
		subject := mapValue(item)
		if stringValue(subject["kind"]) == "Group" {
			boundGroups[stringValue(subject["name"])] = true
		}
	}
	for _, group := range []string{"guardian-persona-read", "guardian-persona-write-basic"} {
		if !boundGroups[group] {
			t.Fatalf("cloud-agent token minter is not bound to %s", group)
		}
	}

	platformPath := runfilePath("src/infrastructure/base/cozystack-identities/platform-admins.yaml")
	platformRaw := readText(t, platformPath)
	for _, want := range []string{
		`object.spec.expirationSeconds <= 3600`,
		`object.spec.audiences[0] == "https://10.8.0.250:6443"`,
	} {
		assertTextContains(t, platformRaw, want, platformPath)
	}

	usernameGuard := findDoc(t, docs, "ValidatingAdmissionPolicy", "guardian-platform-agent-readonly")
	usernameGuardSpec := mapValue(usernameGuard["spec"])
	if stringValue(usernameGuardSpec["failurePolicy"]) != "Fail" {
		t.Fatal("platform-agent username admission policy must fail closed")
	}
	usernameGuardRaw := stringValue(mapValue(sliceValue(usernameGuardSpec["validations"])[0])["expression"])
	for _, want := range []string{
		`guardian-cloud-agent-cursor`,
		`guardian-cloud-agent-devin`,
		`object.spec.expirationSeconds <= 3600`,
		`object.spec.audiences[0] == "https://10.8.0.250:6443"`,
	} {
		if !strings.Contains(usernameGuardRaw, want) {
			t.Fatalf("platform-agent username admission policy is missing %q", want)
		}
	}

	policy := findDoc(t, docs, "ValidatingAdmissionPolicy", "guardian-persona-delivery-read")
	spec := mapValue(policy["spec"])
	if stringValue(spec["failurePolicy"]) != "Fail" {
		t.Fatal("delivery-read admission policy must fail closed")
	}
	validations := sliceValue(spec["validations"])
	if len(validations) != 1 || stringValue(mapValue(validations[0])["expression"]) != "false" {
		t.Fatalf("delivery-read policy must unconditionally deny matched writes: %v", validations)
	}

	for _, forbidden := range []string{"offline_access", "cluster-admin", "kind: Secret\n"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("cloud-agent manifest contains forbidden authority %q", forbidden)
		}
	}
}

func TestCloudAgentSetupDoesNotExportStandingKubernetesIdentity(t *testing.T) {
	setupPath := runfilePath("scripts/agent-cloud-setup.sh")
	setup := readText(t, setupPath)
	for _, want := range []string{
		"unset GUARDIAN_AGENT_KUBERNETES_TOKEN",
		"guardian-${provider}-cloud",
		"tls-server-name: k8s.guardianintelligence.org",
		"auth whoami",
	} {
		assertTextContains(t, setup, want, setupPath)
	}
	for _, forbidden := range []string{
		"offline_access",
		"kubectl-oidc_login",
		"ln -sfn \"${kubeconfig}\" \"${HOME}/.kube/config\"",
	} {
		assertTextNotContains(t, setup, forbidden, setupPath)
	}

	tokenPath := runfilePath("tools/ops/agent-cloud-token")
	tokenHelper := readText(t, tokenPath)
	assertTextContains(t, tokenHelper, "--audience=https://10.8.0.250:6443", tokenPath)
}

func TestCloudAgentAspectTaskRegistered(t *testing.T) {
	configPath := runfilePath(".aspect/config.axl")
	config := readText(t, configPath)
	for _, want := range []string{
		`tools_install_agent_cloud = "install_agent_cloud"`,
		"ctx.tasks.add(tools_install_agent_cloud)",
	} {
		assertTextContains(t, config, want, configPath)
	}

	toolsPath := runfilePath(".aspect/tasks/tools.axl")
	tools := readText(t, toolsPath)
	for _, want := range []string{
		"install_agent_cloud = task(",
		`name = "install-agent-cloud"`,
	} {
		assertTextContains(t, tools, want, toolsPath)
	}
}
