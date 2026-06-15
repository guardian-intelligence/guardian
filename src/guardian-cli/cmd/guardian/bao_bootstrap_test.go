package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	mux.HandleFunc("/v1/sys/policies/acl/", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/v1/sys/policies/acl/")
		var body struct {
			Policy string `json:"policy"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		b.policies[name] = body.Policy
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/auth/kubernetes/role/", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/v1/auth/kubernetes/role/")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		b.roles[name] = body
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
	sitePath, err := toolPath("_main/src/sites/dev/bootstrap.yaml")
	if err != nil {
		t.Fatalf("locate bootstrap.yaml: %v", err)
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
	for _, path := range []string{
		"kv/data/guardian/" + site.Cluster.Name + "/observability/clickhouse-admin",
		"kv/data/guardian/" + site.Cluster.Name + "/observability/grafana-admin",
	} {
		if !strings.Contains(b.policies["observability-secrets"], path) {
			t.Fatalf("observability policy = %q, missing %s", b.policies["observability-secrets"], path)
		}
	}
	if !strings.Contains(b.policies["guardian-oci-secrets"], "kv/data/guardian/"+site.Cluster.Name+"/oci/zot-publisher") {
		t.Fatalf("guardian-oci policy = %q", b.policies["guardian-oci-secrets"])
	}
	for _, path := range []string{
		"kv/data/guardian/" + site.Cluster.Name + "/directus/runtime",
		"kv/data/guardian/" + site.Cluster.Name + "/directus/admin",
		"kv/data/guardian/" + site.Cluster.Name + "/directus/postgres",
	} {
		if !strings.Contains(b.policies["directus-secrets"], path) {
			t.Fatalf("directus policy = %q, missing %s", b.policies["directus-secrets"], path)
		}
	}
	role := b.roles["observability-secrets"]
	if role["audience"] != "openbao" {
		t.Fatalf("role audience = %v, want openbao", role["audience"])
	}
	ociRole := b.roles["guardian-oci-secrets"]
	if ociRole["audience"] != "openbao" {
		t.Fatalf("oci role audience = %v, want openbao", ociRole["audience"])
	}
	directusRole := b.roles["directus-secrets"]
	if directusRole["audience"] != "openbao" {
		t.Fatalf("directus role audience = %v, want openbao", directusRole["audience"])
	}
	projections, err := secretProjections(site)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range baoRequiredSecretsFromProjections(projections) {
		path := secret.path
		for _, key := range secret.required {
			if b.secrets[path][key] == "" {
				t.Fatalf("secret %s has no generated %s", path, key)
			}
		}
	}
	zotPath := "guardian/" + site.Cluster.Name + "/oci/zot-publisher"
	if b.secrets[zotPath]["username"] != "guardian-release" {
		t.Fatalf("zot username = %q, want guardian-release", b.secrets[zotPath]["username"])
	}
	if !strings.HasPrefix(b.secrets[zotPath]["htpasswd"], "guardian-release:$2") {
		t.Fatalf("zot htpasswd = %q, want bcrypt htpasswd entry", b.secrets[zotPath]["htpasswd"])
	}
}

func TestConfigureBaoForProjectionRefusesMissingRestoredSecret(t *testing.T) {
	sitePath, err := toolPath("_main/src/sites/dev/bootstrap.yaml")
	if err != nil {
		t.Fatalf("locate bootstrap.yaml: %v", err)
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

func TestLookupBaoRootTokenSecretEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.env")
	if err := os.WriteFile(path, []byte(`GUARDIAN_OPENBAO_TOKEN="root-token"
GUARDIAN_OPENBAO_ALLOW_SECRET_MIGRATION=1
`), 0o600); err != nil {
		t.Fatal(err)
	}
	token, source, err := lookupBaoRootToken(func(string) string { return "" }, path)
	if err != nil {
		t.Fatal(err)
	}
	if token != "root-token" {
		t.Fatalf("token = %q, want root-token", token)
	}
	if !strings.HasSuffix(source, "secret.env:"+baoRootTokenEnv) {
		t.Fatalf("source = %q, want secret.env source", source)
	}
	allowed, source, err := lookupBaoSecretMigrationAllowed(func(string) string { return "" }, path)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("migration flag = false, want true")
	}
	if !strings.HasSuffix(source, "secret.env:"+baoAllowSecretMigrationEnv) {
		t.Fatalf("source = %q, want secret.env source", source)
	}
}

func TestLookupBaoRootTokenEnvWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.env")
	if err := os.WriteFile(path, []byte(`GUARDIAN_OPENBAO_TOKEN=file-token
GUARDIAN_OPENBAO_ALLOW_SECRET_MIGRATION=1
`), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(key string) string {
		switch key {
		case baoRootTokenEnv:
			return "env-token"
		case baoAllowSecretMigrationEnv:
			return "0"
		default:
			return ""
		}
	}
	token, source, err := lookupBaoRootToken(getenv, path)
	if err != nil {
		t.Fatal(err)
	}
	if token != "env-token" || source != baoRootTokenEnv {
		t.Fatalf("token/source = %q/%q, want env-token/%s", token, source, baoRootTokenEnv)
	}
	allowed, source, err := lookupBaoSecretMigrationAllowed(getenv, path)
	if err != nil {
		t.Fatal(err)
	}
	if allowed || source != baoAllowSecretMigrationEnv {
		t.Fatalf("allowed/source = %v/%q, want false/%s", allowed, source, baoAllowSecretMigrationEnv)
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
