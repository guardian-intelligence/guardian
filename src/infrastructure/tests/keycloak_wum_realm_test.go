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

func TestWumBounceThemeChecksumRollsTheKeycloakDeployment(t *testing.T) {
	t.Parallel()

	const root = "src/infrastructure/deployments/iam/prod/"
	raw, err := os.ReadFile(runfilePath(root + "wum-bounce-theme.yaml"))
	if err != nil {
		t.Fatalf("read wum-bounce-theme.yaml: %v", err)
	}
	_, data, ok := strings.Cut(string(raw), "\ndata:\n")
	if !ok {
		t.Fatal("wum-bounce-theme.yaml has no top-level data: block")
	}
	sum := sha256.Sum256([]byte(data))
	deployment := findDoc(t, yamlDocs(t, runfilePath(root+"keycloak.yaml")), "Deployment", "keycloak")
	checksum := stringValue(nestedValue(t, deployment,
		"spec", "template", "metadata", "annotations", "guardian.dev/wum-bounce-theme-checksum"))
	if want := hex.EncodeToString(sum[:]); checksum != want {
		t.Fatalf("keycloak.yaml wum-bounce theme checksum = %q, want %q; recompute after editing wum-bounce-theme.yaml: awk '/^data:/{f=1;next} f' wum-bounce-theme.yaml | sha256sum", checksum, want)
	}
}

// The WUM realm holds the game's account model apart from the Guardian
// product realm: players never see a Guardian hostname or concept, and the
// Guardian realm carries no game clients. Same doctrine, separate trust.
func TestWakeUpMythraRealmConformance(t *testing.T) {
	t.Parallel()

	const root = "src/infrastructure/deployments/iam/prod/"
	const realmDataKey = "wakeupmythra.com-realm.json"
	listed := false
	for _, doc := range yamlDocs(t, runfilePath(root+"kustomization.yaml")) {
		for _, resource := range sliceValue(doc["resources"]) {
			if stringValue(resource) == "realm-reconciler-wum.yaml" {
				listed = true
			}
		}
	}
	if !listed {
		t.Fatal("IAM kustomization does not apply realm-reconciler-wum.yaml")
	}

	var realm struct {
		Realm               string            `json:"realm"`
		RegistrationAllowed bool              `json:"registrationAllowed"`
		LoginWithEmail      bool              `json:"loginWithEmailAllowed"`
		DuplicateEmails     bool              `json:"duplicateEmailsAllowed"`
		AdminEventsEnabled  bool              `json:"adminEventsEnabled"`
		Attributes          map[string]string `json:"attributes"`
		Clients             []keycloakClientRepresentation
		AuthenticationFlows []struct {
			Alias      string `json:"alias"`
			ProviderID string `json:"providerId"`
			TopLevel   bool   `json:"topLevel"`
			BuiltIn    bool   `json:"builtIn"`
			Executions []struct {
				Authenticator     string `json:"authenticator"`
				Requirement       string `json:"requirement"`
				AuthenticatorFlow bool   `json:"authenticatorFlow"`
				UserSetupAllowed  bool   `json:"userSetupAllowed"`
			} `json:"authenticationExecutions"`
		} `json:"authenticationFlows"`
		Users []struct {
			Username               string              `json:"username"`
			ServiceAccountClientID string              `json:"serviceAccountClientId"`
			ClientRoles            map[string][]string `json:"clientRoles"`
		} `json:"users"`
	}
	var google struct {
		Alias                string `json:"alias"`
		TrustEmail           bool   `json:"trustEmail"`
		StoreToken           bool   `json:"storeToken"`
		LinkOnly             bool   `json:"linkOnly"`
		FirstBrokerLoginFlow string `json:"firstBrokerLoginFlowAlias"`
		Config               struct {
			ClientID     string `json:"clientId"`
			ClientSecret string `json:"clientSecret"`
			SyncMode     string `json:"syncMode"`
		} `json:"config"`
	}
	var realmJSON, settingsJSON string
	settingsFiles := map[string]string{}
	clientJSON := map[string]string{}
	providerJSON := map[string]string{}
	for _, doc := range yamlDocs(t, runfilePath(root+"realm-wum-configmap.yaml")) {
		name := stringValue(mapValue(doc["metadata"])["name"])
		data := mapValue(doc["data"])
		switch name {
		case "keycloak-realm-wum":
			realmJSON = stringValue(data[realmDataKey])
			if err := json.Unmarshal([]byte(realmJSON), &realm); err != nil {
				t.Fatalf("decode WUM realm JSON: %v", err)
			}
			var clientsOnly struct {
				Clients []keycloakClientRepresentation `json:"clients"`
			}
			if err := json.Unmarshal([]byte(realmJSON), &clientsOnly); err != nil {
				t.Fatal(err)
			}
			realm.Clients = clientsOnly.Clients
		case "keycloak-wum-realm-settings":
			settingsJSON = stringValue(data["wakeupmythra.com.json"])
			for key, value := range data {
				settingsFiles[key] = stringValue(value)
			}
		case "keycloak-wum-clients":
			for key, value := range data {
				clientJSON[key] = stringValue(value)
			}
		case "keycloak-wum-identity-providers":
			for key, value := range data {
				providerJSON[key] = stringValue(value)
			}
			if err := json.Unmarshal([]byte(providerJSON["google.json"]), &google); err != nil {
				t.Fatalf("decode Google broker JSON: %v", err)
			}
		}
	}
	if realm.Realm != "wakeupmythra.com" {
		t.Fatalf("realm = %q", realm.Realm)
	}
	if realmDataKey != realm.Realm+"-realm.json" {
		t.Fatalf("realm import filename = %q, want %q", realmDataKey, realm.Realm+"-realm.json")
	}
	if realm.RegistrationAllowed || realm.LoginWithEmail || realm.DuplicateEmails {
		t.Fatal("WUM realm must not register, resolve, or merge accounts by email")
	}
	if !realm.AdminEventsEnabled {
		t.Fatal("admin events are the audit trail for user-store administration and must stay enabled")
	}
	// The frontendUrl is the whole point of the realm's separate identity:
	// every player-facing URL and every token issuer generates from it.
	if realm.Attributes["frontendUrl"] != "https://auth.wakeupmythra.com" {
		t.Fatalf("realm frontendUrl = %q, want https://auth.wakeupmythra.com", realm.Attributes["frontendUrl"])
	}

	var importedSettings, desiredSettings map[string]any
	if err := json.Unmarshal([]byte(realmJSON), &importedSettings); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(settingsJSON), &desiredSettings); err != nil {
		t.Fatalf("decode steady-state realm settings: %v", err)
	}
	delete(importedSettings, "authenticationFlows")
	delete(importedSettings, "clients")
	delete(importedSettings, "users")
	if !reflect.DeepEqual(importedSettings, desiredSettings) {
		t.Fatal("startup realm settings differ from steady-state realm settings")
	}

	if len(realm.AuthenticationFlows) != 1 {
		t.Fatalf("imported authentication flows = %d, want only the headless first-broker-login flow", len(realm.AuthenticationFlows))
	}
	flow := realm.AuthenticationFlows[0]
	if flow.Alias != "broker-create-user-only" || flow.ProviderID != "basic-flow" || !flow.TopLevel || flow.BuiltIn {
		t.Fatalf("headless first-broker-login flow = %#v", flow)
	}
	if len(flow.Executions) != 1 ||
		flow.Executions[0].Authenticator != "idp-create-user-if-unique" ||
		flow.Executions[0].Requirement != "REQUIRED" ||
		flow.Executions[0].AuthenticatorFlow || flow.Executions[0].UserSetupAllowed {
		t.Fatalf("first broker login must only create-user-if-unique as REQUIRED, got %#v", flow.Executions)
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

	web := importedClients["wake-up-mythra"]
	if !web.PublicClient || !web.StandardFlowEnabled || web.DirectAccessGrantsEnabled || web.ServiceAccountsEnabled {
		t.Fatal("the web client must be a public authorization-code client with no service account")
	}
	if len(web.RedirectURIs) != 1 || web.RedirectURIs[0] != "https://wakeupmythra.com/*" {
		t.Fatalf("web client redirect URIs = %#v", web.RedirectURIs)
	}
	if web.Attributes["pkce.code.challenge.method"] != "S256" {
		t.Fatal("the web client must require PKCE S256")
	}

	loadgen := importedClients["mythra-loadgen"]
	if loadgen.Secret != "${WUM_MYTHRA_LOADGEN_CLIENT_SECRET}" || loadgen.PublicClient ||
		!loadgen.ServiceAccountsEnabled || loadgen.FullScopeAllowed ||
		loadgen.StandardFlowEnabled || loadgen.DirectAccessGrantsEnabled {
		t.Fatal("the load driver must be a confidential service account with no login flows")
	}
	var loadgenDesired keycloakClientRepresentation
	if err := json.Unmarshal([]byte(clientJSON["mythra-loadgen.json"]), &loadgenDesired); err != nil {
		t.Fatal(err)
	}
	if loadgenDesired.Secret != "${vault.mythra-loadgen-client-secret}" {
		t.Fatal("the load driver client secret must remain a Vault SPI reference")
	}

	reconcilerClient := importedClients["wum-realm-reconciler"]
	if reconcilerClient.Secret != "${WUM_REALM_RECONCILER_CLIENT_SECRET}" ||
		!reconcilerClient.ServiceAccountsEnabled || !reconcilerClient.FullScopeAllowed {
		t.Fatal("realm reconciler must cold-import as a scoped confidential service account")
	}
	if len(realm.Users) != 1 ||
		realm.Users[0].Username != "service-account-wum-realm-reconciler" ||
		realm.Users[0].ServiceAccountClientID != "wum-realm-reconciler" {
		t.Fatalf("realm service accounts = %#v, want only the reconciler", realm.Users)
	}
	gotRoles := append([]string(nil), realm.Users[0].ClientRoles["realm-management"]...)
	sort.Strings(gotRoles)
	if !reflect.DeepEqual(gotRoles, []string{"realm-admin"}) {
		t.Fatalf("realm reconciler roles = %#v, want realm-admin", gotRoles)
	}

	if google.Alias != "google" || google.StoreToken || google.LinkOnly ||
		google.FirstBrokerLoginFlow != "broker-create-user-only" || google.Config.SyncMode != "IMPORT" {
		t.Fatalf("Google broker = %#v", google)
	}
	// trustEmail is deliberate for Google (verified addresses only): it is
	// what lets the email_verified admission gate work without realm SMTP.
	if !google.TrustEmail {
		t.Fatal("the Google broker must trust email so email_verified can gate admission without SMTP")
	}
	if google.Config.ClientSecret != "${vault.google-client-secret}" {
		t.Fatal("Google client secret must remain a Vault SPI reference")
	}

	if !strings.Contains(realmJSON, `"loginTheme": "wum-bounce"`) {
		t.Fatal(`the realm must pin "loginTheme": "wum-bounce": without it Keycloak renders its own pages`)
	}

	stateFiles := map[string]string{
		"realm/" + realmDataKey: realmJSON,
	}
	for name, raw := range settingsFiles {
		stateFiles["settings/"+name] = raw
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
	job := findDoc(t, yamlDocs(t, runfilePath(root+"realm-reconciler-wum.yaml")), "Job", "keycloak-realm-reconciler-wum")
	checksum := stringValue(nestedValue(t, job, "spec", "template", "metadata", "annotations", "checksum/realm-config"))
	if want := hex.EncodeToString(sum[:]); checksum != want {
		t.Fatalf("WUM realm reconciler config checksum = %q, want %q", checksum, want)
	}

	reconciler, err := os.ReadFile(runfilePath(root + "realm-reconciler-wum.yaml"))
	if err != nil {
		t.Fatalf("read WUM realm reconciler: %v", err)
	}
	assertTextContains(t, string(reconciler), `realm=wakeupmythra.com`,
		"WUM realm reconciler must target the WUM realm")
	assertTextContains(t, string(reconciler), `--client wum-realm-reconciler`,
		"WUM realm reconciler must authenticate as its realm-scoped service account")
	assertTextContains(t, string(reconciler), `for client in /clients/*.json`,
		"WUM realm reconciler must reconcile clients from data files")
	assertTextContains(t, string(reconciler), `for provider in /providers/*.json`,
		"WUM realm reconciler must reconcile providers from data files")
	assertTextContains(t, string(reconciler), `create authentication/flows`,
		"WUM realm reconciler must create the headless first-broker-login flow before the provider loop binds it")
	assertTextNotContains(t, string(reconciler), `KC_ADMIN`,
		"WUM realm reconciler must not depend on a human administrator")
	assertTextNotContains(t, string(reconciler), `get realms`,
		"realm-scoped reconciliation must not enumerate other realms")
	assertTextNotContains(t, string(reconciler), `delete "realms/`,
		"realm-scoped reconciliation must not delete other realms")

	// The Guardian realm must carry no game clients: the split is the
	// boundary, and a leftover client would quietly keep old tokens valid.
	guardianRaw, err := os.ReadFile(runfilePath(root + "realm-configmap.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	assertTextNotContains(t, string(guardianRaw), `wake-up-mythra`,
		"the Guardian realm must not carry the game's web client")
	assertTextNotContains(t, string(guardianRaw), `mythra-loadgen`,
		"the Guardian realm must not carry the game's load driver")
}
