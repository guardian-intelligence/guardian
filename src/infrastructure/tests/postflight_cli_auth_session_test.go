package tests

import (
	"regexp"
	"strings"
	"testing"
)

// `postflight auth status` and `postflight auth logout` answer for the session
// at the issuer, so the only thing that can catch them regressing is a canary
// that runs the shipped binary against a real one. The deep-test runner does
// that with credentials it forges, and it can only forge them for the issuer
// the binary would have used itself: point AUTH_ISSUER anywhere else and the
// checks go green against a server no user ever talks to. Nothing at runtime
// couples the two — the CLI reads the issuer out of the credential file, which
// is exactly the file the canary writes — so this test is the coupling.
func TestDeeptestAuthIssuerMatchesTheCliDefault(t *testing.T) {
	const (
		cliMain = "src/products/postflight-cli/src/main.rs"
		runner  = "src/infrastructure/deployments/guardian/promotion/cli-deeptest-runner.yaml"
	)

	source := readText(t, runfilePath(cliMain))
	match := regexp.MustCompile(`(?m)^(?:pub )?const DEFAULT_ISSUER: &str = "([^"]+)";`).FindStringSubmatch(source)
	if match == nil {
		t.Fatalf("%s no longer defines DEFAULT_ISSUER; the deep test forges credentials for it", cliMain)
	}
	issuer := match[1]

	found := ""
	for _, doc := range yamlDocs(t, runfilePath(runner)) {
		if stringValue(doc["kind"]) != "CronJob" {
			continue
		}
		podSpec := mapValue(mapValue(mapValue(mapValue(mapValue(doc["spec"])["jobTemplate"])["spec"])["template"])["spec"])
		for _, container := range sliceValue(podSpec["containers"]) {
			for _, env := range sliceValue(mapValue(container)["env"]) {
				if stringValue(mapValue(env)["name"]) == "AUTH_ISSUER" {
					found = stringValue(mapValue(env)["value"])
				}
			}
		}
	}
	if found == "" {
		t.Fatalf("cli-deeptest-runner declares no AUTH_ISSUER; the session checks would forge credentials for nothing")
	}
	if found != issuer {
		t.Fatalf("cli-deeptest-runner forges credentials for %s but the CLI signs in to %s: move them together", found, issuer)
	}
}

// The three session checks are the whole point of running the binary against a
// live issuer, and each guards a distinct way the verbs decay: believing the
// file on disk, treating an unreachable issuer as a sign-out, and leaving a
// token behind on the machine. A check dropped from the script takes its
// alerting with it and leaves guardian_cli_deeptest_pass green.
func TestDeeptestRecordsTheAuthSessionChecks(t *testing.T) {
	const runner = "src/infrastructure/deployments/guardian/promotion/cli-deeptest-runner.yaml"

	script := readText(t, runfilePath(runner))
	for _, check := range []string{"auth_status_live", "auth_status_offline", "auth_logout"} {
		if !strings.Contains(script, "record "+check+" 1") || !strings.Contains(script, "record "+check+" 0") {
			t.Fatalf("cli-deeptest-runner no longer records the %s check both ways", check)
		}
	}
}
