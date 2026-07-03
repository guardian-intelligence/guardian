# images.lock: schema and invariants

Status: active as of 2026-07-03. Complements `docs/supply-chain-design.md`
(signing/trust model for this file) and `docs/manifest-conformance-design.md`
(Tier 1 manifest conformance this file's sibling test suite belongs to).

## Purpose

`src/infrastructure/bootstrap/bundle/images.lock` is the hand-maintained,
digest-pinned enumeration of every OCI artifact (container images, Helm/Flux
OCI artifacts, the Talos installer) a cold guardian-mgmt bootstrap must be
able to serve from the workstation mirror. It is BOTH (a) the conformance
target checked against every rendered manifest in the repo, and (b) the
canonical input the Hauler-based dark-uplink bundle generator
(`src/infrastructure/cmd/bundle`) reads FROM — never the reverse; nothing
hand-authors or edits the Hauler manifest independently.

## Schema

The file's own header (lines 1-11 as of this writing) is the format's own
spec-of-record:

```
# images.lock — every OCI artifact a cold guardian-mgmt bootstrap must be able to
# serve from the workstation mirror. One ref@digest per line; # starts a comment.
#
# Derivation (re-run to refresh; CI job to own this is planned):
#   workload section:  kubectl get pods -A -o json -> containerStatuses imageID
#   system section:    talosctl images default (pinned talosctl, matches machine.install.image)
#   artifact section:  cozystack-operator args + OCIRepository status on the live cluster,
#                      ghcr manifest HEAD for installer/chart digests
#
# Invariants (enforced by conformance test, Tier 1):
#   - every entry is digest-pinned
#   - every image reference rendered from this repo appears here with the same digest
```

Line grammar (formal):

```
entry    := ref "@" "sha256:" hex64
ref      := repository (no embedded whitespace or quotes)
comment  := "#" rest-of-line
section  := "# --- " title " ---"
```

- Blank lines and full-line comments are ignored by the parser.
- A line may not mix a trailing comment onto an entry in principle, but the
  current parser's `strings.Index(entry, "#")` truncation in
  `parseImagesLock` (`src/infrastructure/tests/images_lock_test.go`) happens
  to allow it (everything from the first `#` onward is stripped before the
  remainder is parsed as the entry). This is a **documentation-vs-code gap**:
  no test in `TestImagesLockWellFormed` currently exercises a
  trailing-comment-on-an-entry-line case, so the behavior is untested, not
  intentionally guaranteed. Flagging here rather than silently relying on it;
  a follow-up should add an explicit case to `TestImagesLockWellFormed`
  covering `ref@sha256:<hex> # trailing note` before this grammar note can be
  upgraded from "observed" to "specified."

Sections observed today (exact header strings, quoted verbatim from the live
file):

1. `# --- bootstrap artifacts (not pod images) ---` — the Talos installer
   image, the Cozystack installer Helm chart, Flux-pulled OCI artifacts
   (`cozystack-packages`, External Secrets and Kargo Helm charts), and the
   ephemeral `cozy-installer` pre-install labeler hook Job image. Populated
   by reading `cozystack-operator` args, live `OCIRepository` status, and
   ghcr manifest HEAD for installer/chart digests.
2. `# --- Kubernetes system images actually held by the rebuilt cluster (2026-07-02 drill) ---`
   — kubelet, etcd, kube-apiserver, kube-controller-manager, kube-scheduler,
   pause, and coredns, at the versions Cozystack's `Chart.yaml` actually pins
   (not the Talos distribution defaults, which are never pulled because
   Cozystack replaces kube-proxy/flannel with kube-ovn/cilium). Populated by
   `talosctl images default` read from the live cluster's `system`
   containerd namespace.
3. `# --- drill/bench job images (repo-pinned in Go tools, not resident on the cluster) ---`
   — images referenced by Go drill/bench tooling in this repo that are never
   scheduled as cluster pods, so a pod scrape would never find them.
4. `# --- running workload images (scraped from live cluster, 2026-07-02, post-cold-boot-drill) ---`
   — every remaining image actually running on the rebuilt cluster,
   scraped via `kubectl get pods -A -o json` → `containerStatuses[].imageID`.
   Entries duplicating the drill/bench section above are omitted per the
   file's own dedup rule.

Section membership is **informational/provenance-only**: the conformance
test does not gate on section placement, only on global `(repo, digest)`
presence (see Invariant 4 below). Adding, renaming, or reordering sections
is not a breaking change to any existing test.

## Invariants

Numbered, each mapped to an existing or (proposed) new test name:

1. Every non-comment, non-blank line is a digest-pinned OCI reference
   (`repo@sha256:<64 lowercase hex>`). — `TestImagesLockWellFormed` +
   `splitImageRef` (existing, `images_lock_test.go`).
2. The lock is non-empty. — `TestImagesLockWellFormed` (existing).
3. No `(repository, digest)` pair appears twice, counting tag+digest and
   digest-only forms of the same pair as the same entry. — enforced within
   `TestImagesLockWellFormed` via `parseImagesLock`'s duplicate check
   (existing; today enforced as a side effect of parsing rather than a
   separately named test).
4. Every image reference rendered from `src/infrastructure/deployments` and
   `src/infrastructure/base` (`image:` scalars, Helm image-value maps,
   kustomize images-transformer entries) appears in the lock with the same
   digest. — `TestRenderedImagesDigestPinnedAndLocked` (existing).
5. Every `# --- <title> ---` section header is followed by at least one
   entry before EOF or the next header (no silently-empty sections, which
   would make the attestation's per-section counts meaningless). —
   **NEW**: `TestImagesLockSectionsWellFormed`
   (`src/infrastructure/tests/images_lock_test.go`).
6. The file's own sha256 and the count of entries per section, as recorded
   in the most recent signed in-toto attestation for the commit at HEAD,
   match what parsing the file at HEAD produces. — this invariant is
   checked **at attestation-generation time in CI**, not by a repo-local Go
   test (a Go test can't reach the signed attestation offline in the general
   case); documented here as the invariant the attestation exists to prove.
   See Provenance below.

## Prior Art

Guardian evaluated Carvel's `ImagesLock` (`imgpkg.carvel.dev/v1alpha1`) as a
possible schema/tooling adoption and deliberately did not adopt any Carvel
tool (imgpkg, kbld, ytt, kapp): every one of the four duplicates or conflicts
with a component Guardian already chose for the same job (Hauler for
air-gapped bundling over imgpkg; the hand-reviewed plaintext `images.lock` +
bespoke Go conformance test over kbld-generated locks; Kustomize+Flux over
ytt+kapp for rendering/reconciliation), and Carvel's status as of mid-2026
— actively but thinly maintained by a small, largely single-vendor
(VMware/Broadcom-alumni) team, four years stuck at CNCF Sandbox — gives no
fresh reason to reopen any of those calls. See the
`dark-uplink-bundle-tooling` decision for the Hauler-over-imgpkg call in
full; this section only restates the schema-level comparison.

The only thing adopted from Carvel is the *shape* of its `ImagesLock` schema
as a documentation reference point, not any binary or bundle format:

| Carvel `ImagesLock` | Guardian `images.lock` | Comparison note |
|---|---|---|
| `apiVersion: imgpkg.carvel.dev/v1alpha1` | (none — plaintext, no envelope) | Guardian deliberately has no machine-readable envelope; the format is one grep/diff-friendly line per artifact, prioritizing git-reviewability over structured parsing. This is a considered trade, not an oversight. |
| `kind: ImagesLock` | (none) | Same reasoning as above; the file's identity is its path (`src/infrastructure/bootstrap/bundle/images.lock`) and its being the sole input the conformance test and the Hauler manifest generator both read, not a `kind` discriminator. |
| `spec.images: []` list of entries | Flat list of lines, grouped under `# --- <section> ---` comment headers | Carvel's list is unordered/flat with structured fields; Guardian's is ordered into four human-legible provenance sections — the section *is* Guardian's substitute for a `kind`/`annotations`-driven classification, done via comments instead of YAML keys, again favoring git-diff legibility. |
| `image: <name>@sha256:<digest>` field per entry | `ref@sha256:<digest>` bare line per entry | Structurally identical constraint (always digest-pinned, `repo@sha256:hex64`), different serialization: Carvel needs a YAML key to hold the string; Guardian's line *is* the string. `splitImageRef` in `images_lock_test.go` enforces the same shape Carvel's schema would validate. |
| `annotations` map per entry (free-form, e.g. `kbld.carvel.dev/id`, image origin metadata) | `#` line-comments above/beside entries (free-form prose) | Same purpose (provenance/context per entry) via a weaker but zero-schema mechanism: comments aren't machine-parsed today. This is the one place Carvel's schema is *more capable* — a non-adopted capability, not a silent gap. The in-toto attestation's per-section counts (see Provenance below) recover *some* of that machine-checkable provenance without adopting Carvel's annotation schema. |
| No file-level dedup rule specified by the schema itself (tooling-dependent) | Explicit dedup rule: `(repo, digest)` pairs must be unique; tag+digest and digest-only forms of the same pair count as duplicates (enforced by `parseImagesLock`) | Guardian's invariant is stricter and repo-local; Carvel leaves this to whatever produced the lock (typically `kbld`). |

The two formats converge on the same core invariant (digest-pinned OCI
reference, one entry each) because that invariant is not a Carvel invention
— it's the only sound way to express "closure of everything this system
depends on" — and Guardian's format is a deliberately lower-tech,
higher-git-legibility encoding of the same idea, not an inferior copy.

## Provenance

`.github/workflows/images-lock-sign.yml` signs `images.lock` on every push to
`main` that changes it, in two distinct steps:

1. **Signature** (pre-existing): `cosign sign-blob --yes --new-bundle-format
   --bundle images.lock.sigbundle` produces a raw detached signature over the
   file's bytes, published to
   `ghcr.io/guardian-intelligence/supply-chain:images.lock-<sha256>`. This
   proves "CI produced these exact bytes" — nothing about which commit or
   what the file's internal shape is.
2. **Attestation** (this change): `cosign attest-blob --yes --new-bundle-format
   --type https://guardianintelligence.org/attestations/images-lock/v1
   --predicate images.lock.predicate.json --bundle
   images.lock.attestation.sigbundle` additionally proves *which commit* and
   *what section-shape* those bytes claim to represent, published to
   `ghcr.io/guardian-intelligence/supply-chain:images.lock-<sha256>-attestation`
   (a distinct tag suffix so the plain signature and the attestation are
   independently fetchable and neither push races the other).

**Subject:** `src/infrastructure/bootstrap/bundle/images.lock`, addressed by
its own sha256 (the same hash already computed as `$lockhash` in
`images-lock-sign.yml`, reused rather than recomputed).

**predicateType:** `https://guardianintelligence.org/attestations/images-lock/v1`
— a new, stable, guardian-controlled URI. This is deliberately not
`slsaprovenance`/`slsaprovenance1`: this attestation is not build provenance
in the SLSA sense (no builder-invocation-produces-output-from-source-materials
framing fits a hand-curated closure list well); it is a closure/integrity
claim, so it gets its own predicate type, the same way this repo already uses
`spdxjson` for SBOM rather than forcing everything through one shorthand.

**Predicate fields** (flat JSON object, generated by
`src/infrastructure/cmd/images_lock_attest`, never hand-authored):

```json
{
  "gitCommit": "<github.sha of the push that triggered signing>",
  "lockSha256": "<sha256 of images.lock, the same value already computed as $lockhash>",
  "sectionCounts": {
    "<exact section title, dashes stripped>": <entry count>,
    ...
  },
  "totalEntries": <sum of all entries across all sections>
}
```

Section titles are used as map keys (exact `# --- <title> ---` string minus
the leading/trailing `--- ` markers) so the predicate is self-describing
without needing the lock file alongside it to interpret which count is
which. As of this writing the four sections count 6, 7, 2, and 130 entries
respectively (145 total) — regenerate these numbers from the live file
rather than trusting this doc; they drift every time the lock is refreshed.

**Producing workflow:** `.github/workflows/images-lock-sign.yml` (same job,
same trigger, same permissions as the existing signature step — no new
secrets or permissions).

**Verification** (online):

```sh
cosign verify-blob-attestation \
  --certificate-identity "https://github.com/guardian-intelligence/guardian/.github/workflows/images-lock-sign.yml@refs/heads/main" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --type https://guardianintelligence.org/attestations/images-lock/v1 \
  --bundle images.lock.attestation.sigbundle \
  src/infrastructure/bootstrap/bundle/images.lock
```

**Verification** (offline, dark drive — same trusted root as the plain
signature, no second trust root):

```sh
cosign verify-blob-attestation --bundle images.lock.attestation.sigbundle \
  --certificate-identity "https://github.com/guardian-intelligence/guardian/.github/workflows/images-lock-sign.yml@refs/heads/main" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --trusted-root src/infrastructure/bootstrap/bundle/sigstore-trusted-root.json \
  --type https://guardianintelligence.org/attestations/images-lock/v1 \
  src/infrastructure/bootstrap/bundle/images.lock
```

The relationship between the two signing operations in one line: the
sign-blob bundle proves CI produced these exact bytes; the attestation
additionally proves which commit and section-shape those bytes claim to
represent.

Carried forward from the repo's prior in-toto/SLSA experience (three
iterations on the aisucks npm SDK, ending in commit `04c788e`, "release:
migrate SDK attestations to cosign v3"): no custom DSSE-signing code, no
hand-rolled sigstore-js, no CUE-defined schema — stock `cosign attest-blob`
/ `cosign verify-blob-attestation` only, and a small flat predicate object
rather than a nested SLSA buildDefinition.

## Non-Goals

- Talos machine config is NOT an OCI artifact and is NOT covered by this
  lock or its conformance tests; it is validated by its own separate Go
  conformance test suite (`src/infrastructure/tests/talm_render_test.go`).
  This is an already-agreed architectural boundary, not an oversight — the
  Talos *installer image* itself (an OCI artifact) correctly does appear in
  the lock's bootstrap-artifacts section; the machine config that consumes
  it does not.
- OpenBao self-init HCL is NOT an OCI artifact and is NOT covered by this
  lock; it is validated by the OpenBao-specific conformance test suite (see
  `docs/openbao-design.md` and
  `src/infrastructure/tests/openbao_conformance_test.go`). Same boundary
  reasoning as Talos above.
- This spec does not cover *how* the Hauler manifest/bundle is generated or
  verified end-to-end — that is `docs/supply-chain-design.md`'s and the
  dark-uplink-bundle-tooling decision's territory; this doc only specs the
  lock file itself, which is their shared upstream input.
