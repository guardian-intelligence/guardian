package main

import (
	"bytes"
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
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

const (
	defaultArtifactType          = "application/vnd.guardian.sdk.npm.package.v1"
	attestationArtifactType      = "application/vnd.guardian.release.in-toto.bundle.v1"
	attestationBundleMediaType   = "application/vnd.in-toto.bundle+jsonl"
	defaultPayloadMediaType      = "application/gzip"
	deterministicCreated         = "1970-01-01T00:00:00Z"
	defaultDistributable         = "aisucks-ts-sdk"
	defaultPayloadForm           = "npm"
	defaultDescription           = "Guardian aisucks TypeScript SDK npm package tarball"
	defaultExpectedPackageName   = "@guardian-intelligence/aisucks"
	defaultFilenameSuffix        = ".tgz"
	defaultRepositoryPath        = "oci.guardianintelligence.org/guardian/aisucks/sdk/npm:edge"
	defaultAttestationLayerTitle = "guardian-release.intoto.jsonl"
)

type cliConfig struct {
	tarballPath      string
	packJSON         string
	artifactType     string
	payloadMediaType string
	distributable    string
	payloadForm      string
	description      string
	expectedPackage  string
	filenameSuffix   string
	attestationPath  string
	attestationTitle string
	ref              string
	ociLayout        string
	tag              string
	plainHTTP        bool
	credentials      credentialConfig
	sourceRepo       string
	sourceCommit     string
	outputPath       string
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
	SHA256    string `json:"sha256"`
}

type targetRef struct {
	registry   string
	repository string
	tag        string
}

type artifactResult struct {
	Distributable     string `json:"distributable"`
	PayloadForm       string `json:"payload_form"`
	Channel           string `json:"channel"`
	OCIDigest         string `json:"oci_digest"`
	OCIRef            string `json:"oci_ref"`
	AttestationDigest string `json:"attestation_digest,omitempty"`
	AttestationRef    string `json:"attestation_ref,omitempty"`
	PayloadDigest     string `json:"payload_sha256,omitempty"`
	TarballDigest     string `json:"tarball_sha256,omitempty"`
	WheelDigest       string `json:"wheel_sha256,omitempty"`
	NPMIntegrity      string `json:"npm_integrity,omitempty"`
	Package           string `json:"package"`
	Version           string `json:"version"`
	SourceRepo        string `json:"source_repo"`
	SourceCommit      string `json:"source_commit"`
	LayerTitle        string `json:"layer_title"`
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
	flag.StringVar(&cfg.tarballPath, "tarball", "", "package payload produced by Bazel")
	flag.StringVar(&cfg.packJSON, "pack-json", "", "package metadata JSON produced beside the payload")
	flag.StringVar(&cfg.artifactType, "artifact-type", defaultArtifactType, "OCI artifactType for the SDK payload")
	flag.StringVar(&cfg.payloadMediaType, "payload-media-type", defaultPayloadMediaType, "OCI layer media type for the SDK payload")
	flag.StringVar(&cfg.distributable, "distributable", defaultDistributable, "release distributable name recorded in OCI annotations")
	flag.StringVar(&cfg.payloadForm, "payload-form", defaultPayloadForm, "release payload form recorded in OCI annotations")
	flag.StringVar(&cfg.description, "description", defaultDescription, "OCI manifest description")
	flag.StringVar(&cfg.expectedPackage, "expected-package", defaultExpectedPackageName, "expected ecosystem package name; empty disables package-name validation")
	flag.StringVar(&cfg.filenameSuffix, "filename-suffix", defaultFilenameSuffix, "required payload filename suffix; empty disables suffix validation")
	flag.StringVar(&cfg.attestationPath, "attestation-bundle", "", "JSONL in-toto bundle to attach as an OCI referrer")
	flag.StringVar(&cfg.attestationTitle, "attestation-title", defaultAttestationLayerTitle, "OCI layer title for --attestation-bundle")
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
	cfg = cfg.withDefaults()
	if cfg.tarballPath == "" {
		return errors.New("--tarball is required")
	}
	if cfg.packJSON == "" {
		return errors.New("--pack-json is required")
	}
	if cfg.artifactType == "" {
		return errors.New("--artifact-type is required")
	}
	if cfg.payloadMediaType == "" {
		return errors.New("--payload-media-type is required")
	}
	if cfg.distributable == "" {
		return errors.New("--distributable is required")
	}
	if cfg.payloadForm == "" {
		return errors.New("--payload-form is required")
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
	if err := validatePackEntry(entry, cfg); err != nil {
		return err
	}

	payload, err := os.ReadFile(cfg.tarballPath)
	if err != nil {
		return fmt.Errorf("read payload: %w", err)
	}
	if int64(len(payload)) != entry.Size {
		return fmt.Errorf("payload size mismatch: metadata=%d actual=%d", entry.Size, len(payload))
	}
	payloadSHA256 := sha256Hex(payload)
	if entry.SHA256 != "" && normalizeSHA256(entry.SHA256) != payloadSHA256 {
		return fmt.Errorf("payload sha256 mismatch: metadata=%s actual=sha256:%s", entry.SHA256, payloadSHA256)
	}
	if entry.Integrity != "" && entry.Integrity != npmIntegrity(payload) {
		return fmt.Errorf("payload integrity mismatch: metadata=%s actual=%s", entry.Integrity, npmIntegrity(payload))
	}
	attestation, err := readAttestationBundle(cfg.attestationPath)
	if err != nil {
		return err
	}

	annotations := sdkAnnotations(entry, payloadSHA256, cfg)
	channel := ""
	var manifest ocispec.Descriptor
	var attestationManifest *ocispec.Descriptor
	var ref string
	var attestationRef string
	if cfg.ociLayout != "" {
		if cfg.tag == "" {
			return errors.New("--tag is required with --oci-layout")
		}
		manifest, attestationManifest, err = pushLayout(ctx, cfg.ociLayout, cfg.tag, entry, payload, annotations, cfg.payloadMediaType, cfg.artifactType, attestation, cfg.attestationTitle)
		ref = layoutRef(cfg.ociLayout, cfg.tag, manifest.Digest.String())
		if attestationManifest != nil {
			attestationRef = layoutRef(cfg.ociLayout, attestationTag(cfg.tag), attestationManifest.Digest.String())
		}
		channel = cfg.tag
	} else {
		target, err := parseTaggedRef(cfg.ref)
		if err != nil {
			return err
		}
		manifest, attestationManifest, err = pushRemote(ctx, target, cfg.plainHTTP, cfg.credentials, entry, payload, annotations, cfg.payloadMediaType, cfg.artifactType, attestation, cfg.attestationTitle)
		ref = target.repository + "@" + manifest.Digest.String()
		if attestationManifest != nil {
			attestationRef = target.repository + "@" + attestationManifest.Digest.String()
		}
		channel = target.tag
	}
	if err != nil {
		return err
	}

	result := artifactResult{
		Distributable: cfg.distributable,
		PayloadForm:   cfg.payloadForm,
		Channel:       channel,
		OCIDigest:     manifest.Digest.String(),
		OCIRef:        ref,
		PayloadDigest: "sha256:" + payloadSHA256,
		Package:       entry.Name,
		Version:       entry.Version,
		SourceRepo:    cfg.sourceRepo,
		SourceCommit:  cfg.sourceCommit,
		LayerTitle:    entry.Filename,
	}
	switch cfg.payloadForm {
	case "npm":
		result.TarballDigest = "sha256:" + payloadSHA256
		result.NPMIntegrity = entry.Integrity
	case "python-wheel":
		result.WheelDigest = "sha256:" + payloadSHA256
	}
	if attestationManifest != nil {
		result.AttestationDigest = attestationManifest.Digest.String()
		result.AttestationRef = attestationRef
	}
	return writeResult(result, cfg.outputPath, stdout)
}

func (cfg cliConfig) withDefaults() cliConfig {
	if cfg.artifactType == "" {
		cfg.artifactType = defaultArtifactType
	}
	if cfg.payloadMediaType == "" {
		cfg.payloadMediaType = defaultPayloadMediaType
	}
	if cfg.distributable == "" {
		cfg.distributable = defaultDistributable
	}
	if cfg.payloadForm == "" {
		cfg.payloadForm = defaultPayloadForm
	}
	if cfg.description == "" {
		cfg.description = defaultDescription
	}
	if cfg.expectedPackage == "" {
		cfg.expectedPackage = defaultExpectedPackageName
	}
	if cfg.filenameSuffix == "" {
		cfg.filenameSuffix = defaultFilenameSuffix
	}
	return cfg
}

func readPackEntry(path string) (packEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return packEntry{}, fmt.Errorf("read pack json: %w", err)
	}
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		var entry packEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return packEntry{}, fmt.Errorf("parse pack json: %w", err)
		}
		return entry, nil
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

func validatePackEntry(entry packEntry, cfg cliConfig) error {
	if cfg.expectedPackage != "" && entry.Name != cfg.expectedPackage {
		return fmt.Errorf("unexpected package name %q", entry.Name)
	}
	if entry.Version == "" {
		return errors.New("package metadata has empty version")
	}
	if entry.Filename == "" {
		return errors.New("package metadata has empty filename")
	}
	if cfg.filenameSuffix != "" && !strings.HasSuffix(entry.Filename, cfg.filenameSuffix) {
		return fmt.Errorf("package filename %q does not end with %q", entry.Filename, cfg.filenameSuffix)
	}
	if cfg.payloadForm == "npm" && !strings.HasPrefix(entry.Integrity, "sha512-") {
		return fmt.Errorf("npm pack integrity %q is not sha512 SRI", entry.Integrity)
	}
	if entry.Size <= 0 {
		return fmt.Errorf("package size must be positive, got %d", entry.Size)
	}
	return nil
}

func readAttestationBundle(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read attestation bundle: %w", err)
	}
	if len(raw) == 0 {
		return nil, errors.New("attestation bundle is empty")
	}
	return raw, nil
}

func pushRemote(ctx context.Context, target targetRef, plainHTTP bool, creds credentialConfig, entry packEntry, payload []byte, annotations map[string]string, payloadMediaType, artifactType string, attestation []byte, attestationTitle string) (ocispec.Descriptor, *ocispec.Descriptor, error) {
	repo, err := remote.NewRepository(target.repository)
	if err != nil {
		return ocispec.Descriptor{}, nil, fmt.Errorf("remote repository %s: %w", target.repository, err)
	}
	repo.PlainHTTP = plainHTTP
	credFn, err := credentialFunc(target.registry, creds)
	if err != nil {
		return ocispec.Descriptor{}, nil, err
	}
	repo.Client = &auth.Client{
		Client:     retry.DefaultClient,
		Cache:      auth.NewCache(),
		Credential: credFn,
	}
	return pushToTarget(ctx, repo, target.tag, entry, payload, annotations, payloadMediaType, artifactType, attestation, attestationTitle)
}

func pushLayout(ctx context.Context, path, tag string, entry packEntry, payload []byte, annotations map[string]string, payloadMediaType, artifactType string, attestation []byte, attestationTitle string) (ocispec.Descriptor, *ocispec.Descriptor, error) {
	store, err := oci.New(path)
	if err != nil {
		return ocispec.Descriptor{}, nil, fmt.Errorf("oci layout %s: %w", path, err)
	}
	return pushToTarget(ctx, store, tag, entry, payload, annotations, payloadMediaType, artifactType, attestation, attestationTitle)
}

type pusher interface {
	oras.Target
}

func pushToTarget(ctx context.Context, target pusher, tag string, entry packEntry, payload []byte, annotations map[string]string, payloadMediaType, artifactType string, attestation []byte, attestationTitle string) (ocispec.Descriptor, *ocispec.Descriptor, error) {
	layer, err := pushBytes(ctx, target, payloadMediaType, payload)
	if err != nil {
		return ocispec.Descriptor{}, nil, fmt.Errorf("push payload layer: %w", err)
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
		return ocispec.Descriptor{}, nil, fmt.Errorf("pack OCI artifact manifest: %w", err)
	}
	if err := target.Tag(ctx, manifest, tag); err != nil {
		return ocispec.Descriptor{}, nil, fmt.Errorf("tag OCI artifact %s: %w", tag, err)
	}
	manifest, err = resolveTaggedDescriptor(ctx, target, tag, manifest)
	if err != nil {
		return ocispec.Descriptor{}, nil, err
	}
	attestationManifest, err := attachAttestation(ctx, target, tag, manifest, attestation, attestationTitle)
	if err != nil {
		return ocispec.Descriptor{}, nil, err
	}
	return manifest, attestationManifest, nil
}

func attachAttestation(ctx context.Context, target pusher, tag string, subject ocispec.Descriptor, attestation []byte, title string) (*ocispec.Descriptor, error) {
	if len(attestation) == 0 {
		return nil, nil
	}
	if title == "" {
		title = defaultAttestationLayerTitle
	}
	layer, err := pushBytes(ctx, target, attestationBundleMediaType, attestation)
	if err != nil {
		return nil, fmt.Errorf("push attestation bundle layer: %w", err)
	}
	layer.Annotations = map[string]string{
		ocispec.AnnotationTitle: title,
	}
	opts := oras.PackManifestOptions{
		Subject: &subject,
		Layers:  []ocispec.Descriptor{layer},
		ManifestAnnotations: map[string]string{
			ocispec.AnnotationCreated:     deterministicCreated,
			ocispec.AnnotationDescription: "Guardian release in-toto JSONL bundle",
		},
	}
	manifest, err := oras.PackManifest(ctx, target, oras.PackManifestVersion1_1, attestationArtifactType, opts)
	if err != nil {
		return nil, fmt.Errorf("pack attestation OCI artifact manifest: %w", err)
	}
	if err := target.Tag(ctx, manifest, attestationTag(tag)); err != nil {
		return nil, fmt.Errorf("tag attestation OCI artifact %s: %w", attestationTag(tag), err)
	}
	manifest, err = resolveTaggedDescriptor(ctx, target, attestationTag(tag), manifest)
	if err != nil {
		return nil, err
	}
	return &manifest, nil
}

func attestationTag(tag string) string { return tag + ".attestation" }

type resolver interface {
	Resolve(context.Context, string) (ocispec.Descriptor, error)
}

func resolveTaggedDescriptor(ctx context.Context, target resolver, tag string, pushed ocispec.Descriptor) (ocispec.Descriptor, error) {
	resolved, err := target.Resolve(ctx, tag)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("resolve tagged OCI artifact %s: %w", tag, err)
	}
	if resolved.Digest.String() == "" {
		return ocispec.Descriptor{}, fmt.Errorf("resolved tagged OCI artifact %s has empty digest", tag)
	}
	if pushed.Digest.String() != "" && resolved.Digest != pushed.Digest {
		return ocispec.Descriptor{}, fmt.Errorf("resolved tagged OCI artifact %s digest %s does not match pushed digest %s", tag, resolved.Digest, pushed.Digest)
	}
	if resolved.MediaType == "" {
		resolved.MediaType = pushed.MediaType
	}
	if resolved.ArtifactType == "" {
		resolved.ArtifactType = pushed.ArtifactType
	}
	if resolved.Annotations == nil {
		resolved.Annotations = pushed.Annotations
	}
	return resolved, nil
}

func pushBytes(ctx context.Context, target pusher, mediaType string, data []byte) (ocispec.Descriptor, error) {
	desc := content.NewDescriptorFromBytes(mediaType, data)
	if err := target.Push(ctx, desc, bytes.NewReader(data)); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
		return ocispec.Descriptor{}, err
	}
	return desc, nil
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

func sdkAnnotations(entry packEntry, payloadSHA256 string, cfg cliConfig) map[string]string {
	annotations := map[string]string{
		ocispec.AnnotationCreated:     deterministicCreated,
		ocispec.AnnotationDescription: cfg.description,
		ocispec.AnnotationRevision:    cfg.sourceCommit,
		ocispec.AnnotationSource:      cfg.sourceRepo,
		"dev.guardian.distributable":  cfg.distributable,
		"dev.guardian.payload-form":   cfg.payloadForm,
		"dev.guardian.package":        entry.Name,
		"dev.guardian.version":        entry.Version,
		"dev.guardian.payload.sha256": "sha256:" + payloadSHA256,
	}
	switch cfg.payloadForm {
	case "npm":
		annotations["dev.guardian.npm.integrity"] = entry.Integrity
		annotations["dev.guardian.tarball.sha256"] = "sha256:" + payloadSHA256
	case "python-wheel":
		annotations["dev.guardian.python.wheel.sha256"] = "sha256:" + payloadSHA256
	}
	return annotations
}

func npmIntegrity(data []byte) string {
	sum := sha512.Sum512(data)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func normalizeSHA256(value string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "sha256:")
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
