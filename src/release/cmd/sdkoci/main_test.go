package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"
)

func TestParseTaggedRefAllowsRegistryPort(t *testing.T) {
	got, err := parseTaggedRef("127.0.0.1:5000/guardian/aisucks/sdk/npm:edge")
	if err != nil {
		t.Fatal(err)
	}
	if got.repository != "127.0.0.1:5000/guardian/aisucks/sdk/npm" {
		t.Fatalf("repository = %q", got.repository)
	}
	if got.registry != "127.0.0.1:5000" {
		t.Fatalf("registry = %q", got.registry)
	}
	if got.tag != "edge" {
		t.Fatalf("tag = %q", got.tag)
	}
}

func TestParseTaggedRefRejectsDigestPush(t *testing.T) {
	_, err := parseTaggedRef("oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:abc")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCredentialFuncRequiresExplicitPair(t *testing.T) {
	_, err := credentialFunc("oci.guardianintelligence.org", credentialConfig{username: "guardian"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCredentialFuncRejectsEmptyPasswordEnv(t *testing.T) {
	env := "GUARDIAN_SDKOCI_TEST_EMPTY_PASSWORD"
	t.Setenv(env, "")

	_, err := credentialFunc("oci.guardianintelligence.org", credentialConfig{
		username:    "guardian-release",
		passwordEnv: env,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), env+" is empty") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRemoteCredentialConfigRejectsMixedAuthModes(t *testing.T) {
	env := "GUARDIAN_SDKOCI_TEST_ACCESS_TOKEN"
	t.Setenv(env, "token")

	err := validateRemoteCredentialConfig(credentialConfig{
		username:       "guardian-release",
		passwordEnv:    "GUARDIAN_OCI_PASSWORD",
		accessTokenEnv: env,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRemoteCredentialConfigRejectsEmptyAccessTokenEnv(t *testing.T) {
	env := "GUARDIAN_SDKOCI_TEST_EMPTY_ACCESS_TOKEN"
	t.Setenv(env, "")

	err := validateRemoteCredentialConfig(credentialConfig{accessTokenEnv: env})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), env+" is empty") {
		t.Fatalf("error = %v", err)
	}
}

func TestPushRemoteRequiresExplicitCredentials(t *testing.T) {
	_, _, err := pushRemote(
		context.Background(),
		targetRef{
			registry:   "oci.guardianintelligence.org",
			repository: "oci.guardianintelligence.org/guardian/aisucks/sdk/npm",
			tag:        "edge",
		},
		false,
		credentialConfig{},
		packEntry{},
		[]byte("payload"),
		nil,
		defaultPayloadMediaType,
		defaultArtifactType,
		nil,
		defaultAttestationLayerTitle,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "remote OCI push requires") {
		t.Fatalf("error = %v", err)
	}
}

func TestPushRemoteRejectsEmptyPasswordEnv(t *testing.T) {
	env := "GUARDIAN_SDKOCI_TEST_REMOTE_EMPTY_PASSWORD"
	t.Setenv(env, "")

	_, _, err := pushRemote(
		context.Background(),
		targetRef{
			registry:   "oci.guardianintelligence.org",
			repository: "oci.guardianintelligence.org/guardian/aisucks/sdk/npm",
			tag:        "edge",
		},
		false,
		credentialConfig{
			username:    "guardian-release",
			passwordEnv: env,
		},
		packEntry{},
		[]byte("payload"),
		nil,
		defaultPayloadMediaType,
		defaultArtifactType,
		nil,
		defaultAttestationLayerTitle,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), env+" is empty") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunRemoteRequiresExplicitCredentials(t *testing.T) {
	dir := t.TempDir()
	tarballPath, packPath, _ := writePackFixture(t, dir)

	err := run(context.Background(), cliConfig{
		tarballPath:  tarballPath,
		packJSON:     packPath,
		ref:          "oci.guardianintelligence.org/guardian/aisucks/sdk/npm:edge",
		sourceCommit: strings.Repeat("a", 40),
		sourceRepo:   "https://github.com/guardian-intelligence/guardian",
	}, io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "remote OCI push requires") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunRejectsUnsupportedPayloadFormBeforeWritingLayout(t *testing.T) {
	dir := t.TempDir()
	tarballPath, packPath, _ := writePackFixture(t, dir)
	layoutPath := filepath.Join(dir, "layout")

	err := run(context.Background(), cliConfig{
		tarballPath:  tarballPath,
		packJSON:     packPath,
		payloadForm:  "ruby-gem",
		ociLayout:    layoutPath,
		tag:          "edge",
		sourceCommit: strings.Repeat("a", 40),
		sourceRepo:   "https://github.com/guardian-intelligence/guardian",
	}, io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `unsupported payload form "ruby-gem"`) {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(layoutPath); !os.IsNotExist(statErr) {
		t.Fatalf("layout was written or unexpected stat error: %v", statErr)
	}
}

func TestRunRejectsInvalidNPMIntegrityBeforeWritingLayout(t *testing.T) {
	dir := t.TempDir()
	tarballPath, packPath, _ := writePackFixture(t, dir)
	raw, err := os.ReadFile(packPath)
	if err != nil {
		t.Fatal(err)
	}
	var pack []packEntry
	if err := json.Unmarshal(raw, &pack); err != nil {
		t.Fatal(err)
	}
	pack[0].Integrity = "sha512-not-base64"
	raw, err = json.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	layoutPath := filepath.Join(dir, "layout")

	err = run(context.Background(), cliConfig{
		tarballPath:  tarballPath,
		packJSON:     packPath,
		ociLayout:    layoutPath,
		tag:          "edge",
		sourceCommit: strings.Repeat("a", 40),
		sourceRepo:   "https://github.com/guardian-intelligence/guardian",
	}, io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not valid sha512 SRI") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(layoutPath); !os.IsNotExist(statErr) {
		t.Fatalf("layout was written or unexpected stat error: %v", statErr)
	}
}

func TestRunWritesOCILayout(t *testing.T) {
	dir := t.TempDir()
	tarballPath, packPath, tarball := writePackFixture(t, dir)

	layoutPath := filepath.Join(dir, "layout")
	var out strings.Builder
	err := run(context.Background(), cliConfig{
		tarballPath:  tarballPath,
		packJSON:     packPath,
		ociLayout:    layoutPath,
		tag:          "edge",
		sourceCommit: strings.Repeat("a", 40),
		sourceRepo:   "https://github.com/guardian-intelligence/guardian",
	}, &out)
	if err != nil {
		t.Fatal(err)
	}

	var result artifactResult
	if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
		t.Fatal(err)
	}
	if result.Package != defaultExpectedPackageName {
		t.Fatalf("package = %q", result.Package)
	}
	if result.Channel != "edge" {
		t.Fatalf("channel = %q", result.Channel)
	}
	if result.NPMIntegrity != npmIntegrity(tarball) {
		t.Fatalf("integrity = %q", result.NPMIntegrity)
	}

	store, err := oci.New(layoutPath)
	if err != nil {
		t.Fatal(err)
	}
	desc, err := store.Resolve(context.Background(), "edge")
	if err != nil {
		t.Fatal(err)
	}
	if desc.Digest.String() != result.OCIDigest {
		t.Fatalf("digest = %q result = %q", desc.Digest, result.OCIDigest)
	}
}

func TestRunReusesExistingOCILayoutContent(t *testing.T) {
	dir := t.TempDir()
	tarballPath, packPath, _ := writePackFixture(t, dir)
	layoutPath := filepath.Join(dir, "layout")
	cfg := cliConfig{
		tarballPath:  tarballPath,
		packJSON:     packPath,
		ociLayout:    layoutPath,
		tag:          "edge",
		sourceCommit: strings.Repeat("a", 40),
		sourceRepo:   "https://github.com/guardian-intelligence/guardian",
	}

	var first strings.Builder
	if err := run(context.Background(), cfg, &first); err != nil {
		t.Fatal(err)
	}
	var second strings.Builder
	if err := run(context.Background(), cfg, &second); err != nil {
		t.Fatal(err)
	}

	var firstResult artifactResult
	if err := json.Unmarshal([]byte(first.String()), &firstResult); err != nil {
		t.Fatal(err)
	}
	var secondResult artifactResult
	if err := json.Unmarshal([]byte(second.String()), &secondResult); err != nil {
		t.Fatal(err)
	}
	if firstResult.OCIDigest != secondResult.OCIDigest {
		t.Fatalf("digest changed across idempotent rerun: first=%q second=%q", firstResult.OCIDigest, secondResult.OCIDigest)
	}
}

func TestRunWritesPythonWheelOCILayout(t *testing.T) {
	dir := t.TempDir()
	wheel := []byte("example python wheel")
	wheelPath := filepath.Join(dir, "guardian_intelligence_aisucks-0.3.0-py3-none-any.whl")
	if err := os.WriteFile(wheelPath, wheel, 0o644); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(dir, "wheel.json")
	meta := packEntry{
		Name:     "guardian-intelligence-aisucks",
		Version:  "0.3.0",
		Filename: filepath.Base(wheelPath),
		Size:     int64(len(wheel)),
		SHA256:   "sha256:" + sha256Hex(wheel),
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	layoutPath := filepath.Join(dir, "layout")
	var out strings.Builder
	err = run(context.Background(), cliConfig{
		tarballPath:      wheelPath,
		packJSON:         metaPath,
		artifactType:     "application/vnd.guardian.sdk.python.wheel.v1",
		payloadMediaType: "application/vnd.python.wheel",
		distributable:    "aisucks-python-sdk",
		payloadForm:      "python-wheel",
		description:      "Guardian aisucks Python SDK wheel",
		expectedPackage:  "guardian-intelligence-aisucks",
		filenameSuffix:   ".whl",
		ociLayout:        layoutPath,
		tag:              "edge",
		sourceCommit:     strings.Repeat("a", 40),
		sourceRepo:       "https://github.com/guardian-intelligence/guardian",
	}, &out)
	if err != nil {
		t.Fatal(err)
	}

	var result artifactResult
	if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
		t.Fatal(err)
	}
	if result.PayloadForm != "python-wheel" {
		t.Fatalf("payload_form = %q", result.PayloadForm)
	}
	if result.WheelDigest != "sha256:"+sha256Hex(wheel) {
		t.Fatalf("wheel digest = %q", result.WheelDigest)
	}
	if result.NPMIntegrity != "" || result.TarballDigest != "" {
		t.Fatalf("unexpected npm fields: integrity=%q tarball=%q", result.NPMIntegrity, result.TarballDigest)
	}
}

func TestRunWritesAttestationReferrer(t *testing.T) {
	dir := t.TempDir()
	tarballPath, packPath, _ := writePackFixture(t, dir)
	attestationPath := filepath.Join(dir, "guardian-release.intoto.jsonl")
	if err := os.WriteFile(attestationPath, []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	layoutPath := filepath.Join(dir, "layout")
	var out strings.Builder
	err := run(context.Background(), cliConfig{
		tarballPath:     tarballPath,
		packJSON:        packPath,
		attestationPath: attestationPath,
		ociLayout:       layoutPath,
		tag:             "edge",
		sourceCommit:    strings.Repeat("a", 40),
		sourceRepo:      "https://github.com/guardian-intelligence/guardian",
	}, &out)
	if err != nil {
		t.Fatal(err)
	}

	var result artifactResult
	if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
		t.Fatal(err)
	}
	if result.AttestationDigest == "" {
		t.Fatal("expected attestation digest")
	}

	store, err := oci.New(layoutPath)
	if err != nil {
		t.Fatal(err)
	}
	desc, err := store.Resolve(context.Background(), "edge.attestation")
	if err != nil {
		t.Fatal(err)
	}
	if desc.Digest.String() != result.AttestationDigest {
		t.Fatalf("attestation digest = %q result = %q", desc.Digest, result.AttestationDigest)
	}
	rc, err := store.Fetch(context.Background(), desc)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(rc)
	if closeErr := rc.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Subject == nil {
		t.Fatal("attestation manifest has no subject")
	}
	if manifest.Subject.Digest.String() != result.OCIDigest {
		t.Fatalf("attestation subject = %q result = %q", manifest.Subject.Digest, result.OCIDigest)
	}
}

func TestRunWritesDeterministicManifestDigest(t *testing.T) {
	dir := t.TempDir()
	tarballPath, packPath, _ := writePackFixture(t, dir)

	digest := func(layout string) string {
		var out strings.Builder
		err := run(context.Background(), cliConfig{
			tarballPath:  tarballPath,
			packJSON:     packPath,
			ociLayout:    layout,
			tag:          "edge",
			sourceCommit: strings.Repeat("a", 40),
			sourceRepo:   "https://github.com/guardian-intelligence/guardian",
		}, &out)
		if err != nil {
			t.Fatal(err)
		}
		var result artifactResult
		if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
			t.Fatal(err)
		}
		return result.OCIDigest
	}

	first := digest(filepath.Join(dir, "layout-a"))
	second := digest(filepath.Join(dir, "layout-b"))
	if first != second {
		t.Fatalf("manifest digest changed across identical inputs: %s != %s", first, second)
	}
}

func TestResolveTaggedDescriptorUsesResolvedDigest(t *testing.T) {
	resolved := validManifestDescriptor(`{"schemaVersion":2}`)
	got, err := resolveTaggedDescriptor(context.Background(), staticResolver{desc: resolved}, "edge", ocispec.Descriptor{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != resolved.Digest {
		t.Fatalf("digest = %q; want %q", got.Digest, resolved.Digest)
	}
}

func TestResolveTaggedDescriptorRejectsEmptyDigest(t *testing.T) {
	_, err := resolveTaggedDescriptor(context.Background(), staticResolver{desc: ocispec.Descriptor{}}, "edge", ocispec.Descriptor{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "empty digest") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveTaggedDescriptorRejectsDigestMismatch(t *testing.T) {
	pushed := validManifestDescriptor(`{"schemaVersion":2}`)
	resolved := validManifestDescriptor(`{"schemaVersion":2,"different":true}`)
	_, err := resolveTaggedDescriptor(context.Background(), staticResolver{desc: resolved}, "edge", pushed)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "does not match pushed digest") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveTaggedDescriptorRejectsMediaTypeMismatch(t *testing.T) {
	pushed := validManifestDescriptor(`{"schemaVersion":2}`)
	resolved := pushed
	resolved.MediaType = "application/vnd.docker.distribution.manifest.v2+json"

	_, err := resolveTaggedDescriptor(context.Background(), staticResolver{desc: resolved}, "edge", pushed)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "media type") || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveTaggedDescriptorRejectsArtifactTypeMismatch(t *testing.T) {
	pushed := validManifestDescriptor(`{"schemaVersion":2}`)
	resolved := pushed
	resolved.ArtifactType = "application/vnd.guardian.sdk.python.wheel.v1"

	_, err := resolveTaggedDescriptor(context.Background(), staticResolver{desc: resolved}, "edge", pushed)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "artifact type") || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveTaggedDescriptorRejectsSizeMismatch(t *testing.T) {
	pushed := validManifestDescriptor(`{"schemaVersion":2}`)
	resolved := pushed
	resolved.Size++

	_, err := resolveTaggedDescriptor(context.Background(), staticResolver{desc: resolved}, "edge", pushed)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "size") || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateDescriptorRejectsEmptyDescriptor(t *testing.T) {
	err := validateDescriptor("test OCI descriptor", ocispec.Descriptor{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "empty digest") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateDescriptorRejectsMissingMediaType(t *testing.T) {
	desc := validManifestDescriptor(`{"schemaVersion":2}`)
	desc.MediaType = ""
	err := validateDescriptor("test OCI descriptor", desc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "empty media type") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateDescriptorRejectsUnsupportedMediaType(t *testing.T) {
	desc := validManifestDescriptor(`{"schemaVersion":2}`)
	desc.MediaType = "application/vnd.docker.distribution.manifest.v2+json"
	err := validateDescriptor("test OCI descriptor", desc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unsupported media type") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateDescriptorRejectsMissingArtifactType(t *testing.T) {
	desc := validManifestDescriptor(`{"schemaVersion":2}`)
	desc.ArtifactType = ""
	err := validateDescriptor("test OCI descriptor", desc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "empty artifact type") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateDescriptorRejectsInvalidDigestSyntax(t *testing.T) {
	desc := validManifestDescriptor(`{"schemaVersion":2}`)
	desc.Digest = "sha256:not-hex"
	err := validateDescriptor("test OCI descriptor", desc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid digest") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateDescriptorRejectsUnsupportedDigestAlgorithm(t *testing.T) {
	desc := validManifestDescriptor(`{"schemaVersion":2}`)
	desc.Digest = "sha512:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	err := validateDescriptor("test OCI descriptor", desc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unsupported digest algorithm") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateDescriptorRejectsZeroSize(t *testing.T) {
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, nil)
	desc.ArtifactType = defaultArtifactType
	err := validateDescriptor("test OCI descriptor", desc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "non-positive size") {
		t.Fatalf("error = %v", err)
	}
}

func TestPushToTargetRejectsInvalidResolvedManifestDescriptor(t *testing.T) {
	target := &recordingTarget{
		resolve: func(reference string, desc ocispec.Descriptor) ocispec.Descriptor {
			if reference == "edge" {
				desc.Digest = ""
			}
			return desc
		},
	}

	_, _, err := pushToTarget(
		context.Background(),
		target,
		"edge",
		packEntry{Filename: "guardian-intelligence-aisucks-0.3.0.tgz"},
		[]byte("payload"),
		nil,
		defaultPayloadMediaType,
		defaultArtifactType,
		nil,
		defaultAttestationLayerTitle,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "resolved tagged OCI artifact edge has empty digest") {
		t.Fatalf("error = %v", err)
	}
}

func TestPushToTargetRejectsInvalidResolvedAttestationDescriptor(t *testing.T) {
	target := &recordingTarget{
		resolve: func(reference string, desc ocispec.Descriptor) ocispec.Descriptor {
			if reference == "edge.attestation" {
				desc.Digest = ""
			}
			return desc
		},
	}

	_, _, err := pushToTarget(
		context.Background(),
		target,
		"edge",
		packEntry{Filename: "guardian-intelligence-aisucks-0.3.0.tgz"},
		[]byte("payload"),
		nil,
		defaultPayloadMediaType,
		defaultArtifactType,
		[]byte(`{"bundle":true}`),
		defaultAttestationLayerTitle,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "resolved tagged OCI artifact edge.attestation has empty digest") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatePushedDescriptorsRejectsEmptyManifest(t *testing.T) {
	err := validatePushedDescriptors("remote", ocispec.Descriptor{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "remote OCI artifact manifest has empty digest") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatePushedDescriptorsRejectsInvalidManifest(t *testing.T) {
	manifest := validManifestDescriptor(`{"schemaVersion":2}`)
	manifest.MediaType = ""

	err := validatePushedDescriptors("remote", manifest, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "remote OCI artifact manifest") || !strings.Contains(err.Error(), "empty media type") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatePushedDescriptorsRejectsEmptyAttestationManifest(t *testing.T) {
	manifest := validManifestDescriptor(`{"schemaVersion":2}`)
	attestation := ocispec.Descriptor{}

	err := validatePushedDescriptors("remote", manifest, &attestation)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "remote attestation OCI artifact manifest has empty digest") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatePushedDescriptorsRejectsInvalidAttestationManifest(t *testing.T) {
	manifest := validManifestDescriptor(`{"schemaVersion":2}`)
	attestation := validAttestationDescriptor(`{"schemaVersion":2}`)
	attestation.Size = 0

	err := validatePushedDescriptors("remote", manifest, &attestation)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "remote attestation OCI artifact manifest") || !strings.Contains(err.Error(), "non-positive size") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatePushedDescriptorsRejectsWrongAttestationArtifactType(t *testing.T) {
	manifest := validManifestDescriptor(`{"schemaVersion":2}`)
	attestation := validManifestDescriptor(`{"schemaVersion":2,"attestation":true}`)

	err := validatePushedDescriptors("remote", manifest, &attestation)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "artifact type") || !strings.Contains(err.Error(), attestationArtifactType) {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatePushedDescriptorsAcceptsValidManifestAndAttestation(t *testing.T) {
	manifest := validManifestDescriptor(`{"schemaVersion":2}`)
	attestation := validAttestationDescriptor(`{"schemaVersion":2,"attestation":true}`)

	if err := validatePushedDescriptors("remote", manifest, &attestation); err != nil {
		t.Fatal(err)
	}
}

func TestWriteResultRejectsEmptyOCIDigest(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "sdk-oci.json")
	var out strings.Builder
	result := validNPMResult()
	result.OCIDigest = ""
	result.OCIRef = "oci.guardianintelligence.org/guardian/aisucks/sdk/npm@"

	err := writeResult(result, outputPath, &out)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "empty oci_digest") {
		t.Fatalf("error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q", out.String())
	}
	if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
		t.Fatalf("output file exists or unexpected stat error: %v", statErr)
	}
}

func TestWriteResultRejectsInvalidOCIDigest(t *testing.T) {
	result := validNPMResult()
	result.OCIDigest = "sha256:not-hex"
	result.OCIRef = "oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:not-hex"

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid digest") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsMissingPayloadDigest(t *testing.T) {
	result := validNPMResult()
	result.PayloadDigest = ""

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "empty payload_sha256") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsPayloadDigestMismatch(t *testing.T) {
	result := validNPMResult()
	result.TarballDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "does not match payload_sha256") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsUnsupportedPayloadForm(t *testing.T) {
	result := validNPMResult()
	result.PayloadForm = "ruby-gem"
	result.TarballDigest = ""
	result.NPMIntegrity = ""

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `unsupported payload_form "ruby-gem"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsInvalidSourceCommit(t *testing.T) {
	result := validNPMResult()
	result.SourceCommit = "not-a-full-sha"

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "source_commit is not a full 40-character hex SHA") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsRefDigestMismatch(t *testing.T) {
	result := validNPMResult()
	result.OCIRef = "oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "does not point at digest") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsDigestRefWithoutRepository(t *testing.T) {
	result := validNPMResult()
	result.OCIRef = "@" + result.OCIDigest

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "empty repository before digest") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsRefWithMultipleDigestSeparators(t *testing.T) {
	result := validNPMResult()
	result.OCIRef = "oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff@" + result.OCIDigest

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "multiple digest separators") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsInvalidNPMIntegrity(t *testing.T) {
	result := validNPMResult()
	result.NPMIntegrity = "sha512-not-base64"

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not valid sha512 SRI") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsNPMResultWithWheelDigest(t *testing.T) {
	result := validNPMResult()
	result.WheelDigest = result.PayloadDigest

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "wheel_sha256 for npm payload") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsPythonWheelMissingWheelDigest(t *testing.T) {
	result := validPythonWheelResult()
	result.WheelDigest = ""

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "empty wheel_sha256") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsPythonWheelDigestMismatch(t *testing.T) {
	result := validPythonWheelResult()
	result.WheelDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "does not match payload_sha256") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsPythonWheelWithNPMFields(t *testing.T) {
	result := validPythonWheelResult()
	result.TarballDigest = result.PayloadDigest
	result.NPMIntegrity = npmIntegrity([]byte("npm payload"))

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "tarball_sha256 for python-wheel payload") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsAttestationDigestWithoutRef(t *testing.T) {
	result := validNPMResult()
	result.AttestationDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "attestation digest without attestation ref") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsAttestationRefWithoutDigest(t *testing.T) {
	result := validNPMResult()
	result.AttestationRef = "oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "attestation ref without attestation digest") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsInvalidAttestationDigest(t *testing.T) {
	result := validNPMResult()
	result.AttestationDigest = "sha256:not-hex"
	result.AttestationRef = "oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:not-hex"

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid digest") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsAttestationRefDigestMismatch(t *testing.T) {
	result := validNPMResult()
	result.AttestationDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	result.AttestationRef = "oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "attestation ref") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteResultRejectsAttestationRefWithoutRepository(t *testing.T) {
	result := validNPMResult()
	result.AttestationDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	result.AttestationRef = "@" + result.AttestationDigest

	err := writeResult(result, "", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "empty repository before digest") {
		t.Fatalf("error = %v", err)
	}
}

type staticResolver struct {
	desc ocispec.Descriptor
}

func (r staticResolver) Resolve(context.Context, string) (ocispec.Descriptor, error) {
	return r.desc, nil
}

type recordingTarget struct {
	tags    map[string]ocispec.Descriptor
	resolve func(string, ocispec.Descriptor) ocispec.Descriptor
}

func (t *recordingTarget) Push(_ context.Context, _ ocispec.Descriptor, content io.Reader) error {
	_, err := io.Copy(io.Discard, content)
	return err
}

func (t *recordingTarget) Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (t *recordingTarget) Exists(context.Context, ocispec.Descriptor) (bool, error) {
	return false, nil
}

func (t *recordingTarget) Tag(_ context.Context, desc ocispec.Descriptor, reference string) error {
	if t.tags == nil {
		t.tags = make(map[string]ocispec.Descriptor)
	}
	t.tags[reference] = desc
	return nil
}

func (t *recordingTarget) Resolve(_ context.Context, reference string) (ocispec.Descriptor, error) {
	desc := t.tags[reference]
	if t.resolve != nil {
		return t.resolve(reference, desc), nil
	}
	return desc, nil
}

func validManifestDescriptor(data string) ocispec.Descriptor {
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, []byte(data))
	desc.ArtifactType = defaultArtifactType
	return desc
}

func validAttestationDescriptor(data string) ocispec.Descriptor {
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, []byte(data))
	desc.ArtifactType = attestationArtifactType
	return desc
}

func writePackFixture(t *testing.T, dir string) (string, string, []byte) {
	t.Helper()
	tarball := []byte("example npm tarball")
	tarballPath := filepath.Join(dir, "aisucks-sdk.tgz")
	if err := os.WriteFile(tarballPath, tarball, 0o644); err != nil {
		t.Fatal(err)
	}
	packPath := filepath.Join(dir, "pack.json")
	pack := []packEntry{{
		Name:      defaultExpectedPackageName,
		Version:   "0.3.0",
		Filename:  "guardian-intelligence-aisucks-0.3.0.tgz",
		Integrity: npmIntegrity(tarball),
		Size:      int64(len(tarball)),
	}}
	raw, err := json.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return tarballPath, packPath, tarball
}

func validNPMResult() artifactResult {
	return artifactResult{
		Distributable: "aisucks-ts-sdk",
		PayloadForm:   "npm",
		Channel:       "edge",
		OCIDigest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		OCIRef:        "oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PayloadDigest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		TarballDigest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		NPMIntegrity:  npmIntegrity([]byte("npm payload")),
		Package:       defaultExpectedPackageName,
		Version:       "0.3.0",
		SourceRepo:    "https://github.com/guardian-intelligence/guardian",
		SourceCommit:  strings.Repeat("a", 40),
		LayerTitle:    "guardian-intelligence-aisucks-0.3.0.tgz",
	}
}

func validPythonWheelResult() artifactResult {
	return artifactResult{
		Distributable: "aisucks-python-sdk",
		PayloadForm:   "python-wheel",
		Channel:       "edge",
		OCIDigest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		OCIRef:        "oci.guardianintelligence.org/guardian/aisucks/sdk/python@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PayloadDigest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		WheelDigest:   "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		Package:       "guardian-intelligence-aisucks",
		Version:       "0.3.0",
		SourceRepo:    "https://github.com/guardian-intelligence/guardian",
		SourceCommit:  strings.Repeat("a", 40),
		LayerTitle:    "guardian_intelligence_aisucks-0.3.0-py3-none-any.whl",
	}
}
