# Postflight runner — implementation-pass design docs

> **EPHEMERAL.** This directory is deleted when the implementation pass is
> complete: every component below implemented, and the definition of done at
> the bottom of this file verified. Do not link to these files from durable
> docs; anything worth keeping graduates to an ADR or `docs/postflight-product.md`.

## Goal

Every design in this directory implemented in this repo, verified end-to-end
with real Bazel, JavaScript, JVM, Go, Envoy, and LLVM CI workloads dispatched
through GitHub Actions against the tracer host. Each fixture must pass its
ordinary CI before Postflight is introduced. The final artifact includes full
job logs, assignment/rendezvous/restore timings, recovery classifications,
execution timings, and NVMe usage.

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

- **SEV-SNP first.** The product contract is an authenticated encrypted
  workspace/tool/process generation bound to the guest platform fingerprint.
  The tracer may exercise plaintext storage where hardware key release is not
  yet available, but it must not weaken restore classification or identity.
- **Process warmth is optional.** CRIU is the current process artifact. A
  recoverable restore miss invalidates only that artifact and starts the same
  assigned Worker in a cold capsule; an integrity failure recycles the VM.
- **Assignment is local and exact.** A patched Runner.Listener publishes the
  check-run, request, protocol-job, runner, and workflow identity before it
  creates Runner.Worker. No job is predicted from labels or display names.
- **Custom checkout accelerates source convergence.** Correct assignment and
  rendezvous do not depend on the action; fixtures use it to measure the
  durable workspace and delta-commit path.
- **Workspace convergence is a documented customer tradeoff**, not a design
  constraint. No cleaning layers beyond what checkout needs to function; a
  divergence canary is a possible customer feature much later.
- **Destroy-and-refill.** No VM is ever resurrected or reused across jobs.

## Definition of done

- [ ] `bazel test //...` green with all new packages
- [ ] guestd + golden image v0 built and installed on the tracer host
- [ ] `cmd/hostd` running under systemd on the tracer host, synced, slots visible in `host_slots`
- [ ] Fixture workflows using the custom checkout action
- [ ] One dispatch → green with zero manual steps (demand → warm listener → local assignment → rendezvous → checkout → build+test → seal → promote)
- [ ] Second dispatch of the same job clones the promoted generation (warm checkout, zero bundle bytes served)
- [ ] Hammer run: ≥50 dispatches across burst/sustained/cancel patterns, all assertions pass
- [ ] Timings (pickup/exec/seal p50/p90/p100) and per-generation NVMe stats recorded in the hammer report
- [ ] This directory deleted
