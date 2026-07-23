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

// preparedBundle describes a servable pack. File is opened under the repo
// lock and owned by the caller, which must close it: holding the descriptor
// makes the pack immune to a concurrent reap (POSIX keeps the bytes alive
// until the last handle closes).
type preparedBundle struct {
	File      *os.File
	SizeBytes int64
	CacheHit  bool
	ThinBase  string
}

// prepareBundle returns the cached pack for (repository, sha, have) or builds
// it. When have is a target ancestor, the pack contains the commit range and
// may delta-compress objects against that base. All mutation for one
// repository is serialized behind its repo lock, so concurrent identical
// requests collapse into one fetch and the followers take the cache path.
func (s *Service) prepareBundle(ctx context.Context, identity AssignmentIdentity, spec checkoutSpec) (preparedBundle, error) {
	repoKey := repositoryStoreKey(identity)
	unlock := s.lockRepo(repoKey)
	defer unlock()

	bundlePath := s.bundlePath(repoKey, spec.SHA, spec.Have)
	s.touchMirrorStamp(repoKey)
	if file, size, ok := openIfNonEmpty(bundlePath); ok {
		now := time.Now()
		_ = os.Chtimes(bundlePath, now, now) // LRU recency for the reaper
		s.Metrics.CacheHits.Add(1)
		return preparedBundle{File: file, SizeBytes: size, CacheHit: true, ThinBase: spec.Have}, nil
	}

	mirrorDir := s.mirrorDir(repoKey)
	// The clone URL is built from the assignment's repository name, never the
	// request string: validateRequest already proved the two equal under case
	// folding, and the assignment is the authority on which repository this host may
	// fetch.
	if err := s.ensureMirror(ctx, mirrorDir, identity.RepositoryFullName); err != nil {
		return preparedBundle{}, err
	}
	if err := s.fetchCommit(ctx, mirrorDir, spec); err != nil {
		return preparedBundle{}, err
	}
	effectiveHave := ""
	if spec.Have != "" && s.canThinAgainst(ctx, mirrorDir, spec.Have, spec.SHA) {
		effectiveHave = spec.Have
	}
	bundlePath = s.bundlePath(repoKey, spec.SHA, effectiveHave)
	if file, size, ok := openIfNonEmpty(bundlePath); ok {
		now := time.Now()
		_ = os.Chtimes(bundlePath, now, now)
		s.Metrics.CacheHits.Add(1)
		return preparedBundle{File: file, SizeBytes: size, CacheHit: true, ThinBase: effectiveHave}, nil
	}
	if err := s.createBundle(ctx, mirrorDir, bundlePath, spec.SHA, effectiveHave); err != nil {
		return preparedBundle{}, err
	}
	file, size, ok := openIfNonEmpty(bundlePath)
	if !ok {
		return preparedBundle{}, fmt.Errorf("bundle disappeared after creation")
	}
	return preparedBundle{File: file, SizeBytes: size, CacheHit: false, ThinBase: effectiveHave}, nil
}

// openIfNonEmpty opens the pack and returns its size, reporting false when it
// is absent or empty. Called under the repo lock so the file cannot be reaped
// between the open and the size read.
func openIfNonEmpty(path string) (*os.File, int64, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, false
	}
	stat, err := file.Stat()
	if err != nil || stat.Size() == 0 {
		_ = file.Close()
		return nil, 0, false
	}
	return file, stat.Size(), true
}

func (s *Service) mirrorDir(repoKey string) string {
	return filepath.Join(s.cfg.StoreDir, "mirrors", repoKey)
}

func (s *Service) bundlePath(repoKey, sha, have string) string {
	if have == "" {
		have = "full"
	}
	return filepath.Join(s.cfg.StoreDir, "bundles", repoKey, sha, have, bundleFilename)
}

// repositoryStoreKey keys stores by immutable GitHub identity, not by name:
// renames keep their mirror, and two tenants' repos can never collide on a
// path.
func repositoryStoreKey(identity AssignmentIdentity) string {
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
	refFetchSucceeded := false
	if spec.Ref != "" {
		if err := s.runGit(ctx, "fetch_ref", mirrorDir, env,
			"fetch", "--force", "--no-tags", "origin", "+"+spec.Ref+":"+requestedRef); err != nil {
			s.cfg.Logger.Info("checkout ref fetch failed; falling back to sha fetch",
				"ref", spec.Ref, "error", boundedGitError(err))
		} else {
			refFetchSucceeded = true
		}
	}
	if err := s.runGit(ctx, "cat_file_ref", mirrorDir, nil, "cat-file", "-e", spec.SHA+"^{commit}"); err == nil {
		if refFetchSucceeded {
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
		"access denied",
	} {
		if strings.Contains(message, terminal) {
			return fmt.Errorf("%w: %s", errNotFound, boundedGitError(err))
		}
	}
	return fmt.Errorf("%w: %s", errUpstream, boundedGitError(err))
}

// createBundle writes a full target closure or a thin have..target pack to a
// temp file and renames it into the cache. Partial packs are structurally
// unservable: only the rename publishes the path.
func (s *Service) createBundle(ctx context.Context, mirrorDir, bundlePath, sha, have string) error {
	if err := os.MkdirAll(filepath.Dir(bundlePath), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(bundlePath), ".checkout-*.pack")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := s.writePack(ctx, mirrorDir, sha, have, tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, bundlePath)
}

// writePack creates a full target closure or a thin have..target pack,
// enforcing MaxPackBytes as the bytes stream: an oversized pack kills the
// writer mid-flight instead of filling the disk first.
func (s *Service) writePack(ctx context.Context, mirrorDir, sha, have string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GitTimeout)
	defer cancel()

	packArguments := []string{"pack-objects", "--stdout"}
	var revList *exec.Cmd
	if have != "" {
		packArguments = append(packArguments, "--revs", "--thin")
	} else {
		revList = exec.CommandContext(ctx, "git", "rev-list", "--objects", "--no-object-names", "-1", sha)
		revList.Dir = mirrorDir
		revList.Env = s.gitEnv(nil)
	}
	packObjects := exec.CommandContext(ctx, "git", packArguments...)
	packObjects.Dir = mirrorDir
	packObjects.Env = s.gitEnv(nil)

	var revErr, packErr strings.Builder
	packObjects.Stderr = limitBuilder(&packErr)
	if revList == nil {
		packObjects.Stdin = strings.NewReader(sha + "\n^" + have + "\n")
	} else {
		revList.Stderr = limitBuilder(&revErr)
		revStdout, err := revList.StdoutPipe()
		if err != nil {
			return err
		}
		packObjects.Stdin = revStdout
	}
	limited := &limitedWriter{w: out, remaining: s.cfg.MaxPackBytes}
	packObjects.Stdout = limited

	if err := packObjects.Start(); err != nil {
		return fmt.Errorf("git pack_objects: %w", err)
	}
	var revWaitErr error
	if revList != nil {
		if err := revList.Start(); err != nil {
			_ = packObjects.Process.Kill()
			_ = packObjects.Wait()
			return fmt.Errorf("git rev_list: %w", err)
		}
		revWaitErr = revList.Wait()
	}
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

func (s *Service) canThinAgainst(ctx context.Context, mirrorDir, have, target string) bool {
	if err := s.runGit(ctx, "cat_file_have", mirrorDir, nil, "cat-file", "-e", have+"^{commit}"); err != nil {
		return false
	}
	return s.runGit(ctx, "merge_base_have", mirrorDir, nil, "merge-base", "--is-ancestor", have, target) == nil
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

// gitEnv builds a minimal, controlled environment for a git child. It does
// not inherit the daemon's environment: an inherited GIT_TRACE/GIT_CURL_VERBOSE
// would print the extraheader token to stderr, and an inherited system/user
// gitconfig could rewrite URLs or attach an unrelated credential helper to a
// tenant-driven fetch. Only PATH and the credential vars carry over.
func (s *Service) gitEnv(extraEnv []string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + s.cfg.StoreDir,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	}
	return append(env, extraEnv...)
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
	cmd.Env = s.gitEnv(extraEnv)
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
