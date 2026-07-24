Workflows in this directory are the repo's only foothold on
GitHub-administered compute, and the set is closed by design: the cluster,
not GitHub, is the control plane (see the root AGENTS.md coding
guidelines). A workflow earns a file here for exactly one of two reasons:

1. **The merge-time gate** — singular. `build-and-test.yml` is every check a
   pull request runs: build+test `//...`, the secret scan, the tool-pin
   fresh-fetch build, the union images-lock derivation, and the runtime
   gates that need a packed image. A new check is a step in that one job,
   and belongs in the Bazel graph as a test reachable from `//...` unless
   it needs git/GitHub context a hermetic action cannot have (the secret
   scan) or Bazel's own caching would defeat it (the tool-pin fresh
   fetch). Never add a second `pull_request`-triggered workflow.
2. **Trusted publisher identity** — post-merge jobs that build, sign, and
   push artifacts (`images.yml`, `images-lock-sign.yml`,
   `postflight-cli-image.yml`, `postflight-cli-release.yml`). Each workflow
   file path IS a cosign/Fulcio identity the cluster and the world verify.
   NEVER rename these files; thin them in place.

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
