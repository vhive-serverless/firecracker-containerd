# Pmem/DAX integration — WIP

Step 3 remains explicitly WIP. This component adds an optional immutable guest
root through the pinned VMM/SDK's virtio-pmem support. It does not complete
native restore or the five-configuration evaluation.

Clean commit `438c2cb6ea0c7de719bed8eef08b3ba0db1d31b8` passed the complete
worker procedure below: full build, private installation, lint, race units,
forced protobuf regeneration, real DAX/control VMs and native capture. The
[receipt](evidence/438c2cb-pmem-acceptance-receipt.log) and
[raw acceptance](evidence/438c2cb-pmem-acceptance.log) record the clean source,
dependencies, actual binaries/image and pool cleanup. Eight root VM cases
passed in 52.03 s; combined pmem/DAMON capture passed both jailer modes in
112.23 s, and matching ext4 block-control capture passed both in 97.31 s.
Repeated capture retained independent upper/container/FUSE state while the
source advanced. This accepts the component, not native restore or savings.

Build the ordinary guest image and `make image-dax` on the worker. The latter
converts exactly that image tree into a 512 MiB ext4 image with 4 KiB blocks,
no journal and mode 0444. The default remains SquashFS. Both image paths receive
the same static `fc-memory-probe`; its host checks are recorded in the
[probe receipt](../../tools/memory-probe/evidence/c2f5465-host-receipt.log).

Set `CreateVMRequest.RootDrive.UsePmem=true`, `HostPath` to the absolute ext4
image path and `ImageSha256` to its exact digest. Administrator defaults are
`root_drive`, `root_drive_use_pmem` and `root_drive_sha256`. Explicit request
RootDrive values override the defaults. Writable pmem, partitions, block cache
or rate settings, and legacy vHive restore are rejected. DAMON/reporting remains
separately configurable through `Reclamation`.

The runtime verifies the digest and publishes a read-only image into
`<shim_base_dir>/.pmem-images/<sha256>.img` on verified ext3/ext4, XFS or Btrfs
host storage. Concurrent publication is atomic, and an existing image is verified
before reuse. Source changes do not advance the cached copy. Jailed VMs bind
that same inode read-only, without copying or chowning it. This is explicit
sharing of immutable bytes; every writable upper and workload disk remains
private. The cache outlives VM cleanup. It currently needs administrator garbage
collection after all users and retained snapshots have released an image.

For an ext4 virtio-blk control, use the same HostPath and ImageSha256 with
UsePmem=false. This uses the same verified image/cache inode and guest tree;
matching the format avoids attributing SquashFS compression/layout changes to
pmem. The pmem boot uses `rootfstype=ext4 rootflags=dax=always,noload`; conflicting
explicit root arguments are rejected. Device selection for the private writable
upper remains UUID-based.

Acceptance requires STATX_ATTR_DAX on an actual read-only lower file, mmap and
reread correctness, anonymous canary preservation, both jailer modes, multiple
VMs sharing the immutable inode, and independent writable state. A successful
mount or enabled option alone does not prove DAX. The 512 MiB pmem region's
nominal struct-page metadata is 8 MiB at 64 bytes per page; measurements must
also include its actual guest overhead and shared host backing. Summing VM RSS
would double-count shared mappings.

Native capture includes a hashed copy of the pmem backing alongside memory,
VMM state and independent writable disks. Artifact validation rejects missing,
writable, mismatched or unsupported pmem layouts. The file-clone path verifies
and republishes the immutable lower before snapshot load. Firecracker currently
has no pmem-path override on load: jailed snapshots require the same digest
basename; unjailed snapshots require the same absolute cache pathname. A changed
pathname is rejected. This path contract is implemented and unit-tested; it is
not evidence that the inherited end-to-end native restore prototype works.

Worker component checks, after a clean build and private install:

```sh
export STACK_DIR=/users/nehalem/fc-ctrd-mod
tools/modernize/build-stack.sh
tools/modernize/install-stack.sh
tools/modernize/test-pmem.sh
```

The script checks lint, race units, forced protobuf regeneration, guest DAX and
block controls, jail sharing, combined DAMON/pmem correctness and native capture.
It does not claim native restore or the controlled evaluation. Initial worker
unit and full lint checks passed. Development VM checks then passed on a dirty
`6291b38` checkout: eight VMs covered pmem/block and jailed/unjailed combinations
(52.25 s), and combined pmem/DAMON repeated capture passed both jailer modes
(112.04 s). Actual lower-file STATX_ATTR_DAX was true in the four pmem VMs and
false in the four matched block controls. Shared backing, private upper writes,
copy-up/rename/delete, mmap/reread and anonymous canary checks passed.

The [development receipt](evidence/pmem-dev-vm-receipt.log) identifies the dirty
source patch/archive, binaries, image and raw logs. These runs precede the final
artifact-directory fsync and block-control capture corrections in `438c2cb`.
Clean committed validation of that revision subsequently passed, as recorded above.
The raw [root checks](evidence/pmem-dev-PmemRoot_Isolated.log) and
[combined capture checks](evidence/pmem-dev-PmemNativeCheckpoint_Isolated.log)
do not establish native restore or measured memory savings.

The initial lint failure is retained in
[evidence](evidence/step4-pmem-dev-lint-failed.log); its read-only sharing permission
and test-fixture findings were corrected and full lint passed before the VM run.

The immutable cache currently rehashes the full image for every VM creation.
This warms host backing pages and adds image-sized CPU/I/O work. Evaluation
must include that startup cost and distinguish this cache state from cold I/O.

The later [controlled evaluation](../../tools/memory-eval/results/0a268c5589c3/REPORT.md)
passed 21 trials. Pmem saved memory against matched ext4 block controls, while
the full ext4/pmem configurations remained larger than the SquashFS baselines.
The report retains startup, cache, CPU, I/O and reactivation costs.

The later [native runtime restore acceptance](step-4.md#native-runtime-storage-restore)
passed at `72f4fc9`, including pmem and combined profiles with both jailers,
independent writable disks and actual post-restore reporting activity. It does
not complete the preserved Step 3 branch or its Kubernetes/CRI integration.
