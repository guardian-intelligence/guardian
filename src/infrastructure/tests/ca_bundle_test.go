package tests

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

var sharedCABundle = flag.String("guardian-ca-bundle", "", "shared image trust bundle runfile")

func TestSharedCABundleExcludesCompromisedIssuerRoots(t *testing.T) {
	// rootpath renders external repositories relative to the main runfiles
	// directory; Rlocation expects the repository-qualified path instead.
	bundlePath, err := runfiles.Rlocation(strings.TrimPrefix(*sharedCABundle, "../"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	// Removed by Mozilla/curl's 2026-10-20 snapshot and Go's fallback bundle
	// 9f2b28d4331a: these roots issued the compromised eMudhra intermediates.
	blocked := map[string]bool{
		"bc4d809b15189d78db3e1d8cf4f9726a795da1643ca5f1358e1ddb0edc0d7eb3": true,
		"86a1ecba089c4a8d3bbe2734c612ba341d813e043cf9e8a862cd5c57a36bbe6b": true,
		"125609aa301da0a249b97a8239cb6a34216f44dcac9f3954b14292f2e8c8608f": true,
	}
	count := 0
	for {
		block, rest := pem.Decode(data)
		if block == nil {
			break
		}
		data = rest
		if block.Type != "CERTIFICATE" {
			t.Fatalf("unexpected PEM block %q", block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(cert.Raw)
		if blocked[hex.EncodeToString(digest[:])] {
			t.Errorf("shared image bundle still trusts compromised issuer root %s", cert.Subject)
		}
		count++
	}
	if count < 100 {
		t.Fatalf("trust bundle contains only %d certificates", count)
	}
}
