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
// that writes the credential, and the aspect task that exposes `--persona` to an
// operator. A name present in one and missing from another fails in the least
// helpful way possible — an operator authenticates successfully and then
// discovers the session has no authority, or worse, more than the rung they
// asked for. This test is the seam that keeps the three honest, and it is the
// reason adding a persona stays a small, safe edit.
var (
	axlPersonaEntry = regexp.MustCompile(`"([a-z-]+)":\s*"([a-z-]*)"`)
	goPersonaEntry  = regexp.MustCompile(`"([a-z-]+)":\s*\{([^}]*)\}`)
	goPersonaUser   = regexp.MustCompile(`user:\s*"([a-z-]*)"`)
)

func personaSection(t *testing.T, path, section string) string {
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
	if closing := strings.Index(body, "\n}\n"); closing > 0 {
		body = body[:closing]
	}
	return body
}

// A rung with no username is a breakglass rung: it authenticates with x509 from
// the custody bundle and deliberately has no Keycloak identity, so that it still
// works when Keycloak is the thing that is down.
func personaUsersFromAXL(t *testing.T) map[string]string {
	t.Helper()

	found := map[string]string{}
	section := personaSection(t, ".aspect/tasks/infra.axl", "PERSONA_USERS = {")
	for _, match := range axlPersonaEntry.FindAllStringSubmatch(section, -1) {
		found[match[1]] = match[2]
	}
	return found
}

func personaUsersFromGo(t *testing.T) map[string]string {
	t.Helper()

	found := map[string]string{}
	section := personaSection(t, "src/infrastructure/cmd/guardian_auth/main.go", "var personas = map[string]persona{")
	for _, match := range goPersonaEntry.FindAllStringSubmatch(section, -1) {
		user := ""
		if u := goPersonaUser.FindStringSubmatch(match[2]); u != nil {
			user = u[1]
		}
		found[match[1]] = user
	}
	return found
}

func TestPersonaLadderTaskLockstep(t *testing.T) {
	fromAXL := personaUsersFromAXL(t)
	fromGo := personaUsersFromGo(t)
	if len(fromAXL) == 0 || len(fromGo) == 0 {
		t.Fatalf("parsed %d personas from infra.axl and %d from guardian_auth; both must be non-empty",
			len(fromAXL), len(fromGo))
	}
	if len(fromAXL) != len(fromGo) {
		t.Fatalf("persona counts differ: infra.axl has %d (%v), guardian_auth has %d (%v)",
			len(fromAXL), sortedKeys(fromAXL), len(fromGo), sortedKeys(fromGo))
	}
	for name, user := range fromAXL {
		got, ok := fromGo[name]
		if !ok {
			t.Fatalf("persona %s exists in infra.axl but not in guardian_auth", name)
		}
		if got != user {
			t.Fatalf("persona %s maps to %q in infra.axl but %q in guardian_auth", name, user, got)
		}
	}

	// Exactly one rung authenticates without Keycloak. Two would mean a second
	// untracked path to the cluster; none would mean the ladder cannot be
	// climbed while Keycloak is down.
	var breakglass []string
	for name, user := range fromAXL {
		if user == "" {
			breakglass = append(breakglass, name)
		}
	}
	sort.Strings(breakglass)
	if len(breakglass) != 1 || breakglass[0] != "root" {
		t.Fatalf("breakglass rungs = %v, want exactly [root]", breakglass)
	}

	// Every Keycloak-backed persona must resolve to an identity the manifest
	// declares, or the device login succeeds against a user that does not exist.
	docs := yamlDocs(t, runfilePath("src/infrastructure/base/cozystack-identities/platform-admins.yaml"))
	declared := map[string]bool{}
	for _, doc := range docs {
		if stringValue(doc["kind"]) == "KeycloakRealmUser" {
			declared[stringValue(mapValue(doc["spec"])["username"])] = true
		}
	}
	for name, user := range fromAXL {
		if user == "" {
			continue
		}
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
