# Upstream Firecracker migration (in progress)

## Current status (2026-11-13)

The runtime development branch is `work/upstream-firecracker`; the reviewed
implementation head is `c4af54e` (diagnostic evidence through `f35bbf4`). It uses **official, unmodified Firecracker and
jailer v1.18.0 release binaries**, downloaded and verified against pinned archive
checksums. The SDK is published in `vhive-serverless/firecracker-go-sdk`, branch
`firecracker-v1.18.0`, revision
`dd9db322432989b96cff7a38a64703d60b6cabf9`. See
[selected inputs](versions.json) and the
[official-release worker receipt](evidence/upstream-migration/official-release-worker.txt).
Custom drive overrides and source/restore vsock policies are no longer used.

Private disk namespaces and resumable control, events and I/O were integrated in
`1de30c6`. Clean worker build `57b93fc` completed with the official release
binaries and pinned runc metadata. Subsequent host fixes restored source stream
reattachment (`b526339`) and corrected existing-exec identity validation
(`59dddca`, `19599d0`). Mixed-revision diagnostics now pass basic restore,
live-source/two-clone transport and storage isolation, and interrupted-capture
recovery in both jail modes. These diagnostics use guest/daemon `57b93fc` and
newer host shims; Native runtime acceptance subsequently passed all 20 VM cases on the complete
`fc2a8c2` build. **Rewritten milestone acceptance and separate legacy vHive
acceptance remain pending**. See the scoped receipts below.

Step 1/2 branch renames, reconstruction of clean sequential milestone histories,
final runtime branch publication and obsolete branch cleanup are planned, not
yet published. Existing milestone documents and receipts retain their original
revisions and dependency selections as historical evidence. Step 3 CRI
compliance remains WIP. Kubernetes Pod checkpoint/restore endpoints are deferred
to the agreed 1.38 interface work, outside this migration goal; that deferral
does not close the CRI compliance gaps.

Runtime VM tests at `023283d` and `57b93fc` failed during source recovery;
those failures remain recorded. SDK `dd9db32` fixes a separately reproduced
handshake bug where a buffered acknowledgement read discarded the start of an
application frame. It passed 86 top-level privileged SDK tests, with one existing
metrics-test skip. That SDK correction alone did not resolve source recovery;
the later runtime reattachment fix was also required.

## Snapshot contract

The following describes the implemented contract. Diagnostic coverage is scoped
below; rewritten milestone and legacy vHive acceptance remain incomplete.
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

- [Single-revision native acceptance](evidence/upstream-migration/native-fc2a8c2.json)
  passed all 20 expected VM cases at `fc2a8c2`, with no failed or skipped cases:
  fork transport (2), interrupted capture (2), basic restore (2), six storage
  profiles in both jail modes (12), and reporting disabled/enabled (2). The
  [raw script log](evidence/upstream-migration/native-fc2a8c2.log) includes the
  completion marker; receipts record exact source, binaries and test image.
  This does not establish legacy vHive or CRI compliance acceptance.
- [Complete stack build](evidence/upstream-migration/build-fc2a8c2.json) at
  `fc2a8c2` passed full lint and build with official Firecracker/jailer and
  correctly embedded runc/runtime revisions. The
  [binary receipt](evidence/upstream-migration/build-fc2a8c2-receipt.log) records
  exact hashes. Native VM acceptance at this revision also passed, as below.
- The [current SDK worker summary](evidence/upstream-migration/sdk-dd9db32-worker.json)
  and [raw log](evidence/upstream-migration/sdk-dd9db32-worker.log) record 86
  passing top-level privileged tests at SDK `dd9db32`, with one existing skip.
  The older 81-test receipt applies only to `76af8ab`.
- [Basic restore](evidence/upstream-migration/native-restore-b526339-host-diagnostic.json)
  passed both jail modes using host `b526339` and guest/daemon `57b93fc`.
- [Fork transport](evidence/upstream-migration/native-fork-c4af54e-host-diagnostic.json)
  passed both jail modes in 79.32 seconds: continuing source and two clones,
  pre-existing execs, numbered bidirectional streams, independent writable disks
  and volumes, exit events, stdin closure and output EOF. Test source `c4af54e`
  uses host `19599d0` and guest/daemon `57b93fc`. The test uses containerd's
  standard CloseIO call to release the guest shim's retained stdin writer.
- [Interrupted capture](evidence/upstream-migration/native-interrupt-c4af54e-host-diagnostic.json)
  passed both jail modes in 51.07 seconds with the same mixed revisions. It
  checks source continuity after interruption and subsequent capture/restore.
  These three runtime results are diagnostics, not final full-stack acceptance.
- The [namespace component receipt](evidence/upstream-migration/namespace-dev.json)
  records independent roots, missing source paths, read-only backing, dynamic
  disk attachments and an official-VMM preboot API probe. Its source archive and
  updated-file hashes identify a development test; it is not a committed guest
  boot or snapshot/restore acceptance run.
- Recorded local root-disabled race suites passed for `internal/vm`,
  `eventbridge`, `agent` and `runtime`; parent-module packages compiled. These
  component checks do not establish KVM behavior, disk consistency or transport
  correctness across a real upstream snapshot reset.
- Fresh acceptance of the rewritten milestone commits remains open. Required VM
  evidence includes a continuing source, independent restored disks and VMs,
  reconnecting control, lifecycle events and stdin/stdout/stderr across capture,
  plus recovery after interrupted capture. Older tests that stop the source
  before restoring a single clone do not establish these properties. Both
  jailer modes and the separate legacy vHive path need their scoped validation.
- Historical receipts, including Step 4's `06b55e3` results, remain evidence only
  for their named revisions and original custom-stack inputs. They must not be
  presented as acceptance of the current upstream migration.
