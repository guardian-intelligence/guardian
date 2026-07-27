package checkoutbundle

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeBundleFixture(t *testing.T, service *Service, repoKey, sha string, size int, age time.Duration) string {
	t.Helper()
	path := service.bundlePath(repoKey, sha, "", 1)
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

func TestBundleBudgetAccountsForActiveReaders(t *testing.T) {
	service := New(Config{
		StoreDir:          t.TempDir(),
		HostSecret:        testSecret,
		BundleBudgetBytes: 100,
		MaxPackBytes:      100,
	}, &StaticResolver{})
	path := writeBundleFixture(t, service, "repoa", strings.Repeat("a", 40), 100, time.Hour)
	file, _, ok := service.openBundle(path)
	if !ok {
		t.Fatal("open active bundle")
	}

	service.bundleMu.Lock()
	err := service.makeBundleRoom(100)
	service.bundleMu.Unlock()
	if !errors.Is(err, errTooLarge) {
		t.Fatalf("make room with active reader = %v, want %v", err, errTooLarge)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active bundle was unlinked: %v", err)
	}

	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	service.bundleMu.Lock()
	err = service.makeBundleRoom(100)
	service.bundleMu.Unlock()
	if err != nil {
		t.Fatalf("make room after close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("closed bundle survived budget eviction")
	}
}

func TestBundleBudgetReservesConcurrentPackWriters(t *testing.T) {
	service := New(Config{
		StoreDir:          t.TempDir(),
		HostSecret:        testSecret,
		BundleBudgetBytes: 200,
		MaxPackBytes:      100,
	}, &StaticResolver{})
	firstPath := service.bundlePath("repoa", strings.Repeat("a", 40), "", 1)
	secondPath := service.bundlePath("repob", strings.Repeat("b", 40), "", 1)
	thirdPath := service.bundlePath("repoc", strings.Repeat("c", 40), "", 1)

	first, err := service.reserveBundle(firstPath, 100)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.reserveBundle(secondPath, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Write(make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Write(make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	service.SweepOnce()
	for _, path := range []string{first.Name(), second.Name()} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("active reserved temp was reaped: %v", err)
		}
	}
	if _, err := service.reserveBundle(thirdPath, 100); !errors.Is(err, errTooLarge) {
		t.Fatalf("third reservation = %v, want %v", err, errTooLarge)
	}

	firstTmp, secondTmp := first.Name(), second.Name()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	service.releaseBundleReservation(firstTmp, 100)
	service.releaseBundleReservation(secondTmp, 100)
	if service.bundleReserved != 0 {
		t.Fatalf("reserved bytes = %d, want 0", service.bundleReserved)
	}
}

func TestSweepReapsOrphanedTempPacks(t *testing.T) {
	service := New(Config{
		StoreDir:   t.TempDir(),
		HostSecret: testSecret,
		BundleTTL:  24 * time.Hour,
	}, &StaticResolver{})
	dir := filepath.Join(service.cfg.StoreDir, "bundles", "repoa", strings.Repeat("a", 40))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	staleTmp := filepath.Join(dir, ".checkout-abc123.pack")
	if err := os.WriteFile(staleTmp, make([]byte, 50), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(staleTmp, old, old); err != nil {
		t.Fatal(err)
	}
	freshTmp := filepath.Join(dir, ".checkout-def456.pack")
	if err := os.WriteFile(freshTmp, make([]byte, 50), 0o600); err != nil {
		t.Fatal(err)
	}

	service.SweepOnce()

	if _, err := os.Stat(staleTmp); !os.IsNotExist(err) {
		t.Fatal("stale temp pack survived the sweep")
	}
	if _, err := os.Stat(freshTmp); !os.IsNotExist(err) {
		t.Fatal("fresh orphaned temp pack survived the sweep")
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
