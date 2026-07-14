package checkoutbundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeBundleFixture(t *testing.T, service *Service, repoKey, sha string, size int, age time.Duration) string {
	t.Helper()
	path := service.bundlePath(repoKey, sha)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeMirrorFixture(t *testing.T, service *Service, repoKey string, age time.Duration) string {
	t.Helper()
	dir := service.mirrorDir(repoKey)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(dir, mirrorStampFile)
	if err := os.WriteFile(stamp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(stamp, at, at); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSweepBundleTTL(t *testing.T) {
	service := New(Config{
		StoreDir:   t.TempDir(),
		HostSecret: testSecret,
		BundleTTL:  24 * time.Hour,
	}, &StaticResolver{})
	expired := writeBundleFixture(t, service, "repoa", strings.Repeat("a", 40), 10, 48*time.Hour)
	fresh := writeBundleFixture(t, service, "repoa", strings.Repeat("b", 40), 10, time.Hour)

	service.SweepOnce()

	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatal("expired bundle survived the sweep")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh bundle was evicted")
	}
}

func TestSweepBundleBudgetEvictsOldestFirst(t *testing.T) {
	service := New(Config{
		StoreDir:          t.TempDir(),
		HostSecret:        testSecret,
		BundleBudgetBytes: 250,
	}, &StaticResolver{})
	oldest := writeBundleFixture(t, service, "repoa", strings.Repeat("a", 40), 100, 3*time.Hour)
	middle := writeBundleFixture(t, service, "repoa", strings.Repeat("b", 40), 100, 2*time.Hour)
	newest := writeBundleFixture(t, service, "repob", strings.Repeat("c", 40), 100, time.Hour)

	service.SweepOnce()

	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Fatal("oldest bundle survived over-budget sweep")
	}
	for name, path := range map[string]string{"middle": middle, "newest": newest} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s bundle was evicted while under budget", name)
		}
	}
}

func TestSweepMirrorTTL(t *testing.T) {
	service := New(Config{
		StoreDir:   t.TempDir(),
		HostSecret: testSecret,
		MirrorTTL:  24 * time.Hour,
	}, &StaticResolver{})
	stale := writeMirrorFixture(t, service, "stalerepo", 48*time.Hour)
	fresh := writeMirrorFixture(t, service, "freshrepo", time.Hour)

	service.SweepOnce()

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale mirror survived the sweep")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh mirror was evicted")
	}
}
