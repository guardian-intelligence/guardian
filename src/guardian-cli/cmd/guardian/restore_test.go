package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeBao is an in-memory stand-in for OpenBao's HTTP API covering exactly the
// endpoints the restore dance exercises, with the same seal/init state machine:
// fresh → init → unseal → snapshot-force re-seals under the snapshot's keyring.
type fakeBao struct {
	mu      sync.Mutex
	inited  bool
	sealed  bool
	key     string
	gotSnap []byte
}

func newFakeBao() *fakeBao { return &fakeBao{} }

func (b *fakeBao) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/sys/health", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		switch {
		case !b.inited:
			w.WriteHeader(http.StatusNotImplemented) // 501
		case b.sealed:
			w.WriteHeader(http.StatusServiceUnavailable) // 503
		default:
			w.WriteHeader(http.StatusOK) // 200
		}
	})
	mux.HandleFunc("/v1/sys/init", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.inited, b.sealed, b.key = true, true, "throwaway-key-b64"
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys_base64": []string{b.key},
			"root_token":  "throwaway-root",
		})
	})
	mux.HandleFunc("/v1/sys/unseal", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		var body struct {
			Key string `json:"key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Key == b.key {
			b.sealed = false
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sealed": b.sealed})
	})
	mux.HandleFunc("/v1/sys/storage/raft/snapshot-force", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if r.Header.Get("X-Vault-Token") != "throwaway-root" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		b.gotSnap, _ = io.ReadAll(r.Body)
		b.sealed = true // the snapshot's keyring differs; the node re-seals
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestBaoHealthStates(t *testing.T) {
	b := newFakeBao()
	srv := httptest.NewServer(b.handler())
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	if st, err := baoHealth(addr); err != nil || st != baoFresh {
		t.Fatalf("fresh: got %s err %v, want %s", st, err, baoFresh)
	}
	if err := baoAPI(addr, "PUT", "/v1/sys/init", "", strings.NewReader(`{"secret_shares":1,"secret_threshold":1}`), &struct{}{}); err != nil {
		t.Fatalf("init: %v", err)
	}
	if st, err := baoHealth(addr); err != nil || st != baoSealed {
		t.Fatalf("after init: got %s err %v, want %s", st, err, baoSealed)
	}
	if err := baoAPI(addr, "PUT", "/v1/sys/unseal", "", strings.NewReader(`{"key":"throwaway-key-b64"}`), &struct{}{}); err != nil {
		t.Fatalf("unseal: %v", err)
	}
	if st, err := baoHealth(addr); err != nil || st != baoUnsealed {
		t.Fatalf("after unseal: got %s err %v, want %s", st, err, baoUnsealed)
	}
}

func TestBaoDecision(t *testing.T) {
	cases := []struct {
		st        baoState
		restoring bool
		action    baoAction
		err       error
	}{
		{baoFresh, false, actReport, nil},
		{baoSealed, false, actReport, nil},
		{baoUnsealed, false, actReport, nil},
		{baoFresh, true, actRestore, nil},
		{baoSealed, true, actReport, errRestoreOverData},
		{baoUnsealed, true, actReport, errRestoreOverData},
		{baoUnreachable, false, actReport, errBaoUnreachable},
		{baoUnreachable, true, actReport, errBaoUnreachable},
	}
	for _, c := range cases {
		a, err := baoDecision(c.st, c.restoring)
		if a != c.action || !errors.Is(err, c.err) {
			t.Errorf("baoDecision(%s, %v) = (%v, %v), want (%v, %v)", c.st, c.restoring, a, err, c.action, c.err)
		}
	}
}

func TestUpUsageErrors(t *testing.T) {
	// Both failures are caught before any cluster contact, so runUp is safe to
	// call directly here. They must carry errUsage so main exits 2, not 1.
	if err := runUp([]string{"--restore", "snap.bin"}); !errors.Is(err, errUsage) {
		t.Errorf("--restore without --sha256: got %v, want errUsage", err)
	}
	if err := runUp([]string{"--bogus"}); !errors.Is(err, errUsage) {
		t.Errorf("unknown flag: got %v, want errUsage", err)
	}
}

func TestFetchAndVerifySnapshotFile(t *testing.T) {
	dir := t.TempDir()
	data := []byte("raft-snapshot")
	p := filepath.Join(dir, "snap.bin")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := fetchAndVerifySnapshot(p, sha256hex(data), dir)
	if err != nil {
		t.Fatalf("fetchAndVerifySnapshot: %v", err)
	}
	if got != p {
		t.Fatalf("snapPath = %q, want %q", got, p)
	}
}

func TestFetchAndVerifySnapshotURL(t *testing.T) {
	data := []byte("raft-snapshot-over-http")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	dir := t.TempDir()
	got, err := fetchAndVerifySnapshot(srv.URL+"/snap.bin", sha256hex(data), dir)
	if err != nil {
		t.Fatalf("fetchAndVerifySnapshot: %v", err)
	}
	staged, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(staged, data) {
		t.Fatalf("staged blob mismatch: got %q want %q", staged, data)
	}
}

func TestFetchAndVerifySnapshotMismatch(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "snap.bin")
	if err := os.WriteFile(p, []byte("raft-snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := fetchAndVerifySnapshot(p, sha256hex([]byte("a different blob")), dir)
	if !errors.Is(err, errDigestMismatch) {
		t.Fatalf("want errDigestMismatch, got %v", err)
	}
}

func TestFetchAndVerifySnapshotBadSHA(t *testing.T) {
	if _, err := fetchAndVerifySnapshot("/whatever", "not-64-hex", t.TempDir()); !errors.Is(err, errUsage) {
		t.Fatalf("malformed --sha256: want errUsage, got %v", err)
	}
}

func TestFetchAndVerifySnapshotUnsupportedScheme(t *testing.T) {
	// A valid digest gets past validation so we reach the scheme check.
	_, err := fetchAndVerifySnapshot("r2://bucket/snap.bin", sha256hex([]byte("x")), t.TempDir())
	if !errors.Is(err, errUsage) || !strings.Contains(err.Error(), "presign") {
		t.Fatalf("want errUsage + presign guidance for r2:// scheme, got %v", err)
	}
}

func TestRestoreSnapshotDance(t *testing.T) {
	b := newFakeBao()
	srv := httptest.NewServer(b.handler())
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	snap := []byte("the-raft-snapshot-bytes")
	dir := t.TempDir()
	p := filepath.Join(dir, "snap.bin")
	if err := os.WriteFile(p, snap, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := restoreSnapshot(addr, p); err != nil {
		t.Fatalf("restoreSnapshot: %v", err)
	}
	if !bytes.Equal(b.gotSnap, snap) {
		t.Fatalf("server received %q, want %q", b.gotSnap, snap)
	}
	// Restore swapped the barrier: the vault is sealed under the snapshot's
	// keyring, awaiting the site's original shares.
	if st, err := baoHealth(addr); err != nil || st != baoSealed {
		t.Fatalf("post-restore: got %s err %v, want %s", st, err, baoSealed)
	}
}
