# Step 2: vHive compatibility

## Current exact-source coverage and unfixed CNI flake

The maintained branch is `firecracker-v1.18-containerdv2-vhive-integration`.
Exact source `2e03843816cf95d184ad4ad429adf6ea2ccc1412`, tree
`76c862aa60162a0b510d7626d523f047709e48d9`, has complete required check coverage
across separately recorded runs. **Its original full-stack invocation exited 2,
not 0; the observed existing CNI chain-creation flake remains unfixed.** The
[combined receipt](evidence/upstream-step2-2e03843816cf/README.md) preserves the
failed transcript, all diagnostic repeats, passing tail and exact provenance.
This later documentation/cumulative-history tip was not itself tested.

Build/private install, test-image preparation and three unchanged provisional
vHive cases passed: `TestPauseSnapResume` 16.29 s,
`TestSnapshotConcurrentClones` 28.34 s, and
`TestSnapshotSourceMemoryAndDiskIsolation` 21.30 s. The full-stack attempt
completed input/lint/package/tidy/proto/clean-diff checks, all 36 runtime tests
and four snapshotter tests (40 top-level passes), but the fifth snapshotter
case failed on CNI firewall ADD: iptables `nf_tables` exit 4, `CNI-FORWARD`
chain already exists. The subsequent case passed because the snapshotter
Makefile deliberately uses `-k`; it was not silently omitted.

That exact multi-VM case passed in three fresh-container diagnostics at
7.93/7.45/7.76 s, retaining default five concurrent VMs and unchanged source,
image and assertions. The separately run original thinpool/SDK tail passed
with 86 SDK top-level passes and the existing `TestLogAndMetrics` skip. Eight
nested StopVM skips remain in the original log. All original required check
identities and the provenance/cleanup epilogue are covered; no check was
removed or weakened. **Do not report these segments as one green full run or
claim `COMMITTED_STACK_ACCEPTANCE_OK` for the failed invocation.** The tail's
own marker is `STAGE2_REMAINING_THINPOOL_AND_SDK_CHECKS_OK`.

The CNI configuration, plugin pins and five-VM test are byte-identical to
accepted earlier sources. Pinned firewall v1.9.1 performs a non-atomic chain
existence check/create and tolerates existing-chain exit 1 but not the observed
nftables exit 4. The source diagnosis supports an existing dependency race;
concurrent fresh VM networking can still fail. No runtime fix, warm chain,
reduced concurrency, skipped test or silent retry was used to obtain coverage.

The unchanged [input lock](versions.json) selects official unmodified
Firecracker/jailer 1.18.0, published SDK
`dd9db322432989b96cff7a38a64703d60b6cabf9`, containerd 2.3.7 and runc 1.4.2.
The tail verifies clean exact source, byte-identical module graph, clean agent
VCS metadata, all 12 unchanged artifact hashes and exact existing image ID.
Owned resources were independently verified cleaned; unrelated worker resources
were preserved. Earlier [7386 clean acceptance](evidence/upstream-step2-worker-attempts/README.md)
and every failed attempt retain their original scope.

**Final published-runtime-module and installed-vHive consumer acceptance remain
pending.** The provisional harness still uses explicit local runtime replacement.
Publish the actual functional Step 2 tip before finalizing the consumer module
and ready installation manifest. Native/CRI gates belong to later milestones;
full CRI remains WIP and Kubernetes Pod restore is deferred to 1.38.

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

For same-ID unjailed restore, the live shim directory is exposed only at
`/.firecracker-restore-shared`, used as the VMM working directory for short
API/vsock names. Captured disk paths remain independent private mappings at
original addresses. State/memory inputs have explicit read-only binds and
logging has explicit writable binds. Additional disks require validated
private mounts. Later legacy snapshot output paths below the live shim directory
use the alias; captured reset slots retain original absolute paths and only a
verified pristine generated stub is reusable. Generic overlap rejection remains.
Native recapture/jailed ownership rules first belong to Step 3.

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

## Reproduction and artifact boundaries

Use an exact clean checkout and prepared KVM worker with pinned Go, Docker and
passwordless sudo. Preserve new evidence outside the checkout. The scripts and
criteria remain unchanged:

```sh
test "$(git rev-parse HEAD)" = 2e03843816cf95d184ad4ad429adf6ea2ccc1412
test -z "$(git status --porcelain)"
export STACK_DIR=/users/nehalem/fc-ctrd-mod
export PATH="$STACK_DIR/toolchains/go1.26.11/bin:$PATH"
export STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
export DOCKER_IMAGE_TAG=$(git rev-parse --short=12 HEAD)
: "${VHIVE_SOURCE_REPOSITORY:?Set the local pinned vHive test-source repository}"
export VHIVE_SOURCE_REPOSITORY
tools/modernize/build-stack.sh
tools/modernize/install-stack.sh
make test-images
tools/vhive/test-vhive.sh
tools/modernize/test-stack.sh
```

If the known CNI error recurs, preserve the failed outcome and diagnose with
unchanged concurrency. The recorded supplemental launchers and results are in
the receipt; they are separate observations, not an automatic retry policy or
permission to relabel a failed run. Fresh isolated containers/pools keep those
checks away from unrelated host/Kubernetes networks.

Installed rootfs SHA-256 is
`e4ab2bd242a1d3ce5157c99d3c9c1cf38228263c0b8c35a37a6081ffa17d6ca4`;
rebuilt full-stack test rootfs is
`5454922651a3fd2507f15b7e079e5d34288141189cf9a43ab70c3896cfebe4d1`.
The provisional vHive image is separate again; no rootfs byte identity is
asserted. The receipt explicitly documents validated reuse of the earlier
accepted kernel/config rather than claiming fresh kernel compilation. Tail
artifacts/modules/image/cleanup are recorded separately because the original
full-run receipt epilogue was not reached. No final installed-vHive consumer
result or new performance claim follows.

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

## Optional Step 4 legacy harness profile

The Step 4 test image selects `guest_root_mode=ram` explicitly and records its
configuration. The runtime defaults remain disk. This preserves the legacy
two-drive capture profile without requiring Step 4 API fields in the actual
vHive release, which consumes the Step 2 branch.
