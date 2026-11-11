# Step 1: modernized runtime

Branch: `modernize-runtime`. The latest validated implementation is
`b7e5c426e2f91ad90adb67dbbbe4ecb91dc62e9f`. Its clean worker checkout
`/users/nehalem/fc-ctrd-mod/step1-io-committed` passed build,
installation and full acceptance: 35 isolated runtime tests, 5 snapshotter
tests and 79 SDK tests, with the existing SDK log/metrics skip. The
[current receipt](evidence/b7e5c42-io-acceptance-receipt.log) records revisions,
artifact hashes, raw log hashes and cleanup. Earlier acceptance and native
deployment evidence remain below.

The latest correction closes blocked proxy streams, cancels pending vsock
accepts and drains replaced readers before reconnecting I/O. An older proxy's
completion can no longer close its replacement. The
[regression before correction](evidence/step1-io-close-before.log) reproduced
the blocked reader; [committed race tests](evidence/b7e5c42-io-race.log) also
exercise repeated replacement against a real FIFO. The full base acceptance
passed 119 top-level cases (35 runtime, five snapshotter and 79 SDK), with only
the existing SDK metrics skip. The earlier task creation recovery acceptance
remains in its [receipt](evidence/6585eba-acceptance-receipt.log).

The installed stack pulled an OCI image and ran it inside Firecracker, where
it wrote and read back a file on its ext4 container root. A second native test
started a task, used exec to write a file, restarted the controller, and used exec
again to verify the file. It then killed the shim and verified that the VMM,
VM directory, and containerd task bundle were removed.

## Stack and compatibility

`versions.json`, `go.mod`, `go.sum`, the child module manifests, and the gitlinks
pin the complete source graph. The selected VMM is upstream Firecracker v1.18.0,
commit `eaf83bb718a559d324613d566eaf776b31a62340`. No VMM source modification is
needed for this step. The SDK `modernize-runtime` branch preserves the earlier
`feat/update-firecracker-1.14` work and adds Swagger v1.18 generation with
`go-swagger v0.33.1`; its pushed commit is
`0d7a093c9b0cee5b93d0b9551888a571bb478154`.

The runtime uses containerd v2.3.7 (API v1.11.1), runc v1.4.2, CNI plugins v1.9.1,
CNI library v1.3.0, and stargz-snapshotter v0.18.2. The retained tc-redirect-tap
revision `34bf829e9a5c99df47318c7feeb637576df239fc` is still the upstream HEAD
verified on the worker; it is intentionally pinned rather than following HEAD.
The stargz v0.19.1 module would force containerd v2.4.2 and is not part of this
stack. The remote-snapshotter child module repeats the SDK replacement because
Go does not inherit replacements from dependency modules.

The shim uses containerd's version 3 ttrpc task service and the current manager
bootstrap interface. Containerd config version 4 requires explicit gRPC and ttrpc
server plugin addresses; the runner checks the effective addresses before launch.
The devmapper transfer unpack configuration is explicit as well.

## Guest and storage

The guest is Debian trixie from the dated Debian archive in `versions.json`.
Custom files preserve the distribution's usrmerge symlinks. The Amazon Linux
2023 kernel source is pinned at
`7483450dec64b4f923037de31d8c28298a7c3359`, tag
`kernel6.18-6.18.56-126.188.amzn2023`, built as `6.18.56-fcmod`.
The configuration derives from the pinned Firecracker release CI configuration
and enables command-line virtio MMIO devices. The legacy `noapic` argument caused
ACPI/virtio initialization failures on the new kernel and has been removed.
Unified cgroups are enabled. See `baseline-audit.md` for original provenance.

Actual guest inspection confirms the preserved layout: read-only squashfs guest
base, tmpfs guest overlay upper, and separate ext4 container roots on host-backed
devmapper thin devices. Host backing files reside on `/dev/sda3` (ext3), not tmpfs.
The persistent guest overlay redesign belongs to Step 4. The default devmapper container path already
has a disk-backed writable layer. The optional guest stargz service instead mounts
its store on tmpfs; its verified container overlay upper/work directories live
under that store. Step 4 must also account for that separate path. Small runtime tmpfs mounts remain distinct.
The stargz `volume-init` helper is statically linked because it runs inside the
selected workload image, which may have a different libc from the guest.

Thin pools now have separate backing files and loop devices for each name. Removal
is scoped to the selected pool and detaches its owned loop devices. The old LVM
helper's global snapshot purge and global page-cache drop have been removed.

## Recovery

Private atomic `recovery.json` metadata records shim/VMM process identities,
resolved CNI configuration, namespace ownership and the configured runc binary.
After a shim exits, its parent or containerd's delete helper kills only the
recorded VMM, removes its runc jail and CNI allocation, and removes an owned network
namespace. PID start times plus pidfds prevent signalling an unrelated reused PID.
Cleanup errors retain metadata; reuse of that VM identifier requires successful
recovery first. This is VM crash cleanup, not checkpoint/restore.

Runc operations verify the recorded bundle path before signalling or deleting a
jail. Explicit VM IDs must be globally unique across namespaces. The shim leaves
recovery metadata for its parent, which completes CNI cleanup before releasing
the VM's sockets. StopVM waits for this cleanup, including empty private CNI cache
directories. Custom or shared CNI cache directories are preserved.
The shim drains control replies on shutdown, publishes the VM stop event before
exit, and reserves stdout for the containerd bootstrap and delete protocols.
Both crash tests check that containerd removes the task bundle as well as the VM.

## Reproduction procedure

All compilation and execution must run on `nehalem@amd218.utah.cloudlab.us`.
The default workspace is `/users/nehalem/fc-ctrd-mod`. The scripts support
`STACK_DIR` for a separate workspace. Use a fresh SSH connection after provisioning
if it adds the Docker group.

```bash
set -euo pipefail
export STACK_DIR="$HOME/fc-ctrd-mod"
mkdir -p "$STACK_DIR/evidence"
checkout=$(mktemp -d "$STACK_DIR/step1-repro.XXXXXXXX")
git clone --branch modernize-runtime --recurse-submodules \
  https://github.com/vhive-serverless/firecracker-containerd.git "$checkout"
cd "$checkout"
git checkout --detach 6585eba92db4f8c55f6567cf8d2c8d695adcba05
tools/modernize/prepare-worker.sh
# If provisioning added the Docker group, reconnect and return to this checkout.
revision=$(git rev-parse --short=12 HEAD)
run_name="fcmod-repro-$revision-$(date +%s)"
work_dir="$STACK_DIR/$run_name"
logs="$STACK_DIR/evidence/$run_name"
mkdir -p "$logs"
tools/modernize/build-stack.sh 2>&1 | tee "$logs/build-stack.log"
tools/modernize/install-stack.sh 2>&1 | tee "$logs/install-stack.log"
INSTALL_DIR="$STACK_DIR/install/$revision" RUN_NAME="$run_name" WORK_DIR="$work_dir" \
  tools/modernize/run-worker.sh 2>&1 | tee "$logs/lifecycle.log"
sudo -n env RUNTIME_SRC="$PWD" WORK_DIR="$work_dir" \
  RUN_NAME="$run_name" RESTART_CONTROLLER=1 \
  python3 tools/modernize/test-crash-recovery.py \
  2>&1 | tee "$logs/controller-recovery.log"
tools/modernize/test-stack.sh 2>&1 | tee "$logs/acceptance.log"
```

`test-stack.sh` rebuilds the test image with the checkout's revision tag, runs
unprivileged and root unit checks, lint, tracked-module tidy, forced protobuf
regeneration, the complete runtime and snapshotter integration suites, thin-pool
ownership/cleanup tests, and the exact pinned SDK suite from its own clean clone.
The native recovery test kills the dedicated controller, reconnects to the existing
VM, checks its filesystem state, and then kills its shim and verifies VM cleanup.
Test networking is isolated; the SDK's native jailer test uses the host cgroup
namespace for cgroup v2 and removes its specifically owned test cgroup afterward.
`ALLOW_DIRTY=1` exists for development only; it is not valid milestone evidence.
`bin/receipts/` records revisions, Go module graph, binary hashes and build metadata.
The install script uses a dedicated prefix and does not replace the worker's Docker
runtime binaries. Dedicated systemd units, sockets, state and thin pools isolate
these tests from the distro containerd and baseline runtime.

## Initial worker results and subsequent corrections

The worker ran Ubuntu 24.04.5 LTS and Linux 6.8.0-147-generic on an AMD EPYC 7302P
with 32 logical CPUs, about 125 GiB RAM, and KVM. The tested installation is
`/users/nehalem/fc-ctrd-mod/install/e666612819bc`. Its `bin/` contains
`firecracker-containerd`, `firecracker-ctr`, `containerd-shim-aws-firecracker`,
`firecracker`, and `runc`. The installed guest inputs are in `share/runtime/`.
The daemon reported containerd `2.3.7+unknown` and the exact runtime revision;
runc reported `1.4.2`, Go `1.26.11`, and libseccomp `2.5.4`.

| Gate | Worker result |
| --- | --- |
| Build, install, and input verification | Passed with the pinned VMM, SDK, runc, kernel, and guest inputs. |
| Unit tests and generated code | Unprivileged and root suites, lint, tracked-module tidy, and forced protobuf regeneration passed. |
| Runtime integration | 33 passed. Includes CNI, runc jails, CPU and memory accounting and OOM, volume and drive operations, disk exhaustion, signals, exec and attach, logs and exact binary output, startup failures, pause and resume, event ordering, and repeated graceful and crash cleanup. |
| Snapshotter integration | 5 passed. Includes metrics, single and multiple container launch, and a volume copied from a guest image. |
| Native recovery | Controller restart preserved a live task's filesystem state; shim crash cleanup removed the VMM and task bundle. |
| Thin pools and SDK | Pool isolation and cleanup passed. The pinned SDK passed 79 tests, including privileged KVM, jailer, snapshot, and restore cases. Its existing `TestLogAndMetrics` skip remains. |

The full acceptance run ended with `COMMITTED_STACK_ACCEPTANCE_OK`. Its test
pools and SDK test cgroup were removed. The [acceptance receipt](evidence/e666612819bc-acceptance-receipt.log)
contains individual results, binary hashes, and the full log's checksum.
See the [native lifecycle](evidence/e666612819bc-native-lifecycle.log),
[native recovery](evidence/e666612819bc-native-recovery.log),
[build receipt](evidence/e666612819bc-build-receipt.log), and
[worker toolchains](evidence/e666612819bc-worker-toolchains.log) for the exact
paths and versions. Raw build, install, and test logs remain on the worker under
`/users/nehalem/fc-ctrd-mod/evidence/e666612819bc-*.log`.

A fresh repeat at `61cf6e590d4856381839858b9596d30d6197e771` reproduced an
intermittent attach failure. The test now holds the old FIFO writer open until
the new attachment receives the guest's echo, then closes stdin. The volume
helper also decodes a complete JSON request without requiring the caller to
close stdin before the asynchronous copy finishes.

The resulting `0d8e700f68881516637778c57dbc9ee12ac48b10` passed clean
build, install, native lifecycle and recovery, and the full acceptance suite
in `/users/nehalem/fc-ctrd-mod/step1-repro.SwuCJ3fv`. The full acceptance log
is `evidence/0d8e700f6888-acceptance.log` in the worker workspace; SHA-256
`4c09141c78b07953a9708334a81f0e60912d3f0410a4f17cce55c84e9cd81dce`.

Later vHive testing found that a forced stop could return while an unjailed
VMM was still running when the SDK selected the default binary. The shim now
records the VMM's process identity for both binary selection paths and waits
for the owned process to exit before removing its state. The regression test
pauses a VM and checks that forced stop removes it. The startup tests open
their log and metrics readers before calling CreateVM, so an early VM exit
cannot leave a blocking FIFO open behind.

Containerd reports process exit separately from completion of the client's
output copy. A Go client that needs all foreground output should call
`task.IO().Wait()` before deleting the task: containerd 2.3.7's Delete
cancels the client FIFO readers. The binary output test follows that sequence
and checks the byte count and SHA-256 through a slow reader.

The FCControl listener also drains controller connections before the shim
exits. The controller closes its per-call connection after consuming the stop
reply and before waiting for shim exit. This acknowledgement prevents a
paused VM's forced-stop status from racing with transport shutdown; the
shutdown context still bounds a client that keeps its connection open.

Later CRI testing exposed a failure path in the earlier runtime: after a guest
runc create failed, an explicitly retained VM kept the reserved root drive. A
healthy container could then fail because the VM had no free drive slot. The
host now rolls back that drive and its owned bundle links with a separate,
bounded cleanup context. Guest unmount tolerates a bundle already removed by
the failed runc create. Drive tracking is synchronized and completed entries
are removed.

The new regression uses the same one-slot VMM for three failed and healthy
create cycles, checks application output and task deletion, verifies that the
VMM identity is unchanged and checks removal of the failed bundle link. The
same test fails against the earlier `e7395ac` image with no remaining drives.
Commit `6585eba` passed the complete committed acceptance procedure, including
that regression. Its installation is
`/users/nehalem/fc-ctrd-mod/install/6585eba92db4`; the VMM and SDK pins are
unchanged. The current receipt records both the negative control and the full
acceptance evidence.

The runtime branch is pushed to `vhive-serverless/firecracker-containerd`, and
the SDK branch and pinned revision are pushed to `JooyoungPark73/firecracker-go-sdk`.
The Firecracker gitlink uses the existing upstream release tag and needs no VMM
fork change. The enclosing workspace has three independent repositories and no
root submodule references to update.

## Limits of this milestone

This procedure was tested on the designated x86-64 worker. It reconstructs the
pinned stack and records artifact hashes; it does not require byte-identical
artifacts across hosts. Worker packages and build utilities resolve from their
distribution archives, and their tested versions are in the toolchain receipt.
The guest storage layout is intentionally unchanged. CRI, Kubernetes Pod
checkpoint/restore, and the memory experiments belong to subsequent milestones.
The original baseline and its actual Linux 4.14.174 kernel are documented in
`baseline-audit.md` with a separate storage trace.
