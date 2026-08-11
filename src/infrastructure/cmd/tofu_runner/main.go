// tofu-runner — the in-cluster reconcile step for the bootstrap OpenTofu
// roots (docs/tofu-gitops-design.md). One CronJob per root runs this binary
// on the root's schedule: it fetches the exact source artifact Flux
// reconciles, runs tofu, and lets the result decide the exit code. There is
// no long-lived process and no second control plane — the CronJob schedule
// is the interval and R2 conditional-write locking (use_lockfile) is the
// mutex.
//
// Signalling is by exit code, not by an Alerta credential this pod would
// otherwise have to hold: a non-zero exit fails the Job, and the
// tofu-runner-health VMRule pages on kube_job_status_failed exactly as the
// etcd-snapshot CronJob does. Drift on a plan-mode root is the expected
// soak state, so it logs the plan and exits 0; only a tofu error, a failed
// apply, or drift on a page-on-drift root (the token minter's hostile-mint
// detector) is a non-zero exit.
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// mode is the reconcile posture of a root: plan holds after planning (the
// soak), apply executes a non-empty plan.
type mode string

const (
	modePlan  mode = "plan"
	modeApply mode = "apply"
)

type config struct {
	// rootName labels logs and matches the CronJob/Job name the health rule
	// keys on; rootPath is the directory inside the artifact that holds the
	// root's .tf files.
	rootName string
	rootPath string
	mode     mode
	// pageOnDrift makes a non-empty plan a non-zero exit even in plan mode.
	// The token-minter root sets this: for it, drift is not a pending change
	// to soak on but a signal that a token was minted or revoked out of band.
	pageOnDrift bool

	// The Flux source object whose status.artifact this run reconciles. Kind
	// is GitRepository in steady state and OCIRepository in the dark overlay;
	// both expose the same artifact, and the served API version is discovered
	// rather than pinned so a Flux bump to either kind needs no change here.
	sourceKind      string
	sourceName      string
	sourceNamespace string

	tofuBin string
	workDir string
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadConfig() (config, error) {
	cfg := config{
		rootName:        os.Getenv("ROOT_NAME"),
		rootPath:        os.Getenv("ROOT_PATH"),
		mode:            mode(envOr("MODE", string(modePlan))),
		pageOnDrift:     os.Getenv("PAGE_ON_DRIFT") == "true",
		sourceKind:      envOr("SOURCE_KIND", "GitRepository"),
		sourceName:      envOr("SOURCE_NAME", "guardian"),
		sourceNamespace: envOr("SOURCE_NAMESPACE", "cozy-fluxcd"),
		tofuBin:         envOr("TOFU_BIN", "tofu"),
		workDir:         envOr("WORKDIR", "/workspace"),
	}
	if cfg.rootName == "" {
		return cfg, errors.New("ROOT_NAME is required")
	}
	if cfg.rootPath == "" {
		return cfg, errors.New("ROOT_PATH is required")
	}
	if cfg.mode != modePlan && cfg.mode != modeApply {
		return cfg, fmt.Errorf("MODE must be plan or apply, got %q", cfg.mode)
	}
	return cfg, nil
}

// k8sClient talks to the apiserver with the pod's in-cluster credentials.
// This is the runner's only apiserver access and it is read-only: a single
// GET of the Flux source object to learn where the artifact lives.
type k8sClient struct {
	host   string
	token  string
	client *http.Client
}

func inClusterClient() (*k8sClient, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := envOr("KUBERNETES_SERVICE_PORT", "443")
	if host == "" {
		return nil, errors.New("KUBERNETES_SERVICE_HOST unset — not running in-cluster")
	}
	const base = "/var/run/secrets/kubernetes.io/serviceaccount"
	token, err := os.ReadFile(base + "/token")
	if err != nil {
		return nil, fmt.Errorf("read service account token: %w", err)
	}
	caPEM, err := os.ReadFile(base + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("read service account CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("service account CA is not valid PEM")
	}
	return &k8sClient{
		// IPv6 apiserver ClusterIPs arrive bare; JoinHostPort brackets them.
		host:  "https://" + hostPort(host, port),
		token: strings.TrimSpace(string(token)),
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		},
	}, nil
}

func hostPort(host, port string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

func (c *k8sClient) getJSON(ctx context.Context, path string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.host+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, 8<<20)
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, body)
		return resp.StatusCode, fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	if err := json.NewDecoder(body).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("decode %s: %w", path, err)
	}
	return resp.StatusCode, nil
}

// artifact is the subset of a Flux source object's status.artifact this
// runner needs: where to fetch the tarball and the digest to verify it.
type artifact struct {
	URL      string
	Digest   string
	Revision string
}

const sourceGroup = "source.toolkit.fluxcd.io"

// resolveArtifact finds the source object across whatever API version the
// group currently serves the kind at. GitRepository and OCIRepository can
// sit at different versions in the same group, so the group's version list
// is walked (preferred first) rather than pinning one — a Flux upgrade that
// promotes either kind then needs no change here.
func (c *k8sClient) resolveArtifact(ctx context.Context, cfg config) (artifact, error) {
	var group struct {
		PreferredVersion struct {
			Version string `json:"version"`
		} `json:"preferredVersion"`
		Versions []struct {
			Version string `json:"version"`
		} `json:"versions"`
	}
	if _, err := c.getJSON(ctx, "/apis/"+sourceGroup, &group); err != nil {
		return artifact{}, fmt.Errorf("discover %s versions: %w", sourceGroup, err)
	}
	versions := []string{group.PreferredVersion.Version}
	for _, v := range group.Versions {
		if v.Version != group.PreferredVersion.Version {
			versions = append(versions, v.Version)
		}
	}

	plural := pluralize(cfg.sourceKind)
	var lastErr error
	for _, v := range versions {
		if v == "" {
			continue
		}
		path := fmt.Sprintf("/apis/%s/%s/namespaces/%s/%s/%s",
			sourceGroup, v, cfg.sourceNamespace, plural, cfg.sourceName)
		var obj struct {
			Status struct {
				Artifact *struct {
					URL      string `json:"url"`
					Digest   string `json:"digest"`
					Revision string `json:"revision"`
				} `json:"artifact"`
			} `json:"status"`
		}
		code, err := c.getJSON(ctx, path, &obj)
		if code == http.StatusNotFound {
			// This version does not serve the resource (or the object is not
			// there under it); try the next served version.
			lastErr = err
			continue
		}
		if err != nil {
			return artifact{}, err
		}
		if obj.Status.Artifact == nil || obj.Status.Artifact.URL == "" {
			return artifact{}, fmt.Errorf("%s %s/%s has no ready artifact yet",
				cfg.sourceKind, cfg.sourceNamespace, cfg.sourceName)
		}
		return artifact{
			URL:      obj.Status.Artifact.URL,
			Digest:   obj.Status.Artifact.Digest,
			Revision: obj.Status.Artifact.Revision,
		}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no served version of %s exposed %s", sourceGroup, plural)
	}
	return artifact{}, lastErr
}

// pluralize turns a source Kind into its lowercase resource name.
// GitRepository→gitrepositories, OCIRepository→ocirepositories: the y→ies
// rule covers both, which are the only kinds a root ever references.
func pluralize(kind string) string {
	k := strings.ToLower(kind)
	if strings.HasSuffix(k, "y") {
		return k[:len(k)-1] + "ies"
	}
	return k + "s"
}

// fetchArtifact downloads the source tarball, verifies it against the digest
// Flux published, and extracts it under dest. The fetch is plain HTTP: the
// artifact endpoint is source-controller inside the cluster, and the digest
// check — not transport TLS — is what guarantees the bytes. A mismatch is
// fatal: reconciling stale or tampered source is worse than not reconciling.
func fetchArtifact(ctx context.Context, art artifact, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, art.URL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch artifact: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch artifact: %s", resp.Status)
	}

	hasher := sha256.New()
	if err := extractTarGz(io.TeeReader(resp.Body, hasher), dest); err != nil {
		return err
	}
	got := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if !digestMatches(art.Digest, got) {
		return fmt.Errorf("artifact digest mismatch: want %q, got %q", art.Digest, got)
	}
	return nil
}

// digestMatches compares the published digest to the computed one. Flux has
// emitted the digest both as a bare sha256 and, on newer versions, prefixed
// "sha256:"; normalise before comparing so neither form spuriously fails.
func digestMatches(published, computed string) bool {
	norm := func(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "sha256:") }
	return norm(published) != "" && norm(published) == norm(computed)
}

// extractTarGz unpacks a gzipped tar into dest, refusing any entry whose
// path escapes dest (path traversal via "../" or an absolute name). The
// hasher must see every byte, so the whole body is streamed through even
// though extraction only writes regular files and directories.
func extractTarGz(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gunzip artifact: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		target := filepath.Join(root, hdr.Name)
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return fmt.Errorf("tar entry escapes destination: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeFile(target, tr, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		default:
			// Source artifacts are trees of regular files; symlinks and
			// devices have no place in a tofu root and are skipped rather
			// than trusted.
		}
	}
}

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode|0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return nil
}

// runTofu executes one tofu subcommand in the root directory, streaming its
// output to the pod log (which is where the plan lands for the soak). It
// returns the process exit code so the caller can act on tofu plan's
// -detailed-exitcode contract (0 no-change, 2 changes, 1 error).
func runTofu(ctx context.Context, cfg config, rootDir string, args ...string) (int, error) {
	cmd := exec.CommandContext(ctx, cfg.tofuBin, args...)
	cmd.Dir = rootDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// TF_IN_AUTOMATION trims interactive hints from output; the credentials,
	// backend keys, and TF_ENCRYPTION document all arrive as envFrom Secrets
	// on the pod, so the child inherits the process environment as-is.
	cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1", "TF_INPUT=0")
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, err
}

// reconcile runs the fetch→init→plan→(apply) sequence and returns the
// process exit code. Every early error is a non-zero exit so the Job fails
// and the health rule pages; the one deliberate success-with-changes case is
// drift on a plan-mode root that does not page.
func reconcile(ctx context.Context, cfg config) int {
	kc, err := inClusterClient()
	if err != nil {
		slog.Error("in-cluster config", "err", err)
		return 1
	}
	art, err := kc.resolveArtifact(ctx, cfg)
	if err != nil {
		slog.Error("resolve source artifact", "err", err)
		return 1
	}
	slog.Info("reconciling", "root", cfg.rootName, "mode", cfg.mode, "revision", art.Revision)

	if err := os.MkdirAll(cfg.workDir, 0o755); err != nil {
		slog.Error("prepare workdir", "err", err)
		return 1
	}
	if err := fetchArtifact(ctx, art, cfg.workDir); err != nil {
		slog.Error("fetch source artifact", "err", err)
		return 1
	}
	rootDir := filepath.Join(cfg.workDir, filepath.Clean(cfg.rootPath))

	if code, err := runTofu(ctx, cfg, rootDir, "init", "-input=false"); err != nil || code != 0 {
		slog.Error("tofu init failed", "code", code, "err", err)
		return 1
	}

	planCode, err := runTofu(ctx, cfg, rootDir,
		"plan", "-input=false", "-lock-timeout=120s", "-detailed-exitcode", "-out=tfplan")
	if err != nil {
		slog.Error("tofu plan errored", "err", err)
		return 1
	}
	switch planCode {
	case 0:
		slog.Info("no changes", "root", cfg.rootName)
		return 0
	case 2:
		return handleDrift(ctx, cfg, rootDir)
	default:
		// -detailed-exitcode returns 1 only on error; plan already streamed
		// the diagnostics to the log.
		slog.Error("tofu plan failed", "code", planCode)
		return 1
	}
}

// handleDrift decides what a non-empty plan means for this root. Apply-mode
// roots apply it; plan-mode roots hold (the soak) unless they are the
// page-on-drift detector, in which case an unexpected plan is the alarm.
func handleDrift(ctx context.Context, cfg config, rootDir string) int {
	if cfg.mode == modeApply {
		slog.Info("plan has changes, applying", "root", cfg.rootName)
		if code, err := runTofu(ctx, cfg, rootDir,
			"apply", "-input=false", "-lock-timeout=120s", "tfplan"); err != nil || code != 0 {
			slog.Error("tofu apply failed", "code", code, "err", err)
			return 1
		}
		slog.Info("apply complete", "root", cfg.rootName)
		return 0
	}
	if cfg.pageOnDrift {
		slog.Error("plan has changes on a page-on-drift root — treat as out-of-band change",
			"root", cfg.rootName)
		return 1
	}
	slog.Warn("plan has changes (plan-mode soak, holding)", "root", cfg.rootName)
	return 0
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	// The CronJob's activeDeadlineSeconds is the real wall-clock bound; this
	// context ceiling is a generous backstop so a wedged provider call cannot
	// outlive the pod's own deadline silently.
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	defer cancel()
	os.Exit(reconcile(ctx, cfg))
}
