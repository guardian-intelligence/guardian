# 01 — guestd

> **EPHEMERAL.** Delete with this directory when the implementation pass completes.

The agent inside every runner VM. It is the only process that talks to the
host. It starts a generic runner listener while the VM has no customer
volumes, then holds GitHub's synchronous job-start hook until the exact
assigned job's volumes are mounted and ready.

## Shape

- Static Go binary (`src/services/postflight/guestd/`), built like the other
  slim images (static-Go-on-empty doctrine; no TLS roots needed — vsock only).
- Started by systemd inside the guest (`guestd.service`, `Restart=no`:
  a dead guestd is a dead VM by design — hostd's probe fails and the slot is
  destroyed and refilled).
- Listens on vsock port 1 (guest CID assigned by hostd at launch; already in
  the QEMU golden argv). Wire format is `guestproto` JSON-lines, one
  connection at a time, host dials guest.

## Protocol (extends merged `hostd/guestproto`)

Protocol version 11 separates generic listener preparation from customer
rendezvous.

| Verb | Direction | Payload | Semantics |
| - | - | - | - |
| `hello` | guest→host on accept | guestd version, boot id | Liveness + identity probe |
| `prepare` | host→guest | member ID, JIT config blob, pool env | Idempotently starts a generic runner with no customer identity or volume attached. |
| `assignment` | guest→host | check-run ID, request ID, protocol job ID, runner and workflow identity | Published by Runner.Listener before Runner.Worker exists. |
| `rendezvous` | host→guest | member ID, immutable assignment ID, mounts, optional checkpoint | Converges the exact generation and restores or cold-falls back while Worker remains blocked. |
| `authorize` | host→guest | exact assignment identity and job env | Releases the Worker trampoline only after control-plane binding and rendezvous succeed. |
| `runner-status` | guest→host, streamed | lifecycle, restore outcome, timing, exit | hostd folds this into member and assignment reports. |
| `quiesce` | host→guest | mountpoints and checkpoint request | Checkpoints the allowlisted process capsule and flushes filesystems; reply `quiesced` or `quiesce-failed{reason}`. |

## Mount convergence (the invariant)

**No customer step runs until every mount in the rendezvous has converged.**
guestd locates each disk by its device serial (hostd sets `serial=` on the
`scsi-hd` device — the tracer recipe), creates the filesystem if the device
is blank (first generation of a scope arrives as an empty zvol), mounts with
the requested options **always including `discard`** (TRIM must pass through
to the sparse zvol or NVMe accounting measures garbage retention; the SNP
phase preserves it with `--allow-discards`), and only then releases the
blocked hook. A mount that cannot
converge within its deadline keeps Worker blocked. A recoverable process-only
failure tears down the partial process capsule, invalidates that artifact,
and cold-starts the same Worker. An integrity or unprovable-cleanup failure
requests VM recycling; the job is never started against a partial workspace.

Once every mount has converged, guestd writes
`.postflight-workspace` at the mounted workspace root and exposes its path
to the job as `POSTFLIGHT_WORKSPACE_READY_FILE`. That variable name is the
contract with the demo repo's checkout action (05), which hard-fails when
the variable or the file is absent — it proves the action is running on the
resolved Postflight mount rather than the image's underlying directory.

## Runner execution

- Runner tree is baked into the image at `/opt/actions-runner` (02).
- `prepare` writes only pool configuration to the runner's environment,
  drops privileges to the `runner` user, and execs
  `run.sh --jitconfig <blob>`.
- The baked `ACTIONS_RUNNER_HOOK_JOB_STARTED` script atomically reports the
  job identity to guestd, waits for `rendezvous`, imports its job env, and
  exits only after mounts and clock evidence are ready. GitHub cannot start
  the first customer step while this hook is blocked.
- The JIT config exists only in guest RAM and the runner's process
  environment. It is never written to any disk, including the workspace.
- Runner exit code is reported verbatim in `runner-status: exited`. Exit 0
  makes the workspace **seal-eligible**; it never decides promotion (GitHub's
  attempt-specific job conclusion does, control-plane side — see 04).

## Quiesce (why it exists)

The sequence is strict: runner exits → hostd requests checkpoint and flush →
guestd returns the process artifact → hostd destroys QEMU → hostd atomically
seals the workspace, tool, and process zvols. Any quiesce failure skips the
seal (ambiguity never promotes) and still destroys the VM.

## SNP seams

- Mount convergence converges each workspace zvol to an open LUKS2 mapper
  before the mount ladder (`volkey.go`): the volume key is the PSP-derived,
  measurement-bound key, the encryption mode is baked into the image, and a
  plaintext device presented under an encryption mode is refused. Built;
  activates when the golden image bakes `snp` mode and hostd launches SNP
  guests.
- `quiesce` syncs, closes the mapper, and unmounts before the host snapshots,
  so every snapshot is ciphertext at rest. Any ambiguity skips the seal.
- guestd retains the launch attestation report and surfaces it with the job
  record per the security model's evidence requirements.

## Testing

- Unit: protocol handling against the merged fake transport; mount
  convergence against a loopback block device with injected failures
  (blank device, wrong fs, busy unmount).
- Conformance (tracer host): real vsock end-to-end with 03's transport —
  prepare → registered → hook blocked → rendezvous → mounted → released →
  runner exit → quiesce → host snapshot succeeds and the snapshot mounts
  clean on the host.
