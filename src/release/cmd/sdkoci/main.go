package main

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

const (
	artifactType          = "application/vnd.guardian.sdk.npm.package.v1"
	npmTarballMediaType   = "application/gzip"
	deterministicCreated  = "1970-01-01T00:00:00Z"
	expectedPackageName   = "@guardian-intelligence/aisucks"
	defaultRepositoryPath = "oci.gi.org/guardian/aisucks/sdk/npm:edge"
)

type cliConfig struct {
	tarballPath  string
	packJSON     string
	ref          string
	ociLayout    string
	tag          string
	plainHTTP    bool
	credentials  credentialConfig
	sourceRepo   string
	sourceCommit string
	outputPath   string
}

type credentialConfig struct {
	username       string
	passwordEnv    string
	accessTokenEnv string
}

type packEntry struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Filename  string `json:"filename"`
	Integrity string `json:"integrity"`
	Shasum    string `json:"shasum"`
	Size      int64  `json:"size"`
}

type targetRef struct {
	registry   string
	repository string
	tag        string
}

type artifactResult struct {
	Distributable string `json:"distributable"`
	PayloadForm   string `json:"payload_form"`
	Channel       string `json:"channel"`
	OCIDigest     string `json:"oci_digest"`
	OCIRef        string `json:"oci_ref"`
	TarballDigest string `json:"tarball_sha256"`
	NPMIntegrity  string `json:"npm_integrity"`
	Package       string `json:"package"`
	Version       string `json:"version"`
	SourceRepo    string `json:"source_repo"`
	SourceCommit  string `json:"source_commit"`
	LayerTitle    string `json:"layer_title"`
}

func main() {
	cfg := parseFlags()
	if err := run(context.Background(), cfg, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "sdk-oci: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() cliConfig {
	var cfg cliConfig
	flag.StringVar(&cfg.tarballPath, "tarball", "", "npm package tarball produced by Bazel")
	flag.StringVar(&cfg.packJSON, "pack-json", "", "npm pack --json metadata produced beside the tarball")
	flag.StringVar(&cfg.ref, "ref", defaultRepositoryPath, "remote OCI reference to push, including tag")
	flag.StringVar(&cfg.ociLayout, "oci-layout", "", "write an OCI image layout instead of pushing a remote reference")
	flag.StringVar(&cfg.tag, "tag", "", "tag to apply when --oci-layout is used")
	flag.BoolVar(&cfg.plainHTTP, "plain-http", false, "use plain HTTP for the remote registry")
	flag.StringVar(&cfg.credentials.username, "username", "", "registry username for remote pushes")
	flag.StringVar(&cfg.credentials.passwordEnv, "password-env", "", "environment variable containing a registry password for remote pushes")
	flag.StringVar(&cfg.credentials.accessTokenEnv, "access-token-env", "", "environment variable containing a registry bearer access token for remote pushes")
	flag.StringVar(&cfg.sourceRepo, "source-repo", "https://github.com/guardian-intelligence/guardian", "source repository recorded in OCI annotations")
	flag.StringVar(&cfg.sourceCommit, "source-commit", "", "source commit recorded in OCI annotations")
	flag.StringVar(&cfg.outputPath, "output", "", "write result JSON to this file instead of stdout")
	flag.Parse()
	return cfg
}

func run(ctx context.Context, cfg cliConfig, stdout io.Writer) error {
	if cfg.tarballPath == "" {
		return errors.New("--tarball is required")
	}
	if cfg.packJSON == "" {
		return errors.New("--pack-json is required")
	}
	if cfg.sourceCommit == "" {
		commit, err := sourceCommitFromGit()
		if err != nil {
			return err
		}
		cfg.sourceCommit = commit
	}
	if !isFullSHA(cfg.sourceCommit) {
		return fmt.Errorf("source commit is not a full 40-character hex SHA: %q", cfg.sourceCommit)
	}

	entry, err := readPackEntry(cfg.packJSON)
	if err != nil {
		return err
	}
	if err := validatePackEntry(entry); err != nil {
		return err
	}

	tarball, err := os.ReadFile(cfg.tarballPath)
	if err != nil {
		return fmt.Errorf("read tarball: %w", err)
	}
	if int64(len(tarball)) != entry.Size {
		return fmt.Errorf("tarball size mismatch: metadata=%d actual=%d", entry.Size, len(tarball))
	}
	tarballSHA256 := sha256Hex(tarball)
	if entry.Integrity != npmIntegrity(tarball) {
		return fmt.Errorf("tarball integrity mismatch: metadata=%s actual=%s", entry.Integrity, npmIntegrity(tarball))
	}

	annotations := sdkAnnotations(entry, tarballSHA256, cfg.sourceRepo, cfg.sourceCommit)
	channel := ""
	var manifest ocispec.Descriptor
	var ref string
	if cfg.ociLayout != "" {
		if cfg.tag == "" {
			return errors.New("--tag is required with --oci-layout")
		}
		manifest, err = pushLayout(ctx, cfg.ociLayout, cfg.tag, entry, tarball, annotations)
		ref = layoutRef(cfg.ociLayout, cfg.tag, manifest.Digest.String())
		channel = cfg.tag
	} else {
		target, err := parseTaggedRef(cfg.ref)
		if err != nil {
			return err
		}
		manifest, err = pushRemote(ctx, target, cfg.plainHTTP, cfg.credentials, entry, tarball, annotations)
		ref = target.repository + "@" + manifest.Digest.String()
		channel = target.tag
	}
	if err != nil {
		return err
	}

	result := artifactResult{
		Distributable: "aisucks-ts-sdk",
		PayloadForm:   "npm",
		Channel:       channel,
		OCIDigest:     manifest.Digest.String(),
		OCIRef:        ref,
		TarballDigest: "sha256:" + tarballSHA256,
		NPMIntegrity:  entry.Integrity,
		Package:       entry.Name,
		Version:       entry.Version,
		SourceRepo:    cfg.sourceRepo,
		SourceCommit:  cfg.sourceCommit,
		LayerTitle:    entry.Filename,
	}
	return writeResult(result, cfg.outputPath, stdout)
}

func readPackEntry(path string) (packEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return packEntry{}, fmt.Errorf("read pack json: %w", err)
	}
	var entries []packEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return packEntry{}, fmt.Errorf("parse pack json: %w", err)
	}
	if len(entries) != 1 {
		return packEntry{}, fmt.Errorf("expected one npm pack entry, found %d", len(entries))
	}
	return entries[0], nil
}

func validatePackEntry(entry packEntry) error {
	if entry.Name != expectedPackageName {
		return fmt.Errorf("unexpected package name %q", entry.Name)
	}
	if entry.Version == "" {
		return errors.New("npm pack metadata has empty version")
	}
	if entry.Filename == "" {
		return errors.New("npm pack metadata has empty filename")
	}
	if !strings.HasSuffix(entry.Filename, ".tgz") {
		return fmt.Errorf("npm pack filename %q is not a .tgz", entry.Filename)
	}
	if !strings.HasPrefix(entry.Integrity, "sha512-") {
		return fmt.Errorf("npm pack integrity %q is not sha512 SRI", entry.Integrity)
	}
	if entry.Size <= 0 {
		return fmt.Errorf("npm pack size must be positive, got %d", entry.Size)
	}
	return nil
}

func pushRemote(ctx context.Context, target targetRef, plainHTTP bool, creds credentialConfig, entry packEntry, tarball []byte, annotations map[string]string) (ocispec.Descriptor, error) {
	repo, err := remote.NewRepository(target.repository)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("remote repository %s: %w", target.repository, err)
	}
	repo.PlainHTTP = plainHTTP
	credFn, err := credentialFunc(target.registry, creds)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	repo.Client = &auth.Client{
		Client:     retry.DefaultClient,
		Cache:      auth.NewCache(),
		Credential: credFn,
	}
	return pushToTarget(ctx, repo, target.tag, entry, tarball, annotations)
}

func pushLayout(ctx context.Context, path, tag string, entry packEntry, tarball []byte, annotations map[string]string) (ocispec.Descriptor, error) {
	store, err := oci.New(path)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("oci layout %s: %w", path, err)
	}
	return pushToTarget(ctx, store, tag, entry, tarball, annotations)
}

type pusher interface {
	oras.Target
	Tag(context.Context, ocispec.Descriptor, string) error
}

func pushToTarget(ctx context.Context, target pusher, tag string, entry packEntry, tarball []byte, annotations map[string]string) (ocispec.Descriptor, error) {
	layer, err := oras.PushBytes(ctx, target, npmTarballMediaType, tarball)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("push tarball layer: %w", err)
	}
	layer.Annotations = map[string]string{
		ocispec.AnnotationTitle: entry.Filename,
	}

	opts := oras.PackManifestOptions{
		Layers:              []ocispec.Descriptor{layer},
		ManifestAnnotations: annotations,
	}
	manifest, err := oras.PackManifest(ctx, target, oras.PackManifestVersion1_1, artifactType, opts)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("pack OCI artifact manifest: %w", err)
	}
	if err := target.Tag(ctx, manifest, tag); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("tag OCI artifact %s: %w", tag, err)
	}
	return manifest, nil
}

func parseTaggedRef(ref string) (targetRef, error) {
	if ref == "" {
		return targetRef{}, errors.New("--ref is required unless --oci-layout is set")
	}
	if strings.Contains(ref, "@") {
		return targetRef{}, fmt.Errorf("push reference must be tag-addressed, got %q", ref)
	}
	lastSlash := strings.LastIndex(ref, "/")
	lastColon := strings.LastIndex(ref, ":")
	if lastColon <= lastSlash {
		return targetRef{}, fmt.Errorf("push reference must include a tag, got %q", ref)
	}
	repository := ref[:lastColon]
	tag := ref[lastColon+1:]
	if repository == "" || tag == "" {
		return targetRef{}, fmt.Errorf("invalid tagged reference %q", ref)
	}
	firstSlash := strings.Index(repository, "/")
	if firstSlash <= 0 {
		return targetRef{}, fmt.Errorf("push reference must include registry and repository, got %q", ref)
	}
	return targetRef{registry: repository[:firstSlash], repository: repository, tag: tag}, nil
}

func credentialFunc(registry string, cfg credentialConfig) (auth.CredentialFunc, error) {
	if cfg.accessTokenEnv != "" {
		if cfg.username != "" || cfg.passwordEnv != "" {
			return nil, errors.New("--access-token-env cannot be combined with --username or --password-env")
		}
		token := os.Getenv(cfg.accessTokenEnv)
		if token == "" {
			return nil, fmt.Errorf("%s is empty", cfg.accessTokenEnv)
		}
		return auth.StaticCredential(registry, auth.Credential{AccessToken: token}), nil
	}
	if cfg.username == "" && cfg.passwordEnv == "" {
		return nil, nil
	}
	if cfg.username == "" || cfg.passwordEnv == "" {
		return nil, errors.New("--username and --password-env must be set together")
	}
	password := os.Getenv(cfg.passwordEnv)
	if password == "" {
		return nil, fmt.Errorf("%s is empty", cfg.passwordEnv)
	}
	return auth.StaticCredential(registry, auth.Credential{
		Username: cfg.username,
		Password: password,
	}), nil
}

func sdkAnnotations(entry packEntry, tarballSHA256, sourceRepo, sourceCommit string) map[string]string {
	return map[string]string{
		ocispec.AnnotationCreated:     deterministicCreated,
		ocispec.AnnotationDescription: "Guardian aisucks TypeScript SDK npm package tarball",
		ocispec.AnnotationRevision:    sourceCommit,
		ocispec.AnnotationSource:      sourceRepo,
		"dev.guardian.distributable":  "aisucks-ts-sdk",
		"dev.guardian.payload-form":   "npm",
		"dev.guardian.package":        entry.Name,
		"dev.guardian.version":        entry.Version,
		"dev.guardian.npm.integrity":  entry.Integrity,
		"dev.guardian.tarball.sha256": "sha256:" + tarballSHA256,
	}
}

func npmIntegrity(data []byte) string {
	sum := sha512.Sum512(data)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeResult(result artifactResult, outputPath string, stdout io.Writer) error {
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if outputPath != "" {
		if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
			return fmt.Errorf("create output dir: %w", err)
		}
		if err := os.WriteFile(outputPath, raw, 0o644); err != nil {
			return err
		}
	}
	_, err = stdout.Write(raw)
	return err
}

func layoutRef(path, tag, digest string) string {
	return filepath.Clean(path) + ":" + tag + "@" + digest
}

func sourceCommitFromGit() (string, error) {
	head, err := os.ReadFile(".git/HEAD")
	if err != nil {
		return "", fmt.Errorf("--source-commit is required outside a git worktree: %w", err)
	}
	text := strings.TrimSpace(string(head))
	if strings.HasPrefix(text, "ref: ") {
		ref := strings.TrimPrefix(text, "ref: ")
		commit, err := readGitRef(ref)
		if err != nil {
			return "", err
		}
		text = commit
	}
	if !isFullSHA(text) {
		return "", fmt.Errorf("git HEAD is not a full commit SHA: %q", text)
	}
	return text, nil
}

func readGitRef(ref string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(".git", filepath.FromSlash(ref)))
	if err == nil {
		return strings.TrimSpace(string(raw)), nil
	}
	packed, packedErr := os.ReadFile(filepath.Join(".git", "packed-refs"))
	if packedErr != nil {
		return "", fmt.Errorf("read git ref %s: %w", ref, err)
	}
	for _, line := range strings.Split(string(packed), "\n") {
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		sha, name, ok := strings.Cut(line, " ")
		if ok && name == ref {
			return sha, nil
		}
	}
	return "", fmt.Errorf("git ref %s not found in ref file or packed-refs", ref)
}

func isFullSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
