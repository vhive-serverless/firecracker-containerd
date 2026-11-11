# Step 4 — storage, memory and native runtime validation

The user authorized starting this branch on 2026-11-11 while **Step 3 remains
incomplete, explicitly WIP**. `damon-virtio-pmem` starts at the preserved Step 3
commit `26ceead7d0b4d1c6ea32a012c24b76efb4fb0439`. This is an explicit exception
to the original milestone ordering. It does not waive either milestone's tests.

The preserved Step 3 branch supports ordinary Firecracker RuntimeClass Pods and
native capture, but retains recorded CRI conformance failures and incomplete
Kubernetes Pod checkpoint/restore integration. Its runtime restore prototype
was unaccepted. This Step 4 branch fixes and validates direct runtime restore
at `72f4fc9173bef33cc1c981ad285758716f635e02`, alongside storage, mechanisms and
the controlled memory evaluation. That result does not complete Step 3 or its
remaining Kubernetes/CRI acceptance gates.

## Baseline evidence

Before storage changes, the worker ran `TestStorageBaseline_Isolated` using the
committed capture stack `6d9d052aca10056916920b6df585e5fd8b508591` and a separately
transferred inspection test. This is a development audit, not acceptance of the
Step 4 branch. The first inspection attempt failed because its recursive root
bind interfered with temporary mount cleanup. Using a nonrecursive root bind
and a separate upper mount corrected the inspection.

The successful [guest log](evidence/step4-storage-audit.log) proves:

| Layer | Actual backing |
| --- | --- |
| Guest root | OverlayFS, immutable SquashFS lower, tmpfs upper/work |
| Container writable root | ext4 on a devmapper-backed virtio-blk drive |
| Explicit runtime temporary mounts | tmpfs for `/dev`, `/run`, `/dev/shm` |
| Host checkout and Docker data | `/dev/sda3`, ext3; [mount inspection](evidence/step4-storage-audit-host.log) |

DAMON sysfs exists in the running guest. This does not prove reclamation was
enabled. Source inspection found pmem, DAX, balloon reporting and DAMON support
in the pinned VMM/SDK/kernel; runtime configuration and activation still need
implementation. The kernel currently disables EROFS, and SquashFS cannot supply
the required DAX lower, so the pmem path needs a suitable filesystem and image.

## Mechanisms and evaluation

The [DSec paper, sections 5.1–5.2](https://arxiv.org/html/2609.22978v1)
uses immutable EROFS lowers, an ext4 writable upper, pmem/DAX for selected
immutable layers, and DAMON reclamation with balloon free-page reporting.
It identifies pmem page metadata and cold faults as costs. Its reported
memory reductions are reference results, not expected outcomes for this worker.

Implementation order is a bounded private ext4 guest upper, independently
configurable pmem/DAX and DAMON/reporting, then five controlled evaluation arms.
Container writable roots already use disk, so the storage comparison applies
to the guest upper. Writable files must be private to each VM and allocated on
verified disk storage; immutable sharing must be explicit.

The committed evaluation at `0a268c5589c37832e1c3dc89a7b60ed2701e0b9e`
passed all 21 trials: seven configurations in three randomized blocks, with two
512 MiB VMs per trial. It measures dedicated-cgroup memory peak and memory-time,
guest anon/cache, mapped PSS, CPU, faults, I/O, allocation and instrumented
latency. Shared RSS and guest cache are not added to cgroup memory. The
[receipt](evidence/0a268c5-evaluation-receipt.log) records clean source,
dependencies, commands and evidence hashes.

Results are mixed. Against the matching ext4 block-device control, pmem reduces
common-idle cgroup memory by about 100.55 MiB across two VMs (4.59%). The combined
configuration still uses more total memory than the SquashFS configurations;
filesystem/cache and startup costs outweigh that saving. Reclamation alone
reduces idle memory by about 14.18 MiB (0.81%) while adding approximately three
CPU-seconds per trial. The [results and raw observations](../../tools/memory-eval/results/0a268c5589c3/REPORT.md)
retain the individual trials and costs. These are observations from one worker
and workload, with verified/warmed host backing, rather than a general estimate
of application savings.

Recorded reporting counters may precede consumption of the final metrics flush.
They do not certify complete reporting traffic or failures through trial end.
The cgroup/PSS/CPU observations and guest correctness checks are independent of
that limitation. Reporting traffic is never used as a physical-memory saving.

Pmem comparisons must hold the read-only filesystem format and image contents
constant. Converting SquashFS to a DAX-capable format requires a matching
virtio-blk control; otherwise filesystem/compression effects would be mixed
into the claimed pmem result. Additional controls may supplement the five
requested configurations for this reason.

## Disk upper configuration (committed component validation passed)

`guest_root_mode` defaults to `disk`; `guest_root_size_mib` defaults to 512.
`CreateVMRequest.GuestRoot` can select `disk` or the measured original `ram`
layout and override capacity from 64 to 65536 MiB. The administrator must place
`shim_base_dir` on ext3/ext4, XFS or Btrfs disk storage; unidentifiable backing,
including an OverlayFS host path, is rejected. Integration runners bind an
explicit worker disk directory into the test container for this reason.

Each VM owns `guest-overlay.ext4` in its jail directory. It has an independent
ext4 UUID; boot locates that UUID instead of assuming a block-device number.
Mount failure stops boot. Allocation reserves the entire capacity after mkfs;
ext3 uses a byte-preserving rewrite because it cannot preallocate extents.
The worker test exposed holes left by mkfs, which is why reservation follows
formatting. Initialization failure removes the file, and VM cleanup removes
its directory. Guest ENOSPC must be reported and recover after files are removed.
This reservation is not a host-wide storage quota or a guarantee against later
administrator truncation or unrelated hardware failure.

The legacy vHive snapshot API supports only the explicit RAM layout; it cannot
silently omit the new upper. Native capture includes the upper alongside the
other disks. Native restore validation is recorded separately; DAMON and pmem
component acceptance are recorded below.

The development worker passed disk upper inspection, write/copy-up/rename/delete,
guest ENOSPC recovery, ownership and cleanup, with and without the runc jailer
(10.09 s). The explicit RAM-layout audit also passed (7.51 s). Native capture
passed both jailer configurations (88.05 s), including independent upper and
container disks, repeated capture, and source writes after capture. These runs
used a dirty development checkout and do not establish committed acceptance.
The earlier invalid drive ID and early-boot UUID lookup failures were retained.
UUID lookup now probes devtmpfs block nodes directly, before proc/udev startup.

From a clean checkout on the designated worker, reproduce the storage component
with the pinned toolchain prepared as described in Step 1:

```sh
tools/modernize/build-stack.sh
tools/modernize/install-stack.sh
tools/modernize/test-storage.sh
```

The storage script also tests a real host allocation failure inside a private
32 MiB loop-backed ext4 filesystem, without filling the shared worker disk.
Its final marker covers storage and native **capture**, not native restore,
Kubernetes Pod checkpoint/restore, pmem/DAX or DAMON acceptance.

Clean commit `8f8d00fc2fb122d40c2e034f62e67978ba194c1e` passed the complete
storage script after the FIFO correction, plus full build and private install.
The [receipt](evidence/8f8d00f-storage-acceptance-receipt.log) and
[raw acceptance](evidence/8f8d00f-storage-acceptance.log) record component tests,
host/guest ENOSPC, both jailer modes, RAM control, independent repeated capture,
cross-filesystem telemetry and all eight CreateVM cases. That receipt accepts
storage and capture; the later runtime restore acceptance is recorded below.

Test VM state now defaults to the sibling directory
`../.ficd-test-vms/<checkout-name>` via `FICD_VM_STORAGE`. This keeps root-owned
scratch disks outside Go package discovery and Docker build contexts. It must
reside on real host disk; allocation verifies the opened file's filesystem.
Existing stopped test data under `runtime/vm-storage` or `runtime/.vm-storage`
must be kept outside the source tree before reusing that old checkout. Fresh
checkouts use the new location directly.

## Free-page reporting — component validation passed

`FirecrackerBalloonDevice.FreePageReporting` now reaches the VMM and is returned
by the control API. The runtime uses the pinned SDK's optional balloon request
field; no dependency change is needed. Ordinary balloon targets remain separate.
The development test `TestFreePageReporting_Isolated` requires target/actual
balloon size zero, positive reporting counters, a physical VMM RSS drop after
resident pages are freed, and correct retained data after fresh allocations.
Clean worker commit `020f85b7c137be6989d96fbf4d63c1a3b3e9b71e` passed full
build, private installation, lint, forced protobuf regeneration and both the
reporting test and ordinary balloon regression. The
[receipt](evidence/020f85b-reporting-acceptance-receipt.log) identifies source,
dependencies, actual binaries, image and log hashes. The checkout remained clean.

| Reporting | Balloon target/actual | RSS after filling / after freeing (KiB) | Report descriptors | Reported bytes | Failures |
| --- | --- | --- | --- | --- | --- |
| Disabled | 0 / 0 MiB | 236700 / 237260 | 0 | 0 | 0 |
| Enabled | 0 / 0 MiB | 237780 / 174536 | 82 | 337641472 | 0 |

The enabled case released about 61.8 MiB of VMM RSS after a 64 MiB resident
file was removed. Both cases retained a separate file's checksum after fresh
allocations. Reported bytes count cumulative descriptor traffic, including
initially free pages; they are not net or unique physical savings. This is a
mechanism acceptance check, not the five-configuration workload evaluation.
See the [raw reporting run](evidence/020f85b-FreePageReporting.log) and
[ordinary balloon regression](evidence/020f85b-MemoryBalloon.log).

The broader committed storage check exposed the jailer's hard-link assumption
for external telemetry FIFOs: putting the jail on disk made `/tmp` a different
filesystem in the test container. Commit `c7d73c7` prepares runc bind mounts for
that case before startup. Its development cross-filesystem, VM creation and
race checks pass, and committed storage revalidation passed at `8f8d00f` above.
The shared correction also passed bounded checks on Steps 1–3; Step 3 remains WIP.

[Reclamation integration notes](step-4-reclamation.md) record the exact pinned
kernel controls and clean committed DAMON acceptance at `9706231`. That run
passed full build/private installation, lint, race units, forced protobuf
regeneration, reporting, DAMON and ordinary balloon regression tests. The
controlled evaluation is recorded above, including the matching filesystem
controls and costs.

Native capture already copies every configured virtio-blk drive after guest
quiescing and VMM pause. New disks need capture regression coverage. Pmem backing
is referenced by pathname in VMM snapshots, so relocation and immutable lifetime
need an explicit contract. The later runtime restore validation below covers
those storage paths while retaining Step 3's Kubernetes/CRI limitations.

[Pmem integration notes](step-4-pmem.md) describe the optional immutable ext4
root, digest-pinned sharing, matching virtio-blk control and artifact handling.
Clean pmem acceptance passed at `438c2cb`, including actual lower-file DAX,
matched block controls, shared read-only backing, private writable state and
both native capture profiles across both jailers. The controlled evaluation is
recorded above; native restore and Kubernetes/CRI integration have separate
acceptance requirements.

## Native runtime storage restore

Clean worker commit `72f4fc9173bef33cc1c981ad285758716f635e02` passed full build,
private installation, lint, race units, all **14 native restore cases**, and both
cold-boot reporting cases. The [receipt](evidence/72f4fc9-native-receipt.log),
[verified case counts](evidence/72f4fc9-native-verification.json) and
[raw acceptance log](evidence/72f4fc9-native-acceptance.log) record exact pins,
binaries, test image and commands. The checkout stayed clean and its private
pool was removed. Ordinary restore took 55.83 s; the twelve storage cases took
489.51 s; the reporting regression took 19.83 s.

All six reclamation-enabled clones also demonstrated actual post-restore
reporting activity: deleting a resident 64 MiB cache file released 60.9–63.8 MiB
of VMM RSS, with target/actual balloon size zero. Fresh 64 MiB allocations then
preserved an independent 16 MiB file's checksum. This establishes the exercised
reporting/reuse path; RSS release is not a net physical-memory saving estimate.
Actual lower-file DAX matched all twelve storage cases (true for four pmem
cases). These acceptance checks run separately from the frozen memory results.

`CheckpointVM` and `CreateVMRequest.NativeCheckpointDirectory` operate on a
whole VM through the runtime control API. They do not rebuild Kubernetes or
containerd sandbox ownership. The restore tests reconnect to the adopted tasks
through the shim's task service; success there does not establish Kubernetes
Pod restore or full CRI conformance.

Capture serializes VM mutations, pauses running task groups and then the VMM,
and freezes the VM's exported-volume server. Firecracker prepares block devices
by draining pending operations and flushing their host backing before saving
device state. Guest dirty cache remains part of captured RAM; this is not an
application-level commit protocol. The runtime copies memory, VMM state, every
configured block drive and immutable pmem backing, records hashes, and syncs
the staged files/directories. It resumes the source before atomically publishing
the artifact. External host writers to exported volumes must also be quiescent;
the VM's gate cannot stop writes that bypass it.

Restore validates the manifest, namespace, file sizes and checksums, creates
private copies of writable disks, restores the exported-volume tables and loads
memory/device state into a new VMM. Pmem uses its explicit digest-pinned read-only
cache. Captured balloon state must be loaded with the snapshot: sending
`PUT /balloon` first configures boot resources and makes Firecracker reject
snapshot loading. Control-created shims also publish their task-service address
so restored tasks can be reached after source termination.

Writable clone storage must satisfy the same disk-filesystem allowlist as
fresh creation. The runtime checks an opened workspace before copying any
native files, then checks the opened cloned upper and reserves its full
capacity without changing its bytes. Thin workload disk copies retain sparse
allocation; their remaining logical capacity is not an additional host-wide
reservation. Failed/canceled clones remove their newly created workspace.

The storage test covers SquashFS disk, matching ext4 block, pmem, SquashFS
reclamation, combined and ext4 reclamation, with both jailers. It advances source
upper/workload/volume data after capture, terminates the original VMM and removes
its VM directory, then requires the clone's original memory token/counter and
checkpointed filesystem data. Clone writes must leave artifact hashes unchanged;
actual VMM-open writable disks must have private inodes. It exercises restored
State, Exec, logs, Stats, Kill, Wait and Delete, together with lower-file DAX and
reclamation configuration checks.

Build/install on the designated worker, then run:

```sh
export STACK_DIR=/users/nehalem/fc-ctrd-mod
tools/modernize/build-stack.sh
tools/modernize/install-stack.sh
tools/modernize/test-native-restore.sh
```

The acceptance script requires a clean committed checkout, uses a private pool,
and also runs the fresh-metrics reader regression and actual free-page-reporting
test. It does not run Kubernetes restore. The native tests use the same host and
namespace, with no network devices. Restoring networked VMs requires explicit
replacement TAPs and matching captured MAC/IP configuration; neither cross-host
relocation nor restored network connections are accepted by these tests.
Unjailed pmem restore also retains the captured absolute cache prefix; jailed
snapshots use the stable digest basename. These tests do not accept relocating
that pmem path.

Earlier failures remain available. The initial
[storage restore run](evidence/step4-native-dev-storage.log) rejected all
reclamation-enabled clones because a pre-load balloon PUT configured boot
resources. The later [385aca4 run](evidence/385aca4-native-acceptance.log) exposed
an empty source response after checkpoint: the shell created `/response` before
writing it. The fixture now publishes a complete response by atomic rename.
Neither earlier run is relabeled as acceptance of the final commit.
