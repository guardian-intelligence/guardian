# October 2026 security upgrade rollout

This change prepares the cloudflared certificate-trust remediation and repairs
dependency-only web image publication. Merging it has production effects:
Flux rolls the tunnel Deployment, and the image publisher rebuilds all 15 images
because its own workflow changed. First-party image automation can promote those
images. Keep the PR in draft until the operator approves those effects.

## Evidence before rollout

- Main and Flux source revision: `c8b308ac98f4757f3ecabe908ed08319931e3660`.
- The tunnel Deployment and both running pod image IDs use
  `cloudflare/cloudflared:2026.7.2`, digest
  `sha256:4f6655284ab3d252b7f28fedb19fe6c8fc82ee5b1295c20ac74d475e5398a52d`.
  Both replicas were Ready, on separate nodes, with zero restarts. The PDB
  requires one available replica. Kubernetes `/readyz` succeeded through the
  existing operator tunnel.
- Both Mac access agents run the primary checkout's Bazel-managed cloudflared
  binary, which reports `2026.8.2`. Both loopback listeners were healthy.
- [Cloudflare's advisory](https://blog.cloudflare.com/emudhra-certificate-compromise/)
  requires an updated cloudflared and redeployment of fixed containers. The
  [2026.10.3 release](https://github.com/cloudflare/cloudflared/releases/tag/2026.10.3)
  is used for Linux, containers and non-Homebrew macOS.
- The Linux download SHA-256 matches the release notes and GitHub asset digest.
  The macOS archive's actual SHA-256 is
  `1165d14b6f6360a3b8c283d7d1100024d6ec7cd9bc9e32519e96150645f3ec55`.
  This matches GitHub release asset `662573250`'s `digest` field, although the
  release-body checksum differs. `codesign --verify` succeeds, the signed binary
  has Cloudflare team identifier `68WVV388M8`, and `--version` reports 2026.10.3.
- The fixed image index digest is
  `sha256:faa8e0a1475f137c910be6025f43c8e4092fdd35461b31e19fd1390657a1ff39`,
  calculated from the registry's manifest bytes. Its Linux amd64 child is
  `sha256:c3f24df7c5e68c1467afc5486c155689f247eb02246b3a124fc5f42458728672`;
  its arm64 child is
  `sha256:e2fa661d7fabf0c0d57cf4ae602aec0b92551b7acd8d11bd286e82212ce29182`.
- TanStack PR [#1611](https://github.com/guardian-intelligence/guardian/pull/1611)
  is already merged at `7066dd5ea3eb1eab18d46015d8d572a3d98163b6`.
  Its root pnpm changes triggered the image workflow but selected no images.
  This change makes those inputs select all five web images, with regression
  tests in the universal Bazel gate. Source convergence alone does not prove
  that the running web images contain the fix.

## Approval and execution

Required approval: merge the reviewed PR through normal branch controls and allow
its automatic GitOps tunnel rollout and image publication/promotion; then replace
the primary checkout's pinned tool and restart the two Mac access agents one at a
time. No new credential or access expansion is needed. Do not perform the merge
or either restart under preparation-only authorization.

1. Recheck exact PR head, required CI, source revision, both tunnel replicas,
   their node placement, available node resources and `/readyz`. Resolve any
   relevant failing Flux dependency first. Preserve the existing read identity.
2. Merge normally. Watch `cozy-fluxcd/guardian-mgmt-dns-controller`, whose
   source path is `./src/infrastructure/base/dns`. The explicit `maxUnavailable: 0`, `maxSurge: 1`
   policy retains both old replicas until a replacement is Ready. Do not drain
   nodes or restart Mac access during this step. A surge pod must be schedulable.
3. Watch the tunnel Deployment until both replicas use the fixed image digest,
   remain Ready, and register healthy edge connections. Repeatedly check
   Kubernetes `/readyz` and Talos access through the existing Mac listeners.
   A stalled rollout is a stop condition; inspect events and repair forward.
4. Update the primary checkout only when its working tree is clean and its
   current owner's work is preserved. Run the repo tool installation from the
   approved source; verify the installed binary's hash and `--version` before
   restarting either agent. Installing a new shim does not update running PIDs.
5. With the cluster rollout healthy, restart the Talos access agent first and
   verify listener and authenticated read, then restart the Kubernetes access
   agent and verify listener and `/readyz`. Use the existing launchd declarations
   and credentials. Never stop both listeners together. Keep a second terminal
   with the established working Kubernetes route during the Talos restart.
6. Confirm the image workflow at the exact merge commit actually publishes the
   affected web images. Record their immutable registry digests, follow Flux
   image automation and Flagger canaries, and match running pod image IDs to
   the new artifacts. Verify web health/liveness probes and user signals.
   Monitor Alerta for at least 15 minutes after convergence. Report source,
   built artifacts, running versions and health separately.

## Failure and recovery

The rollout's first recovery mechanism is retaining the healthy old replicas while
the fixed image fails readiness. Do not delete those pods. Inspect scheduling,
image-pull and edge-connection failures, then repair the reviewed IaC forward.
Do not use `kubectl set image` or a manual rollout as a second control plane.

Keep the previous immutable image reference and Mac binary available for diagnosis.
Returning to either old version restores the known certificate-trust exposure;
it is an emergency, time-limited access-recovery decision requiring separate
operator approval, followed immediately by a fixed forward version. Never disable
TLS verification. If access is lost, stop dependent changes and use the existing
approved recovery/authentication runbook; do not mint broader credentials.
