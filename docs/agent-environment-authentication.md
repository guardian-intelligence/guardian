# Agent environment authentication

| Environment                                      | Authentication                                                                 | Identity and authority                                                                                                                                                                              | Lifetime                                                                                            |
| ------------------------------------------------ | ------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| Local agent, routine read and product operations | `aspect infra auth --persona=read` with browser/device login                   | `#platform-agent`: cluster read and port-forward; may derive 15-minute, product-scoped capabilities such as `guardian-mythra-observer` and `guardian-mythra-operator`; no Secrets or general writes | Unattended `offline_access` with a 30-day idle window; derived capabilities expire after 15 minutes |
| Local agent, routine repair                      | `aspect infra auth --persona=write-basic` with fresh browser/device approval   | `#platform-write-basic`: read, port-forward, delete pods/jobs, scale workloads, and mint scoped writer tokens                                                                                       | Keycloak session; no `offline_access`                                                               |
| Local agent, emergency                           | `aspect infra auth --persona=write-all` with fresh browser/device approval     | `#platform-write-all`: cluster-admin                                                                                                                                                                | Keycloak session; no `offline_access`                                                               |
| Local agent, Keycloak unavailable                | `aspect infra auth --persona=root --reason "<why>"` from custody               | Audited, paging x509 breakglass: cluster-admin                                                                                                                                                      | Short certificate lifetime                                                                          |
| Guardian-hosted agent VM                         | Native workload identity exchanged through declared OpenBao federation         | Dedicated workload ServiceAccount with the least required role                                                                                                                                      | Workload-bound, automatically renewed                                                               |
| In-cluster agent workload                        | Projected Kubernetes token with an explicit audience                           | Dedicated workload ServiceAccount with the least required role                                                                                                                                      | Pod-bound, automatically rotated                                                                    |
| GitHub-hosted CI or untrusted external runner    | No cluster credential; use Git/PR status or `aspect infra watch --mode=stream` | No cluster access                                                                                                                                                                                   | None                                                                                                |

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

That shim is a **copy** of the binary, not a symlink into Bazel's output tree,
because the HOME location alone does not buy the lifetime it looks like it
does. Bazel derives `output_base` from a hash of the workspace path, so a
worktree owns its own and takes any symlink target with it when it is removed;
`bazel clean --expunge` and external-repo eviction do the same to any
workspace. The resulting failure reads as a puzzle — the dangling link still
lists in the directory, so every cluster call dies with

```
exec: fork/exec /home/…/.guardian/tools/bin/kubectl-oidc_login: no such file or directory
```

for a file that is plainly there. If you hit it on an older checkout, `aspect
tools install` repairs the copy in place; it only ever refreshes a shim that
already exists, since minting the credential plugin is `aspect infra auth`'s
job.

A launchd agent gets none of the Terminal's privacy grants, so if the checkout
lives under `~/Documents`, `~/Desktop`, or `~/Downloads`, macOS withholds it
from the agent: every git call fails with `Operation not permitted`. Grant Full
Disk Access to `/usr/bin/python3` (the interpreter named in the plist) once, or
keep the checkout outside those folders. `workspace-watch install` runs the
agent and reports which of the two you need before it claims success.
