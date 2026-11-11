# Step 2: vHive compatibility

Latest committed revalidation: `4e0033b81a17d9a32c2861263c2792ebcfe37ebd`.
The clean worker checkout `/users/nehalem/fc-ctrd-mod/vhive-io-committed`
passed build, private installation, 36 runtime, five snapshotter and 79 SDK
cases (one existing SDK metrics skip). Two independent vHive
deploy/invoke/snapshot/source-stop/restore/cleanup runs also passed.
The [FIFO correction receipt](evidence/4e0033b-fifo-acceptance-receipt.log)
records exact binary, image and log hashes, process identities, cleanup, and
two retained unsuccessful harness attempts: shared lint-lock contention and
an overlong temporary path. The successful full acceptance rerun used a short
private temporary directory; no source change was needed for those retries.

This backports the shared jailer FIFO ordering correction from
`c7d73c703d18d33e07b39c2785dbcb1f77af039f`. Telemetry hard links or
cross-filesystem bind mounts are prepared before runc starts. The new real-VM
`TestJailerTelemetryCrossFilesystem_Isolated` verified delivery to both
external FIFO readers, and the full `TestCreateVM_Isolated` matrix passed.
Dependency pins and earlier acceptance receipts remain unchanged. The vHive
profile below remains the standalone unjailed deployment.

Earlier metrics revalidation: `2780f2e92d8a376e6514d1e099b2c03b0e1847ed`. The clean worker checkout
`/users/nehalem/fc-ctrd-mod/vhive-io-committed` passed build, private installation,
35 runtime, five snapshotter and 79 SDK tests (one existing metrics skip).
Two independent vHive deploy/invoke/snapshot/source-stop/restore/cleanup runs also passed.
The [metrics receipt](evidence/2780f2e-metrics-acceptance-receipt.log) records exact artifact and log
hashes. This includes the I/O replacement correction and a metrics teardown
correction that returns ports under a mutex and makes shutdown idempotent.
Earlier acceptance records below retain their original tested revisions.

The earlier validated runtime is `d959a78de45faf1d8d13fea282b7fb980b629789`
on `vhive-compatible`. Its clean worker checkout
`/users/nehalem/fc-ctrd-mod/vhive-failed-create-committed` passed build,
installation, 35 runtime and 5 snapshotter isolated tests, and 79 SDK tests
with the existing SDK log/metrics skip. Two independent vHive deployment,
invocation, snapshot, source-stop, new-VMM restore and cleanup runs also passed.
The [earlier receipt](evidence/d959a78-acceptance-receipt.log) records inputs,
process identities, artifact hashes, log hashes and cleanup.

The supported deployment is the pinned vHive standalone orchestrator with
devmapper, a single function container per unjailed VM, an immutable guest root,
and Firecracker's File memory backend. The acceptance test runs vHive's
orchestrator and function gRPC services, containerd, the shim, the guest agent,
runc, and KVM on the worker in `worker.txt`. The stock containerd instance that
vHive's legacy CRI proxy expects runs in the test container with separate state
and socket paths. This is the vHive milestone; Kubernetes and Pod checkpoint
integration are separate.

This branch started from Step 1 completion commit
`207899bf3c45ad83d07b06d161b4dcec4a0c338b` and includes the later validated
shutdown correction `e7395acb2c88cfe8c5d32ba61ff65c17e031858c` and task
creation cleanup correction `6585eba92db4f8c55f6567cf8d2c8d695adcba05`.
The [Step 1 receipt](evidence/6585eba-acceptance-receipt.log) records its full
acceptance. The latter correction releases a reserved drive and owned bundle
links after a failed guest create, allowing a healthy container to reuse the
same one-slot VMM. The previous vHive completion at `122d7084043a` remains
documented in its [receipt](evidence/122d7084043a-acceptance-receipt.log).
The VMM, SDK and vHive source pins below are unchanged by this correction.

## Port and inputs

| Component | Source |
| --- | --- |
| vHive | `1f373249770f51bd930939471607b14a04f9b708` from [vhive-serverless/vHive](https://github.com/vhive-serverless/vHive), plus [the checked-in patch](../../tools/vhive/modern-stack.patch) |
| Firecracker | [`vhive-compatible`](https://github.com/JooyoungPark73/firecracker/tree/df2d5b98ffe7e62bd367371e962fc00b0ee26e63), `df2d5b98ffe7e62bd367371e962fc00b0ee26e63`, based on v1.18.0 (`eaf83bb718a559d324613d566eaf776b31a62340`) |
| Go SDK | [`vhive-compatible`](https://github.com/JooyoungPark73/firecracker-go-sdk/tree/51a10ee490d4627076b887c7defa0c05399e8d21), `51a10ee490d4627076b887c7defa0c05399e8d21`, module replacement `v0.0.0-20261111001616-51a10ee490d4` |
| Containerd | 2.3.7 (source `6f589cf13dc01a5b493911355427e05b2ff005d7`), API 1.11.1; vHive's containerd 1.7.33 Go client still uses the same gRPC endpoints |
| Function image | `ghcr.io/ease-lab/helloworld@sha256:12dc6715ed1a8306f246ceaf7742c09e38a52a79d17421e4a50d7e0e09fdbc25` |

The full lock is [versions.json](versions.json); [modules.txt](modules.txt)
records the Go modules. Guest, CNI, runc, and snapshotter versions carry forward
from [Step 1](step-1.md). The build checks the SDK's Swagger byte for byte
against the selected Firecracker source. The SDK's models and operations were
regenerated using go-swagger v0.33.1. The [VMM parser receipt](evidence/df2d5b98ffe7-vmm-parser-receipt.log)
records the worker test from the exact pushed Firecracker commit.

The old `vhive-v1.8.3` runtime branch (`bb007a1ef569`, also pinned by the
vHive client) supplies the external network namespace, pause/resume, snapshot
creation and restore wire fields. The `firecracker-v1.13.1-vhive-integration`
branch (`d65c452e6bcd`) also includes diff snapshot configuration, demux
connection fixes, and experimental shim and VM prewarming. The current vHive
client does not request prepared shims. The port preserves the wire numbers
used by vHive, carries forward the Step 1 demux and crash recovery changes, and
passes diff tracking and File/Uffd memory backend selection through to the SDK.

The legacy Firecracker fork (`235000e84556`) chose the container drive by
position and a path substring. The v1.18 port instead supplies explicit drive
IDs to the restore request and updates their backings before virtio block
devices are opened. The immutable root can be reopened at the configured root
image path. The workload drive must be a separate writable device. The runtime
joins vHive's existing network namespace, supplies the host TAP binding and
the new vsock path, resumes the VM, and connects to the guest agent. Network
namespace ownership stays with vHive.

The vHive patch leaves kernel arguments to the modern runtime. It captures the
workload's ext4 block image while the source is paused, after the VMM writes its
state and memory. A file-level rsync batch cannot reproduce the ext4 metadata
and journal that live guest memory refers to. Restore writes the complete image
into a newly allocated devmapper device of the same size. VMM snapshots reset
the source's old vsock transport; the source can serve TCP invocations after
resume. To dispose of a captured source or restored function, vHive pauses and
stops the VMM before removing the device and returning its network to the pool.

## Reproduce on the worker

In a fresh clone from the project's configured remote, check out the
`vhive-compatible` completion commit. Use the [Step 1 worker setup](step-1.md)
for the pinned tools, sudo, KVM, and Docker. Then run:

```sh
git checkout --detach 4e0033b81a17d9a32c2861263c2792ebcfe37ebd
export STACK_DIR=/users/nehalem/fc-ctrd-mod
export TMPDIR=/tmp/s2f-4e0033b
mkdir -p "$TMPDIR"
tools/modernize/build-stack.sh
tools/modernize/install-stack.sh
tools/modernize/test-stack.sh
tools/vhive/test-vhive.sh
tools/vhive/test-vhive.sh
```

The build records inputs and binary hashes in `bin/receipts/build`. Installation
uses `~/fc-ctrd-mod/install/<runtime-commit-12>`. The runtime tests record
their image and binary hashes in `bin/receipts/acceptance`; they run the
privileged SDK suite against the built Firecracker and jailer. The vHive
script builds the patched pinned source, starts the two containerd daemons
and vHive in a private Docker network, and uses a dedicated devmapper pool.
Host IPC and the udev control socket are needed for devmapper udev
synchronization. No host containerd socket is mounted.

`VHIVE_LOGS` points to the directory with the revisions, base test image ID,
binary hashes, daemon logs, and the acceptance transcript. The script checks
two warm invocations on the source, nonempty VMM/memory/disk artifacts,
source VMM and device removal, a cold invocation on the restored VM, another
warm invocation, and removal of the restored VMM and its device. It verifies
distinct VMM identities and block devices, no cold boot containerd task on
restore, and cleanup of the vHive network pool and snapshot directory.
`VHIVE_DEPLOY_INVOKE_SNAPSHOT_RESTORE_OK` marks in-container success;
`VHIVE_WORKER_ACCEPTANCE_OK` follows removal of the worker thin pool.

Snapshot files from the old VMM and rsync batches are incompatible with this
deployment. The worker acceptance covers File snapshots on one host and the
standalone vHive profile. Jailed, multi-drive, remote snapshotter, userfaultfd,
and prewarmed shim combinations are outside that profile. The vHive path
restores the guest function; it does not rebuild Kubernetes or containerd
task ownership inside a restored VM.
