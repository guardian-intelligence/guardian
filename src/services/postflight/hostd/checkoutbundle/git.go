package checkoutbundle

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	bundleFilename  = "checkout.pack"
	mirrorStampFile = ".postflight-last-used"
	requestedRef    = "refs/postflight/requested"
	fetchedSHARef   = "refs/postflight/sha"
)

var (
	errNotFound = errors.New("commit is not available from origin")
	errTooLarge = errors.New("checkout pack exceeds the size limit")
	errUpstream = errors.New("origin fetch failed")
)

// preparedBundle describes a servable pack.
type preparedBundle struct {
	Path      string
	SizeBytes int64
	CacheHit  bool
}

// prepareBundle returns the cached pack for (repository, sha) or builds it:
// ensure the bare mirror, fetch the commit with the job's GitHub token, and
// write the single-commit pack closure atomically into the cache. All
// mutation for one repository is serialized behind its repo lock, so
// concurrent same-SHA requests collapse into one fetch and the followers
// take the cache path.
func (s *Service) prepareBundle(ctx context.Context, identity LeaseIdentity, spec checkoutSpec) (preparedBundle, error) {
	repoKey := repositoryStoreKey(identity)
	unlock := s.lockRepo(repoKey)
	defer unlock()

	bundlePath := s.bundlePath(repoKey, spec.SHA)
	s.touchMirrorStamp(repoKey)
	if stat, err := os.Stat(bundlePath); err == nil && stat.Size() > 0 {
		now := time.Now()
		_ = os.Chtimes(bundlePath, now, now) // LRU recency for the reaper
		s.Metrics.CacheHits.Add(1)
		return preparedBundle{Path: bundlePath, SizeBytes: stat.Size(), CacheHit: true}, nil
	}

	mirrorDir := s.mirrorDir(repoKey)
	if err := s.ensureMirror(ctx, mirrorDir, spec.Repository); err != nil {
		return preparedBundle{}, err
	}
	if err := s.fetchCommit(ctx, mirrorDir, spec); err != nil {
		return preparedBundle{}, err
	}
	if err := s.createBundle(ctx, mirrorDir, bundlePath, spec.SHA); err != nil {
		return preparedBundle{}, err
	}
	stat, err := os.Stat(bundlePath)
	if err != nil {
		return preparedBundle{}, err
	}
	return preparedBundle{Path: bundlePath, SizeBytes: stat.Size(), CacheHit: false}, nil
}

func (s *Service) mirrorDir(repoKey string) string {
	return filepath.Join(s.cfg.StoreDir, "mirrors", repoKey)
}

func (s *Service) bundlePath(repoKey, sha string) string {
	return filepath.Join(s.cfg.StoreDir, "bundles", repoKey, sha, bundleFilename)
}

// repositoryStoreKey keys stores by immutable GitHub identity, not by name:
// renames keep their mirror, and two tenants' repos can never collide on a
// path.
func repositoryStoreKey(identity LeaseIdentity) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d:%d", identity.InstallationID, identity.RepositoryID))
	return hex.EncodeToString(sum[:])
}

// touchMirrorStamp records use for the mirror reaper. Best-effort: a missing
// stamp just makes the mirror look older than it is.
func (s *Service) touchMirrorStamp(repoKey string) {
	dir := s.mirrorDir(repoKey)
	if _, err := os.Stat(dir); err != nil {
		return
	}
	stamp := filepath.Join(dir, mirrorStampFile)
	if err := os.WriteFile(stamp, nil, 0o600); err == nil {
		now := time.Now()
		_ = os.Chtimes(stamp, now, now)
	}
}

func (s *Service) ensureMirror(ctx context.Context, mirrorDir, repository string) error {
	if err := os.MkdirAll(filepath.Dir(mirrorDir), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(mirrorDir, "HEAD")); errors.Is(err, os.ErrNotExist) {
		if err := s.runGit(ctx, "init_bare", "", nil, "init", "--bare", mirrorDir); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := s.runGit(ctx, "remote_set_url", mirrorDir, nil, "remote", "set-url", "origin", s.remoteURL(repository)); err == nil {
		return nil
	}
	return s.runGit(ctx, "remote_add", mirrorDir, nil, "remote", "add", "origin", s.remoteURL(repository))
}

// fetchCommit makes spec.SHA present in the mirror. The ref is fetched first
// (the common case, and it advances the mirror), then the SHA directly if
// still absent — that fallback covers force-pushes, rebased PR heads, and
// deleted branches, so a ref-fetch failure is logged and not fatal by itself.
func (s *Service) fetchCommit(ctx context.Context, mirrorDir string, spec checkoutSpec) error {
	env := credentialEnv(s.remoteHost(), spec.GitHubToken)
	fetched := false
	if spec.Ref != "" {
		fetched = true
		if err := s.runGit(ctx, "fetch_ref", mirrorDir, env,
			"fetch", "--force", "--no-tags", "origin", "+"+spec.Ref+":"+requestedRef); err != nil {
			s.cfg.Logger.Info("checkout ref fetch failed; falling back to sha fetch",
				"ref", spec.Ref, "error", boundedGitError(err))
		}
	}
	if err := s.runGit(ctx, "cat_file_ref", mirrorDir, nil, "cat-file", "-e", spec.SHA+"^{commit}"); err == nil {
		if fetched {
			s.Metrics.MirrorFetches.Add(1)
		}
		return nil
	}
	s.Metrics.MirrorFetches.Add(1)
	if err := s.runGit(ctx, "fetch_sha", mirrorDir, env,
		"fetch", "--force", "--no-tags", "origin", "+"+spec.SHA+":"+fetchedSHARef); err != nil {
		return classifyFetchError(err)
	}
	if err := s.runGit(ctx, "cat_file_sha", mirrorDir, nil, "cat-file", "-e", spec.SHA+"^{commit}"); err != nil {
		return fmt.Errorf("%w: fetched ref did not contain the commit", errNotFound)
	}
	return nil
}

// classifyFetchError separates "the commit or repository is not obtainable
// with this token" (a terminal 404 for the client) from transient transport
// failures (a retryable 502). The match set is the stable core of git's
// error vocabulary; anything unrecognized is treated as transient, which at
// worst costs the client its bounded retries.
func classifyFetchError(err error) error {
	message := strings.ToLower(err.Error())
	for _, terminal := range []string{
		"not found",
		"not our ref",
		"couldn't find remote ref",
		"no such remote ref",
		"unadvertised object",
		"authentication failed",
		"could not read from remote repository",
		"access denied",
	} {
		if strings.Contains(message, terminal) {
			return fmt.Errorf("%w: %s", errNotFound, boundedGitError(err))
		}
	}
	return fmt.Errorf("%w: %s", errUpstream, boundedGitError(err))
}

// createBundle writes the exact single-commit pack closure (commit, trees,
// blobs — no history) to a temp file and renames it into the cache. Partial
// packs are structurally unservable: only the rename publishes the path.
func (s *Service) createBundle(ctx context.Context, mirrorDir, bundlePath, sha string) error {
	if err := os.MkdirAll(filepath.Dir(bundlePath), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(bundlePath), ".checkout-*.pack")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := s.writePack(ctx, mirrorDir, sha, tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, bundlePath)
}

// writePack pipes `git rev-list --objects -1 <sha>` into
// `git pack-objects --stdout`, enforcing MaxPackBytes as the bytes stream:
// an oversized pack kills the writer mid-flight instead of filling the disk
// first.
func (s *Service) writePack(ctx context.Context, mirrorDir, sha string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GitTimeout)
	defer cancel()

	revList := exec.CommandContext(ctx, "git", "rev-list", "--objects", "--no-object-names", "-1", sha)
	revList.Dir = mirrorDir
	packObjects := exec.CommandContext(ctx, "git", "pack-objects", "--stdout")
	packObjects.Dir = mirrorDir

	var revErr, packErr strings.Builder
	revList.Stderr = limitBuilder(&revErr)
	packObjects.Stderr = limitBuilder(&packErr)

	revStdout, err := revList.StdoutPipe()
	if err != nil {
		return err
	}
	packObjects.Stdin = revStdout
	limited := &limitedWriter{w: out, remaining: s.cfg.MaxPackBytes}
	packObjects.Stdout = limited

	if err := packObjects.Start(); err != nil {
		return fmt.Errorf("git pack_objects: %w", err)
	}
	if err := revList.Start(); err != nil {
		_ = packObjects.Process.Kill()
		_ = packObjects.Wait()
		return fmt.Errorf("git rev_list: %w", err)
	}
	revWaitErr := revList.Wait()
	packWaitErr := packObjects.Wait()
	if limited.exceeded {
		return errTooLarge
	}
	if revWaitErr != nil {
		return fmt.Errorf("git rev_list: %w: %s", revWaitErr, strings.TrimSpace(revErr.String()))
	}
	if packWaitErr != nil {
		return fmt.Errorf("git pack_objects: %w: %s", packWaitErr, strings.TrimSpace(packErr.String()))
	}
	return nil
}

// limitedWriter fails the stream once the limit is crossed.
type limitedWriter struct {
	w         io.Writer
	remaining int64
	exceeded  bool
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.remaining {
		l.exceeded = true
		return 0, errTooLarge
	}
	n, err := l.w.Write(p)
	l.remaining -= int64(n)
	return n, err
}

func (s *Service) remoteURL(repository string) string {
	return strings.TrimRight(s.cfg.GitHubWebBaseURL, "/") + "/" + repository + ".git"
}

func (s *Service) remoteHost() string {
	parsed, err := url.Parse(strings.TrimSpace(s.cfg.GitHubWebBaseURL))
	if err != nil || parsed.Host == "" {
		return "github.com"
	}
	return parsed.Host
}

// credentialEnv injects the job's GitHub token as an HTTP extraheader via
// GIT_CONFIG_* environment variables: never argv (readable in /proc), never
// the remote URL (persisted into mirror config), never disk.
func credentialEnv(host, token string) []string {
	credential := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://" + host + "/.extraheader",
		"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic " + credential,
	}
}

// runGit executes one git command under the configured timeout. The label
// names the step in errors; stderr is captured bounded and only ever
// surfaces through boundedGitError.
func (s *Service) runGit(ctx context.Context, label, dir string, extraEnv []string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), extraEnv...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		bounded := output
		if len(bounded) > 4096 {
			bounded = bounded[:4096]
		}
		return fmt.Errorf("git %s: %w: %s", label, err, strings.TrimSpace(string(bounded)))
	}
	return nil
}

// boundedGitError trims an error for logging. Credentials never appear in
// git stderr by construction (they travel via extraheader env), but the
// bound keeps log lines sane regardless.
func boundedGitError(err error) string {
	message := err.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

// limitBuilder bounds a stderr sink.
func limitBuilder(b *strings.Builder) io.Writer {
	return &boundedBuilderWriter{b: b}
}

type boundedBuilderWriter struct{ b *strings.Builder }

func (w *boundedBuilderWriter) Write(p []byte) (int, error) {
	const maxStderr = 4096
	if w.b.Len() < maxStderr {
		room := maxStderr - w.b.Len()
		if len(p) > room {
			w.b.Write(p[:room])
		} else {
			w.b.Write(p)
		}
	}
	return len(p), nil
}
