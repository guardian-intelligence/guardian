package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeResolver struct {
	identity workloadIdentity
	err      error
}

func (r fakeResolver) Verify(context.Context, string) (workloadIdentity, error) {
	return r.identity, r.err
}

type mintCall struct {
	namespace      string
	serviceAccount string
	audience       string
	seconds        int64
}

type fakeMinter struct {
	calls []mintCall
	err   error
}

func (m *fakeMinter) Mint(_ context.Context, namespace, serviceAccount, audience string, seconds int64) (mintedToken, error) {
	m.calls = append(m.calls, mintCall{namespace: namespace, serviceAccount: serviceAccount, audience: audience, seconds: seconds})
	if m.err != nil {
		return mintedToken{}, m.err
	}
	return mintedToken{Token: "kubernetes-token", Expiration: time.Date(2026, 8, 14, 19, 0, 0, 0, time.UTC)}, nil
}

func testBroker(identity workloadIdentity, minter *fakeMinter) *broker {
	return &broker{
		cfg: config{
			Namespace:          defaultNamespace,
			KubernetesAudience: defaultKubernetesAudience,
		},
		resolver: fakeResolver{identity: identity},
		minter:   minter,
		limiter:  newRunLimiter(),
	}
}

func request(t *testing.T, app *broker, body, authorization, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/kubernetes", strings.NewReader(body))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	recorder := httptest.NewRecorder()
	app.routes().ServeHTTP(recorder, req)
	return recorder
}

func TestMintSharedPersonasForEveryProvider(t *testing.T) {
	for _, test := range []struct {
		name           string
		provider       string
		body           string
		serviceAccount string
		seconds        int64
	}{
		{name: "Cursor read", provider: "cursor", body: `{}`, serviceAccount: "guardian-cloud-agent-cursor", seconds: 900},
		{name: "Cursor write-basic", provider: "cursor", body: `{"persona":"write-basic"}`, serviceAccount: "guardian-cloud-agent-cursor-write-basic", seconds: 600},
		{name: "Devin read", provider: "devin", body: `{"persona":"read"}`, serviceAccount: "guardian-cloud-agent-devin", seconds: 900},
		{name: "Devin write-basic", provider: "devin", body: `{"persona":"write-basic"}`, serviceAccount: "guardian-cloud-agent-devin-write-basic", seconds: 600},
	} {
		t.Run(test.name, func(t *testing.T) {
			minter := &fakeMinter{}
			app := testBroker(workloadIdentity{Provider: test.provider, RunID: test.provider + "-run", Principal: "principal"}, minter)
			recorder := request(t, app, test.body, "Bearer provider-jwt", "application/json")
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if len(minter.calls) != 1 {
				t.Fatalf("mint calls = %d, want 1", len(minter.calls))
			}
			call := minter.calls[0]
			if call.namespace != defaultNamespace || call.serviceAccount != test.serviceAccount || call.audience != defaultKubernetesAudience || call.seconds != test.seconds {
				t.Fatalf("mint call = %+v", call)
			}
			var response execCredential
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.APIVersion != "client.authentication.k8s.io/v1" || response.Kind != "ExecCredential" || response.Status.Token != "kubernetes-token" {
				t.Fatalf("response = %+v", response)
			}
			if recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", recorder.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestCursorPolicyRequiresManagedOwnerAndCompleteRepositorySet(t *testing.T) {
	policy := cursorPolicy{
		Subject:     defaultCursorSubject,
		OwnerUserID: defaultCursorOwnerUserID,
		Repository:  defaultCursorRepository,
	}
	valid := cursorClaims{
		Subject:         defaultCursorSubject,
		AgentRuntime:    "managed",
		OwnerUserID:     defaultCursorOwnerUserID,
		RepositoryURL:   defaultCursorRepository,
		RepositoryURLs:  []string{defaultCursorRepository},
		RepositoryCount: 1,
		CloudAgentID:    "bc-123",
	}
	identity, err := normalizeCursorIdentity(policy, valid)
	if err != nil || identity.Provider != "cursor" || identity.RunID != "bc-123" {
		t.Fatalf("valid Cursor identity = %+v, %v", identity, err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*cursorClaims)
	}{
		{name: "subject", mutate: func(c *cursorClaims) { c.Subject = "user:other" }},
		{name: "owner", mutate: func(c *cursorClaims) { c.OwnerUserID = "other" }},
		{name: "runtime", mutate: func(c *cursorClaims) { c.AgentRuntime = "local" }},
		{name: "primary repository", mutate: func(c *cursorClaims) { c.RepositoryURL = "github.com/example/other" }},
		{name: "repository count", mutate: func(c *cursorClaims) { c.RepositoryCount = 2 }},
		{name: "repository set", mutate: func(c *cursorClaims) { c.RepositoryURLs = append(c.RepositoryURLs, "github.com/example/other") }},
		{name: "run id", mutate: func(c *cursorClaims) { c.CloudAgentID = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			claims := valid
			claims.RepositoryURLs = append([]string(nil), valid.RepositoryURLs...)
			test.mutate(&claims)
			if _, err := normalizeCursorIdentity(policy, claims); err == nil {
				t.Fatal("invalid Cursor identity was accepted")
			}
		})
	}
}

func TestDevinPolicyRequiresOrganizationSessionAndExplicitPrincipal(t *testing.T) {
	policy := devinPolicy{
		OrganizationID:        "org-guardian",
		AccountID:             "account-guardian",
		Repository:            "guardian-intelligence/guardian",
		RequestingUserIDs:     []string{"user-shovon"},
		RequestingUserEmails:  []string{"im.shovonhasan@gmail.com"},
		AllowedServiceUserIDs: []string{"svc-automation"},
	}
	valid := devinClaims{
		Subject:             "org_id:org-guardian",
		OrganizationID:      "org-guardian",
		AccountID:           "account-guardian",
		DevinID:             "devin-123",
		RequestingUserID:    "user-shovon",
		RequestingUserEmail: "IM.SHOVONHASAN@GMAIL.COM",
		RepositoryName:      "guardian-intelligence/guardian",
	}
	identity, err := normalizeDevinIdentity(policy, valid)
	if err != nil || identity.Provider != "devin" || identity.RunID != "devin-123" || identity.Principal != "user:user-shovon" {
		t.Fatalf("valid Devin identity = %+v, %v", identity, err)
	}

	serviceClaims := valid
	serviceClaims.RequestingUserID = ""
	serviceClaims.ServiceUserID = "svc-automation"
	identity, err = normalizeDevinIdentity(policy, serviceClaims)
	if err != nil || identity.Principal != "service_user:svc-automation" {
		t.Fatalf("valid Devin service identity = %+v, %v", identity, err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*devinClaims)
	}{
		{name: "organization", mutate: func(c *devinClaims) { c.OrganizationID = "org-other" }},
		{name: "account", mutate: func(c *devinClaims) { c.AccountID = "account-other" }},
		{name: "session", mutate: func(c *devinClaims) { c.DevinID = "session-123" }},
		{name: "repository", mutate: func(c *devinClaims) { c.RepositoryName = "other/repo" }},
		{name: "requesting user", mutate: func(c *devinClaims) { c.RequestingUserID = "user-other"; c.RequestingUserEmail = "other@example.com" }},
		{name: "service user", mutate: func(c *devinClaims) { c.ServiceUserID = "svc-other" }},
		{name: "subject", mutate: func(c *devinClaims) { c.Subject = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			claims := valid
			test.mutate(&claims)
			if _, err := normalizeDevinIdentity(policy, claims); err == nil {
				t.Fatal("invalid Devin identity was accepted")
			}
		})
	}
}

func TestJWTIssuerOnlySelectsRegisteredVerifier(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://issuer.example"}`))
	issuer, err := jwtIssuer("header." + payload + ".signature")
	if err != nil || issuer != "https://issuer.example" {
		t.Fatalf("issuer = %q, err = %v", issuer, err)
	}
	for _, token := range []string{"", "not-a-jwt", "header.%%%25.signature", "header..signature"} {
		if _, err := jwtIssuer(token); err == nil {
			t.Fatalf("malformed token %q was accepted", token)
		}
	}
}

func TestRejectsInvalidRequestsAndProviderFailures(t *testing.T) {
	identity := workloadIdentity{Provider: "cursor", RunID: "run-1", Principal: "principal"}
	for _, test := range []struct {
		name          string
		body          string
		authorization string
		contentType   string
		status        int
	}{
		{name: "missing bearer", body: `{}`, contentType: "application/json", status: http.StatusUnauthorized},
		{name: "wrong scheme", body: `{}`, authorization: "Basic nope", contentType: "application/json", status: http.StatusUnauthorized},
		{name: "missing content type", body: `{}`, authorization: "Bearer jwt", status: http.StatusUnsupportedMediaType},
		{name: "unknown persona", body: `{"persona":"admin"}`, authorization: "Bearer jwt", contentType: "application/json", status: http.StatusBadRequest},
		{name: "unknown field", body: `{"persona":"read","extra":true}`, authorization: "Bearer jwt", contentType: "application/json", status: http.StatusBadRequest},
		{name: "multiple objects", body: `{} {}`, authorization: "Bearer jwt", contentType: "application/json", status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			minter := &fakeMinter{}
			recorder := request(t, testBroker(identity, minter), test.body, test.authorization, test.contentType)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, test.status, recorder.Body.String())
			}
			if len(minter.calls) != 0 {
				t.Fatalf("mint calls = %d, want 0", len(minter.calls))
			}
		})
	}

	minter := &fakeMinter{}
	app := testBroker(identity, minter)
	app.resolver = fakeResolver{err: errors.New("bad signature")}
	if recorder := request(t, app, `{}`, "Bearer jwt", "application/json"); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("OIDC failure status = %d", recorder.Code)
	}

	minter = &fakeMinter{err: errors.New("denied")}
	if recorder := request(t, testBroker(identity, minter), `{}`, "Bearer jwt", "application/json"); recorder.Code != http.StatusBadGateway {
		t.Fatalf("Kubernetes failure status = %d", recorder.Code)
	}
}

func TestRateLimitIsPerProviderRun(t *testing.T) {
	minter := &fakeMinter{}
	app := testBroker(workloadIdentity{Provider: "cursor", RunID: "run-1", Principal: "principal"}, minter)
	for i := 0; i < 12; i++ {
		if recorder := request(t, app, `{}`, "Bearer jwt", "application/json"); recorder.Code != http.StatusOK {
			t.Fatalf("request %d status = %d", i+1, recorder.Code)
		}
	}
	if recorder := request(t, app, `{}`, "Bearer jwt", "application/json"); recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited status = %d, want 429", recorder.Code)
	}
}
