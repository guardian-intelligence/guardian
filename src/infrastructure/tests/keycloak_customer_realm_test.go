package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
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
		Realm                   string `json:"realm"`
		OrganizationsEnabled    *bool  `json:"organizationsEnabled"`
		RegistrationAllowed     bool   `json:"registrationAllowed"`
		LoginWithEmail          bool   `json:"loginWithEmailAllowed"`
		DuplicateEmails         bool   `json:"duplicateEmailsAllowed"`
		AdminPermissionsEnabled bool   `json:"adminPermissionsEnabled"`
		AdminEventsEnabled      bool   `json:"adminEventsEnabled"`
		Groups                  []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"groups"`
		Clients             []keycloakClientRepresentation `json:"clients"`
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
		Components map[string][]struct {
			ProviderID string              `json:"providerId"`
			Config     map[string][]string `json:"config"`
		} `json:"components"`
		Users []struct {
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
	settingsFiles := map[string]string{}
	clientJSON := map[string]string{}
	providerJSON := map[string]string{}
	mapperJSON := map[string]string{}
	workflowJSON := map[string]string{}
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
			for key, value := range data {
				settingsFiles[key] = stringValue(value)
			}
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
		case "keycloak-identity-provider-mappers":
			for key, value := range data {
				mapperJSON[key] = stringValue(value)
			}
		case "keycloak-workflows":
			for key, value := range data {
				workflowJSON[key] = stringValue(value)
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
	if !realm.AdminPermissionsEnabled {
		t.Fatal("fine-grained admin permissions scope the canary janitor and must stay enabled")
	}
	if !realm.AdminEventsEnabled {
		t.Fatal("admin events are the audit trail for user-store administration and must stay enabled")
	}
	if len(realm.Groups) != 1 || realm.Groups[0].Name != "canary-principals" ||
		realm.Groups[0].Path != "/canary-principals" || realm.Groups[0].ID == "" {
		t.Fatalf("realm groups = %#v, want only canary-principals with a pinned id", realm.Groups)
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
	delete(importedSettings, "components")
	delete(importedSettings, "users")
	delete(importedSettings, "groups")
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

	profileComponents := realm.Components["org.keycloak.userprofile.UserProfileProvider"]
	if len(profileComponents) != 1 || profileComponents[0].ProviderID != "declarative-user-profile" ||
		len(profileComponents[0].Config["kc.user.profile.config"]) != 1 {
		t.Fatalf("imported user profile components = %#v", profileComponents)
	}
	var importedProfile, desiredProfile map[string]any
	if err := json.Unmarshal([]byte(profileComponents[0].Config["kc.user.profile.config"][0]), &importedProfile); err != nil {
		t.Fatalf("decode imported user profile: %v", err)
	}
	if err := json.Unmarshal([]byte(settingsFiles["up-config.json"]), &desiredProfile); err != nil {
		t.Fatalf("decode steady-state user profile: %v", err)
	}
	if !reflect.DeepEqual(importedProfile, desiredProfile) {
		t.Fatal("startup user profile differs from steady-state user profile")
	}
	var profile struct {
		Attributes []struct {
			Name        string          `json:"name"`
			Required    json.RawMessage `json:"required"`
			Permissions struct {
				View []string `json:"view"`
				Edit []string `json:"edit"`
			} `json:"permissions"`
		} `json:"attributes"`
	}
	if err := json.Unmarshal([]byte(settingsFiles["up-config.json"]), &profile); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, attribute := range profile.Attributes {
		names[attribute.Name] = true
		if (attribute.Name == "firstName" || attribute.Name == "lastName") && attribute.Required != nil {
			t.Fatalf("%s must be optional: a brokered GitHub account has no guaranteed name", attribute.Name)
		}
		if attribute.Name == "github_id" {
			for _, role := range append(attribute.Permissions.View, attribute.Permissions.Edit...) {
				if role != "admin" {
					t.Fatal("github_id selects canary-fleet identities: a user-writable value would let anyone impersonate the fleet at signup")
				}
			}
		}
	}
	for _, name := range []string{"username", "email", "firstName", "lastName", "github_id"} {
		if !names[name] {
			t.Fatalf("user profile is missing attribute %q", name)
		}
	}

	importedClients := map[string]keycloakClientRepresentation{}
	for _, client := range realm.Clients {
		importedClients[client.ClientID] = client
	}
	if len(importedClients) != 5 || len(clientJSON) != 5 {
		t.Fatalf("managed clients: import=%d steady-state=%d, want 5", len(importedClients), len(clientJSON))
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
	janitor := importedClients["guardian-canary-janitor"]
	if janitor.Secret != "${CANARY_JANITOR_CLIENT_SECRET}" || janitor.PublicClient ||
		!janitor.ServiceAccountsEnabled || janitor.FullScopeAllowed ||
		janitor.StandardFlowEnabled || janitor.DirectAccessGrantsEnabled {
		t.Fatal("canary janitor must be a confidential service account with no login flows")
	}
	var janitorDesired keycloakClientRepresentation
	if err := json.Unmarshal([]byte(clientJSON["guardian-canary-janitor.json"]), &janitorDesired); err != nil {
		t.Fatal(err)
	}
	if janitorDesired.Secret != "${vault.canary-janitor-client-secret}" {
		t.Fatal("canary janitor client secret must remain a Vault SPI reference")
	}
	// The janitor's entire admin capability is the fine-grained grant the
	// reconciler maintains on the canary group; a realm-management role here
	// would widen it to the whole user store.
	if len(realm.Users) != 1 ||
		realm.Users[0].Username != "service-account-guardian-realm-reconciler" ||
		realm.Users[0].ServiceAccountClientID != "guardian-realm-reconciler" {
		t.Fatalf("realm service accounts = %#v, want only the reconciler: every other service account starts with zero roles", realm.Users)
	}
	// The workflows admin API hard-requires the realm-admin composite
	// (WorkflowsResource calls auth.requireRealmAdmin()); no set of
	// individual realm-management roles satisfies it.
	gotRoles := append([]string(nil), realm.Users[0].ClientRoles["realm-management"]...)
	sort.Strings(gotRoles)
	wantRoles := []string{"realm-admin"}
	if !reflect.DeepEqual(gotRoles, wantRoles) {
		t.Fatalf("realm reconciler roles = %#v, want %#v", gotRoles, wantRoles)
	}

	if github.Alias != "github" || github.TrustEmail || github.StoreToken || github.LinkOnly ||
		github.FirstBrokerLoginFlow != "broker-create-user-only" || github.Config.SyncMode != "IMPORT" {
		t.Fatalf("GitHub broker = %#v", github)
	}
	registry := yamlDocs(t, runfilePath("src/infrastructure/deployments/iam/github-oauth-apps.yaml"))[0]
	prod := mapValue(mapValue(registry["stages"])["prod"])
	if github.Config.ClientID != stringValue(prod["githubClientID"]) ||
		realm.Realm != stringValue(prod["keycloakRealm"]) ||
		github.Alias != stringValue(prod["keycloakIdpAlias"]) {
		t.Fatal("production GitHub provider differs from the OAuth App registry")
	}

	var idMapper struct {
		Name   string `json:"name"`
		Alias  string `json:"identityProviderAlias"`
		Mapper string `json:"identityProviderMapper"`
		Config struct {
			JSONField     string `json:"jsonField"`
			UserAttribute string `json:"userAttribute"`
			SyncMode      string `json:"syncMode"`
		} `json:"config"`
	}
	if err := json.Unmarshal([]byte(mapperJSON["github--github-id.json"]), &idMapper); err != nil {
		t.Fatalf("decode github-id broker mapper: %v", err)
	}
	if idMapper.Name != "github-id" || idMapper.Alias != "github" ||
		idMapper.Mapper != "github-user-attribute-mapper" {
		t.Fatalf("github-id broker mapper = %#v", idMapper)
	}
	if idMapper.Config.JSONField != "id" || idMapper.Config.UserAttribute != "github_id" {
		t.Fatal("fleet identity must key on GitHub's immutable numeric id: logins are re-registrable")
	}
	if idMapper.Config.SyncMode != "FORCE" {
		t.Fatal("github_id must re-stamp at every sign-in so existing users backfill")
	}
	for name, raw := range mapperJSON {
		alias, _, ok := strings.Cut(name, "--")
		if !ok {
			t.Fatalf("broker mapper file %q must be named <alias>--<name>.json", name)
		}
		if _, exists := providerJSON[alias+".json"]; !exists {
			t.Fatalf("broker mapper file %q targets undeclared provider %q", name, alias)
		}
		_ = raw
	}

	var enrollment struct {
		Name  string `json:"name"`
		On    string `json:"on"`
		If    string `json:"if"`
		Steps []struct {
			Uses string `json:"uses"`
			With struct {
				Group string `json:"group"`
			} `json:"with"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(workflowJSON["enroll-canary-principals.json"]), &enrollment); err != nil {
		t.Fatalf("decode enrollment workflow: %v", err)
	}
	if enrollment.Name != "enroll-canary-principals" || enrollment.On != "user-created" {
		t.Fatalf("enrollment workflow = %#v", enrollment)
	}
	fleetOnly := regexp.MustCompile(`^has-user-attribute\(github_id:[0-9]+\)( or has-user-attribute\(github_id:[0-9]+\))*$`)
	if !fleetOnly.MatchString(enrollment.If) {
		t.Fatalf("enrollment condition %q must be an or-chain of numeric github_id terms and nothing else", enrollment.If)
	}
	if len(enrollment.Steps) != 1 || enrollment.Steps[0].Uses != "join-group" ||
		enrollment.Steps[0].With.Group != "canary-principals" {
		t.Fatalf("enrollment workflow steps = %#v, want exactly one join-group into canary-principals", enrollment.Steps)
	}
	if len(workflowJSON) != 1 {
		t.Fatalf("realm workflows = %d files: each new workflow needs its own conformance ruling", len(workflowJSON))
	}

	// The guardian-bounce theme is what closes the GitHub-deny dead end:
	// Keycloak's login page bounces back to the product surface instead of
	// rendering as a dead end, and the device-flow terminal pages bounce to
	// the product's approval surfaces. The realm must keep pinning it —
	// removing the theme silently reopens visible Keycloak UI.
	if !strings.Contains(realmJSON, `"loginTheme": "guardian-bounce"`) {
		t.Fatal(`the realm must pin "loginTheme": "guardian-bounce": without it Keycloak renders its own pages (GitHub-deny dead end included)`)
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
	for name, raw := range mapperJSON {
		stateFiles["provider-mappers/"+name] = raw
	}
	for name, raw := range workflowJSON {
		stateFiles["workflows/"+name] = raw
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
	assertTextContains(t, string(reconciler), `create authentication/flows`,
		"realm reconciler must create the headless first-broker-login flow before the provider loop binds it")
	assertTextContains(t, string(reconciler), `if test -z "$exec_id"`,
		"realm reconciler must guard the flow execution independently of the flow so a run that died between the creates converges on retry")
	assertTextContains(t, string(reconciler), `update users/profile`,
		"realm reconciler must reconcile the user profile, which no realm update carries")
	assertTextContains(t, string(reconciler), `for mapper in /provider-mappers/*.json`,
		"realm reconciler must apply broker mappers from data files: no realm update or provider representation carries them")
	assertTextContains(t, string(reconciler), `for workflow in /workflows/*.json`,
		"realm reconciler must sync workflows from data files: they are invisible to realm import and export")
	assertTextContains(t, string(reconciler), `create groups`,
		"realm reconciler must converge the canary group on the live realm, which no realm update creates")
	assertTextContains(t, string(reconciler), `\"scopes\":[\"view-members\",\"manage-members\"]`,
		"the janitor grant must be exactly view-members and manage-members: manage-membership would open the add-then-delete escalation")
	assertTextContains(t, string(reconciler), `permission/scope/$perm_id`,
		"realm reconciler must rewrite the janitor grant every run so a drifted grant shape self-heals")
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
	assertTextContains(t, string(secrets), `name: keycloak-canary-janitor-client`,
		"canary janitor must have a generated steady-state client secret")
	assertTextContains(t, string(secrets), `keycloak/org-canary-github`,
		"the org-owning canary account credentials must resolve from OpenBao")
	assertTextNotContains(t, string(secrets), `keycloak-admin-bootstrap`,
		"temporary Keycloak bootstrap administrators must not be steady-state secrets")

	canary, err := os.ReadFile(runfilePath(root + "journey-canary.yaml"))
	if err != nil {
		t.Fatalf("read Guardian journey canary: %v", err)
	}
	assertTextContains(t, string(canary), `ghcr.io/guardian-intelligence/canary-journeys@sha256:`,
		"Guardian journey canary must run the signed browser image")
	assertTextContains(t, string(canary), `value: https://guardianintelligence.org/postflight`,
		"Guardian journey canary must start at the public Postflight route")
	assertTextContains(t, string(canary), `schedule: "*/15 * * * *"`,
		"Guardian journey canary must stay below GitHub's per-user OAuth token issuance limit")
	assertTextContains(t, string(canary), `name: GITHUB_CANARY_TOTP_SECRET`,
		"Guardian journey canary must exercise GitHub MFA")
	assertTextNotContains(t, string(canary), `grant_type=password`,
		"Guardian journey canary must not use a password grant")
	assertTextNotContains(t, string(canary), `KC_ADMIN`,
		"Guardian journey canary must not use Keycloak administration")

	promotion, err := os.ReadFile(runfilePath(
		"src/infrastructure/deployments/guardian/promotion/pipelines/iam-journey-canary-stage-prod.yaml",
	))
	if err != nil {
		t.Fatalf("read Guardian journey canary promotion: %v", err)
	}
	assertTextContains(t, string(promotion),
		`path: ./repo/src/infrastructure/deployments/iam/prod/journey-canary.yaml`,
		"journey canary promotion must update the CronJob image")
	assertTextContains(t, string(promotion), `key: releases.journey-canary.prod.image`,
		"journey canary promotion must update the release manifest")
}
