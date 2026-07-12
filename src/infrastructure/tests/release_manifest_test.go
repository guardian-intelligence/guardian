package tests

// Tier-1 release-manifest conformance: the release manifest
// (deployments/guardian/system/release-manifest.yaml) is the reviewable
// definition of what Guardian releases (docs/registry-design.md), and it
// must never drift from what the cluster runs: for every first-party
// repository, the manifest's digest set equals the digest set rendered
// across the workload manifests. A pin bump that forgets the manifest — or
// a manifest lane naming a digest nothing runs — fails here. Equality is
// per-repo digest UNION, not per lane: mid-promotion a stage lane diverges
// from its siblings, and both digests are then legitimately released.

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/guardian-intelligence/guardian/src/infrastructure/imageset"
)

const (
	releaseManifestRunfile = "src/infrastructure/deployments/guardian/system/release-manifest.yaml"
	firstPartyPrefix       = "ghcr.io/guardian-intelligence/"
)

// The projector and the countersigner both enumerate first-party refs with
// the shell grammar ghcr.io/guardian-intelligence/[a-z0-9-]+@sha256:... — a
// repo name outside that class would silently vanish from both loops'
// estates while every gauge reads healthy. This test is what makes that
// impossible: a nonconforming name fails CI at onboarding time instead.
var firstPartyRefGrammar = regexp.MustCompile(`^ghcr\.io/guardian-intelligence/[a-z0-9-]+@sha256:[a-f0-9]{64}$`)

func firstPartyRepoDigest(t *testing.T, ref imageset.RenderedRef) (string, string, bool) {
	t.Helper()
	if !strings.HasPrefix(ref.Ref, firstPartyPrefix) {
		return "", "", false
	}
	name, digest, found := strings.Cut(ref.Ref, "@")
	if !found {
		t.Fatalf("%s: first-party ref %q is not digest-pinned", ref.File, ref.Ref)
	}
	// tag+digest refs normalize to the bare repo.
	if idx := strings.LastIndex(name, ":"); idx > strings.LastIndex(name, "/") {
		name = name[:idx]
	}
	if !firstPartyRefGrammar.MatchString(name + "@" + digest) {
		t.Fatalf("%s: first-party ref %q does not match the grammar the countersigner and release projector enumerate with (%s) — the signing and projection loops would silently skip it; rename the repo or widen both scripts' greps and this pattern together",
			ref.File, ref.Ref, firstPartyRefGrammar)
	}
	return name, digest, true
}

func TestReleaseManifestCoversRenderedReleases(t *testing.T) {
	extracted, err := imageset.CollectRendered(repoRootFromRunfiles(t))
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]map[string]bool{}
	workloads := map[string]map[string]bool{}
	manifestSeen := false
	for _, ref := range extracted {
		repo, digest, ok := firstPartyRepoDigest(t, ref)
		if !ok {
			continue
		}
		side := workloads
		if ref.File == releaseManifestRunfile {
			side = manifest
			manifestSeen = true
		}
		if side[repo] == nil {
			side[repo] = map[string]bool{}
		}
		side[repo][digest] = true
	}
	if !manifestSeen {
		t.Fatalf("no first-party refs extracted from %s; the release manifest is empty or its image keys moved", releaseManifestRunfile)
	}

	repos := map[string]bool{}
	for repo := range manifest {
		repos[repo] = true
	}
	for repo := range workloads {
		repos[repo] = true
	}
	var failures []string
	for repo := range repos {
		if missing := digestDiff(workloads[repo], manifest[repo]); len(missing) > 0 {
			failures = append(failures,
				repo+" runs "+strings.Join(missing, ", ")+" but the release manifest does not list it — bump the matching lane in "+releaseManifestRunfile)
		}
		if stale := digestDiff(manifest[repo], workloads[repo]); len(stale) > 0 {
			failures = append(failures,
				repo+" lists "+strings.Join(stale, ", ")+" in the release manifest but nothing renders it — the lane is stale")
		}
	}
	sort.Strings(failures)
	if len(failures) > 0 {
		t.Fatalf("release manifest and rendered workload pins disagree:\n  %s", strings.Join(failures, "\n  "))
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
