# Step 1: modernized runtime

Status: implementation under worker validation; **not a completed milestone**.
The completion revision and final committed-checkout results will be recorded only
when every Step 1 gate passes. Later milestone branches have not been created.

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

## Candidate reproduction procedure

All compilation and execution must run on `nehalem@amd218.utah.cloudlab.us`.
The default workspace is `/users/nehalem/fc-ctrd-mod`. The scripts support
`STACK_DIR` for a separate workspace. Use a fresh SSH connection after provisioning
if it adds the Docker group.

```bash
set -euo pipefail
mkdir -p "$HOME/fc-ctrd-mod/evidence"
cd "$HOME/fc-ctrd-mod"
git clone --branch modernize-runtime --recurse-submodules \
  https://github.com/vhive-serverless/firecracker-containerd.git runtime
cd runtime
tools/modernize/prepare-worker.sh
# In a new SSH session, enter the same committed checkout:
tools/modernize/build-stack.sh 2>&1 | tee ../evidence/build-stack.log
tools/modernize/install-stack.sh 2>&1 | tee ../evidence/install-stack.log
INSTALL_DIR="$HOME/fc-ctrd-mod/install/$(git rev-parse --short=12 HEAD)" \
RUN_NAME=fcmod-validation WORK_DIR="$HOME/fc-ctrd-mod/validate-run" \
  tools/modernize/run-worker.sh 2>&1 | tee ../evidence/validation-lifecycle.log
tools/modernize/test-stack.sh 2>&1 | tee ../evidence/acceptance.log
sudo -n env RUNTIME_SRC="$PWD" WORK_DIR="$HOME/fc-ctrd-mod/validate-run" \
  RUN_NAME=fcmod-validation RESTART_CONTROLLER=1 \
  python3 tools/modernize/test-crash-recovery.py \
  2>&1 | tee ../evidence/controller-recovery.log
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

## Evidence to date

The original committed baseline was built and ran OCI containers on the worker;
its old guest kernel was actually 4.14.174, not the version implied by newer
configuration files. The baseline evidence and full storage trace are under
`evidence/` and `baseline-audit.md`.

Development validation has passed SDK unit tests, a real VMM release API test,
privileged KVM lifecycle tests, SDK tests from its pushed commit, runtime short
tests, protobuf regeneration, native VMM/kernel/rootfs builds, real OCI lifecycle
and filesystem writes, guest cgroup v2 inspection, separate-pool ownership and
cleanup, and a real shim SIGKILL recovery test. These development results do not
replace the pending committed-checkout build/install and full acceptance suite.

The committed implementation checkpoint `c21a0e8` independently built and
installed on the worker, ran an OCI container from its installed prefix, passed
root unit tests, and survived controller restart followed by shim-crash cleanup.
Subsequent worker tests passed explicit cgroup v2 memory/CPU limits, accounting and
throttling, three CNI/runc-jail crash-cleanup iterations, exact 4 MiB binary output
under a slow consumer, and stargz image execution with filesystem writes. The SDK
snapshot-load regression found by its complete privileged suite has been fixed
and pushed. Tests cover snapshot load with a memory backend and SSH content checks.

The complete final acceptance script is still pending on the current completion
candidate. Earlier development runs and the checkpoint above are evidence of
progress; they are not a declaration that this milestone is complete. No
Step 2, CRI, Kubernetes checkpoint/restore, or memory-efficiency outcome is claimed.
