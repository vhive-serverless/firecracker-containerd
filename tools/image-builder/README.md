# Create Firecracker VM images for use with firecracker-containerd #

> Historical upstream guide: version requirements and command examples below
> are not the tested setup for the maintained Step 1–4 branches. Follow the
> [modernization branch guide](../../docs/modernization/README.md) and its pinned worker procedures.

## Debian root image ##

### Overview ###

The image builder component of firecracker-containerd will build a
microVM image including the necessary components to support container
management inside the microVM. In particular, the
firecracker-containerd runtime agent and runc binary will be installed
in the image.

The image is generated as a read-only squashfs image. A read/write
overlay layer is supported via the /sbin/overlay-init program, which
should be used as init (e.g. by passing `init=/sbin/overlay-init` as a
kernel boot parameter). By default, overlay-init allocates a tmpfs
filesystem for use as the upper layer, but a block device can be
provided via the `overlay_root` kernel parameter,
e.g. `overlay_root=vdc`. This device should already contain a
(possibly empty) ext4 filesystem. By using a block device, it is
possible to preserve the filesystem state beyond the termination of
the VM, and potentially re-use it for subsequent VM execution.

### Generation ###

There are two alternatives for providing the build environment. You
can perform the image build in Docker, in which case the only
build-time dependency is that you can launch Docker container directly
(i.e. without `sudo`, etc). To build an image in this configuration,
run from [the root of the firecracker-containerd package](../../Makefile):

`$ make image`

Alternatively, to build outside a container, you'll need:

* To pre-build the `agent` binary, set appropriate permissions and place it
  under the rootfs builder directory.
  * For example, run `make agent` from the root of the firecracker-containerd
    package and copy `agent/agent` to 
	`tools/image-builder/files_ephemeral/usr/local/bin/agent`.
* To pre-build the `runc` binary, set appropriate permissions and place it
  under the rootfs builder directory.
  * For example, run `make -C _submodules/runc static` from the root of the 
    firecracker-containerd package and copy `_submodules/runc/runc` to 
	`tools/image-builder/files_ephemeral/usr/local/bin/runc`
* [`debootstrap`](https://salsa.debian.org/installer-team/debootstrap)
  (Install via the package of the same name on Debian and Ubuntu)
* `mksquashfs`, available in the
   [`squashfs-tools`](https://packages.debian.org/bullseye/squashfs-tools)
   package on Debian and Ubuntu.
* To run the image build process as root.

Then execute `make rootfs.img` from this directory (`tools/image-builder`)

### Usage ###

The generated root filesystem contains all the components necessary
for use with firecracker-containerd, including the runc and agent
binaries.

You can tell the firecracker-containerd runtime component where to
find the root filesystem image by setting the `root_drive` value in
`/etc/containerd/firecracker-runtime.json` to the complete path to the
generated image file.

In order to start the agent at VM startup, systemd should be
instructed to boot to the `firecracker.target` via the kernel
command line.

In order to use the root filesystem as a reusable "lower layer" for an
overlay-based based filesystem, `init=/sbin/overlay-init` should be
the final parameter passed on the kernel command line.

A complete command line, settable via the `kernel_args` setting in `/etc/containerd/firecracker-runtime.json`, is:

    ro console=ttyS0 noapic reboot=k panic=1 pci=off nomodules systemd.journald.forward_to_console systemd.unit=firecracker.target init=/sbin/overlay-init

### Security ###

In order to ensure sufficient entropy is consistently available within 
the VM, the rootfs is configured to start the 
[`haveged`](https://manpages.debian.org/bullseye/haveged/haveged.8.en.html)
daemon during boot. [More information on its method of operation and other
details can be found in its FAQ](https://issihosts.com/haveged/faq.html).
Users of the image created by this utility are encouraged to evaluate 
`haveged` against their security requirements before running any
cryptographically-sensitive workloads inside their microVMs and containers.

For the optional disk-backed writable OS upper and its snapshot lifecycle, see
[the disk overlay baseline guide](../../docs/guest-disk-overlay.md).

## Common image and kernel for the disk baseline

The [disk-baseline runtime example](../../config/disk-baseline.json.example)
selects a read-only ext4 root through ordinary virtio-blk, a private 512 MiB
disk-backed writable OS upper, an empty CPU template, and explicit
`damon_reclaim.enabled=N`. This branch has no pmem-root or guest-reclamation
configuration fields and does not create a balloon by default. Do not supply
a per-VM balloon for the baseline. The generic runtime defaults do not select
this comparison: an omitted guest-root mode retains the original RAM upper,
and `default-rootfs.img` can remain SquashFS. Select the example explicitly.

For the comparison with `damon-virtio-pmem`, build **one common ext4 guest image
on the DSec branch** and reuse its exact bytes on both branches. This includes
the DSec-built guest agent, runc, `/sbin/overlay-init`, packages and all other
image contents. Independently building one image per branch does not satisfy
this requirement, even with matching package versions. On the DSec checkout,
use its pinned worker build and `make image-dax`; the conversion produces
`tools/image-builder/rootfs-dax.img`. Record the source revision, image digest
and embedded agent/runc identities, then install that same file for both arms:

```bash
sudo install -m 0444 "$common_ext4_image" \
  /var/lib/firecracker-containerd/runtime/common-rootfs.ext4
sudo install -m 0444 "$common_kernel_binary" \
  /var/lib/firecracker-containerd/runtime/common-vmlinux.bin
sha256sum /var/lib/firecracker-containerd/runtime/common-rootfs.ext4 \
  /var/lib/firecracker-containerd/runtime/common-vmlinux.bin \
  /usr/local/bin/firecracker
```

`common_ext4_image` must name the DSec-built image and `common_kernel_binary`
the one selected guest kernel binary. Reuse that kernel file verbatim, with the
same kernel configuration, across both arms; equal version strings alone are
insufficient. Both branches retain the official Firecracker/jailer v1.18.0
release and existing SDK pin. No VMM modification is needed for this baseline.
Pass this example's path through `FIRECRACKER_CONTAINERD_RUNTIME_CONFIG_PATH`
in the runtime shim environment, or install it as the dedicated runtime's
`/etc/containerd/firecracker-runtime.json`. Adjust common paths together when
using private install prefixes.

Match vCPU count, guest memory, empty CPU template, SMT, upper capacity,
snapshotter, workload image, host backing, jailer choice and observation
settings. The baseline keeps DAMON disabled and has no balloon/free-page
reporting. The DSec configuration separately selects pmem and DAMON/reporting;
the common ext4 image is not mounted through DAX in the baseline. Verify actual
guest mounts, lower-file DAX state, guest kernel and device state, writable
readback and cleanup on both stacks before collecting results. The shared guest
agent must pass native compatibility checks with each branch's host runtime.

Branch histories and nonoptimization runtime code remain separate. In
particular, the baseline's vHive snapshot/restore path and DSec's native
restore/CRI changes are not made equivalent by sharing the image and kernel.
Those residual runtime and restore differences remain comparison confounds;
do not attribute every difference to pmem or reclamation. This is an ext4
comparison, not a full reproduction of DSec's EROFS design.

### Optional ext4 conversion on this branch

`make image-dax` adds an optional local conversion without changing ordinary
`make image`. The helper is copied verbatim from DSec source
`c15164a93e75f5a5a1435ecca634cf4e291aa2ad`; its builder uses the same Debian
snapshot and e2fsprogs tools. It converts the exact SquashFS tree to a clean,
read-only 512 MiB ext4 image with 4 KiB blocks and no journal, recording image,
source-image, normalized tree hashes and superblock details in sidecars.
Conversion preserves the guest tree; it does not make a baseline-built guest
agent identical to the DSec-built agent. The matched comparison above therefore
uses the one shared DSec-built image rather than separate conversions of the
two branches' independently built images. Build and native validation remain
worker tasks; a successful image conversion alone does not prove guest boot,
DAX, restore compatibility or memory savings.
