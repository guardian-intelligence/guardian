# Release runner runbook

Provisions the self-hosted GitHub Actions runner that executes
`.github/workflows/release.yml` (the automated form of
docs/runbooks/aisucks-release.md). Customer-grade: every step is a copy-paste
command with an expected outcome.

**Placement:** an operator workstation — never dev/gamma/prod. Traffic-serving
hosts run commit-pinned release artifacts and never build from source
(AGENTS.md constraints), and this host runs Bazel on every release. The runner
holds gamma+prod cluster credentials: treat the host as a prod credential
store. When the GitHub-App forge lands, this moves to an ephemeral in-cluster
runner; this runbook is the v0.

## 0. Preconditions

```sh
bazelisk version | head -1     # expect: a Bazelisk version line (.bazeliskrc pins Bazel 9.1.0 + sha)
                               # (a "Cannot write to standard output" line after it is bazelisk's
                               # broken-pipe complaint about head — harmless)
ls ~/.local/state/guardian/guardian-gamma ~/.local/state/guardian/guardian-prod
# expect, each: controlplane.yaml  kubeconfig  secrets.yaml  talosconfig
ls ~/.local/bin/kubectl        # expect: present (guardian tool shim; the gate uses it)
```

If the state dirs are missing, this host has never operated the fleet: copy
`~/.local/state/guardian/` from the machine of record over an encrypted
channel. It never goes in git or GitHub secrets.

Network reach (the sites' ingress firewall allows 80/443/6443/50000):

```sh
curl -fsS -o /dev/null -w 'gamma %{http_code}\n' https://gamma.aisucks.app/healthz   # expect: gamma 200
curl -fsS -o /dev/null -w 'prod %{http_code}\n'  https://aisucks.app/healthz         # expect: prod 200
```

## 1. Install the runner (version + sha pinned)

The directory is guardian-specific on purpose: `~/actions-runner` is the
conventional home of runners for *other* repos (this host already carries
apm2's), and a registered runner's directory can never be shared.

```sh
mkdir -p ~/actions-runner-guardian && cd ~/actions-runner-guardian
curl -fsSLO https://github.com/actions/runner/releases/download/v2.335.1/actions-runner-linux-x64-2.335.1.tar.gz
echo "4ef2f25285f0ae4477f1fe1e346db76d2f3ebf03824e2ddd1973a2819bf6c8cf  actions-runner-linux-x64-2.335.1.tar.gz" | sha256sum -c
# expect: actions-runner-linux-x64-2.335.1.tar.gz: OK
tar xzf actions-runner-linux-x64-2.335.1.tar.gz
```

(`svc.sh` is not in the tarball — `config.sh` generates it in step 2.)

## 2. Register (repo-scoped, labeled, no self-update)

```sh
TOKEN=$(gh api -X POST repos/Anveio/guardian/actions/runners/registration-token -q .token)
./config.sh --url https://github.com/Anveio/guardian --token "$TOKEN" \
  --name guardian-release-0 --labels guardian-release \
  --disableupdate --unattended
# expect: √ Runner successfully added / √ Settings Saved.
```

(No `gh`? The token also lives at repo → Settings → Actions → Runners → New
self-hosted runner.)

`--disableupdate` holds the binary at the pinned version. GitHub refuses jobs
from runners more than ~30 days behind, so on that cadence re-run step 1 with
the then-current version and update the pin here in the same commit.

## 3. Run as a service

```sh
sudo ./svc.sh install "$USER" && sudo ./svc.sh start
sudo ./svc.sh status   # expect: active (running)
```

## 4. Lock the repo down (one-time)

- Repo → Settings → Actions → General → Fork pull request workflows: require
  approval for **all** outside collaborators. While the repo is private GitHub
  rejects this setting (422) because fork PR workflows cannot run at all —
  already stricter than required. **If the repo ever goes public, apply it that
  day:** `gh api -X PUT repos/Anveio/guardian/actions/permissions/fork-pr-contributor-approval -f approval_policy=all_external_contributors`
- Never add a `pull_request` trigger to any workflow with
  `runs-on: guardian-release`. PR code is untrusted and this runner can
  converge prod — the AGENTS.md trust-boundary axis is exactly this line.

## 5. Verify end-to-end

```sh
gh workflow run release --repo Anveio/guardian --ref main
gh run watch --repo Anveio/guardian
# expect: test → converge gamma → gate gamma → promote prod → gate prod → tag
git fetch --tags && git tag -n1 -l 'aisucks/v*' | tail -1
# expect: the new tag, annotated with aisucks@sha256:<digest>
```
