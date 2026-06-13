package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	_, err := parseTaggedRef("oci.gi.org/guardian/aisucks/sdk/npm@sha256:abc")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCredentialFuncRequiresExplicitPair(t *testing.T) {
	_, err := credentialFunc("oci.gi.org", credentialConfig{username: "guardian"})
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
	if result.Package != expectedPackageName {
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
		Name:      expectedPackageName,
		Version:   "0.2.0",
		Filename:  "guardian-intelligence-aisucks-0.2.0.tgz",
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
