# Pmem/DAX integration — WIP

Step 3 remains explicitly WIP. This component adds an optional immutable guest
root through the pinned VMM/SDK's virtio-pmem support. It does not complete
native restore or the five-configuration evaluation.

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
unit checks passed; actual VM validation and clean committed acceptance remain
pending. The initial lint failure is retained in
[evidence](evidence/step4-pmem-dev-lint-failed.log); its read-only sharing permission
and test-fixture findings are being corrected and rechecked.
