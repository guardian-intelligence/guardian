# Postflight runner — implementation-pass design docs

> **EPHEMERAL.** This directory is deleted when the implementation pass is
> complete: every component below implemented, and the definition of done at
> the bottom of this file verified. Do not link to these files from durable
> docs; anything worth keeping graduates to an ADR or `docs/postflight-product.md`.

## Goal

Every design in this directory implemented in this repo, verified end-to-end
with a real CI workload: the `postflight-nextjs-demo` build-and-test jobs
dispatched repeatedly against the tracer host, completing green with zero
manual steps, with full build-and-test logs observable in the test repo's
Actions UI, and pickup/exec/seal timings plus NVMe usage recorded by the
hammer harness.

## Component map

| Doc | Component | New code lives at |
| - | - | - |
| [01-guestd](01-guestd.md) | In-guest agent: mounts, runner exec, quiesce | `src/services/postflight/guestd/` |
| [02-golden-image](02-golden-image.md) | Golden image v0 build → zvol template | `src/services/postflight/image/` |
| [03-hostd](03-hostd.md) | `cmd/hostd` main, systemd launcher, vsock transport | `hostd/cmd/hostd/`, `hostd/vm/` |
| [04-workspace-generations](04-workspace-generations.md) | Generation lifecycle, seal/promote CAS, affinity | `controlplane/`, `hostd/agent/` |
| [05-checkout-action](05-checkout-action.md) | Custom checkout action (required for integration) | demo repo `.github/actions/` |
| [06-hammer](06-hammer.md) | Load harness, assertions, measurements | `src/tools/postflight-hammer/` |

## Sequencing

```
 (A) 01-guestd ──┬── 02-golden-image ──┐
 (B) 03-hostd ───┴──────────────────── ├── first zero-manual green run ── 06-hammer
 (C) 04-workspace-generations ─────────┤
 (D) 05-checkout-action ───────────────┘
```

Tracks A–D are independent until integration. The vsock transport (in 03)
and guestd (01) share `guestproto` and should land together. 04's control
plane changes are inert behind the existing `SCHEDULER_ENABLED` /
`HOSTD_SYNC_SECRET` gates.

## Standing rulings that constrain this pass

- **Pre-TEE scope.** Plaintext zvols; generation identity is the ZFS
  snapshot GUID. LUKS, in-guest tree hashing, and attestation documents
  arrive with the SEV-SNP phase, after the dedicated key-handling security
  review. Seams are specified where they land; nothing is implemented early.
- **No vmstate anywhere.** Warmth, when it arrives, is a CRIU image on an
  ordinary zvol. Out of scope for this pass.
- **Polling shape is accepted.** No interval tuning, no NOTIFY retrofits.
  The full pipeline redesign happens after e2e is proven.
- **Custom checkout is required for integration.** Stock `actions/checkout`
  compatibility is a later lane with a version matrix.
- **Workspace convergence is a documented customer tradeoff**, not a design
  constraint. No cleaning layers beyond what checkout needs to function; a
  divergence canary is a possible customer feature much later.
- **Destroy-and-refill.** No VM is ever resurrected or reused across jobs.

## Definition of done

- [ ] `bazel test //...` green with all new packages
- [ ] guestd + golden image v0 built and installed on the tracer host
- [ ] `cmd/hostd` running under systemd on the tracer host, synced, slots visible in `host_slots`
- [ ] Demo repo workflow using the custom checkout action
- [ ] One dispatch → green with zero manual steps (webhook → demand → lease → JIT → VM → checkout → build+test → seal → promote)
- [ ] Second dispatch of the same job clones the promoted generation (warm checkout, zero bundle bytes served)
- [ ] Hammer run: ≥50 dispatches across burst/sustained/cancel patterns, all assertions pass
- [ ] Timings (pickup/exec/seal p50/p90/p100) and per-generation NVMe stats recorded in the hammer report
- [ ] This directory deleted
