# Disk-backed guest OS overlay baseline

This branch extends the Firecracker 1.18/containerd v2 baseline with a private
ext4 disk for the guest OS OverlayFS upper. It does not enable PMEM, DAX,
DAMON, free-page reporting, or change the Firecracker VMM or its SDK pin.
The container workload filesystem remains on its existing snapshotter disk.

Set these fields in the runtime JSON configuration:

```json
{
  "guest_root_mode": "disk",
  "guest_root_size_mib": 512
}
```

Rebuild the host shim and the ordinary `tools/image-builder/rootfs.img` so that
its `/sbin/overlay-init` supports UUID selection. Keep the ordinary read-only
virtio-block root image. No guest kernel or VMM rebuild is required. The shim
requires `mkfs.ext4` on the host. Guest UUID discovery uses `blkid` from
`util-linux`, before udev starts.

Each fresh VM receives a private `guest-overlay.ext4` in its shim directory.
The shim formats and reserves its full configured capacity (64–65536 MiB) on
ext3/ext4, XFS or Btrfs host storage, attaches it as an ordinary writable virtio
block drive, and adds `overlay_root=UUID=...` to the boot arguments. The guest
mounts it at `/overlay` and uses `root` and `work` subdirectories. A failed mount
stops boot rather than falling back to tmpfs. VM teardown removes the disk.

An omitted mode preserves the existing boot configuration and RAM-upper default.
Explicit `ram` selects tmpfs. Do not combine explicit mode configuration with a
manual `overlay_root` kernel argument.

## vHive snapshot and restore

The existing unjailed vHive PauseVM/CreateSnapshot/LoadSnapshot lifecycle now
also supports a guest-upper drive. The prepared transport pause still defines
the snapshot boundary. For a state file `/path/state`, retain all of:

- `/path/state` and its memory file;
- `/path/state.drives.json` (publication marker, version 3 for a disk upper);
- `/path/state.guest-overlay.ext4` (captured OS upper);
- the independently captured workload disk already required by vHive.

The sidecar's `guest_overlay` object records the logical `Path`
`guest-overlay.ext4`, `SHA256`, and `Size`. Restore reads the upper adjacent to
the state file, verifies its bytes, reserves an independent mutable copy in the
new shim directory, and maps it at the captured VMM-visible path. It never
formats a new upper for restored memory. Multiple restores cannot share upper
writes, and the original VM/disk can be removed before restore. Version 2 RAM
snapshots remain supported. Move the upper artifact together with the state and
sidecar; renaming the state requires renaming the upper prefix as well.

Validation: the runtime tests cover ext4 UUID/capacity, RAM backing rejection,
failed allocation cleanup, independent restored copies, checksum rejection,
metadata layout, and preservation of legacy defaults. Native boot and restore
must additionally be checked with the rebuilt guest image on a KVM host.
