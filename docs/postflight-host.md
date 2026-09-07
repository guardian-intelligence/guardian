# Postflight host

Status: current first-party Turbo host, 2026-09-06.

The worker host runs hostd, QEMU/KVM, and local encrypted ZFS. Turbo trusts
the host; Confidential keeps a separate image, launch, and key profile.
See the [security model](postflight-security-model.md) for the distinction.

## Development platforms

Postflight's production substrate is Linux amd64; its policy, protocol, and
orchestration packages support native development on macOS. The ordinary
developer loop stays Bazel-native:

```sh
bazelisk build //src/postflight/...
bazelisk test //src/postflight/...
```

The split is explicit rather than capability-skipping:

- portable sources and fake-backed orchestration tests compile and execute on
  every development host;
- Linux syscall adapters live in `*_linux.go` files;
- non-Linux counterparts fail with checkable unsupported errors, and native
  tests assert those errors instead of pretending the privileged operation
  succeeded;
- the public `hostd` and `guestd` Bazel labels are `go_cross_binary` Linux
  artifacts, so building an image on macOS cannot install a Darwin binary;
- the control-plane image likewise consumes explicit Linux cross-binaries.

Go build constraints choose implementation files inside a target. The Bazel
test target itself remains compatible and executes on macOS; this is not a
`target_compatible_with` exclusion hidden by a `//...` wildcard. A green macOS
run proves the portable state machines and the compilation of the production
Linux binaries. Kernel conformance for KVM, AF_VSOCK, systemd, mount, CRIU, and
ZFS still requires the Linux host suite described by the golden-image verify
procedure; macOS never substitutes an approximation for that evidence.

## Host substrate and reconciliation

The [host manifest and reconciler](../src/postflight/host/README.md) declare
two 4-vCPU/16-GiB slots on `rust-forge-01`, pinned QEMU 8.2.2, SeaBIOS, the
384-GiB file-backed pool, and its encrypted `postflight` child. Secrets are
root-only files outside Git. A systemd timer follows protected public main
from root-owned source and build locations; unrelated commits reuse artifacts.
Storage imports and unlocks before hostd. Guests use a filtered TAP bridge
with DHCP/DNS and public egress; host access is limited to DHCP, DNS, and
checkout. Existing nftables tables are preserved.

Hostd dials the control plane using the bounded authenticated
[JSON sync contract](../src/postflight/hostd/syncproto). It owns pool/member
reconciliation, plans, assignment rendezvous, ZFS materialization/sealing,
VM adoption, and lifecycle telemetry. This runtime does not implement the
older proposed pair of protobuf/gRPC streams.

## QEMU and boot templates

The [launch spec](../src/postflight/hostd/vm/spec.go) gives Turbo a
`pc-q35-8.2` KVM machine and host CPU model. QEMU drops to the system account
`postflight-vm`; a root-owned helper creates its TAP interface. Detached
systemd scopes let hostd restart and adopt active VMs. Tenant disks arrive by
stable serial using virtio-scsi hot-attach. One guest executes one job.

When `HOSTD_WARM_TEMPLATE_DIR` is configured, hostd builds or verifies its
[generic RAM/root template](../src/postflight/hostd/vm/warm_template.go) before
starting the agent. Only a never-registered, unassigned guest with no tenant
volumes qualifies as donor. Paused memory and root state are coupled after
donor destruction. Restores are bound to exact host-boot/runtime inputs;
Confidential cannot select this path. Memory resides at
`/var/lib/postflight/warm-templates`; `/var/lib/postflight/templates` is
reserved for the ZFS dataset and must not hide the memory files.

## The guest contract

[guestd](../src/postflight/guestd/guestd.go) receives bounded vsock messages.
A restored Turbo VM refreshes its entropy, clock, machine identity, network
MAC, and DHCP state before receiving fresh GitHub JIT registration. Guestd
supervises the patched Listener, observes assignment before Worker starts,
converges storage mounts, and releases Worker only after rendezvous succeeds.
The Listener/Worker retain GitHub's native assignment and live-log protocol.

Turbo images bake `host-zfs`: ext4 workspace/tool devices rely on native host
ZFS encryption. Hostd checks encryption and loaded keys recursively before
serving. No SNP device is required, no per-lineage key is sent to the Turbo
guest, and the trusted host can see plaintext while storage is unlocked.
Confidential images retain their SNP-derived LUKS path; a host flag cannot
downgrade that baked image mode.

Customer process checkpoint restore and publication remain disabled.
Completion flushes durable filesystems and destroys QEMU before sealing disk
generations; a generic RAM donor never contains runner or customer state.
See the [runner lifecycle](postflight-runner-lifecycle.md) for the ordered
publication and failure behavior, and [image verification](../src/postflight/image/README.md)
for the Linux conformance boundary. Portable tests prove policy, not KVM
migration, real guest identity renewal, or ZFS cache reuse.
