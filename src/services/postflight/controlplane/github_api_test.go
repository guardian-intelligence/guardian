package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestInstallationTokensAreCachedPerInstallation(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	mints := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost ||
			!strings.HasPrefix(r.URL.Path, "/app/installations/") ||
			!strings.HasSuffix(r.URL.Path, "/access_tokens") {
			http.NotFound(w, r)
			return
		}
		installation := strings.TrimSuffix(
			strings.TrimPrefix(r.URL.Path, "/app/installations/"),
			"/access_tokens",
		)
		mu.Lock()
		mints[installation]++
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "token-" + installation})
	}))
	defer server.Close()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	client, err := newGitHubClient(config{
		appID:          1,
		apiBaseURL:     server.URL,
		privateKeyPEM: string(privateKey),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, installation := range []int64{101, 202, 101, 202} {
		token, err := client.installationAccessToken(t.Context(), installation)
		if err != nil {
			t.Fatal(err)
		}
		if token != "token-"+strconv.FormatInt(installation, 10) {
			t.Fatalf("token for installation %d = %q", installation, token)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if mints["101"] != 1 || mints["202"] != 1 {
		t.Fatalf("token mint counts = %#v, want one per installation", mints)
	}
}
