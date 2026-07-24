package tests

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The persona ladder is declared in three places that must agree: the manifest
// that creates the Keycloak identities and binds their authority, the Go binary
// that writes the kubeconfig exec credential, and the aspect task that exposes
// `--persona` to an operator. A name present in one and missing from another
// fails in the least helpful way possible — an operator authenticates
// successfully and then discovers the session has no authority, or worse, more
// than the rung they asked for. This test is the seam that keeps the three
// honest, and it is the reason adding a persona stays a small, safe edit.
var (
	axlPersonaEntry = regexp.MustCompile(`"([a-z-]+)":\s*"(platform-[a-z-]+)"`)
	goPersonaEntry  = regexp.MustCompile(`"([a-z-]+)":\s*\{user:\s*"(platform-[a-z-]+)"`)
)

func personaMap(t *testing.T, path string, re *regexp.Regexp, section string) map[string]string {
	t.Helper()

	raw, err := os.ReadFile(runfilePath(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	body := string(raw)
	start := strings.Index(body, section)
	if start < 0 {
		t.Fatalf("%s no longer contains %q; the persona ladder lockstep cannot be checked", path, section)
	}
	body = body[start:]
	end := len(body)
	if closing := strings.Index(body, "\n}\n"); closing > 0 {
		end = closing
	}
	found := map[string]string{}
	for _, match := range re.FindAllStringSubmatch(body[:end], -1) {
		found[match[1]] = match[2]
	}
	if len(found) == 0 {
		t.Fatalf("%s: no persona entries parsed out of %s", path, section)
	}
	return found
}

func TestPersonaLadderTaskLockstep(t *testing.T) {
	fromAXL := personaMap(t, ".aspect/tasks/infra.axl", axlPersonaEntry, "PERSONA_USERS = {")
	fromGo := personaMap(t, "src/infrastructure/cmd/guardian_auth/main.go", goPersonaEntry, "var personas = map[string]persona{")

	if len(fromAXL) != len(fromGo) {
		t.Fatalf("persona counts differ: infra.axl has %d (%v), guardian_auth has %d (%v)",
			len(fromAXL), sortedKeys(fromAXL), len(fromGo), sortedKeys(fromGo))
	}
	for name, user := range fromAXL {
		if fromGo[name] != user {
			t.Fatalf("persona %s maps to %q in infra.axl but %q in guardian_auth", name, user, fromGo[name])
		}
	}

	// Every persona must resolve to a Keycloak identity the manifest actually
	// declares, or the device login succeeds against a user that does not exist.
	docs := yamlDocs(t, runfilePath("src/infrastructure/base/cozystack/platform-admins.yaml"))
	declared := map[string]bool{}
	for _, doc := range docs {
		if stringValue(doc["kind"]) == "KeycloakRealmUser" {
			declared[stringValue(mapValue(doc["spec"])["username"])] = true
		}
	}
	for name, user := range fromAXL {
		if !declared[user] {
			t.Fatalf("persona %s resolves to %s, which platform-admins.yaml does not declare", name, user)
		}
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
