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

## Completion assessment

**Step 4 is complete in the authorized storage and memory scope.** Clean
worker source `06b55e3b78cc6bedfe3f0671fcdc49df73046eb2` passed build, private
installation and the full acceptance script. It recorded 47 runtime, seven
snapshotter and 79 SDK top-level passes, with three explicit skips listed in
the [final receipt](evidence/step4-stargz/clean-06b55e3/verification.json).
Package tests, CRI unit/race tests, generation checks, initializer checks and
cleanup also passed. [CI passed for that same source](https://github.com/vhive-serverless/firecracker-containerd/actions/runs/43172572356).

The retained guest stargz store now binds `/var/lib/containerd-stargz-grpc` to
`/rom/overlay/stargz` on the private guest-upper filesystem, including container
overlay upper/work. Four storage profiles across both jailers and twelve native
restore cases passed. These use static owned TAPs; they do not establish default
CNI or cold lazy-fetch coverage. Stargz workload lowers remain FUSE-backed;
pmem/DAX applies to the immutable guest base. The frozen 21-trial standard-image
memory evaluation is unchanged and supplies no stargz-specific savings claim.

See the [current requirement matrix](step-4-completeness.md) and
[stargz evidence](evidence/step4-stargz/README.md). Step 3 remains explicitly WIP
for CRI conformance. Kubernetes Pod checkpoint/restore is separately deferred
to the 1.38 interface work; no particular 1.38 build is claimed to implement it.
The measured memory results remain mixed rather than a universal reduction.

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

At the initial audit, DAMON sysfs existed in the running guest; this did not
prove reclamation was enabled. Source inspection found pmem, DAX, balloon
reporting and DAMON support in the pinned VMM/SDK/kernel. The runtime controls
and activation were subsequently implemented and validated as described below.
The kernel disables EROFS, and SquashFS cannot supply a DAX lower, so the
implemented pmem path uses a separately built ext4 image.

## Mechanisms and evaluation

The [DSec paper, sections 5.1–5.2](https://arxiv.org/html/2609.22978v1)
uses immutable EROFS lowers, an ext4 writable upper, pmem/DAX for selected
immutable layers, and DAMON reclamation with balloon free-page reporting.
It identifies pmem page metadata and cold faults as costs. Its reported
memory reductions are reference results, not expected outcomes for this worker.

Implementation order is a bounded private ext4 guest upper, independently
configurable pmem/DAX and DAMON/reporting, then five controlled evaluation arms.
Default devmapper container writable roots already use disk, so the controlled
storage comparison applies to the guest upper. It does not cover the optional
guest stargz store. That path's subsequent disk migration requires separate
functional acceptance and does not establish new memory savings. Writable files must be private to each VM and allocated on
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

Test VM storage now defaults to the sibling directory
`../.ficd-test-vms/<checkout-name>` via `FICD_VM_STORAGE`. Runtime and snapshotter
runners allocate and clean a private child directory for each invocation. This keeps root-owned
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

## Ordinary Kubernetes Pod regression

Clean harness commit `06936f6b2167386321755acca65100f84c40ddf2` passed all four
profiles (`disk`, `pmem`, `reclaim`, `combined`) on the designated worker using
k3s `v1.37.2+k3s1`. The runtime image was the already built and validated
`72f4fc9` image, pinned by ID
`sha256:0ed23833cf6043e6f2e0fa6ef3e590cfd9626c03e69d66303f9a693b079021b5`.
The harness checkout's Go sources, module files and submodule pins match that
runtime revision. This is validation of the committed harness against that
pinned runtime image, not a new runtime build.

The [verification record](evidence/06936f6-k3s/verification.json) identifies each
profile's raw logs, SHA-256 hashes, Pod and sandbox identities and live container
statistics. All four exercised two containers in one Firecracker VM, exec,
logs, shared/projected volumes, DNS and Service networking. Each used a fully
allocated 512 MiB guest upper on host disk and removed it during Pod deletion.
DAMON's enabled state matched each profile, and pmem profiles mapped their
hash-pinned immutable image read-only. Actual DAX and post-restore reporting
remain covered by the separate mechanism and restore tests above.

On session continuation, all 92 raw artifacts were copied without modification
and verified against the recorded hashes; the harness's evidence validator
passed for each profile. Worker inspection confirmed the harness checkout was
clean and the private storage directories, device-mapper pools and node
containers were absent. These checks preserve the earlier completed runs;
they do not represent four additional Pod runs.

To reproduce the recorded test with the retained worker image and DAX image,
use the clean `06936f6` harness checkout (`/users/nehalem/fc-ctrd-mod/step4-k3s-dev`):

```sh
export STACK_DIR=/users/nehalem/fc-ctrd-mod
export RUNTIMECLASS_BASE_IMAGE=sha256:0ed23833cf6043e6f2e0fa6ef3e590cfd9626c03e69d66303f9a693b079021b5
export RUNTIMECLASS_RUNTIME_REVISION=72f4fc9173bef33cc1c981ad285758716f635e02
for profile in disk pmem reclaim combined; do
    KUBE_MEMORY_PROFILE="$profile" tools/kubernetes/test-firecracker-node.sh
done
```

For a fresh reconstruction, build/install the pinned stack as above, run
`make image-dax` and `make test-images` with `DOCKER_IMAGE_TAG` set to the first
12 characters of the checkout revision, and omit the two `RUNTIMECLASS_*`
overrides so the harness selects that checkout's image. The runtime image and
DAX image hashes may differ from the historical receipt; retain the new inputs.

**Step 3 remains WIP:** these are ordinary Pod lifecycle regressions, not CRI
conformance or Kubernetes Pod checkpoint/restore acceptance. The inherited
integration gate remains open even though Step 4's storage, memory evaluation,
direct runtime restore and ordinary Pod checks have passed.

## Broader lifecycle and CRI regression follow-up

The continuation audit found that the CRI and snapshotter test containers had
not yet mounted host-disk VM storage. Their runners now bind private disk-backed
storage. Runtime and snapshotter invocations also use
`tools/modernize/run-isolated-vm-test.sh`: each Docker invocation receives a new
child directory under `FICD_VM_STORAGE`, and the wrapper removes its container
before removing that directory. This preserves the isolation formerly supplied
by Docker's private filesystem. Some older tests deliberately leave their VM
running until the test container exits; those fixtures must not become input
to the next test. This runner cleanup does not replace explicit runtime cleanup
assertions inside the tests.

The unfiltered CRI run used clean harness `393f5f6` and the pinned `72f4fc9`
runtime image recorded above. It completed 113 of 142 specs: **97 passed,
16 failed, 29 skipped**. The failed test names exactly match the preserved
`20727f4` result: host namespaces/networking, mount-propagation/read-only
semantics and AppArmor setup. There are no new failed names in this run, but
**the suite still fails and CRI compliance remains WIP**. The
[CRI verification](evidence/393f5f6-cri/verification.json) preserves counts,
failed names, input hashes and raw report/logs. Its private storage, pool and
container were removed.

The broader runtime checks retained the exact 35 isolated test names from the
accepted `20727f4` baseline and ran them against `72f4fc9`. The initial run had
30 passes and five failures. Four cleanup assertions encountered the persistent
immutable cache or fixtures from earlier Docker invocations; the block-device
assertion omitted the new 512 MiB guest upper. These failures are retained in
[the raw baseline run](evidence/step4-regression/72f4fc9-base-regression.log).
Corrections keep strict cleanup checks, allow only digest-verified read-only
cache images, isolate test invocation storage, and explicitly assert the added
guest block device.

Separate committed correction checks passed:

- `5fb79f1`: both modules' lint and six jailer/block-device subcases with an
  intentionally retained real immutable cache.
- `f5b3ba6`: all five snapshotter cases, including remote images, multiple VMs,
  metrics and guest volumes, with host-disk VM backing.
- `d5739e5`: drive mounting followed by all three affected jailer cases,
  pmem/block sharing and DAX checks, both native restore jailer modes, and the
  snapshotter guest-volume case. Every invocation removed its private storage.
- `7c69ccc`: the remaining shim-cleanup and block-device cases, both modules'
  lint, preservation of an expected exit status 37, and interruption cleanup
  in five seconds for a 60-second workload. The earlier wrapper deferred its
  signal handler until the workload exited; `29534de` corrected that behavior.

The [regression verification](evidence/step4-regression/verification.json)
records exact source revisions, raw hashes, commands and cleanup checks.
These are an initial run plus targeted corrections, **not one uninterrupted
passing full-suite run**. All runs use the validated `72f4fc9` runtime image;
production Go sources, module files and dependency pins remain identical.
Only tests, runners and documentation changed. The frozen memory evaluation
and earlier native storage-restore acceptance retain their original revisions.

Reproduce the unfiltered CRI regression from a clean current harness checkout
with the retained pinned image:

```sh
export STACK_DIR=/users/nehalem/fc-ctrd-mod
CRI_BASE_IMAGE=sha256:0ed23833cf6043e6f2e0fa6ef3e590cfd9626c03e69d66303f9a693b079021b5 \
CRI_RUNTIME_REVISION=72f4fc9173bef33cc1c981ad285758716f635e02 \
    tools/critest/test-cri.sh
```

For a fresh stack, use `tools/modernize/build-stack.sh`,
`tools/modernize/install-stack.sh` and `tools/modernize/test-stack.sh` on the
worker, then run CRI without the image/revision overrides. The general runtime
acceptance timeout is now 1200 seconds per isolated invocation, matching the
dedicated restore runner and accommodating the measured eight-minute storage
restore matrix. All original test assertions remain enabled.

## Cache eviction test maintenance

Commit `a67e5e6` fixes the timing-dependent eviction error test
observed in [CI run 43079780458](https://github.com/vhive-serverless/firecracker-containerd/actions/runs/43079780458).
The test now waits for the reaper error log, checks its text, and verifies the
entry was removed. Production code is unchanged. The clean committed worker
checkout passed 200 targeted repetitions with one scheduler thread, 50 targeted
race repetitions and the full cache package under the race detector.
The [shared receipt](evidence/step4-cache-test/verification.json) records all four
validated milestone backports, exact commands and raw hashes. The old failure
was not reproduced in 30 baseline repetitions; these checks are test maintenance,
not new full-stack acceptance. Step 3 CRI remains WIP; Kubernetes Pod restore is deferred to 1.38 interface work.

## Capture interruption after quiescing

`TestNativeCheckpointInterrupt_Isolated` in `5186d40` adds a real in-flight
capture cancellation check. The existing pre-cancelled request test does not
exercise this phase. A nonempty staged memory file establishes that task pause,
VMM pause and volume freeze/save have already occurred. The test then closes a
dedicated control connection, which propagates cancellation through the control
forwarder to the shim. With pinned ttrpc v1.2.8, cancelling only the caller's
context stops its local wait; it does not send a cancellation frame.

Both jailer modes must return a failed interrupted request, remove the destination
and temporary stage, resume the source task, support guest exec, and complete a
subsequent capture. The test uses 512 MiB VMs and observes the capture phase;
it does not add production hooks or rely on a fixed cancellation delay.
The [evidence](evidence/step4-capture-interrupt/README.md) distinguishes earlier
observer-design failures, final development validation and committed acceptance.
This covers native runtime capture interruption, not Kubernetes Pod restore.

Clean committed worker validation at `5186d40` passed both jailer modes in
31.54 seconds and both modules' lint. Production Go sources, module manifests
and submodule pins match the retained `72f4fc9` runtime image. The private pool
and storage were removed; the unrelated control-plane workload was preserved.

## Retained stargz path and final regression

The disk migration at `de2e471` places the guest snapshotter's persistent cache,
metadata and overlay upper/work on raw ext4 backing rather than the guest-root
OverlayFS. The initializer rejects missing/unsupported backing, checks bind
identity, and accepts RAM only for an explicit `overlay_root=ram` baseline.
The store shares the private guest-upper disk's capacity and snapshot lifetime.

The final clean run at `06b55e3` passed stargz disk, reclaim, pmem and combined
storage profiles with both jailers in 84.80 seconds. It verifies actual host and
guest backing, private inodes and ownership, capacity, overlay copy-up,
rename/delete, exhaustion/recovery and writeback. The twelve-case stargz restore
matrix passed in 308.74 seconds: disk, ext4-block, pmem, reclaim, combined and
ext4-reclaim, each jailed and unjailed. It terminates the source VMM, restores a
new VMM, verifies memory and disk cutoff, private writable copies, lower reads,
exec, guest-to-host TCP, host snapshotter reconnection, artifact immutability
and cleanup. This is same-host direct runtime restore with preserved guest
network identity and a replacement TAP, not containerd/CRI ownership recovery.

The development runs exposed two inherited production defects: jailed VM
socket paths and host demux shutdown on SIGPIPE. Both were fixed on Step 1,
validated there and merged forward. The final real-VM suite exercises those
corrections. Two test-fixture defects were also corrected: CRI annotations
incorrectly routed guest-only bind sources through host-volume preparation,
and automatic bridge MAC selection changed gateway identity during TAP
replacement. A fixed bridge MAC preserves the restored guest's ARP mapping.
No snapshotter refresh, TCP-reset workaround or enlarged test timeout remains.
The [failed and corrected runs](evidence/step4-stargz/README.md) are retained.

The uninterrupted full acceptance run passed 47 runtime and seven snapshotter
isolated cases plus 79 SDK top-level cases. Its explicit verbose skips are the
separately driven memory-evaluation case, the SDK's existing `TestLogAndMetrics`,
and its dedicated-script `TestNativeSnapshotVsockPolicy`. The generic SDK suite
did not run that dedicated policy test; the full runtime native restore matrices
did run. The separately pinned 21 memory trials remain the evaluation evidence.
Both private pools were removed, VM storage was empty, no candidate test
container remained, and the unrelated control plane stayed running.

Reproduce from a clean committed checkout on the designated worker:

```sh
export STACK_DIR=/users/nehalem/fc-ctrd-mod
tools/modernize/build-stack.sh
tools/modernize/install-stack.sh
tools/modernize/test-stack.sh
```

After build/install, `tools/modernize/test-stargz-storage.sh` selects just the
initializer and two stargz matrices. The final recorded run used the broader
`test-stack.sh`. Exact input hashes, test-image identity, source cleanliness,
commands and raw output are in the [receipt](evidence/step4-stargz/clean-06b55e3/verification.json).
