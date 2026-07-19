package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type keycloakClientRepresentation struct {
	ClientID                  string            `json:"clientId"`
	Secret                    string            `json:"secret"`
	PublicClient              bool              `json:"publicClient"`
	StandardFlowEnabled       bool              `json:"standardFlowEnabled"`
	DirectAccessGrantsEnabled bool              `json:"directAccessGrantsEnabled"`
	ServiceAccountsEnabled    bool              `json:"serviceAccountsEnabled"`
	FullScopeAllowed          bool              `json:"fullScopeAllowed"`
	RedirectURIs              []string          `json:"redirectUris"`
	Attributes                map[string]string `json:"attributes"`
}

func TestCustomerIdentityRealmConformance(t *testing.T) {
	t.Parallel()

	const root = "src/infrastructure/deployments/iam/prod/"
	const realmDataKey = "guardianintelligence.org-realm.json"
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
		Clients              []keycloakClientRepresentation `json:"clients"`
		Users                []struct {
			Username               string              `json:"username"`
			ServiceAccountClientID string              `json:"serviceAccountClientId"`
			ClientRoles            map[string][]string `json:"clientRoles"`
		} `json:"users"`
	}
	var github struct {
		Alias                string `json:"alias"`
		TrustEmail           bool   `json:"trustEmail"`
		StoreToken           bool   `json:"storeToken"`
		LinkOnly             bool   `json:"linkOnly"`
		FirstBrokerLoginFlow string `json:"firstBrokerLoginFlowAlias"`
		Config               struct {
			ClientID string `json:"clientId"`
			SyncMode string `json:"syncMode"`
		} `json:"config"`
	}
	var realmJSON, settingsJSON string
	clientJSON := map[string]string{}
	providerJSON := map[string]string{}
	for _, doc := range yamlDocs(t, runfilePath(root+"realm-configmap.yaml")) {
		name := stringValue(mapValue(doc["metadata"])["name"])
		data := mapValue(doc["data"])
		switch name {
		case "keycloak-realm-guardian":
			realmJSON = stringValue(data[realmDataKey])
			if err := json.Unmarshal([]byte(realmJSON), &realm); err != nil {
				t.Fatalf("decode Guardian realm JSON: %v", err)
			}
		case "keycloak-realm-settings":
			settingsJSON = stringValue(data["guardianintelligence.org.json"])
		case "keycloak-clients":
			for key, value := range data {
				clientJSON[key] = stringValue(value)
			}
		case "keycloak-identity-providers":
			for key, value := range data {
				providerJSON[key] = stringValue(value)
			}
			if err := json.Unmarshal([]byte(providerJSON["github.json"]), &github); err != nil {
				t.Fatalf("decode GitHub broker JSON: %v", err)
			}
		}
	}
	if realm.Realm != "guardianintelligence.org" {
		t.Fatalf("realm = %q", realm.Realm)
	}
	if realmDataKey != realm.Realm+"-realm.json" {
		t.Fatalf("realm import filename = %q, want %q", realmDataKey, realm.Realm+"-realm.json")
	}
	if realm.OrganizationsEnabled != nil {
		t.Fatal("login realm must not embed organization authorization")
	}
	if realm.RegistrationAllowed || realm.LoginWithEmail || realm.DuplicateEmails {
		t.Fatal("Guardian realm must not register, resolve, or merge accounts by email")
	}

	var importedSettings, desiredSettings map[string]any
	if err := json.Unmarshal([]byte(realmJSON), &importedSettings); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(settingsJSON), &desiredSettings); err != nil {
		t.Fatalf("decode steady-state realm settings: %v", err)
	}
	delete(importedSettings, "clients")
	delete(importedSettings, "users")
	if !reflect.DeepEqual(importedSettings, desiredSettings) {
		t.Fatal("startup realm settings differ from steady-state realm settings")
	}

	importedClients := map[string]keycloakClientRepresentation{}
	for _, client := range realm.Clients {
		importedClients[client.ClientID] = client
	}
	if len(importedClients) != 3 || len(clientJSON) != 3 {
		t.Fatalf("managed clients: import=%d steady-state=%d, want 3", len(importedClients), len(clientJSON))
	}
	for filename, raw := range clientJSON {
		var desired keycloakClientRepresentation
		if err := json.Unmarshal([]byte(raw), &desired); err != nil {
			t.Fatalf("decode client %s: %v", filename, err)
		}
		imported, ok := importedClients[desired.ClientID]
		if !ok {
			t.Fatalf("steady-state client %q is missing from startup import", desired.ClientID)
		}
		imported.Secret = ""
		desired.Secret = ""
		if !reflect.DeepEqual(imported, desired) {
			t.Fatalf("startup and steady-state client %q differ", desired.ClientID)
		}
	}
	postflight := importedClients["postflight-web"]
	if postflight.PublicClient || !postflight.StandardFlowEnabled || postflight.DirectAccessGrantsEnabled {
		t.Fatal("Postflight must be a confidential authorization-code client")
	}
	if len(postflight.RedirectURIs) != 1 ||
		postflight.RedirectURIs[0] != "https://guardianintelligence.org/postflight/auth/callback" {
		t.Fatalf("Postflight redirect URIs = %#v", postflight.RedirectURIs)
	}
	if postflight.Attributes["pkce.code.challenge.method"] != "S256" {
		t.Fatal("Postflight client must require PKCE S256")
	}
	var postflightDesired keycloakClientRepresentation
	if err := json.Unmarshal([]byte(clientJSON["postflight-web.json"]), &postflightDesired); err != nil {
		t.Fatal(err)
	}
	if postflightDesired.Secret != "${vault.postflight-client-secret}" {
		t.Fatal("Postflight client secret must remain a Vault SPI reference")
	}

	reconcilerClient := importedClients["guardian-realm-reconciler"]
	if reconcilerClient.Secret != "${REALM_RECONCILER_CLIENT_SECRET}" ||
		!reconcilerClient.ServiceAccountsEnabled || !reconcilerClient.FullScopeAllowed {
		t.Fatal("realm reconciler must cold-import as a scoped confidential service account")
	}
	if len(realm.Users) != 1 ||
		realm.Users[0].Username != "service-account-guardian-realm-reconciler" ||
		realm.Users[0].ServiceAccountClientID != "guardian-realm-reconciler" {
		t.Fatalf("realm service accounts = %#v", realm.Users)
	}
	gotRoles := append([]string(nil), realm.Users[0].ClientRoles["realm-management"]...)
	sort.Strings(gotRoles)
	wantRoles := []string{
		"manage-clients",
		"manage-identity-providers",
		"manage-realm",
		"query-clients",
		"view-clients",
		"view-identity-providers",
		"view-realm",
	}
	if !reflect.DeepEqual(gotRoles, wantRoles) {
		t.Fatalf("realm reconciler roles = %#v, want %#v", gotRoles, wantRoles)
	}

	if github.Alias != "github" || github.TrustEmail || github.StoreToken || github.LinkOnly ||
		github.FirstBrokerLoginFlow != "first broker login" || github.Config.SyncMode != "IMPORT" {
		t.Fatalf("GitHub broker = %#v", github)
	}
	registry := yamlDocs(t, runfilePath("src/infrastructure/deployments/iam/github-oauth-apps.yaml"))[0]
	prod := mapValue(mapValue(registry["stages"])["prod"])
	if github.Config.ClientID != stringValue(prod["githubClientID"]) ||
		realm.Realm != stringValue(prod["keycloakRealm"]) ||
		github.Alias != stringValue(prod["keycloakIdpAlias"]) {
		t.Fatal("production GitHub provider differs from the OAuth App registry")
	}

	stateFiles := map[string]string{
		"realm/" + realmDataKey:                       realmJSON,
		"settings/guardianintelligence.org.json":      settingsJSON,
	}
	for name, raw := range clientJSON {
		stateFiles["clients/"+name] = raw
	}
	for name, raw := range providerJSON {
		stateFiles["providers/"+name] = raw
	}
	stateNames := make([]string, 0, len(stateFiles))
	for name := range stateFiles {
		stateNames = append(stateNames, name)
	}
	sort.Strings(stateNames)
	parts := make([]string, 0, len(stateNames))
	for _, name := range stateNames {
		parts = append(parts, name+"\x00"+stateFiles[name])
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	job := findDoc(t, yamlDocs(t, runfilePath(root+"realm-reconciler.yaml")), "Job", "keycloak-realm-reconciler")
	checksum := stringValue(nestedValue(t, job, "spec", "template", "metadata", "annotations", "checksum/realm-config"))
	if want := hex.EncodeToString(sum[:]); checksum != want {
		t.Fatalf("realm reconciler config checksum = %q, want %q", checksum, want)
	}

	reconciler, err := os.ReadFile(runfilePath(root + "realm-reconciler.yaml"))
	if err != nil {
		t.Fatalf("read realm reconciler: %v", err)
	}
	assertTextContains(t, string(reconciler), `realm=guardianintelligence.org`,
		"realm reconciler must target the Guardian realm")
	assertTextContains(t, string(reconciler), `for client in /clients/*.json`,
		"realm reconciler must reconcile clients from data files")
	assertTextContains(t, string(reconciler), `for provider in /providers/*.json`,
		"realm reconciler must reconcile providers from data files")
	assertTextContains(t, string(reconciler), `name: KC_CLI_CLIENT_SECRET`,
		"realm reconciler must pass its service-account secret through Keycloak CLI's environment")
	assertTextContains(t, string(reconciler), `--client guardian-realm-reconciler`,
		"realm reconciler must authenticate as its realm-scoped service account")
	assertTextNotContains(t, string(reconciler), `KC_ADMIN`,
		"realm reconciler must not depend on a human administrator")
	assertTextNotContains(t, string(reconciler), `KC_CLI_PASSWORD`,
		"realm reconciler must not use password-grant administration")
	assertTextNotContains(t, string(reconciler), `--password`,
		"realm reconciler must not expose a credential in a child process argument")
	assertTextNotContains(t, string(reconciler), `get realms`,
		"realm-scoped reconciliation must not enumerate other realms")
	assertTextNotContains(t, string(reconciler), `delete "realms/`,
		"realm-scoped reconciliation must not delete other realms")
	assertTextNotContains(t, string(reconciler), `organizationsEnabled`,
		"login reconciliation must not depend on Keycloak Organizations")

	secrets, err := os.ReadFile(runfilePath(root + "secrets.yaml"))
	if err != nil {
		t.Fatalf("read IAM secrets: %v", err)
	}
	assertTextContains(t, string(secrets), `name: keycloak-realm-reconciler-client`,
		"realm reconciler must have a generated steady-state client secret")
	assertTextNotContains(t, string(secrets), `keycloak-admin-bootstrap`,
		"temporary Keycloak bootstrap administrators must not be steady-state secrets")

	canary, err := os.ReadFile(runfilePath(root + "login-canary.yaml"))
	if err != nil {
		t.Fatalf("read Guardian login canary: %v", err)
	}
	assertTextContains(t, string(canary), `ghcr.io/guardian-intelligence/login-canary@sha256:`,
		"Guardian login canary must run the signed browser image")
	assertTextContains(t, string(canary), `value: https://guardianintelligence.org/postflight`,
		"Guardian login canary must start at the public Postflight route")
	assertTextContains(t, string(canary), `schedule: "*/15 * * * *"`,
		"Guardian login canary must stay below GitHub's per-user OAuth token issuance limit")
	assertTextContains(t, string(canary), `name: GITHUB_CANARY_TOTP_SECRET`,
		"Guardian login canary must exercise GitHub MFA")
	assertTextNotContains(t, string(canary), `grant_type=password`,
		"Guardian login canary must not use a password grant")
	assertTextNotContains(t, string(canary), `KC_ADMIN`,
		"Guardian login canary must not use Keycloak administration")

	promotion, err := os.ReadFile(runfilePath(
		"src/infrastructure/deployments/guardian/promotion/pipelines/iam-login-canary-stage-prod.yaml",
	))
	if err != nil {
		t.Fatalf("read Guardian login canary promotion: %v", err)
	}
	assertTextContains(t, string(promotion),
		`path: ./repo/src/infrastructure/deployments/iam/prod/login-canary.yaml`,
		"login canary promotion must update the CronJob image")
	assertTextContains(t, string(promotion), `key: releases.login-canary.prod.image`,
		"login canary promotion must update the release manifest")
}
