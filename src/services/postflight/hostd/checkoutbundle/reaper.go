package checkoutbundle

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RunReaper sweeps the stores until the context ends. Sweeps are safe at any
// time: bundles are regenerable from mirrors, mirrors from GitHub — eviction
// is always at worst a cache miss.
func (s *Service) RunReaper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.SweepOnce()
		}
	}
}

// SweepOnce applies bundle TTL + byte budget and mirror TTL, logging what it
// dropped — silent truncation reads as "covered everything" when it didn't.
func (s *Service) SweepOnce() {
	removedBundles, freedBytes := s.sweepBundles()
	removedMirrors := s.sweepMirrors()
	if removedBundles > 0 || removedMirrors > 0 {
		s.cfg.Logger.Info("checkout store swept",
			"bundles_removed", removedBundles,
			"bundle_bytes_freed", freedBytes,
			"mirrors_removed", removedMirrors)
	}
}

type bundleEntry struct {
	path    string
	modTime time.Time
	size    int64
}

func (s *Service) sweepBundles() (removed int, freed int64) {
	root := filepath.Join(s.cfg.StoreDir, "bundles")
	var entries []bundleEntry
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		// Temp packs left by a crash mid-createBundle are otherwise invisible
		// to TTL and budget forever; count and reap the stale ones.
		if info.Name() != bundleFilename {
			if strings.HasPrefix(info.Name(), ".checkout-") &&
				info.ModTime().Before(time.Now().Add(-s.cfg.BundleTTL)) {
				if err := os.Remove(path); err == nil {
					removed++
					freed += info.Size()
				}
			}
			return nil
		}
		entries = append(entries, bundleEntry{path: path, modTime: info.ModTime(), size: info.Size()})
		return nil
	})

	cutoff := time.Now().Add(-s.cfg.BundleTTL)
	var live []bundleEntry
	var liveBytes int64
	for _, entry := range entries {
		if entry.modTime.Before(cutoff) {
			if s.removeBundle(entry.path) {
				removed++
				freed += entry.size
			}
			continue
		}
		live = append(live, entry)
		liveBytes += entry.size
	}

	// Oldest-first eviction down to the byte budget.
	sort.Slice(live, func(i, j int) bool { return live[i].modTime.Before(live[j].modTime) })
	for _, entry := range live {
		if liveBytes <= s.cfg.BundleBudgetBytes {
			break
		}
		if s.removeBundle(entry.path) {
			removed++
			freed += entry.size
			liveBytes -= entry.size
		}
	}
	return removed, freed
}

// removeBundle deletes a pack and its per-SHA directory. It needs no lock: a
// request that already opened the pack keeps serving it (POSIX holds the bytes
// until the descriptor closes), and one that has not yet opened it simply
// misses and rebuilds under the repo lock.
func (s *Service) removeBundle(path string) bool {
	if err := os.Remove(path); err != nil {
		return false
	}
	_ = os.Remove(filepath.Dir(path)) // best-effort: fails if non-empty
	return true
}

// sweepMirrors removes mirrors whose stamp (touched on every use, under the
// repo lock) is older than MirrorTTL. Removal takes the repo lock so an
// in-flight request never sees a half-deleted mirror.
func (s *Service) sweepMirrors() (removed int) {
	root := filepath.Join(s.cfg.StoreDir, "mirrors")
	dirEntries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-s.cfg.MirrorTTL)
	for _, entry := range dirEntries {
		if !entry.IsDir() {
			continue
		}
		repoKey := entry.Name()
		mirror := filepath.Join(root, repoKey)
		lastUsed := mirrorLastUsed(mirror)
		if !lastUsed.Before(cutoff) {
			continue
		}
		unlock := s.lockRepo(repoKey)
		// Re-check under the lock: a request may have raced the scan.
		if mirrorLastUsed(mirror).Before(cutoff) {
			if err := os.RemoveAll(mirror); err == nil {
				removed++
			}
		}
		unlock()
	}
	return removed
}

func mirrorLastUsed(mirror string) time.Time {
	if info, err := os.Stat(filepath.Join(mirror, mirrorStampFile)); err == nil {
		return info.ModTime()
	}
	if info, err := os.Stat(mirror); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}
