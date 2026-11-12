# Upstream Firecracker migration (in progress)

This development branch uses the official Firecracker and jailer v1.18.0 release binaries, verified against pinned archive checksums. The SDK is maintained in `vhive-serverless/firecracker-go-sdk`, branch `firecracker-v1.18.0`, revision `76af8abe770941a987e6071677b8228df75cdd86`. Custom drive overrides and source/restore vsock policies are no longer used.

## Snapshot contract

A coordinated pause freezes running workload tasks, closes host task admission, drains both directions of framed container I/O to matching acknowledged byte positions, and acknowledges lifecycle events only after host publication. Preparation has a 30-second maximum; backpressure fails capture explicitly and releases the preparation without discarding pending bytes. Already-paused tasks retain their state.

Upstream snapshot creation resets established vsock connections. Source resume reconnects control and certifies a fresh stream barrier before releasing read gates and workload tasks. Guest listeners and process FIFO ownership persist. Source input still unread in external host FIFOs remains source-only. Restored instances receive independent endpoints and session identities. The protocol covers coordinated resets with a live host shim, not exactly-once application effects across arbitrary host crashes.

Native artifact version 2 includes a sealed transport cut. Legacy vHive snapshots require a version 2 `<state-file>.drives.json` sidecar carrying original drive paths and the sealed cut. The sidecar must travel with the state and memory files. Older custom-VMM artifacts cannot supply this guarantee and are rejected. Legacy callers still use PauseVM/CreateSnapshot/ResumeVM; no native checkpoint endpoint is required by vHive.

Restored VMMs see independent writable backing files at the captured paths through private mount namespaces. Jailed restores use OCI mounts; unjailed restores use a private mount root. Dynamic unjailed attachments use detached open_tree/move_mount, requiring host support for those Linux APIs. Immutable PMEM mappings remain verified and read-only. Original source paths need not exist on the host.

## Evidence and outstanding gates

- Official release download/version verification and 81 passing top-level privileged SDK tests on the designated worker are recorded in `evidence/upstream-migration`; the existing SDK metrics test is skipped.
- Development namespace tests passed on the worker: independent roots, missing source paths, read-only backing, dynamic disk attachments, and an official-VMM preboot API probe. These are component tests, not guest snapshot/restore acceptance.
- Local root-disabled race suites passed for `internal/vm`, `eventbridge`, `agent`, and `runtime`; all parent-module packages compile.
- Full committed runtime build, VM snapshot/restore and vHive acceptance remain pending. Existing historical receipts refer to their original revisions, not this migration.
- Step 3 CRI remains WIP. Kubernetes Pod checkpoint/restore endpoints remain deferred to the agreed 1.38 work.
- Milestone history reconstruction, final branch publication, and obsolete branch cleanup remain pending.
