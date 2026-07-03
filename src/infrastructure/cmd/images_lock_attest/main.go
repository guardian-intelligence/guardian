// images_lock_attest builds the predicate JSON for the in-toto/cosign
// attestation over src/infrastructure/bootstrap/bundle/images.lock. It is a
// pure projection: given the lock's bytes plus two CI-supplied facts (the
// triggering commit and the lock's own sha256, already computed once by
// images-lock-sign.yml as $lockhash), it emits a small flat JSON object and
// nothing else. It never signs anything — cosign attest-blob is the only
// thing that ever produces a signature; this tool only produces the
// predicate cosign attest-blob is handed on the command line.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var sectionHeaderPattern = regexp.MustCompile(`^# --- (.+) ---$`)

// predicate is the flat JSON object recorded by the images.lock closure
// attestation (predicateType
// https://guardianintelligence.org/attestations/images-lock/v1). Field
// names and the set of fields are load-bearing: cosign attest-blob embeds
// this verbatim, and offline verifiers key off exactly these names. Do not
// rename or add fields without updating docs/images-lock-spec.md.
type predicate struct {
	GitCommit     string         `json:"gitCommit"`
	LockSHA256    string         `json:"lockSha256"`
	SectionCounts map[string]int `json:"sectionCounts"`
	TotalEntries  int            `json:"totalEntries"`
}

func main() {
	var lockPath, gitCommit, lockSHA256 string
	flag.StringVar(&lockPath, "lock", "src/infrastructure/bootstrap/bundle/images.lock", "path to images.lock")
	flag.StringVar(&gitCommit, "git-commit", "", "commit sha of the push that triggered signing (required)")
	flag.StringVar(&lockSHA256, "lock-sha256", "", "sha256 of images.lock, already computed by the caller (required)")
	flag.Parse()

	if gitCommit == "" {
		exitErr(errors.New("--git-commit is required"))
	}
	if lockSHA256 == "" {
		exitErr(errors.New("--lock-sha256 is required"))
	}

	raw, err := os.ReadFile(lockPath)
	if err != nil {
		exitErr(err)
	}

	sectionCounts, total, err := countSectionsAndEntries(string(raw))
	if err != nil {
		exitErr(err)
	}

	payload, err := json.MarshalIndent(predicate{
		GitCommit:     gitCommit,
		LockSHA256:    lockSHA256,
		SectionCounts: sectionCounts,
		TotalEntries:  total,
	}, "", "  ")
	if err != nil {
		exitErr(err)
	}
	fmt.Println(string(payload))
}

// countSectionsAndEntries walks images.lock line by line and returns, per
// "# --- <title> ---" section header, the count of digest-pinned entry
// lines that follow it (before EOF or the next header), plus the grand
// total. Comment-only and blank lines are never counted. An entry line
// found before any section header, or one that is not digest-pinned, is a
// hard error: the attestation must never silently misreport the lock's
// shape.
func countSectionsAndEntries(raw string) (map[string]int, int, error) {
	sectionCounts := map[string]int{}
	var currentSection string
	total := 0

	for i, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if m := sectionHeaderPattern.FindStringSubmatch(trimmed); m != nil {
			currentSection = m[1]
			if _, ok := sectionCounts[currentSection]; !ok {
				sectionCounts[currentSection] = 0
			}
			continue
		}

		entry := trimmed
		if idx := strings.Index(entry, "#"); idx >= 0 {
			entry = entry[:idx]
		}
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		if err := validateEntry(entry); err != nil {
			return nil, 0, fmt.Errorf("images.lock:%d: %w", i+1, err)
		}
		if currentSection == "" {
			return nil, 0, fmt.Errorf("images.lock:%d: entry %q appears before any section header", i+1, entry)
		}
		sectionCounts[currentSection]++
		total++
	}

	return sectionCounts, total, nil
}

var sha256DigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// validateEntry checks the same shape TestImagesLockWellFormed enforces
// (src/infrastructure/tests/images_lock_test.go's splitImageRef): a
// digest-pinned OCI reference with no embedded whitespace or quotes. It
// intentionally does not track (repo, digest) de-duplication — that
// invariant belongs to the conformance test, not the attestation predicate.
func validateEntry(ref string) error {
	if strings.ContainsAny(ref, " \t\"'") {
		return fmt.Errorf("%q is not a valid image reference", ref)
	}
	name, digest, pinned := strings.Cut(ref, "@")
	if !pinned {
		return fmt.Errorf("%q is not digest-pinned (missing @sha256:<digest>)", ref)
	}
	if !sha256DigestPattern.MatchString(digest) {
		return fmt.Errorf("%q has malformed digest %q", ref, digest)
	}
	if name == "" {
		return fmt.Errorf("%q has an empty repository", ref)
	}
	return nil
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}
