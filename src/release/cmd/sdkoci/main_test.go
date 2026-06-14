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
