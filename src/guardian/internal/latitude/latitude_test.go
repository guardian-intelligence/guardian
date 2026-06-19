package latitude

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReinstallIPXESendsJSONAPIRequest(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/servers/sv_dev/reinstall" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Token: "token"}
	if err := client.ReinstallIPXE(context.Background(), "sv_dev", "gi-ash-dev-platform-01", "https://pxe.example/boot.ipxe"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	data := gotBody["data"].(map[string]any)
	if data["type"] != "reinstalls" {
		t.Fatalf("type = %v", data["type"])
	}
	attrs := data["attributes"].(map[string]any)
	if attrs["operating_system"] != "ipxe" || attrs["hostname"] != "gi-ash-dev-platform-01" || attrs["ipxe"] != "https://pxe.example/boot.ipxe" {
		t.Fatalf("attributes = %#v", attrs)
	}
}

func TestReinstallIPXETreatsProvisioningAsSentinel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"code":"SERVER_BEING_PROVISIONED"}]}`))
	}))
	defer server.Close()

	err := (Client{BaseURL: server.URL, Token: "token"}).ReinstallIPXE(context.Background(), "sv_dev", "host", "https://pxe.example/boot.ipxe")
	if !errors.Is(err, ErrServerBeingProvisioned) {
		t.Fatalf("error = %v, want ErrServerBeingProvisioned", err)
	}
}

func TestGetServerDecodesSafetyFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{
		  "data": {
		    "id": "sv_dev",
		    "attributes": {
		      "hostname": "gi-ash-dev-platform-01",
		      "primary_ipv4": "206.223.228.101",
		      "status": "on",
		      "locked": false,
		      "project": {"name": "guardian"},
		      "operating_system": {"slug": "ubuntu_24_04_x64_lts"}
		    }
		  }
		}`))
	}))
	defer server.Close()

	got, err := (Client{BaseURL: server.URL, Token: "token"}).GetServer(context.Background(), "sv_dev")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "sv_dev" || got.PrimaryIPv4 != "206.223.228.101" || got.OS != "ubuntu_24_04_x64_lts" {
		t.Fatalf("server = %#v", got)
	}
}
