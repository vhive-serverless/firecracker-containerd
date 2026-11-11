# Step 4 — WIP

The user authorized starting this branch on 2026-11-11 while **Step 3 remains
incomplete, explicitly WIP**. `damon-virtio-pmem` starts at the preserved Step 3
commit `26ceead7d0b4d1c6ea32a012c24b76efb4fb0439`. This is an explicit exception
to the original milestone ordering. It does not waive either milestone's tests.

Step 3 supports ordinary Firecracker RuntimeClass Pods and native capture, but
has unresolved CRI conformance failures, no released Kubernetes whole-Pod restore
path, and an unaccepted runtime restore prototype. Its latest development
restore test failed before reconnecting to the clone shim socket. Step 4 must
retain these limitations; passing storage tests cannot imply restore acceptance.

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

Evaluation will preserve resource/workload parity and include read/idle/reread
and write/idle/readback correctness. Measure host physical peak and memory-time,
guest anon/cache, shared backing, CPU, faults, I/O, capacity and latency. Shared
RSS and guest cache must not be added together as independent physical memory.
No evaluation results or savings are claimed yet.

Native capture already copies every configured virtio-blk drive after guest
quiescing and VMM pause. New disks need capture regression coverage. Pmem backing
is referenced by pathname in VMM snapshots, so relocation and immutable lifetime
need an explicit contract. End-to-end native restore remains WIP from Step 3.
