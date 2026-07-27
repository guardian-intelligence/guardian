package tests

import (
	"regexp"
	"strings"
	"testing"
)

// The cluster scheduler replaces a workflow whose App identity, command
// allowlist, and dead-man behavior are all security or availability
// boundaries. Keep those properties coupled in one manifest so a routine
// Renovate self-bump cannot silently weaken the replacement.
func TestRenovateClusterSchedulerContract(t *testing.T) {
	const manifest = "src/infrastructure/deployments/guardian/imageops/renovate.yaml"
	source := readText(t, runfilePath(manifest))

	for _, want := range []string{
		`schedule: "17 */6 * * *"`,
		"concurrencyPolicy: Forbid",
		"activeDeadlineSeconds: 2700",
		"backoffLimit: 0",
		`value: '["tools/ops/multitool-repin","tools/ops/oathtool-repin"]'`,
		`value: "guardian-renovate[bot] <301916145+guardian-renovate[bot]@users.noreply.github.com>"`,
		"readOnlyRootFilesystem: true",
		"runAsUser: 12021",
		"name: tenant-root-vminsert-from-renovate",
		"absent_over_time(guardian_renovate_heartbeat[13h])",
		`namespace="guardian-imageops", cronjob="renovate"`,
	} {
		assertTextContains(t, source, want, manifest)
	}
	assertTextNotContains(t, source, "ghcr.io/guardian-intelligence/", manifest)

	imageMatch := regexp.MustCompile(
		`image: docker\.io/renovate/renovate:([0-9]+\.[0-9]+\.[0-9]+)-full@sha256:[a-f0-9]{64}`,
	).FindStringSubmatch(source)
	if len(imageMatch) != 2 {
		t.Fatalf("%s must pin a versioned -full Renovate image by sha256 digest", manifest)
	}
	validator := readText(t, runfilePath(".github/scripts/check-renovate-config.sh"))
	validatorMatch := regexp.MustCompile(
		`-p renovate@([0-9]+\.[0-9]+\.[0-9]+)`,
	).FindStringSubmatch(validator)
	if len(validatorMatch) != 2 {
		t.Fatal("Renovate config validator must carry an explicit Renovate version")
	}
	if imageMatch[1] != validatorMatch[1] {
		t.Fatalf(
			"Renovate runtime version %s does not match validator version %s",
			imageMatch[1],
			validatorMatch[1],
		)
	}

	config := findDoc(t, yamlDocs(t, runfilePath(manifest)), "ConfigMap", "renovate")
	script := stringValue(mapValue(config["data"])["run.sh"])
	for _, want := range []string{
		"set -euo pipefail",
		"openssl dgst -sha256",
		"/app/installations/${RENOVATE_GITHUB_APP_INSTALLATION_ID}/access_tokens",
		`jq -er '.token'`,
	} {
		assertTextContains(t, script, want, "renovate auth wrapper")
	}
	if !strings.HasSuffix(strings.TrimSpace(script), "renovate\nbeat") {
		t.Fatal("renovate wrapper must heartbeat only after Renovate exits successfully")
	}
}

func TestRenovateAppSecretStaysNamespaceScoped(t *testing.T) {
	const secrets = "src/infrastructure/deployments/guardian/imageops/secrets.yaml"
	source := readText(t, runfilePath(secrets))

	for _, want := range []string{
		"name: renovate-github-app",
		`githubAppID: "4260384"`,
		`githubAppInstallationID: "145549950"`,
		"key: guardian/guardian-mgmt/guardian-imageops/renovate/github-app",
		"property: githubAppPrivateKey",
	} {
		assertTextContains(t, source, want, secrets)
	}
}
