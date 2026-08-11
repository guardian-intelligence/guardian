package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPluralize(t *testing.T) {
	cases := map[string]string{
		"GitRepository": "gitrepositories",
		"OCIRepository": "ocirepositories",
		"Bucket":        "buckets",
	}
	for kind, want := range cases {
		if got := pluralize(kind); got != want {
			t.Errorf("pluralize(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestDigestMatches(t *testing.T) {
	const bare = "abc123"
	cases := []struct {
		published, computed string
		want                bool
	}{
		{"sha256:" + bare, "sha256:" + bare, true},
		{bare, "sha256:" + bare, true},
		{"sha256:" + bare, bare, true},
		{"sha256:deadbeef", "sha256:" + bare, false},
		{"", "sha256:" + bare, false},
		{"sha256:", "sha256:" + bare, false},
	}
	for _, c := range cases {
		if got := digestMatches(c.published, c.computed); got != c.want {
			t.Errorf("digestMatches(%q, %q) = %v, want %v", c.published, c.computed, got, c.want)
		}
	}
}

func TestLoadConfigValidation(t *testing.T) {
	// A clean environment for each case: only the vars a case sets exist.
	base := map[string]string{"ROOT_NAME": "guardian-stripe-sandbox", "ROOT_PATH": "./x"}
	set := func(env map[string]string) {
		for _, k := range []string{"ROOT_NAME", "ROOT_PATH", "MODE", "PAGE_ON_DRIFT"} {
			os.Unsetenv(k)
		}
		for k, v := range env {
			os.Setenv(k, v)
		}
	}
	t.Run("defaults to plan mode", func(t *testing.T) {
		set(base)
		cfg, err := loadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.mode != modePlan {
			t.Errorf("mode = %q, want plan", cfg.mode)
		}
		if cfg.pageOnDrift {
			t.Error("pageOnDrift should default false")
		}
	})
	t.Run("missing root name", func(t *testing.T) {
		set(map[string]string{"ROOT_PATH": "./x"})
		if _, err := loadConfig(); err == nil {
			t.Error("want error for missing ROOT_NAME")
		}
	})
	t.Run("bad mode", func(t *testing.T) {
		env := map[string]string{}
		for k, v := range base {
			env[k] = v
		}
		env["MODE"] = "destroy"
		set(env)
		if _, err := loadConfig(); err == nil {
			t.Error("want error for invalid MODE")
		}
	})
	t.Run("page on drift parsed", func(t *testing.T) {
		env := map[string]string{"PAGE_ON_DRIFT": "true"}
		for k, v := range base {
			env[k] = v
		}
		set(env)
		cfg, err := loadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.pageOnDrift {
			t.Error("pageOnDrift should be true")
		}
	})
}

func makeTarGz(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), "sha256:" + hex.EncodeToString(sum[:])
}

func TestExtractTarGzHappyPath(t *testing.T) {
	data, _ := makeTarGz(t, map[string]string{
		"src/infrastructure/bootstrap/root/main.tf": "resource {}",
	})
	dest := t.TempDir()
	if err := extractTarGz(bytes.NewReader(data), dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "src/infrastructure/bootstrap/root/main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "resource {}" {
		t.Errorf("extracted content = %q", got)
	}
}

func TestExtractTarGzRefusesTraversal(t *testing.T) {
	data, _ := makeTarGz(t, map[string]string{"../escape.tf": "malicious"})
	dest := t.TempDir()
	if err := extractTarGz(bytes.NewReader(data), dest); err == nil {
		t.Fatal("expected traversal to be refused")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escape.tf")); !os.IsNotExist(err) {
		t.Error("traversal wrote a file outside the destination")
	}
}

func TestFetchArtifactVerifiesDigest(t *testing.T) {
	data, digest := makeTarGz(t, map[string]string{"root/main.tf": "ok"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(data)
	}))
	defer srv.Close()

	t.Run("good digest extracts", func(t *testing.T) {
		dest := t.TempDir()
		if err := fetchArtifact(context.Background(), artifact{URL: srv.URL, Digest: digest}, dest); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("bad digest is fatal", func(t *testing.T) {
		dest := t.TempDir()
		err := fetchArtifact(context.Background(), artifact{URL: srv.URL, Digest: "sha256:deadbeef"}, dest)
		if err == nil {
			t.Fatal("expected digest mismatch error")
		}
	})
}

// fakeAPIServer serves the group-discovery document and the source object,
// exercising resolveArtifact's version walk without a real apiserver.
func TestResolveArtifactWalksVersions(t *testing.T) {
	const art = "http://source-controller.cozy-fluxcd.svc/gitrepository/cozy-fluxcd/guardian/abc.tar.gz"
	mux := http.NewServeMux()
	mux.HandleFunc("/apis/"+sourceGroup, func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"preferredVersion": map[string]string{"version": "v1"},
			"versions":         []map[string]string{{"version": "v1"}, {"version": "v1beta2"}},
		})
	})
	// v1 does not serve ocirepositories in this fake; only v1beta2 does. The
	// walk must fall through to it rather than give up on the first 404.
	mux.HandleFunc("/apis/"+sourceGroup+"/v1/namespaces/cozy-fluxcd/ocirepositories/guardian",
		func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "not found", http.StatusNotFound) })
	mux.HandleFunc("/apis/"+sourceGroup+"/v1beta2/namespaces/cozy-fluxcd/ocirepositories/guardian",
		func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"status": map[string]any{
					"artifact": map[string]string{"url": art, "digest": "sha256:abc", "revision": "main@sha1:abc"},
				},
			})
		})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	kc := &k8sClient{host: srv.URL, token: "t", client: srv.Client()}
	got, err := kc.resolveArtifact(context.Background(), config{
		sourceKind: "OCIRepository", sourceName: "guardian", sourceNamespace: "cozy-fluxcd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != art {
		t.Errorf("artifact URL = %q, want %q", got.URL, art)
	}
}

func TestResolveArtifactNoArtifactYet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/apis/"+sourceGroup, func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"preferredVersion": map[string]string{"version": "v1"}})
	})
	mux.HandleFunc("/apis/"+sourceGroup+"/v1/namespaces/cozy-fluxcd/gitrepositories/guardian",
		func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"status": map[string]any{}})
		})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	kc := &k8sClient{host: srv.URL, token: "t", client: srv.Client()}
	_, err := kc.resolveArtifact(context.Background(), config{
		sourceKind: "GitRepository", sourceName: "guardian", sourceNamespace: "cozy-fluxcd",
	})
	if err == nil {
		t.Fatal("expected error when artifact not ready")
	}
}
