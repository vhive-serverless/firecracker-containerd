# Upstream Firecracker migration (in progress)

## Current status (2026-11-13)

The runtime development branch is `work/upstream-firecracker`; the reviewed
implementation head is `4f43f8d`. It uses **official, unmodified Firecracker and
jailer v1.18.0 release binaries**, downloaded and verified against pinned archive
checksums. The SDK is published in `vhive-serverless/firecracker-go-sdk`, branch
`firecracker-v1.18.0`, revision
`76af8abe770941a987e6071677b8228df75cdd86`. See
[selected inputs](versions.json) and the
[official-release worker receipt](evidence/upstream-migration/official-release-worker.txt).
Custom drive overrides and source/restore vsock policies are no longer used.

Private disk namespaces and resumable control, events and I/O were integrated in
`1de30c6`. **Full VM snapshot/restore, continuing-source and vHive acceptance of
the migrated runtime remain pending.** The designated worker build is pinned to
`023283d` and is creating guest images; it does not yet validate later commits.
The worker log is
`upstream-migration/evidence/runtime-build-023283d.log` in the worker workspace.
Later changes fix embedded runc revision metadata (`8559ebe`), strengthen and
enable interrupted-capture recovery acceptance (`97cd1e2`, `ea6738b`), and remove
custom-VMM toolchain provisioning (`4f43f8d`); those changes still need the
appropriate committed worker validation.

Step 1/2 branch renames, reconstruction of clean sequential milestone histories,
final runtime branch publication and obsolete branch cleanup are planned, not
yet published. Existing milestone documents and receipts retain their original
revisions and dependency selections as historical evidence. Step 3 CRI
compliance remains WIP. Kubernetes Pod checkpoint/restore endpoints are deferred
to the agreed 1.38 interface work, outside this migration goal; that deferral
does not close the CRI compliance gaps.

## Snapshot contract

The following describes the implemented contract, not completed VM acceptance.
Coordinated preparation closes host task admission, freezes running workload
tasks, drains both directions of framed container I/O to matching acknowledged
byte positions, and acknowledges lifecycle events only after host publication.
The I/O/event preparation phase has a 30-second timeout; unresolved backpressure
fails capture explicitly, with cleanup releasing preparation without discarding
pending bytes. Already-paused tasks retain their state. The timeout is not a
bound on the complete disk and memory capture.

Upstream snapshot creation resets established vsock connections. Source resume
reconnects control and certifies a fresh stream barrier before releasing read
gates and workload tasks. Guest listeners and process FIFO ownership persist.
Source input still unread in external host FIFOs remains source-only. Restored
instances receive independent endpoints and session identities. The protocol
covers coordinated resets with a live host shim, not exactly-once application
effects across arbitrary host crashes.

Native artifact version 2 includes a sealed **transport cut version 1**, containing
process stream identities, byte/FIN cursors, the event publication cursor and
the tasks to resume. Legacy vHive snapshots require a version 2
`<state-file>.drives.json` sidecar carrying original drive paths and the same
version 1 cut. The sidecar must travel with the state and memory files. Older
custom-VMM artifacts without this metadata are rejected. Legacy callers still
use PauseVM/CreateSnapshot/ResumeVM; no native checkpoint endpoint is required
by vHive. The implementation is in
[checkpoint transport](../../runtime/checkpoint_transport.go),
[legacy pause/recovery](../../runtime/legacy_checkpoint_transport.go),
[control reconnection](../../runtime/agent_session.go), and
[framed I/O](../../internal/vm/transport_io.go), with publication receipts in the
[event journal](../../eventbridge/journal.go).

Filesystem snapshots and VM snapshots serve different purposes. Devmapper
provides the default ext4 container writable device; a Firecracker state/memory
snapshot does not capture that device's contents. Native runtime capture copies
complete attached disk contents plus exported-volume state into its artifact,
then verifies and copies artifact files for each restore. Those writable restore
files are private even if the source was a devmapper device. vHive instead owns
its workload ext4 image capture and restoration into a new devmapper device;
the runtime maps the supplied device. External host writers to exported volumes
must be quiescent separately. The guest overlay upper, container root,
exported volumes and optional stargz store remain distinct storage layers; see
the [architecture](../architecture.md#filesystems-and-vm-snapshots).

Restored VMMs see their selected backing at the captured paths through private
mount namespaces. Jailed restores use OCI mounts; unjailed restores use a private
mount root. Dynamic unjailed attachments use detached open_tree/move_mount,
requiring host support for those Linux APIs. Immutable PMEM mappings remain
verified and read-only. Original source paths need not exist on the host. This
mapping preserves the paths expected by upstream Firecracker without changing
host-global paths; writable-disk independence also requires the per-restore
copies or devices described above. See
[restore mount preparation](../../runtime/restore_mounts.go) and
[unjailed namespaces](../../runtime/restore_namespace.go).

## Evidence and outstanding gates

- The [SDK worker summary](evidence/upstream-migration/sdk-76af8ab-worker.json)
  and [raw log](evidence/upstream-migration/sdk-76af8ab-worker.log) record 81
  passing top-level privileged tests at exact SDK revision `76af8ab`, with the
  official release VMM/jailer. The existing SDK metrics test is skipped. The
  reused test environment is not runtime acceptance.
- The [namespace component receipt](evidence/upstream-migration/namespace-dev.json)
  records independent roots, missing source paths, read-only backing, dynamic
  disk attachments and an official-VMM preboot API probe. Its source archive and
  updated-file hashes identify a development test; it is not a committed guest
  boot or snapshot/restore acceptance run.
- Recorded local root-disabled race suites passed for `internal/vm`,
  `eventbridge`, `agent` and `runtime`; parent-module packages compiled. These
  component checks do not establish KVM behavior, disk consistency or transport
  correctness across a real upstream snapshot reset.
- The committed worker build and runtime acceptance remain open. Required VM
  evidence includes a continuing source, independent restored disks and VMs,
  reconnecting control, lifecycle events and stdin/stdout/stderr across capture,
  plus recovery after interrupted capture. Older tests that stop the source
  before restoring a single clone do not establish these properties. Both
  jailer modes and the separate legacy vHive path need their scoped validation.
- Historical receipts, including Step 4's `06b55e3` results, remain evidence only
  for their named revisions and original custom-stack inputs. They must not be
  presented as acceptance of the current upstream migration.
