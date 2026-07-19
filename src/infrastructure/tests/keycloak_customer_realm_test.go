package tests

import (
	"encoding/json"
	"os"
	"testing"
)

func TestCustomerIdentityRealmConformance(t *testing.T) {
	t.Parallel()

	const root = "src/infrastructure/deployments/iam/prod/"
	listed := false
	for _, doc := range yamlDocs(t, runfilePath(root+"kustomization.yaml")) {
		for _, resource := range sliceValue(doc["resources"]) {
			if stringValue(resource) == "realm-reconciler.yaml" {
				listed = true
			}
		}
	}
	if !listed {
		t.Fatal("IAM kustomization does not apply realm-reconciler.yaml")
	}

	var realm struct {
		Realm                string `json:"realm"`
		OrganizationsEnabled *bool  `json:"organizationsEnabled"`
		RegistrationAllowed  bool   `json:"registrationAllowed"`
		LoginWithEmail       bool   `json:"loginWithEmailAllowed"`
		DuplicateEmails      bool   `json:"duplicateEmailsAllowed"`
		Clients              []struct {
			ClientID                 string            `json:"clientId"`
			PublicClient             bool              `json:"publicClient"`
			StandardFlowEnabled      bool              `json:"standardFlowEnabled"`
			DirectAccessGrantsEnabled bool              `json:"directAccessGrantsEnabled"`
			RedirectURIs             []string          `json:"redirectUris"`
			Attributes               map[string]string `json:"attributes"`
		} `json:"clients"`
		Users []json.RawMessage `json:"users"`
	}
	var github struct {
		Alias                string `json:"alias"`
		TrustEmail           bool   `json:"trustEmail"`
		StoreToken           bool   `json:"storeToken"`
		LinkOnly             bool   `json:"linkOnly"`
		FirstBrokerLoginFlow string `json:"firstBrokerLoginFlowAlias"`
		Config               struct {
			SyncMode string `json:"syncMode"`
		} `json:"config"`
	}
	for _, doc := range yamlDocs(t, runfilePath(root+"realm-configmap.yaml")) {
		data := mapValue(doc["data"])
		if raw := stringValue(data["guardian-realm.json"]); raw != "" {
			if err := json.Unmarshal([]byte(raw), &realm); err != nil {
				t.Fatalf("decode Guardian realm JSON: %v", err)
			}
		}
		if raw := stringValue(data["github.json"]); raw != "" {
			if err := json.Unmarshal([]byte(raw), &github); err != nil {
				t.Fatalf("decode GitHub broker JSON: %v", err)
			}
		}
	}
	if realm.Realm != "guardianintelligence.org" {
		t.Fatalf("realm = %q", realm.Realm)
	}
	if realm.OrganizationsEnabled != nil {
		t.Fatal("login realm must not embed organization authorization")
	}
	if realm.RegistrationAllowed || realm.LoginWithEmail || realm.DuplicateEmails {
		t.Fatal("Guardian realm must not register, resolve, or merge accounts by email")
	}
	if len(realm.Users) != 0 {
		t.Fatal("login realm must not ship synthetic users")
	}
	var postflightFound bool
	for _, client := range realm.Clients {
		if client.ClientID != "postflight-web" {
			continue
		}
		postflightFound = true
		if client.PublicClient || !client.StandardFlowEnabled || client.DirectAccessGrantsEnabled {
			t.Fatal("Postflight must be a confidential authorization-code client")
		}
		if len(client.RedirectURIs) != 1 ||
			client.RedirectURIs[0] != "https://guardianintelligence.org/postflight/auth/callback" {
			t.Fatalf("Postflight redirect URIs = %#v", client.RedirectURIs)
		}
		if client.Attributes["pkce.code.challenge.method"] != "S256" {
			t.Fatal("Postflight client must require PKCE S256")
		}
	}
	if !postflightFound {
		t.Fatal("Postflight client is missing")
	}
	if github.Alias != "github" || github.TrustEmail || github.StoreToken || github.LinkOnly ||
		github.FirstBrokerLoginFlow != "first broker login" || github.Config.SyncMode != "IMPORT" {
		t.Fatalf("GitHub broker = %#v", github)
	}

	reconciler, err := os.ReadFile(runfilePath(root + "realm-reconciler.yaml"))
	if err != nil {
		t.Fatalf("read realm reconciler: %v", err)
	}
	assertTextContains(t, string(reconciler), `realm=guardianintelligence.org`,
		"realm reconciler must target the Guardian realm")
	assertTextContains(t, string(reconciler), `for provider in /providers/*.json`,
		"realm reconciler must reconcile providers from data files")
	assertTextContains(t, string(reconciler), `name: KC_CLI_PASSWORD`,
		"realm reconciler must pass the admin password through Keycloak CLI's environment")
	assertTextNotContains(t, string(reconciler), `--password`,
		"realm reconciler must not expose the admin password in a child process argument")
	assertTextNotContains(t, string(reconciler), `organizationsEnabled`,
		"login reconciliation must not depend on Keycloak Organizations")

	canary, err := os.ReadFile(runfilePath(root + "login-canary.yaml"))
	if err != nil {
		t.Fatalf("read Guardian login canary: %v", err)
	}
	assertTextContains(t, string(canary), `ghcr.io/guardian-intelligence/login-canary@sha256:`,
		"Guardian login canary must run the signed browser image")
	assertTextContains(t, string(canary), `value: https://guardianintelligence.org/postflight`,
		"Guardian login canary must start at the public Postflight route")
	assertTextContains(t, string(canary), `name: GITHUB_CANARY_TOTP_SECRET`,
		"Guardian login canary must exercise GitHub MFA")
	assertTextNotContains(t, string(canary), `grant_type=password`,
		"Guardian login canary must not use a password grant")
	assertTextNotContains(t, string(canary), `KC_ADMIN`,
		"Guardian login canary must not use Keycloak administration")
}
