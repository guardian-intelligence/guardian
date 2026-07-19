package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	authorizationv1 "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/authorization/v1"
)

func TestCheckForwardsConsistencyAndDecision(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing SpiceDB bearer token")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		consistency := body["consistency"].(map[string]any)
		atLeast := consistency["atLeastAsFresh"].(map[string]any)
		if atLeast["token"] != "zed-token" {
			t.Errorf("consistency token = %v", atLeast["token"])
		}
		_, _ = w.Write([]byte(`{"permissionship":"PERMISSIONSHIP_HAS_PERMISSION","checkedAt":{"token":"checked-token"}}`))
	}))
	defer server.Close()

	service := &authorizationService{spice: &spiceDBClient{
		baseURL: server.URL,
		token:   "test-token",
		http:    server.Client(),
	}}
	response, err := service.Check(context.Background(), connect.NewRequest(&authorizationv1.CheckRequest{
		Resource:   &authorizationv1.ObjectReference{Type: "organization", Id: "guardian"},
		Permission: "view",
		Subject: &authorizationv1.SubjectReference{
			Object: &authorizationv1.ObjectReference{Type: "guardian_account", Id: "subject"},
		},
		AtLeastAsFreshToken: "zed-token",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !response.Msg.GetAllowed() || response.Msg.GetCheckedAtToken() != "checked-token" {
		t.Fatalf("response = %+v", response.Msg)
	}
}

func TestRelationshipContractRejectsProviderIdentity(t *testing.T) {
	t.Parallel()
	_, err := checkedRelationship(&authorizationv1.RelationshipUpdate{
		Operation: authorizationv1.RelationshipOperation_RELATIONSHIP_OPERATION_TOUCH,
		Resource:  &authorizationv1.ObjectReference{Type: "provider_connection", Id: "github"},
		Relation:  "account",
		Subject: &authorizationv1.SubjectReference{
			Object: &authorizationv1.ObjectReference{Type: "guardian_account", Id: "subject"},
		},
	})
	if err == nil {
		t.Fatal("provider identity relationship was accepted")
	}
}

func TestRelationshipContractAcceptsRepositoryParent(t *testing.T) {
	t.Parallel()
	update, err := checkedRelationship(&authorizationv1.RelationshipUpdate{
		Operation: authorizationv1.RelationshipOperation_RELATIONSHIP_OPERATION_TOUCH,
		Resource:  &authorizationv1.ObjectReference{Type: "postflight_repository", Id: "repo"},
		Relation:  "project",
		Subject: &authorizationv1.SubjectReference{
			Object: &authorizationv1.ObjectReference{Type: "postflight_project", Id: "project"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if update.Operation != "OPERATION_TOUCH" {
		t.Fatalf("operation = %q", update.Operation)
	}
}

func TestAuthorizationAPIRequiresBearerToken(t *testing.T) {
	t.Parallel()
	called := false
	handler := authenticate("check-token", "write-token", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodPost, "/guardian.authorization.v1.AuthorizationService/Check", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || called {
		t.Fatalf("unauthenticated status=%d called=%v", response.Code, called)
	}

	request = httptest.NewRequest(http.MethodPost, "/guardian.authorization.v1.AuthorizationService/Check", nil)
	request.Header.Set("Authorization", "Bearer check-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || !called {
		t.Fatalf("authenticated status=%d called=%v", response.Code, called)
	}
}

func TestAuthorizationCheckTokenCannotWriteRelationships(t *testing.T) {
	t.Parallel()
	called := false
	handler := authenticate("check-token", "write-token", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(
		http.MethodPost,
		"/guardian.authorization.v1.AuthorizationService/WriteRelationships",
		nil,
	)
	request.Header.Set("Authorization", "Bearer check-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || called {
		t.Fatalf("check-token write status=%d called=%v", response.Code, called)
	}

	request = httptest.NewRequest(
		http.MethodPost,
		"/guardian.authorization.v1.AuthorizationService/WriteRelationships",
		nil,
	)
	request.Header.Set("Authorization", "Bearer write-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || !called {
		t.Fatalf("write-token status=%d called=%v", response.Code, called)
	}
}
