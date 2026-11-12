# Step 2: vHive compatibility

> PROVISIONAL HISTORY DRAFT: Step 2 code and inputs match `2e03843816cf95d184ad4ad429adf6ea2ccc1412`. New acceptance and final documentation/results integration are pending. Earlier receipts retain their exact named source scope.

## Current exact-source acceptance

The maintained branch is `firecracker-v1.18-containerdv2-vhive-integration`.
Clean source `7386ab5c5e8c3519ad1698c27f548803e714e304`, tree
`45c82884ea40ff22e5407da36911cf344d6f9e6c`, passed build/private installation,
full-stack regression and all three unchanged provisional vHive VM cases.
The [source acceptance receipt](evidence/upstream-step2-worker-attempts/README.md)
preserves commands, raw logs, artifacts, selected module graphs and failed
earlier attempts. These results belong to that exact source, not this later
documentation tip or a future assembled/published commit automatically.

The stack uses official unmodified Firecracker/jailer v1.18.0 release binaries
and published vHive SDK `dd9db322432989b96cff7a38a64703d60b6cabf9`.
[versions.json](versions.json) is the exact input lock. The full-stack run ended
with `COMMITTED_STACK_ACCEPTANCE_OK` and exit 0: 36 runtime, five snapshotter
and 86 SDK top-level passes. SDK `TestLogAndMetrics` remains skipped; eight
existing StopVM subtests also skip (`SIGKILLFirecracker`, `ErrorExit`, `PauseStop`
and `Suspend`, each ordinary/jailed). Counts exclude nested cases and the
separate vHive tests. Dedicated test pools were removed and the checkout stayed
clean; unrelated/shared worker resources were preserved.

The three provisional cases passed independently:
`TestPauseSnapResume` (15.50 s), `TestSnapshotConcurrentClones` (28.12 s), and
`TestSnapshotSourceMemoryAndDiskIsolation` (21.73 s). They validate continued
source execution, independent live clones, captured task/exec state and private
disk continuity, and successful graceful stops. Managed tasks now stop before
final agent cancellation. The preceding `9000` dependency-cleanliness failure
is preserved; `7386` changes only the existing dependency's direct/indirect
classification and passes the clean full-stack rerun without changing tests or
acceptance timeouts. The rejected speculative drain and diagnostic code remain
outside corrective implementation ancestry.

**Final published-runtime-module and installed-vHive consumer acceptance remain
pending.** The provisional harness uses an explicitly recorded local runtime
replacement. Freeze/publish the functional Step 2 commit before finalizing the
consumer module/ready installation manifest, avoiding a circular SHA pin. Native
restore and CRI have later milestone gates; Kubernetes Pod checkpoint/restore
remains deferred to the 1.38 interface work.

## Migrated legacy profile

The legacy wire fields retain numbers 15–21. CreateVMResponse adds generic
restored FIFO endpoints at field 6. PauseVM freezes running workload tasks,
prepares both FIFO endpoints and seals the guest event acknowledgement before
pausing vCPUs. CreateSnapshot publishes memory/state and then the required
`<SnapshotPath>.drives.json` sidecar (version 2, sealed transport cut version 1).
ResumeVM reconnects retained source streams before thawing tasks. LoadSnapshot
maps independent root/workload backings at captured paths in a private mount
namespace and adopts the existing guest task and exec processes. The supported
legacy profile remains unjailed, read-only guest root, one writable workload
drive, and File memory; Uffd is schema-supported but has no acceptance here.

[tools/vhive/test-vhive.sh](../../tools/vhive/test-vhive.sh) uses the exact clean
vHive test-source revision in the input lock. Its disposable build binds this
clean runtime archive as a local Go module and records that substitution; this
is provisional candidate coverage, not published-consumer acceptance. The
selected tests exercise source control/stdio after capture, independent live
clones, guest process memory continuity and disk isolation. The recorded worker
test source is `89593103ac667cc096f89770c94a77b2b93c3a7d`, supplied through a local Git
repository or bundle containing that exact commit; the harness still verifies
the pinned revision and records the source repository. The default organization
URL requires that exact source revision to be available there. Run on a separate
privileged KVM worker after building the exact runtime stack. Compilation alone
does not establish this acceptance. The final vHive runtime
module pin and ready installation manifest follow publication of the accepted
Step 2 commit, avoiding a circular pin.

The old `modern-stack.patch` and probe are retained as historical reproduction
artifacts; the migrated harness does not apply that obsolete client patch.

## Current reproduction and artifact boundaries

Use an exact clean checkout of the tested source, with the pinned tools,
passwordless sudo, Docker and KVM described in the Step 1 worker setup. Keep new
logs and receipts rather than overwriting the archived originals.

```sh
test "$(git rev-parse HEAD)" = 7386ab5c5e8c3519ad1698c27f548803e714e304
test -z "$(git status --porcelain)"
export STACK_DIR=/users/nehalem/fc-ctrd-mod
export PATH="$STACK_DIR/toolchains/go1.26.11/bin:$PATH"
: "${VHIVE_SOURCE_REPOSITORY:?Set the local Git repository or bundle containing the pinned vHive test source}"
export VHIVE_SOURCE_REPOSITORY
tools/modernize/build-stack.sh
tools/modernize/install-stack.sh
tools/modernize/test-stack.sh
tools/vhive/test-vhive.sh
```

`bin/receipts/acceptance` records the final test binaries, agent build metadata,
module graph and test-image identity. The full-stack log records clean tidy and
protobuf generation. `VHIVE_LOGS` records the separate provisional run's source,
local substitution, binary hashes, selected image and daemon/test output. See the
combined receipt for the distinct installed rootfs and rebuilt test-rootfs
hashes; their byte identity is not asserted. Final installed-consumer validation
must use the finalized published dependency and verified installation receipt.

## Historical accepted custom stack

The functional acceptance revision for this milestone is
`4e0033b81a17d9a32c2861263c2792ebcfe37ebd`. This is the tested runtime revision,
not a claim that every later documentation or maintenance commit reran the full
suite. Its clean worker checkout passed build, private installation,
36 runtime, five snapshotter and 79 SDK tests (one existing SDK metrics skip),
plus two independent standalone vHive deploy/invoke/snapshot/source-stop/
restore/cleanup runs. The [acceptance receipt](evidence/4e0033b-fifo-acceptance-receipt.log)
records the tested source, artifacts, process identities and cleanup. It also
preserves two failed harness attempts caused by lint-lock contention and an
overlong temporary path; the successful full rerun used a short private temp
directory without source changes.

The accepted correction prepares telemetry hard links or cross-filesystem bind
mounts before runc starts. Its real-VM regression verified delivery to both
external FIFO readers, and all eight jailed/unjailed `TestCreateVM_Isolated`
cases passed. This runtime coverage accompanies a standalone, unjailed vHive
deployment. It does not validate Kubernetes or Pod checkpoint/restore.

Commits after that full acceptance contain bounded maintenance checks, not a
new runtime or vHive run: install failure propagation at `46b2fa2`, task-test
synchronization at `b2df9bc`, and cache eviction test synchronization at
`be200ad`. The [install receipt](evidence/46b2fa2-install-guard-receipt.log),
[task-test receipt](evidence/b2df9bc-task-wait-sync-receipt.log), and shared
[cache-test receipt](evidence/step4-cache-test/verification.json) document
their scope and results. These checks do not change the functional acceptance
revision or dependency pins.

The current Step 2 source also carries two focused production corrections from
Step 1: jailed VM socket path reporting (`93d594c`) and demux SIGPIPE handling
(`e09cf27`, carried at this branch's source tip `7c8d2ce`). On clean source
`7c8d2ce1fd562323b447f08a64b0d0f5e557247c`, both correction regressions and
`make lint` passed. The [maintenance receipt](evidence/step2-socket-sigpipe-maintenance.md)
records the commands and exact results. These targeted checks did not rerun the
full runtime or vHive acceptance; `4e003b` remains the accepted Step 2 runtime
revision.

## Functional validation history

Earlier full acceptance runs covered the failed-create correction (`d959a78`) and
metrics/I/O corrections (`2780f2e`). This branch started from Step 1 completion
commit `207899bf3c45ad83d07b06d161b4dcec4a0c338b`, and carries the validated
Step 1 task-create cleanup fix at `6585eba`. That fix releases a reserved drive
and owned bundle links after a failed guest create; its [Step 1 receipt](evidence/6585eba-acceptance-receipt.log)
records full acceptance. The earlier vHive integration checkpoint `122d7084043a`
is retained in its [receipt](evidence/122d7084043a-acceptance-receipt.log).
Each result remains tied to its own source revision and receipt.

The supported profile is the pinned standalone vHive orchestrator with
devmapper, one function container per unjailed VM, an immutable guest root, and
Firecracker's File memory backend. The worker test runs vHive's orchestrator
and function gRPC services, containerd, the shim, guest agent, runc, and KVM.
The stock containerd expected by vHive's legacy CRI proxy runs in the test
container with separate state and socket paths. This acceptance does not cover
Kubernetes or Pod checkpoint/restore.

## Historical port and inputs

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

## Historical reproduction

In a fresh clone from the project's configured remote, check out the
Step 2 functional acceptance revision `4e0033b81a17d9a32c2861263c2792ebcfe37ebd`.
Use the [Step 1 worker setup](step-1.md)
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

## Cache eviction test maintenance

Commit `be200ad` fixes the timing-dependent eviction error test
observed in [CI run 43079780458](https://github.com/vhive-serverless/firecracker-containerd/actions/runs/43079780458).
The test now waits for the reaper error log, checks its text, and verifies the
entry was removed. Production code is unchanged. The clean committed worker
checkout passed 200 targeted repetitions with one scheduler thread, 50 targeted
race repetitions and the full cache package under the race detector.
The [shared receipt](evidence/step4-cache-test/verification.json) records all four
validated milestone backports, exact commands and raw hashes. The old failure
was not reproduced in 30 baseline repetitions; these checks are test maintenance,
not new full-stack acceptance. Step 3 CRI and Kubernetes restore remain WIP.
