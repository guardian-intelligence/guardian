Workflows in this directory are the repo's only foothold on
GitHub-administered compute, and the set is closed by design: the cluster,
not GitHub, is the control plane (see the root AGENTS.md coding
guidelines). A workflow earns a file here for exactly one of two reasons:

1. **Merge-time gate** — a required or advisory check validating untrusted
   PR code. The safelist: the universal Bazel gate (`build.yml`: build+test
   `//...`, secret scan, actions allowlist, tool-pin fetch verification)
   and the pin-provenance gates (the `*-gate` jobs in `*-image.yml`, which
   cosign-verify moved image pins). A new gate belongs in the Bazel
   graph as a test reachable from `//...` unless the network is its
   subject or it needs git/GitHub context a hermetic action cannot have.
2. **Trusted publisher identity** — post-merge jobs that build, sign, and
   push artifacts (`*-image.yml` main-push jobs, `images-lock-sign.yml`).
   Each workflow file path IS a cosign/Fulcio identity the cluster
   verifies. NEVER rename these files; thin them in place.

Nothing else: schedulers, preview environments, promotion glue, and any
form of cluster administration run in-cluster. YAML residents of that
class are migration debt, not precedent.

The threat model that separates the two classes: `pull_request`-triggered
steps execute the PR head's code — treat every such step as running
attacker-controlled code. PR-time jobs do only untrusted operations: no
secrets beyond the default read-only `GITHUB_TOKEN`, no cluster or
registry credentials, no cache writes (the Bazel cache restores on every
run but only main pushes save), and never `pull_request_target` with a
PR-head checkout. Trusted operations — signing, pushing, App tokens —
run only on `push` to main, on code that has already passed the gate.
