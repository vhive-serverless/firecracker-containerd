# Step 3 — WIP

Step 3 is explicitly **WIP and incomplete**. On 2026-11-11 the user authorized
starting Step 4 without completing this milestone. Steps 1 and 2 retain their accepted branches. This branch
currently adds a containerd CRI adapter, RuntimeClass deployment tests and native
snapshot capture and restore components. Full CRI compliance, whole-Pod restore
and Kubernetes checkpoint/restore remain incomplete.

The [API audit](step-3-api-audit.md) records the v1.38 alpha target and the missing
workflow in the released builds tested so far. Step 4 proceeds from a pinned WIP baseline by explicit user instruction.

This branch also carries the validated task creation cleanup correction from
Step 1 commit `6585eba` through Step 2 commit `d959a78`. Their receipts are linked
from [Step 1](step-1.md) and [Step 2](step-2.md). The results below identify their
tested source revisions; earlier results do not establish acceptance of the
merged Step 3 runtime.

## Restorable host volumes

`internal/volfs` can freeze RPC execution and capture its exported files together
with capability tokens, ordered-session replay records, logical inode IDs,
lookup references and open file handles. Restore rebuilds those tables in a new
host server and copies all writable data into a private workspace. Hard links,
overlapping exports, raw byte names and retained unlinked regular files survive.
The source server can resume, and independent restores cannot modify the artifact.

The caller must pause the guest and quiesce every other writer before capture.
Freezing the host server alone does not establish that consistency boundary.
The artifact supports regular files, directories and symlinks. Unsupported inode
types and directory graphs with multiple parents or cycles fail capture. Host
ctime, physical allocation and filesystem-wide statistics can differ after
copying to another host filesystem. Very long host restore paths can fail with
`ENAMETOOLONG`; no partial restore workspace is retained on failure.

Run the component checks on the designated worker with the pinned Go toolchain:

```sh
go test -mod=readonly -race -count=1 -timeout=180s ./internal/volfs
make lint
FC_NATIVE_VMM=/users/nehalem/fc-ctrd-mod/cri-committed/bin/firecracker \
FC_NATIVE_KERNEL=/users/nehalem/fc-ctrd-mod/cri-committed/default-vmlinux.bin \
    tools/modernize/test-native-volumes.sh
```

The VM test uses a purpose-built initramfs with two FUSE mounts of a shared
export. It captures guest memory and the host tables at counter 41, resumes the
source to counter 42, terminates its VMM and removes the source directory. A new
VMM and host server restore counter 41, both shared views, an open unlinked file
and the original logical inode. The clone can then advance to counter 42.
This component test does not exercise OCI task adoption, block-device capture,
runtime checkpoint RPCs or Kubernetes checkpoint/restore.
The [committed volume receipt](evidence/e21b3f6-volume-receipt.log) records the
clean `e21b3f6a29da715382a5feeb60be005b08b36226` checkout, race tests, privileged
mount checks, lint, real VM result and exact binary/log hashes.

## Native checkpoint coordinator

The control service's `CheckpointVM` RPC captures an existing VM into a new
absolute directory. It serializes task/device mutations, records task and exec
state, pauses running container cgroups and then pauses vCPUs. It freezes and
captures host volume exports, saves full VMM/memory state with source vsock
connections retained, and copies every live drive into independent sparse
files with SHA-256 digests. The manifest also records OCI specifications, FIFO
configuration, volume capabilities, drive-slot ownership, the I/O port allocator,
resolved network configuration, MMDS data when initialized and the VM creation
request. Initially paused containers remain paused.

The coordinator thaws its volume server, resumes the VM and resumes only the
containers it paused before publishing the artifact by atomic directory rename.
Request cancellation and failed capture run bounded cleanup with an uncancelled
context. An existing destination is never replaced. Destinations inside VM or
task cleanup directories are rejected. Every external writer of an exported
host volume must be quiescent for the capture's duration.

`TestNativeCheckpoint_Isolated` exercises two OCI containers sharing a FUSE
volume in both unjailed and runc-jailed VMs. It checks source exec and writes
after capture, independent raw ext4 contents with `debugfs`, reconstruction of
captured volume contents after source advancement, a second capture of the new
data, preservation of an initially paused container, destination rejection and
an already-cancelled request. It runs as part of the runtime acceptance suite.
These capture checks do not establish whole-Pod restore or the Kubernetes API
integration. The coordinator still needs the native restore and CRI paths.

## Restored process ownership

The task manager's `AdoptProcess` operation checks the restored guest task ID
and captured PID, creates fresh host lifetime contexts, attaches the supplied
I/O proxy and monitors the existing process. Initial tasks must be adopted
before execs. It preserves paused state and participates in ordinary deletion
and shutdown bookkeeping. Failed adoption releases only new host ownership;
it does not delete the guest process. Unit tests cover task/exec adoption,
identity mismatch and an exec whose parent has not been adopted. Connecting
this operation to the runtime's native restore coordinator remains pending. The
[committed adoption receipt](evidence/40b4ca7-adopt-receipt.log) records unit/race
and lint checks from clean commit `40b4ca7c92cb872454c96e2325f3a44242795d3e`.

## Pod mapping and the current CRI path

The host shim recognizes containerd's standard `io.kubernetes.cri.sandbox-id`
and `io.kubernetes.cri.container-type` OCI annotations. A sandbox creates one
VM whose ID is its sandbox ID. Workload containers join that VM's existing
shim. They cannot create a replacement VM after the sandbox has disappeared.
Ordinary non-CRI OCI containers retain their existing mapping behavior.

Containerd owns CNI allocation, the Pod IP and network namespace. The CNI chain
ends with `tc-redirect-tap`; the shim reads the completed libcni allocation and
boots the VM with that TAP, MAC and IPv4 address. It does not invoke CNI again.
Port mappings use the pinned CNI v1.9.1 `portmap` plugin. The guest's network is
the Pod network, so the shim removes the host-only network namespace reference
from the guest OCI spec. The guest agent applies network sysctls in the VM before
runc creates the container.

Generated `resolv.conf`, `hosts` and `hostname` bind files travel in the existing
host-to-agent task protocol and are materialized inside the guest task bundle.
Each file is limited to 64 KiB. Host source paths are lookup keys, not guest output
paths. The Pod's containers share a guest tmpfs for `/dev/shm`. Other supported
bind mounts use the descriptor-based host/guest volume service described below.

The runtime configuration adds `cri_cni_conf_dir` (default `/etc/cni/net.d`),
`cri_cni_cache_dir` (default `/var/lib/cni`) and `cri_container_count` (default 8,
including the sandbox). The x86 MMIO profile permits 2–14 slots, reserving
interrupts for root, network, vsock and ACPI devices. Host network, host PID and host IPC requests are
rejected rather than interpreted as guest-host namespace requests.

## Host volume export layer

`internal/volfs` supplies the host descriptor table for guest volume
sharing. An export authorizes a directory or one regular file. Descendant
operations take inode identifiers and single byte-sequence names, and never
resolve a caller-supplied host path. Symlinks are returned for resolution by
the guest VFS. Regular-file reads and writes refer to the same host inode,
including after rename or unlink. The layer includes creation, directory
listing, hard links, ownership, mode, timestamps, truncation, synchronization
and backing-filesystem capacity. Descriptor limits, read-only exports and
cleanup apply independently of the transport.

The package also supplies a reconnectable RPC client/server and a guest FUSE
client using `github.com/hanwen/go-fuse/v2` v2.10.1 (source commit
`1d16325e0693e4aeb40b3c899938e028cc8e538e`). The version manifest pins the module,
source commit and both module checksums. Exports use random capabilities rather
than caller-selected paths. Frames are limited to 2 MiB, file IO to 1 MiB,
registered exports to 256 and concurrent connections to 64 per server.

Each export has one ordered client session and caches its last response before
delivery. A lost reply can be retried with identical request bytes and ID without
repeating a create, append, rename or handle release. Changed replay contents,
sequence gaps and another client's session are rejected. Request cancellation
is honored before issue; an issued request resolves using the mount lifetime
until it completes or the mount closes. Kernel lookup reference releases are
queued without blocking the FUSE forget callback.

The FUSE mount uses kernel `default_permissions` for DAC in the initial guest
user namespace. Read-only policy is enforced by both the mount and the host.
Regular-file IO bypasses the guest page cache so same-size host rewrites remain
visible even with ext3 timestamp granularity. Memory mapping is not enabled;
coherent mapping invalidation still needs implementation. Host FIFOs and device
nodes are rejected rather than acquiring unrelated guest-local behavior.

The shim registers bind exports per task and listens on the VMM backend's Unix
socket with suffix `_10901`. The guest connects to host vsock CID 2, port 10901.
An export carries a mount index and capability in the generated task protobuf;
the guest mounts it under its own bundle and substitutes the resulting guest
path into that OCI mount. No capability is added to Pod annotations. Single-file
exports support mounts such as `/dev/termination-log`.

Host exports use detached private mount views from `open_tree(2)`. `bind` and
`rbind` select whether existing submounts are included; subsequent host mounts
do not propagate. The shim revokes registrations after failed task creation,
container deletion and VM shutdown. The agent closes its filesystem client
before unmounting, with a detach fallback for borrowed file descriptors, then
removes the task bundle.

Shared/slave mount propagation, user namespace ID mapping, extended attributes,
host/guest lock coherence and explicit checkpoint serialization remain pending.
Read-only exports currently make their entire FUSE view read-only: they do not
represent the writable child mounts required by non-recursive read-only bind
semantics. Reconnection against a live host table does not reconstruct that
table for a new VM or shim. These limitations still prevent unqualified CRI and
filesystem compatibility claims.

Clean committed checkout `6b3cf8fd851eeb66e3173aa2fcc2406a01c7ff5f` passed
ordinary bind mounts and symlink host sources through real VMMs. The optional
`TestPodVolumes` check additionally verifies guest mount information for writable,
read-only and termination-log mounts, immediate host/guest write visibility,
hard links and open unlinked files. It repeats ten failed guest creates in the
same sandbox and verifies the existing workload still runs. All four selected
upstream CRI checks, three VM resource cases and the volume check passed,
including cleanup. The [volume receipt](evidence/6b3cf8f-cri-volume-receipt.log)
identifies the tested checkout, image and binaries; this is partial Step 3
validation, with the full-suite failures recorded below.

```sh
CRI_INTEGRATION_TESTS=1 CRI_FOCUS='Runtime info|starting container with volume' \
    tools/critest/test-cri.sh
```

The integration flag runs both the volume checks and the three Pod resource
cases after the selected upstream CRI tests, then requires complete cleanup.

Run the host filesystem checks on the worker:

```sh
go test -mod=readonly -race -count=1 ./internal/volfs
go test -mod=readonly -race -c -o bin/volfs.test ./internal/volfs
sudo -n bin/volfs.test -test.v -test.timeout=120s
sudo -n env VOLFS_MOUNT_TESTS=1 unshare --mount --propagation private \
    bin/volfs.test -test.v -test.count=1 -test.timeout=120s
```

The root invocation also checks the host service's hard-link capability and
guest UID/GID ownership with setgid directory inheritance. The final command
also exercises real FUSE mounts in a private mount namespace: ordinary file IO,
host coherence, open unlinked files, hard links, directory cookies, reconnection,
single-file exports, read-only enforcement, permissions and ownership. Fixtures
are confined to temporary directories and removed by test cleanup.

Clean committed checkout `37f68f172a9f9994768ceb8fe2f01ad8cf1bbbed` passed
both module linters and the host filesystem checks on the worker. The ordinary
race run passed eight tests and skipped the root-only ownership case; the
privileged invocation passed all nine tests with no skips. The
[host receipt](evidence/37f68f1-volfs-host-receipt.log) records source and test
binary hashes, build metadata and raw log hashes. This validates the export
layer independently; it does not establish guest mount support.

Clean committed checkout `20578179278235e44daff729dc102b427239a4fa` passed
the RPC and FUSE component checks on the worker. Both module linters,
dependency tidy and input verification passed. The unprivileged race run
passed 14 tests and skipped five privileged cases; the privileged race binary
passed all 19 tests, including four real kernel mount cases, with no skips.
The [RPC/FUSE receipt](evidence/2057817-volfs-rpc-fuse-receipt.log) preserves
the exact commands, dependency verification, binary/source hashes and raw
log hashes. Its kernel mount tests use the worker host kernel `6.8.0-147-generic`;
that receipt predates the shim/agent mount wiring and does not establish its
Firecracker guest acceptance.

## One-node k3s RuntimeClass profile

The [deployment test](../../tools/kubernetes/test-firecracker-node.sh) starts
the required released k3s `v1.37.2+k3s1` against this runtime's CRI endpoint,
using its own privileged Docker container, private cgroup/network namespaces
and disposable thin pool. The cluster is removed after the test. Its
[RuntimeClass and workload manifest](../../tools/kubernetes/firecracker-smoke.yaml)
runs two containers in one Firecracker VM with a shared `emptyDir` and the
ordinary projected service-account volume.

Ordinary system Pods use runc and a bridge CNI chain. The `firecracker` runtime
handler selects a separate chain with `tc-redirect-tap`; both chains share
their bridge and IPAM allocation name. Both configure a default route. The
nested one-node profile uses kube-proxy service masquerading. CoreDNS and the
workload images are pinned by digest in the scripts/manifest and version ledger.
AppArmor is disabled in this profile; the conformance limitations below remain.

After building the committed stack and test image on the designated worker:

```sh
tools/kubernetes/test-firecracker-node.sh
```

The test requires successful logs and exec, readable projected metadata, an
explicit IPv4 DNS answer for the Kubernetes service, shared guest FUSE mounts,
HTTP service access from an ordinary runc Pod, positive CPU/memory statistics
for both Firecracker containers, and deletion without a remaining Firecracker
VM or sandbox. It checks the Pod UID, CRI sandbox, two container IDs and actual
VMM command line together. BusyBox's DNS exit status alone is insufficient:
the test asserts the returned address, and queries the absolute name's A record.

Clean deployment-test checkout
`1085202018904cff880eb7901e470a23c80cf5a9`, using committed runtime image
`20727f4002990a522c4d46bc174475959c28f93c`, passed all these checks. The
[RuntimeClass receipt](evidence/1085202-k3s-runtimeclass-receipt.log) records
both revisions, exact inputs, Pod/CRI/VMM identities, positive statistics,
image binary hashes and raw evidence hashes. This establishes ordinary Pod
lifecycle integration; native Pod restore and Kubernetes checkpoint/restore
remain incomplete.

## Native snapshot transport

This branch pins Firecracker
`ce86b117d59ca93b1c04b791a251b445ace76703` and SDK
`832b1a5132e06f5e2167b64b97b5cd8946604005` on matching
`cri-native-snapshot-restore` branches. The API can retain established source
vsock connections during checkpoint creation and reset captured connections
only in the restored guest. The SDK exposes `WithRetainedSourceVsock(true)`
and `WithSnapshotVsockReset(true)`. Existing defaults keep the vHive path's
behavior, and the serialized VMM snapshot format is unchanged.

Both committed dependency checkouts passed worker validation. Four real-VMM
cases cover MMIO/PCI and retained/legacy source policies: the source continues
after a failed and successful retained snapshot, exits before restore, and a
new VMM returns the captured memory counter with its listener working and stale
connections released. Captured memory/state files remain unchanged after the
restored guest writes. The full privileged SDK suite passed 79 cases; its gated
native test passed separately. The
[transport receipt](evidence/ce86b11-832b1a5-native-vsock-receipt.log) records
the two SDK skips, targeted VMM tests, lint, race/vet checks, exact Swagger
regeneration, binary hashes and commands. SDK snapshot tests also passed against
the previous VMM, confirming that ordinary load calls omit the new option.

This validates transport and memory primitives independently. The fixture has
no persistent disks or container tasks. The runtime still needs native Pod
checkpoint coordination, independent consistent disk capture, saved task/volume
metadata and restored IO/events/ownership. These dependency results do not
establish whole-Pod restore or Kubernetes checkpoint/restore integration.

## VM capacity from Pod resources

The shim reads containerd 2.3.7's standard `io.kubernetes.cri.sandbox-cpu-period`,
`sandbox-cpu-quota`, `sandbox-cpu-shares` and `sandbox-memory` OCI annotations.
Containerd derives these from `LinuxPodSandboxConfig.Resources`; the pause
container's own cgroup is not the Pod resource envelope. Kubelet annotations
are preserved unchanged.

The VM gets enough whole vCPUs for a positive CPU quota divided by its period.
An omitted period uses Linux's 100 ms default. With an unlimited quota, CPU
shares supply the requested CPU capacity, with a one-vCPU fallback. SMT rounds
multi-vCPU capacity to an even count. Requests exceeding the selected
Firecracker KVM backend's 32-vCPU ceiling fail explicitly.

A positive Pod memory limit is rounded up to MiB and receives guest headroom
from `cri_memory_overhead_mib` (default 128 MiB). An unbounded or missing memory
limit uses the total `cri_default_memory_mib` capacity (default 512 MiB). Zero
configuration values select these defaults. The headroom is for the guest
kernel and agent; RuntimeClass scheduling overhead must also account for host
shim and VMM costs. The pinned kubelet calculates sandbox resources without
Pod overhead, which is supplied separately in CRI and is not included in these
containerd OCI annotations.

This implements initial VM sizing. Charging the host VMM to the Pod cgroup,
aggregate resource accounting and live resource changes still require work.
A Pod with no positive memory limit receives the configured finite fallback,
and the VMM cannot grow beyond its boot capacity. These limits must be resolved
or made explicit in the eventual Kubernetes deployment profile.

The optional worker check compiles a static test client on the worker and
mounts it into the driver's private test container:

```sh
CRI_INTEGRATION_TESTS=1 CRI_FOCUS='Runtime info' tools/critest/test-cri.sh
```

It checks the actual VMM machine configuration, the CPU and memory visible to
a container, unchanged Pod annotations, and cleanup. Development runs do not
establish acceptance of a committed stack; committed results are recorded in
the worker receipts.

## CRI endpoint and guest port forwarding

`firecracker-cri` is a runtime component built and installed from this branch.
Kubelet's runtime and image endpoints must both select its Unix socket. It
forwards ordinary CRI operations to the private containerd socket, retaining
containerd's sandbox/container stores, image management and lifecycle behavior.
It preserves gRPC deadlines, application metadata, response headers/trailers,
status errors and cancellation of server streams. Exec and attach retain
containerd's streaming URLs.

For a Firecracker Pod, `PortForward` checks containerd's ready sandbox and runtime
type, issues a single-use upstream CRI streaming URL, and connects the stream
through that Pod's VMM vsock socket to the guest agent on port 10900. The agent
connects only to guest loopback at the requested TCP port. Entering the host Pod
network namespace cannot reach a service bound to guest loopback, which is why
containerd's default port-forward callback cannot serve this VM layout. Other
runtimes retain containerd's callback. The guest listener starts only for CRI
tasks; ordinary OCI containers retain their existing agent behavior.

For example, after starting containerd with the documented runtime configuration:

```sh
firecracker-cri \
    --address /run/firecracker-containerd/cri.sock \
    --backend /run/firecracker-containerd/containerd.sock \
    --runtime-config /etc/containerd/firecracker-runtime.json \
    --stream-address 127.0.0.1:0
```

The frontend refuses to replace an existing socket, creates its socket mode 0600,
and defaults to loopback streaming. This command describes the component's
configuration; Kubernetes RuntimeClass acceptance remains pending. The worker
test driver starts both components in its private test container automatically.

The `cri/` Go module pins CRI API and streaming v0.37.2, while containerd 2.3.7
retains its compatible CRI API v0.36.3 dependency. CRI 1.37 renamed the generated
Signal enum constants, breaking compilation of containerd 2.3.7 if imposed on
the parent module; its wire numbers remain unchanged. This separate module uses
parent source from the same Git checkout through the explicit `=> ..`
replacement. It repeats the exact pinned SDK replacement because Go does not
inherit replacements from dependencies. `verify-inputs.py` checks both module
selections and that the parent source resolves to this checkout. The resolved
frontend module versions are in [cri-modules.txt](cri-modules.txt).
Docker builds receive the source revision explicitly from the host, because a
worktree's Git metadata path may be outside its container bind mount. The test
image build verifies both daemon and frontend revisions before proceeding.

Typed forwarders are generated from the pinned API proto with
`make -C cri generate`; descriptor coverage tests check every RuntimeService and
ImageService method. Although the frontend exposes the new `CheckpointPod` and
`RestorePod` method definitions, it currently forwards them to an unimplemented
backend. Their presence does not provide native Pod snapshot/restore.

Worker development validation passed the normal SPDY streaming selection,
including exec, attach and port forwarding through a real VMM. Protocol tests
also check a 4 MiB response after a request half-close and cancellation of both
relay directions. These are development results, not acceptance of a committed
milestone. Direct CRI WebSocket diagnostics (`CRI_WEBSOCKETS=1`) still fail all
four streaming checks with HTTP 403 during protocol negotiation; the pinned
streaming server and modern client do not negotiate a common protocol. The
default tests use SPDY. Kubernetes API server streaming has not yet been tested
with this runtime, so no claim about `kubectl` streaming is made.

The broader development selection in the reproduction command below ran 20
checks: 19 passed and the host-network port-forward check failed because host
networking is explicitly unsupported. The raw worker log is
`~/fc-ctrd-mod/evidence/cri-portforward-focused19-worker.log`. This preserves the
known failure; it is not a successful full conformance result.

## Worker reproduction

The VMM and SDK remain pinned to the accepted Step 2 revisions in `versions.json`.
No dependency source change is required for this adapter. From a clean committed
worker checkout:

```sh
tools/modernize/build-stack.sh
export DOCKER_IMAGE_TAG=$(git rev-parse --short=12 HEAD)
make test-images
CRI_FOCUS='Multiple Containers|Networking|PodSandbox runtime should support basic operations|PodSandbox runtime should support sysctls|Runtime info|Streaming' \
    tools/critest/test-cri.sh
# The unfiltered suite is also required; the selection above is a focused check.
tools/critest/test-cri.sh
```

The driver provisions an exclusive thin pool and private containerd daemon in a
test container, with no host CRI socket mounted and no kubelet sharing its CRI
service. The containerd configuration uses version 4 plugin keys and an explicit
devmapper transfer unpack configuration. The test script uses the pinned critest
v1.37.0 from the test image, records the actual daemon revision twice, image ID,
focus, source state and script hashes, and saves a Ginkgo JSON report. Failure
exit codes are preserved. There is no expected-failure diff or retry-to-green.

Successful tests also require no remaining tasks, VMM processes, CNI namespaces
or runtime recovery records before removing the test container and thin pool.
`CRI_WORKER_TESTS_PASSED` reports only the selection recorded in that run. It is
not a claim of complete CRI conformance or completion of Step 3.

Development image overrides (`CRI_BASE_IMAGE` and `CRI_RUNTIME_REVISION`) are
recorded explicitly and do not establish acceptance of a committed stack.
The worker rebuilt the complete stack from clean committed checkout
`9d60ca4adf9a911eee2f18c667465c6c7ec4633e`, passed lint, and passed all 15 tests
in the selection above. The
[focused receipt](evidence/9d60ca4-cri-focused-receipt.log) records the source,
actual daemon revision, image, selection and log hashes. Full-suite and
Kubernetes results must be reported separately.

The unfiltered trial with Alpine was repeated from clean committed checkout
`9d60ca4` with the same result: 113 tests completed, 89 passed, 24 failed and
29 were skipped by the upstream suite. The
[full-suite receipt](evidence/9d60ca4-cri-full-receipt.log) preserves every
failed and skipped test name, the reported skip reasons and the complete raw
log/report hashes. This result prevents a claim of CRI conformance. It exposed real missing
volume and port-forward integration plus unsupported host namespaces. Nine
AppArmor tests failed during setup because the test image lacks `sudo`;
their runtime behavior has not yet been tested. Two failures also exposed an
invalid fixture choice: the upstream
suite expects BusyBox 1.29-2 behavior for raw-ICMP capability checks and its
`/bin/ls` layout for masked-path checks. The test script now pins that exact
upstream default image. Those cases must be rerun before classifying them as
runtime failures; no tests or assertions have been removed. A separate worker
development rerun with the pinned BusyBox image passed both cases, including
cleanup, in 13.8 seconds. The raw log is
`~/fc-ctrd-mod/evidence/cri-busybox-fixture-regression.log` (SHA-256
`4a0e620984f496f3c83c1a6bb464fbbce7e469080db4916db57b7b01883bda55`).

The subsequent clean committed run at
`3ceb2f506c98dd5cee5380d82b26b52155e41170` passed build, installation, lint,
race tests, dependency tidy, regeneration and explicit revision checks after
forcing the CRI component to compile inside Docker. Its unfiltered SPDY CRI run
completed 113 of 142 checks: **92 passed, 21 failed and 29 were skipped**. Normal
Pod port forwarding and both BusyBox fixture regressions passed. The
[receipt](evidence/3ceb2f5-cri-full-receipt.log) records every failed and skipped
test, upstream skip reasons, source state, image identity, actual image binary
hashes and raw log/report hashes. The remaining failures cover volumes,
unsupported host namespaces and nine AppArmor setup failures due to missing
`sudo`; they prevent a claim of CRI compliance. This result predates the merged
task creation cleanup correction and remains a baseline for subsequent changes.

The merged cleanup correction at
`06bb03ff16bae291d071e6f7b583def117b14dde` passed source checks, build,
installation and the same-VMM drive-reuse regression. Its unfiltered CRI run
again recorded 92 passes, 21 failures and 29 skips. The
[merged-run receipt](evidence/06bb03f-cri-full-receipt.log) preserves the actual
image binary hashes and all failed/skipped cases. This verifies the correction's
propagation while retaining the remaining CRI failures explicitly.

The Pod resource-sizing change at
`200d7ec7294f04aa585f240defd2701a8a2c6a46` passed a clean worker build,
installation, source checks, nine selected CRI checks and three actual-VMM
resource cases. The cases verified 2-vCPU VMs with 384, 256 and 512 MiB,
including an unspecified CPU period and an unbounded Pod resource envelope.
They checked the guest's reported resources, unchanged Pod annotations and
cleanup. The unfiltered suite again recorded **92 passes, 21 failures and
29 skips**. The [resource receipt](evidence/200d7ec-cri-resource-receipt.log)
records both selections, actual image binary hashes and raw evidence hashes.
These results validate initial sizing; they do not establish host Pod resource
accounting or completion of the remaining Step 3 requirements.

The committed volume integration at
`6b3cf8fd851eeb66e3173aa2fcc2406a01c7ff5f` passed a full build and installation,
regeneration, dependency tidy, parent/CRI lint and race checks, and all 22
privileged filesystem tests with no skips. Its focused CRI and real-VM checks
are described above. The unfiltered CRI run completed 113 checks: **97 passed,
16 failed and 29 were skipped**. Five ordinary volume, image-volume and private
mount-propagation checks improved relative to the previous result. The remaining
failures are four host namespace/host-network streaming checks, two shared/slave
mount-propagation checks, one non-recursive read-only child-mount check and nine
AppArmor setup failures caused by missing `sudo`. No failures were whitelisted.

The base runtime regression initially passed 34 of 35 tests. Its random-number
statistical test reported five FIPS block failures against the unchanged
tolerance of four. One targeted repeat passed; the original failed log remains
in the evidence. The remaining five snapshotter and 79 SDK cases passed, with
the previously documented SDK metrics skip. The initial aggregate invocation
is therefore not reported as an uninterrupted passing run. The
[volume receipt](evidence/6b3cf8f-cri-volume-receipt.log) records these outcomes,
all full-suite failed/skipped names, exact binary hashes and raw evidence
hashes. These volume checks did not establish CRI compliance, native whole-Pod
restore or Kubernetes RuntimeClass acceptance; subsequent RuntimeClass results
are recorded above.

The subsequent native VMM/SDK pin at
`20727f4002990a522c4d46bc174475959c28f93c` passed an uninterrupted committed
build, installation and base acceptance run: all 35 runtime, five snapshotter
and 79 SDK cases passed. The two SDK skips are the pre-existing metrics test and
the gated native snapshot fixture. That fixture passed all four cases separately
against the actual runtime-built VMM, whose hash matches the test image. The
four selected upstream CRI checks, three resource cases and guest volume check
also passed. The full CRI result remained **97 passed, 16 failed and 29 skipped**.
The [native pin receipt](evidence/20727f4-native-pin-receipt.log) preserves
the complete evidence and failed/skipped names. Ordinary RuntimeClass acceptance
was subsequently demonstrated as described above; full CRI compliance and Pod
checkpoint/restore remain incomplete.

## Preserved restore prototype

The `CreateVMRequest.NativeCheckpointDirectory` path is a WIP prototype. It
clones and checksums artifact files, restores host volume tables, loads a new
VMM with a vsock reset and attempts to adopt captured tasks and fresh I/O.
Source compilation and lint passed on the worker; end-to-end restore acceptance
has not been established. The standalone restore test is retained for later
work. No CRI or Kubernetes Pod restore completion is claimed.
