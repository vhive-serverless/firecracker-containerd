# Modernization acceptance ledger

Step 1 has passed worker acceptance from a committed checkout. See `step-1.md`
for the tested revision, pushed dependency references, and evidence. Each later
branch starts at the validated completion commit of its prerequisite.

## Initial audit

- [x] Read the requested specification and worker.txt; check ancestor/repository instructions (no AGENTS.md found).
- [x] Inventory local Git status, remotes, branches, module declarations, submodule pins and build inputs in versions.json.
- [x] Verify worker SSH, sudo, KVM, CPU/memory, disk backing, cgroup v2 and existing workloads.
- [x] Locate both vHive branches in firecracker-containerd and inspect changed-file scope.
- [x] Inspect SDK feat/update-firecracker-1.14 history; it descends from current SDK main and contains API regeneration, helpers and jailer fixes.
- [x] Resolve upstream Firecracker v1.18.0 tag and peeled commit.
- [x] Reproduce baseline build, installation and container operations on worker; save existing failures separately.
- [x] Inspect actual baseline kernel/rootfs/binary versions and guest mounts on worker.
- [x] Complete host-to-guest-to-container storage trace, including volumes and stargz.
- [x] Pin compatible target revisions and reproducible build/artifact configurations.

## Step 1: modernize-runtime

- [x] Create runtime branch from master be68640 (current upstream dependency and security fixes; vHive changes ported in Step 2).
- [x] Preserve relevant SDK 1.14 work and regenerate from exact v1.18.0 Swagger using a pinned generator.
- [x] Update SDK defaults, API helpers, validation/error handling and tests; push SDK commit before runtime reference.
- [x] Select supported containerd 2.x; migrate imports, plugins, config, task/events, clients and snapshotters.
- [x] Update actual runc binary and module together; update guest agent and host shim/control layer.
- [x] Inspect Amazon Linux kernel provenance; update source/artifact path, kernel config, boot args, init, virtio/vsock, filesystems and security features.
- [x] Update guest rootfs with reproducible provenance, preserving baseline storage behavior.
- [x] Validate cgroup v2 host and guest limits, accounting, OOM and cleanup.
- [x] Update CNI/plugin binaries, tc-redirect-tap and network dependencies.
- [x] Update devmapper/thin-pool, retained stargz, drive mapping and volumes.
- [x] Update toolchains, builders, system libraries, protobuf/ttrpc generation and CI.
- [x] Reconcile module pins, submodules, downloaded tests and docs; audit jailer and recovery paths.
- [x] Worker clean build/install; image pull; create/start/exec/stop/delete OCI containers in Firecracker.
- [x] Worker networking, filesystem writes, supported volumes, logs, signals and resource limits.
- [x] Worker repeated operations and representative failures without resource leaks; relevant tests pass.
- [x] Commit/push all repositories; validate committed checkout without untracked state; capture exact versions/commands/paths and limitations.

## Step 2: vhive-compatible

- [x] Create branch from validated Step 1 completion commit.
- [x] Inspect integration behavior/dependencies, port SDK/runtime/network/storage/snapshot changes without downgrades.
- [x] Pin and deploy supported vHive on worker.
- [x] Demonstrate function deploy/invoke/repeated invoke/cleanup and applicable snapshot/restore.
- [x] Provide end-to-end regression script and rerun Step 1 acceptance.
- [x] Push compatible dependency/runtime commits, validate independent committed reconstruction and report revisions/commands/results/limitations.

## Step 3: cri-native-snapshot-restore — WIP

- [x] Create branch from validated Step 2 completion commit.
- [x] Independently validate a released upstream v1.37.2 single-node stock-runtime baseline: Pods, exec, logs, filesystem writes, DNS and API discovery; record that this does not satisfy the Firecracker or Pod restore gates below.
- [x] Validate ordinary Firecracker RuntimeClass Pods on required k3s v1.37.2+k3s1 from a clean deployment checkout: two containers in one VMM, shared/projected volumes, exec/logs, exact IPv4 DNS answer, Service networking from a runc Pod, positive container statistics and cleanup; receipt 1085202. This does not satisfy the remaining CRI or restore gates.
- [ ] Verify installed k3s v1.37.2+k3s1 binary/component versions and actual Pod checkpoint/restore alpha APIs/gates/RPCs; select newer compatible release only if required.
- [ ] Define Pod-to-VM mapping; reuse containerd CRI for sandbox/container lifecycle with Firecracker integration.
- [ ] Connect supported Kubernetes Pod checkpoint/restore and applicable CheckpointPod/RestorePod CRI RPCs to native VM implementation.
- [ ] Capture memory, VMM/device state, independent consistent writable disks and complete runtime metadata; quiesce/flush/copy in documented order.
- [ ] RuntimeClass Pods and applicable CRI conformance tests; unresolved failures qualify compliance claims.
- [ ] Kubernetes-initiated checkpoint and restore into a new VMM preserves application memory and filesystem state.
- [ ] Exec/logs/network/statistics/normal lifecycle after restore.
- [ ] Source can continue after checkpoint without changing snapshot disk state; restored writable backings are isolated.
- [ ] Repeated/interrupted checkpoint/restore and cleanup; separate runtime test restores after original VMM termination.
- [ ] Document identity, networking, same-node/cross-node and alpha limitations.
- [ ] Commit/push implementation, manifests/gates/tests, dependency pins and independent reproduction procedure; report outcome/revisions/results.

## Step 4: damon-virtio-pmem (in progress; Step 3 remains WIP)

- [x] Create branch from pinned Step 3 WIP commit `26ceead`; user explicitly authorized this ordering change on 2026-11-11. Scope remains writable storage and memory reduction; inherited restore gaps remain visible.
- [x] Read arXiv 2609.22978; verify running host/guest/container storage before changes (f52d26c audit).
- [x] Private disk-backed ext4 guest upper/work; immutable base; container writable roots already ext4; explicitly ephemeral tmpfs retained. Clean storage/capture component validation at 8f8d00f.
- [x] Host disk/device/mount inspection, bounded capacity/reservation, jail ownership, cleanup, and real host/guest ENOSPC checks passed at 8f8d00f.
- [x] Independently configurable virtio-pmem/DAX and DAMON plus actual balloon free-page reporting; component acceptance at 020f85b, 9706231 and 438c2cb records pinned VMM/SDK/kernel support and deviations.
- [x] Account for pmem metadata, cold accesses, filesystem/DAX compatibility and explicit immutable sharing; matching ext4 block controls and measured configuration costs are retained in the 0a268c5 evaluation.
- [x] Integrate consistent disks with memory/VMM snapshots; quiesce/flush/snapshot/restore ordering, private copied writable disks, full cloned-upper reservation and actual VMM-open inode checks passed clean 72f4fc9 across six storage profiles and both jailers.
- [x] Worker write/copy-up/rename/delete/writeback/exhaustion tests and idle-reclaim/reactivation correctness passed in storage/mechanism acceptance and all 21 controlled trials.
- [x] Worker direct runtime restore/lifecycle checks passed at 72f4fc9: fourteen cases, original VMM terminated, memory/filesystem state, matched logs, exec/stats/kill/wait/delete and six actual post-restore reporting/reuse checks.
- [x] Ordinary Kubernetes Pod lifecycle passed all four disk/pmem/reclaim/combined profiles with clean 06936f6 harness and pinned 72f4fc9 runtime image; raw evidence and cleanup verified. See step-4.md.
- [ ] Full inherited Step 3 integration acceptance: CRI failures and Kubernetes Pod checkpoint/restore remain explicitly WIP; direct runtime tests do not satisfy those gates.
- [x] Run identical workloads/resources for original baseline, disk baseline, pmem only, reclamation only and combined, plus matching ext4 block controls: clean 0a268c5, 21 trials, three randomized blocks.
- [x] Measure peak/integrated host memory, guest anonymous/page cache, shared backing, CPU, faults/I/O, disk, latency and correctness; separate accounting domains and retain missing metrics and reporting-counter freshness limitations.
- [x] Commit/push evaluation scripts/config/results; frozen 0a268c5 observations, strict summaries, independently checked integrals, plots, commands and limitations are preserved. Runtime restore acceptance remains a separate gate above.
