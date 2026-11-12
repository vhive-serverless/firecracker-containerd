# Original worker baseline

The original runtime at `be68640a5d2237f5b427c37c1f5809ec154126c5`
builds and runs an OCI container on the designated worker. This is evidence for
the pre-migration baseline, not completion of Step 1. No regression has yet
been established against this baseline.

The root workspace has no commits, remotes or tracked submodule entries. Its
three child repositories are independent Git repositories. No root gitlink
update is applicable in that state. Unrelated root files are preserved.

The worker is `nehalem@amd218.utah.cloudlab.us`, hostname
`node-005.cluster.ntu-cloud-pg0.utah.cloudlab.us`. Its account home is
`/users/nehalem`, not `/home/nehalem`. It runs Ubuntu 24.04.5, Linux
6.8.0-147-generic, host cgroup v2, and has working KVM and passwordless sudo.
There were no Docker, containerd, k3s or VMM workloads before provisioning.
No raw disk was formatted; all test storage is allocated as files on `/dev/sda3`
(ext3). `/dev/sdb` is untouched.

## Reproduction

Prepare the worker with `tools/modernize/prepare-worker.sh`. Create a separate
checkout at `$HOME/fc-ctrd-mod/baseline` at the original commit; initialize all
submodules at their committed pins. With Go 1.26.11 on PATH:

```sh
cd "$HOME/fc-ctrd-mod/baseline"
git checkout --detach be68640a5d2237f5b427c37c1f5809ec154126c5
git submodule update --init --recursive
STATIC_AGENT=on make -j8
DISABLE_ROOT_TESTS=1 go test -short ./...
make image firecracker default-vmlinux
bash /path/to/modernized-checkout/tools/modernize/run-baseline.sh
```

The provisioning script installs Docker as a build tool; the executing account
must have permission to use it. On this initially unused worker, `nehalem` was
added to the Docker group and a new SSH session picked up the membership.
The baseline runner uses its own `fcmod-baseline` systemd service, thin pool,
state directory, runtime configuration and socket. It does not replace the
distribution's containerd service.

Observed results, stored in `evidence/`:

- `baseline-build.log`: all original make build targets passed.
- `baseline-unit-tests.log`: non-root short unit tests passed.
- `baseline-artifacts.log`: original guest rootfs, Firecracker and kernel targets passed.
- `baseline-lifecycle.log`: OCI image pull, VM/container creation/start, ext4
  write/read, container exit and deletion passed (`BASELINE_CONTAINER_OK`).
- `baseline-guest-storage.log`: actual guest mounts, command line, block devices and OS.
- `baseline-build-receipt.log`: versions, binary paths/hashes, thin-pool backing
  and an empty task list after the smoke tests.

The baseline image is Alpine 3.23.0, index digest
`sha256:51183f2cfa6320055da30872f211093f9ff1d3cf06f39a0bdb212314c5dc7375`
and amd64 manifest digest
`sha256:a107a3c031732299dd9dd607bb13787834db2de38cfa13f1993b7105e4814c60`.
The current short suite does not validate networking, privileged jailer recovery,
CRI compliance or full lifecycle resource accounting. These remain Step 1 gates.

## Actual binaries and provenance

All baseline paths below are relative to `$HOME/fc-ctrd-mod/baseline`.

| Component | Actual input/output |
| --- | --- |
| Runtime/client/shim | containerd 1.7.33; `firecracker-control/cmd/containerd/firecracker-containerd`, `firecracker-ctr`, `runtime/containerd-shim-aws-firecracker` |
| Guest agent | `agent/agent`, statically built; embedded SDK module `v1.0.1-0.20250818195323-ed6ff32aa924` |
| Firecracker | submodule v1.1.0, commit `77cfb9ceaa6a54e22a8259f50fb621ad1e39292b`; `bin/firecracker` |
| runc | submodule and module v1.2.8, commit `eeb7e6024f9ee43876301b1d23c353384fa6dcdd`; `_submodules/runc/runc` |
| Guest kernel | downloaded `default-vmlinux.bin`; actual running release **4.14.174**, built July 2021; SHA-256 `ea5e7d5cf494a8c4ba043259812fc018b44880d70bcbbfc4d57d2760631b1cd6` |
| Guest rootfs | `tools/image-builder/rootfs.img`; Debian 11 bullseye, read-only squashfs; built via a Debian bookworm builder |
| CNI binaries | Makefile builds **1.1.0** despite module plugins **1.7.1**; not exercised by the network-free smoke test |
| stargz | source submodule `639461966645e6904838b914bf6eda0177bd4b25`, while example module resolves **v0.11.3**; not exercised by the smoke test |

The original VMM build uses `public.ecr.aws/firecracker/fcuvm:v35`, observed
digest `sha256:ea29f43f186dd8a9efa5a4ad1e28a6687e4841fa0b45849be9d09d33ebbf1fa7`.
The original runc builder's Go 1.25 image resolved to
`sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437`.
The original recipes do not pin all distribution package inputs, so their
generated rootfs hash is an observation, not a bit-for-bit rebuild guarantee.

The original kernel source-build path uses Firecracker's older `build_kernel`
devtool command and local 4.14/5.10 configurations. The default download bypasses
that path; its runtime kernel release, rather than the config header or SDK
checkout, identifies the kernel actually tested. The selected v1.18 release
uses Amazon Linux 2023 sources through `resources/rebuild.sh`, and its checked-in
6.18 config names `6.18.39-79.141.amzn2023`. That upstream script chooses the
newest matching tag at execution time. The modernization needs an exact Amazon
Linux source pin rather than retaining that mutable selection.

## Storage and persistence boundaries

The live guest inspection was performed by a privileged test container joining
the guest PID namespace and then entering PID 1's mount namespace. Its mounts
show:

1. The shared base image is `/dev/vda`, mounted as read-only squashfs at `/rom`.
2. `overlay-init` mounts tmpfs at `/overlay` (visible at `/rom/overlay` after
   pivot). The guest `/` uses OverlayFS with `upperdir=/overlay/root` and
   `workdir=/overlay/work`. Guest system writes are memory-backed and vanish
   with the VM unless the optional `overlay_root` ext4 device is supplied.
3. The host devmapper snapshotter allocates a thin device per container.
   `runtime/service.go` exposes that block device through a Firecracker stub
   drive; `agent/drive_handler.go` mounts it as ext4 at
   `/container/<task>/rootfs`. The observed container device is `/dev/vdb`.
   Container writable image layers are **disk-backed** in this baseline.
4. The thin pool uses loop devices backed by `baseline/tmp/devmapper/data`
   (10 GiB sparse) and `metadata` (1 GiB sparse) on host ext3. It is not a disk
   image on host tmpfs. Container snapshot data survives the VMM process until
   containerd removes that snapshot; this is distinct from VM checkpointing.
5. `/run`, `/dev/shm`, device mounts and other explicit temporary mounts remain
   tmpfs. Their existence does not make the ext4 container root memory-backed.
6. The guest uses a hybrid cgroup layout, with controller mounts in cgroup v1
   and a cgroup2 unified subtree. The original boot arguments explicitly select
   `systemd.unified_cgroup_hierarchy=0`; Step 1 must change and test this.

Supported block drive mounts follow the same host-device/guest-ext4 path. The
volume implementation can also copy OCI image content into a guest path or
share a drive between in-VM snapshotters; the backing of each such path needs
separate validation. The optional guest stargz/FUSE path stores writable upper
layers under its guest state directory and can therefore inherit the guest
root's tmpfs backing. It is not covered by the ext4 smoke-test observation.

Step 4 must preserve these distinctions: moving the guest upper layer to disk
does not imply that the baseline container ext4 writable layer was RAM-backed.
