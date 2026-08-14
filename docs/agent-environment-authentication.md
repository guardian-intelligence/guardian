# Agent environment authentication

| Environment | Authentication | Identity and authority | Lifetime |
| --- | --- | --- | --- |
| Local agent, routine read | `aspect infra auth --persona=read` with browser/device login | `#platform-agent`: cluster read, port-forward, no Secrets or general writes | Unattended `offline_access` with a 30-day idle window |
| Local agent, routine repair | `aspect infra auth --persona=write-basic` with fresh browser/device approval | `#platform-write-basic`: read, port-forward, delete pods/jobs, scale workloads, and mint scoped writer or delivery-read tokens | Keycloak session; no `offline_access` |
| Local agent, emergency | `aspect infra auth --persona=write-all` with fresh browser/device approval | `#platform-write-all`: cluster-admin | Keycloak session; no `offline_access` |
| Local agent, Keycloak unavailable | `aspect infra auth --persona=root --reason "<why>"` from custody | Audited, paging x509 breakglass: cluster-admin | Short certificate lifetime |
| Codex Cloud | [`scripts/codex-cloud-setup.sh`](../scripts/codex-cloud-setup.sh) with Codex-scoped Cloudflare Access and portable `platform-agent` OIDC material | `#platform-agent`: cluster read and port-forward; no Secrets or general writes | OIDC cache plus Codex environment lifecycle |
| Cursor-hosted Cloud Agent | [`scripts/agent-cloud-setup.sh`](../scripts/agent-cloud-setup.sh) with `GUARDIAN_AGENT_PROVIDER=cursor`, Cursor-scoped Cloudflare Access, and a JIT TokenRequest | `guardian-cloud-agent-cursor`: Flux/Kargo/Flagger, workload, event, and log reads; no Secrets, sensitive cluster state, exec, port-forward, or writes | 10 minutes to 1 hour; expired sessions fail closed |
| Devin-hosted session | [`scripts/agent-cloud-setup.sh`](../scripts/agent-cloud-setup.sh) with `GUARDIAN_AGENT_PROVIDER=devin`, Devin-scoped Cloudflare Access, and a JIT TokenRequest | `guardian-cloud-agent-devin`: Flux/Kargo/Flagger, workload, event, and log reads; no Secrets, sensitive cluster state, exec, port-forward, or writes | 10 minutes to 1 hour; expired sessions fail closed |
| Guardian-hosted agent VM | Native workload identity exchanged through declared OpenBao federation | Dedicated workload ServiceAccount with the least required role; use delivery-read for development | Workload-bound, automatically renewed |
| In-cluster agent workload | Projected Kubernetes token with an explicit audience | Dedicated workload ServiceAccount with the least required role | Pod-bound, automatically rotated |
| GitHub-hosted CI or untrusted external runner | No cluster credential; use Git/PR status or `aspect infra watch --mode=stream` | No cluster access | None |

## Keeping the standing read token alive

The `read` persona is the only rung that refreshes unattended, and it does so
only while it is used: Keycloak issues it as an offline token with a 30-day
idle window, so a month of silence closes the window and the next login needs a
browser. `tools/ops/workspace-watch install` arms a launchd agent that probes
the apiserver every 20 minutes — each probe resets the window — restarts the
mgmt tunnel when it is down, and re-mints the persona when the probe fails. It
notifies the operator only for the one step no daemon can perform: the device
approval.

The kubeconfig records the path of the kubelogin credential plugin, so that
path has to outlive the workspace that minted it. `aspect infra auth` installs
the shim under `~/.guardian/tools/bin` for exactly that reason: a
workspace-relative shim disappears with its worktree and takes standing cluster
access with it.

A launchd agent gets none of the Terminal's privacy grants, so if the checkout
lives under `~/Documents`, `~/Desktop`, or `~/Downloads`, macOS withholds it
from the agent: every git call fails with `Operation not permitted`. Grant Full
Disk Access to `/usr/bin/python3` (the interpreter named in the plist) once, or
keep the checkout outside those folders. `workspace-watch install` runs the
agent and reports which of the two you need before it claims success.
