package tests

// Tier-1 release-manifest conformance: the release manifest
// (deployments/guardian/system/release-manifest.yaml) is the reviewable
// definition of what Guardian releases to users (docs/registry-design.md) —
// today, the postflight CLI's release channels. It must never drift from the
// channel pins: for every released repository, the manifest's digest set
// equals the digest set pinned in the CLI channels file. A channel bump that
// forgets the manifest — or a manifest lane naming a digest no channel pins —
// fails here. Equality is per-repo digest UNION, not per lane: mid-promotion
// a channel diverges from its siblings, and both digests are then
// legitimately released.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/guardian-intelligence/guardian/src/infrastructure/imageset"
)

const (
	releaseManifestRunfile = "src/infrastructure/deployments/guardian/system/release-manifest.yaml"
	releaseChannelsFile    = "src/products/postflight-cli/release/channels.yaml"
	firstPartyPrefix       = "ghcr.io/guardian-intelligence/"
)

// The projector and the countersigner both enumerate first-party refs with
// the shell grammar ghcr.io/guardian-intelligence/[a-z0-9-]+@sha256:... — a
// repo name outside that class would silently vanish from both loops'
// estates while every gauge reads healthy. This test is what makes that
// impossible: a nonconforming name fails CI at onboarding time instead.
var firstPartyRefGrammar = regexp.MustCompile(`^ghcr\.io/guardian-intelligence/[a-z0-9-]+@sha256:[a-f0-9]{64}$`)

func firstPartyRepoDigest(t *testing.T, file, ref string) (string, string) {
	t.Helper()
	name, digest, found := strings.Cut(ref, "@")
	if !found {
		t.Fatalf("%s: first-party ref %q is not digest-pinned", file, ref)
	}
	// tag+digest refs normalize to the bare repo.
	if idx := strings.LastIndex(name, ":"); idx > strings.LastIndex(name, "/") {
		name = name[:idx]
	}
	if !firstPartyRefGrammar.MatchString(name + "@" + digest) {
		t.Fatalf("%s: first-party ref %q does not match the grammar the countersigner and release projector enumerate with (%s) — the signing and projection loops would silently skip it; rename the repo or widen both scripts' greps and this pattern together",
			file, ref, firstPartyRefGrammar)
	}
	return name, digest
}

func collectFirstPartyRefs(t *testing.T, path string) map[string]map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	refRe := regexp.MustCompile(`ghcr\.io/guardian-intelligence/[a-z0-9-]+(?::[A-Za-z0-9._-]+)?@sha256:[a-f0-9]{64}`)
	out := map[string]map[string]bool{}
	for _, ref := range refRe.FindAllString(string(raw), -1) {
		repo, digest := firstPartyRepoDigest(t, path, ref)
		if out[repo] == nil {
			out[repo] = map[string]bool{}
		}
		out[repo][digest] = true
	}
	return out
}

func TestReleaseManifestCoversReleaseChannels(t *testing.T) {
	root := repoRootFromRunfiles(t)

	// The manifest side still flows through the imageset extractor: that is
	// what enforces digest pinning on the `image:` leaves and carries the
	// refs into the image union — this assertion keeps the manifest visible
	// to that machinery.
	extracted, err := imageset.CollectRendered(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]map[string]bool{}
	for _, ref := range extracted {
		if ref.File != releaseManifestRunfile || !strings.HasPrefix(ref.Ref, firstPartyPrefix) {
			continue
		}
		repo, digest := firstPartyRepoDigest(t, ref.File, ref.Ref)
		if manifest[repo] == nil {
			manifest[repo] = map[string]bool{}
		}
		manifest[repo][digest] = true
	}
	if len(manifest) == 0 {
		t.Fatalf("no first-party refs extracted from %s; the release manifest is empty or its image keys moved", releaseManifestRunfile)
	}

	channels := collectFirstPartyRefs(t, filepath.Join(root, releaseChannelsFile))
	if len(channels) == 0 {
		t.Fatalf("no first-party refs found in %s; the channels file is empty or its image keys moved", releaseChannelsFile)
	}

	repos := map[string]bool{}
	for repo := range manifest {
		repos[repo] = true
	}
	for repo := range channels {
		repos[repo] = true
	}
	var failures []string
	for repo := range repos {
		if missing := digestDiff(channels[repo], manifest[repo]); len(missing) > 0 {
			failures = append(failures,
				repo+" is pinned to "+strings.Join(missing, ", ")+" in "+releaseChannelsFile+" but the release manifest does not list it — bump the matching lane in "+releaseManifestRunfile)
		}
		if stale := digestDiff(manifest[repo], channels[repo]); len(stale) > 0 {
			failures = append(failures,
				repo+" lists "+strings.Join(stale, ", ")+" in the release manifest but no release channel pins it — the lane is stale")
		}
	}
	sort.Strings(failures)
	if len(failures) > 0 {
		t.Fatalf("release manifest and release-channel pins disagree:\n  %s", strings.Join(failures, "\n  "))
	}
}

func digestDiff(have, covered map[string]bool) []string {
	var missing []string
	for digest := range have {
		if !covered[digest] {
			missing = append(missing, digest)
		}
	}
	sort.Strings(missing)
	return missing
}
