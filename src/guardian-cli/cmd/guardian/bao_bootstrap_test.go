package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeBaoConfig struct {
	mu       sync.Mutex
	mounts   map[string]baoMount
	auths    map[string]baoMount
	secrets  map[string]map[string]string
	policies map[string]string
	roles    map[string]map[string]any
}

func newFakeBaoConfig() *fakeBaoConfig {
	return &fakeBaoConfig{
		mounts:   map[string]baoMount{},
		auths:    map[string]baoMount{},
		secrets:  map[string]map[string]string{},
		policies: map[string]string{},
		roles:    map[string]map[string]any{},
	}
}

func (b *fakeBaoConfig) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/sys/mounts", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": b.mounts})
	})
	mux.HandleFunc("/v1/sys/mounts/kv", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		b.mounts["kv/"] = baoMount{Type: "kv", Options: map[string]string{"version": "2"}}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/sys/auth", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": b.auths})
	})
	mux.HandleFunc("/v1/sys/auth/kubernetes", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		b.auths["kubernetes/"] = baoMount{Type: "kubernetes"}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/auth/kubernetes/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/sys/policies/acl/observability-secrets", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Policy string `json:"policy"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		b.policies["observability-secrets"] = body.Policy
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/auth/kubernetes/role/observability-secrets", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		b.roles["observability-secrets"] = body
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/kv/data/", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/v1/kv/data/")
		switch r.Method {
		case http.MethodGet:
			secret, ok := b.secrets[path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": secret}})
		case http.MethodPost:
			var body struct {
				Data map[string]string `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			b.secrets[path] = body.Data
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	return mux
}

func TestConfigureBaoForProjectionCreatesFreshSecrets(t *testing.T) {
	sitePath, err := toolPath("_main/src/sites/dev/site.yaml")
	if err != nil {
		t.Fatalf("locate site.yaml: %v", err)
	}
	site, err := loadSite(sitePath)
	if err != nil {
		t.Fatal(err)
	}
	b := newFakeBaoConfig()
	srv := httptest.NewServer(b.handler())
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	if err := configureBaoForProjection(addr, "root", site, true); err != nil {
		t.Fatalf("configureBaoForProjection: %v", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mounts["kv/"].Type != "kv" || b.mounts["kv/"].Options["version"] != "2" {
		t.Fatalf("kv mount = %#v, want kv v2", b.mounts["kv/"])
	}
	if b.auths["kubernetes/"].Type != "kubernetes" {
		t.Fatalf("kubernetes auth = %#v, want kubernetes", b.auths["kubernetes/"])
	}
	if !strings.Contains(b.policies["observability-secrets"], "kv/data/guardian/"+site.Cluster.Name+"/observability/*") {
		t.Fatalf("observability policy = %q", b.policies["observability-secrets"])
	}
	role := b.roles["observability-secrets"]
	if role["audience"] != "openbao" {
		t.Fatalf("role audience = %v, want openbao", role["audience"])
	}
	for _, secret := range requiredBaoSecrets {
		path := secret.path(site)
		if b.secrets[path]["password"] == "" {
			t.Fatalf("secret %s has no generated password", path)
		}
	}
}

func TestConfigureBaoForProjectionRefusesMissingRestoredSecret(t *testing.T) {
	sitePath, err := toolPath("_main/src/sites/dev/site.yaml")
	if err != nil {
		t.Fatalf("locate site.yaml: %v", err)
	}
	site, err := loadSite(sitePath)
	if err != nil {
		t.Fatal(err)
	}
	b := newFakeBaoConfig()
	b.mounts["kv/"] = baoMount{Type: "kv", Options: map[string]string{"version": "2"}}
	b.auths["kubernetes/"] = baoMount{Type: "kubernetes"}
	srv := httptest.NewServer(b.handler())
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	err = configureBaoForProjection(addr, "root", site, false)
	if err == nil || !strings.Contains(err.Error(), baoAllowSecretMigrationEnv) {
		t.Fatalf("configureBaoForProjection missing restored secret = %v, want %s guidance", err, baoAllowSecretMigrationEnv)
	}
}

func TestBaoAPIStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("missing"))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	err := baoAPI(addr, "GET", "/v1/missing", "", nil, nil)
	var httpErr *baoHTTPError
	if !errors.As(err, &httpErr) || httpErr.status != http.StatusNotFound {
		t.Fatalf("baoAPI error = %v, want baoHTTPError 404", err)
	}
}
