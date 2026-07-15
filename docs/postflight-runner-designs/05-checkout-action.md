# 05 — Custom checkout action

> **EPHEMERAL.** Delete with this directory when the implementation pass completes.

`postflight-checkout`: the required checkout path for this integration pass.
Stock `actions/checkout` compatibility is a separate later lane (tested
against a version matrix); nothing here depends on it.

## What it actually does (and doesn't)

By the time any step runs, the heavy lifting already happened: hostd cloned
the scope's current generation at claim and guestd mounted it at
`$GITHUB_WORKSPACE` before the runner started (01's convergence invariant).
So the action is deliberately thin git plumbing, not a mount manager:

1. Assert the workspace is the mounted zvol: guestd writes a marker file
   after mount convergence and exports its path in the runner env as
   `POSTFLIGHT_WORKSPACE_READY_FILE` (01 wires it); fail loud if either
   half is absent — this action only supports our runners in this pass.
2. If the job's SHA is already in the workspace object store (warm rerun):
   no network at all, straight to `git checkout --force <sha>`.
3. Otherwise (cold, or warm at a new SHA): fetch the SHA's single-commit
   pack closure from the host-local checkout-bundle server (merged
   `hostd/checkoutbundle` — a per-SHA closure cache; it does not speak the
   git wire protocol, so there is no client-side delta negotiation — the
   delta economy lives host-side in the mirror's incremental origin fetch),
   `git index-pack` it, record the commit in `.git/shallow` when its
   parents are absent (depth-1 semantics: `git log`/`rev-list`/`describe`
   behave exactly as under stock `actions/checkout`), then
   `git checkout --force <sha>`. A foreground `git gc --auto` (pack limit
   4) after each fetch bounds pack accretion across the sealed lineage;
   superseded closures age out via git's default expiry.
4. No cleaning layers beyond `checkout --force` for tracked files. Stale
   generated/ignored files persisting across runs is a **documented customer
   tradeoff** (ruled 2026-07-15): huge time savings, reinvest a little into
   keeping your repo cache-clean. The action's README carries this warning
   prominently, with the "import from a deleted file still passes" incident
   as the worked example.

Implementation: a small JS action (node runtime is in the image), vendored
into the demo repo as `.github/actions/postflight-checkout` for this pass.
A dedicated public `guardian-intelligence/checkout` repo is the distribution
story later, once the interface survives the hammer.

## Speculative demand (phase 2 of this action — designed, not in this pass)

The action doubles as an early demand signal: a repo whose workflow
references it can be pre-warmed before the `workflow_job` webhook arrives
(e.g. an early job signalling for downstream jobs). Design constraints,
recorded so the interface doesn't paint over them:

- Ping creates a `speculative` demand row with a TTL; it must be robust to
  checkout-without-workload (minimum-timeout-to-start, silent expiry).
- The authoritative webhook upgrades the row in place; a TTL expiry releases
  whatever was reserved. Never an error in either direction.

Not needed for the first green run: the webhook already leads the checkout
step by several seconds, which covers claim-time pre-cloning.

## Demo repo changes

`postflight-nextjs-demo` gets a third workflow (or the existing postflight
workflow updated): identical build-and-test steps, with
`uses: ./.github/actions/postflight-checkout` replacing stock checkout.
The GitHub-hosted comparison workflow keeps stock checkout — the hammer
compares like-for-like conclusions and timings, and its full build-and-test
logs in the Actions UI are the observability surface for the pass.

## Testing

- Action unit tests run in the demo repo (node, no Bazel).
- The e2e proof is 06: a warm rerun of an already-fetched SHA must serve
  zero bundle bytes, a warm run at a new SHA exactly one host-local pack
  (bundle-server bytes-served + cache-hit counters attest it), a cold run
  a full closure, all green.
