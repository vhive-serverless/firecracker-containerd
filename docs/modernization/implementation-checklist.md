# Modernization acceptance ledger

## Current corrected Step 2 check coverage

Exact `2e03843816cf95d184ad4ad429adf6ea2ccc1412` has complete required check
coverage across separately recorded runs. The original full-stack run remains
exit 2 on an unfixed existing CNI chain race (40 top-level passes, one failure).
Three unchanged fresh-container repeats retained five concurrent VMs and passed;
the remaining original thinpool/SDK scripts passed separately (86 SDK passes,
one metrics skip). The three provisional vHive VM cases also passed.
The [combined receipt](evidence/upstream-step2-2e03843816cf/README.md) maps every
required check and exact artifact/image/cleanup identity. No criterion was
weakened and no clean uninterrupted full-run marker is claimed. This later
documentation/assembled tip was not tested; final published runtime and
installed-vHive consumer acceptance remains separate. Later CRI/native gates are not
accepted by this Step 2 result.

## Earlier clean Step 2 acceptance at 7386

The maintained base is `firecracker-v1.18-containerdv2`; its vHive successor is
`firecracker-v1.18-containerdv2-vhive-integration`. Official Firecracker/jailer
1.18.0 binaries and the published vHive SDK dd9db32 are selected in the current
lock and module manifests. Clean source
`7386ab5c5e8c3519ad1698c27f548803e714e304` passed build/private installation,
the full stack and three provisional vHive source/clone cases. The
[current receipt](evidence/upstream-step2-worker-attempts/README.md) records
127 full-stack top-level passes, the SDK metrics skip, eight nested StopVM
skips, exact artifacts and failed prior attempts. This documentation tip was
not rebuilt. Final published-module and installed-vHive consumer acceptance
remain pending; source acceptance does not establish those consumer gates.

The inherited Step 1 lint/build/install/test-stack pipeline and separate
installed-prefix lifecycle/controller-restart/shim-crash checks passed at
`1074bdc8f2b6`, with service and thinpool cleanup verified. Its
[receipt and limitations](evidence/upstream-step1-1074bdc8f2b6/README.md) retain
the SDK metrics skip and installed-versus-final-test rootfs distinction; the
[local validation note](upstream-step1-local-validation.md) records earlier
compilation checks. These results concern that exact Step 1 source.

The checklist below preserves the original modernization ledger. Its checked
items, former branch names, planned later steps and acceptance statements
belong to that historical source, not the migrated candidates. See the
[current branch guide](README.md) and [Step 2 procedure](step-2.md) for current
inputs and validation gates. Step 3 remains WIP; Kubernetes Pod restore is
deferred to the 1.38 interface work.

## Historical initial audit

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

## Historical Step 1: modernize-runtime

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

## Historical Step 2: vhive-compatible (not started)

- [ ] Create branch from validated Step 1 completion commit.
- [ ] Inspect integration behavior/dependencies, port SDK/runtime/network/storage/snapshot changes without downgrades.
- [ ] Pin and deploy supported vHive on worker.
- [ ] Demonstrate function deploy/invoke/repeated invoke/cleanup and applicable snapshot/restore.
- [ ] Provide end-to-end regression script and rerun Step 1 acceptance.
- [ ] Push compatible dependency/runtime commits, validate independent committed reconstruction and report revisions/commands/results/limitations.

## Historical Step 3: cri-native-snapshot-restore (not started)

- [ ] Create branch from validated Step 2 completion commit.
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

## Historical Step 4: damon-virtio-pmem (not started)

- [ ] Create branch from validated Step 3 completion commit; scope limited to writable storage and memory reduction.
- [ ] Read arXiv 2609.22978; verify current running host/guest/container storage and persistence boundaries before changes.
- [ ] Disk-backed per-VM persistent guest/container writes; immutable base; ext4 upperdir/workdir where OverlayFS retained; only explicitly ephemeral tmpfs.
- [ ] Verify host disk backing (no tmpfs), devices and mounts; capacity, allocation, ownership, cleanup and ENOSPC handling.
- [ ] Independently configurable virtio-pmem/DAX and DAMON plus actual balloon free-page reporting; verify/implement VMM/SDK/kernel support and document deviations.
- [ ] Account for pmem metadata, cold accesses, filesystem/DAX compatibility and explicit immutable sharing; never share writable state.
- [ ] Integrate consistent disks with memory/VMM snapshots; quiesce/flush/snapshot/restore ordering, no advanced disks or writable sharing.
- [ ] Worker write/copy-up/rename/delete/writeback/exhaustion tests and idle-reclaim/reactivation correctness.
- [ ] Worker Step 3 lifecycle/snapshot suite for supported optimization combinations.
- [ ] Run identical workloads/resources for original baseline, disk baseline, pmem only, reclamation only and combined (omit redundant original comparison if Step 3 already disk-backed).
- [ ] Measure peak/integrated host memory, guest anonymous/page cache, shared backing, CPU, faults/I/O, disk, latency and correctness; avoid shared-memory double counting and conflating storage savings.
- [ ] Commit/push scripts/config/results; independently reconstruct and report measured savings/costs, revisions, worker, commands and limitations.
