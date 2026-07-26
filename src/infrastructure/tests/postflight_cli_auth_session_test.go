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

// The device-flow journey canary is the only proof that signing out ends the
// session at the issuer, and it can only prove it about the token the CLI
// actually holds. The scope is what decides that: a device request without
// `openid` mints a plain OAuth access token, and Keycloak's userinfo answers it
// 403 "Missing openid scope" however healthy the session is — which is exactly
// how this drifted into a failing prod canary on 2026-07-26. Nothing at runtime
// ties the two requests together, so this does.
func TestJourneyCanaryRequestsTheScopeTheCliRequests(t *testing.T) {
	const (
		deviceFlow = "src/products/postflight-cli/src/device.rs"
		journey    = "src/products/viteplus-monorepo/packages/canary-journeys/journeys/device-flow.spec.ts"
	)

	source := readText(t, runfilePath(deviceFlow))
	match := regexp.MustCompile(`\("scope",\s*"([^"]+)"\)`).FindStringSubmatch(source)
	if match == nil {
		t.Fatalf("%s no longer names the scope it starts the device grant with", deviceFlow)
	}
	scope := match[1]

	spec := readText(t, runfilePath(journey))
	want := `scope: "` + scope + `"`
	if !strings.Contains(spec, want) {
		t.Fatalf("the device-flow journey canary does not request %s; it would mint a token the CLI never holds", want)
	}
}
