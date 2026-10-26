# First annual control-plane conversion

This procedure is the first stage of reducing Latitude to the two existing
prepaid annual servers plus ash-water monthly. Project moves are not required.
The source changes describe a target; they do not prove it has been applied.

## Scope and identities

| Item                                        | Identity                               |
| ------------------------------------------- | -------------------------------------- |
| First annual                                | ash-worker0 / sv_EvjLaBxRQNoqy         |
| Public/private                              | 206.223.228.99 / 10.8.0.14 on VLAN2140 |
| System SSD that may be reset after approval | 362510FCEFF6                           |
| Preserved WUM data SSD                      | 362510FCEFD5                           |
| Private NIC permanent MAC                   | 90:5a:08:33:bb:14 / enp1s0f0           |
| Public NIC permanent MAC                    | 90:5a:08:33:bb:15 / enp1s0f1           |

NVMe enumeration is not identity. Resolve and assert both serials and NIC MACs
immediately before any destructive or network action. Keep the current physical
interface names and addresses; add only the control-plane VIP on enp1s0f0.2140.

## Required proof before cutover approval

- Independently verified HTML5 console on this exact server, with no host input
  needed for the proof. An authenticated BMC page alone is insufficient.
- Complete candidate composed from the reviewed declarations and existing
  current cluster genesis, strictly validated in memory. No keys or plaintext
  configuration in Git, Mac files, command arguments or logs. The standard Talm
  bundle renderer also creates an unused operator client certificate; do not
  invoke credential-generating tooling without its applicable approval.
- Current worker pod/mount/volume inventory and preserved-data hashes match the
  retained recovery evidence. WUM remains paused and its second SSD intact.
- Three healthy existing monthly voting members, fresh recovery snapshot,
  all-resource storage quorum/generation/local-state checks, complete passing
  exact-route coverage and no new alerts. Completed restore drills are reused.
- The current worker configuration is recoverable from current genesis and
  declared source; account for any live source drift before replacing STATE.
- Verify the pinned v1.13.10 installer, ISO, enrollment files, signer and public
  key fingerprints in annual-controlplane-assets.yaml. Existing monthly pins
  remain unchanged. No downgrade to the older worker asset declaration.

## Bounded operation requiring exact approval

The approval must name sv_EvjLaBxRQNoqy and system SSD 362510FCEFF6, the temporary
worker evacuation/reboots, mounting the pinned public recovery ISO, Sidero trust
enrollment and Secure Boot enablement, STATE/EPHEMERAL reset, and the machine
identity/certificates required to join this existing cluster. It must expressly
preserve data SSD 362510FCEFD5 and all three existing monthly voters. Obtain
approval only once the exact boot/recovery procedure and complete config have
passed review. No server termination belongs in this approval.

The normal signed ISO profile is the recovery path. The ISO and installed UKIs
also contain destructive reset profiles. Never select a reset profile casually
or use a full-disk/reset-user-disk command in this stage. STATE and EPHEMERAL
must become LUKS2 protected by TPM, Secure Boot/PCR7 and the declared signed PCR11
policy; changing a YAML encryption field does not encrypt the current XFS data
in place. Firmware trust changes may prevent the old installation from booting.

Apply only the reviewed, merged node declarations plus the overlay. Confirm
that no worker registration flags, UserVolumeConfig or RawVolumeConfig survive
composition. Removing a UserVolumeConfig unmounts it without deleting its
partition data; never follow removal with a wipe or drop-partition operation.
See the [Talos user-volume removal contract](https://docs.siderolabs.com/talos/v1.13/configure-your-talos-cluster/storage-and-disk-management/disk-management/user#remove-user-volumes).
Do not bootstrap a new cluster. All three monthly voters remain
online while the annual member joins, ending with four voters and quorum three.

## First-stage admission and recovery

The source deliberately sets guardian.dev/openbao-static-seal=false. Do not make
this destination eligible for OpenBao until its existing seal key has been
installed and independently verified. This stage declares no LINSTOR data pool
and must not allocate anything on the preserved WUM data SSD.

Require authenticated Talos running/ready state, Secure Boot/UKI/module-signature
proof, TPM-encrypted STATE and EPHEMERAL, four healthy voting etcd members with
no alarms, Kubernetes/CNI/DNS health, storage quorum and exact-route/alert
observation before continuing. A failed gate stops subsequent changes. Recover
only this annual machine; do not remove a monthly member, force storage resync,
bootstrap another cluster or restore stale etcd over the live quorum.

The second annual guardian-w1 conversion and the later annual data-SSD reclamation
are separate stages. guardian-w1 requires a proven console, current Ubuntu
preservation and a reviewed cross-project VLAN attachment. Only after both annual
control planes and storage/local-state migration are healthy may earth and wind
be drained and removed sequentially, then separately approved for termination.
