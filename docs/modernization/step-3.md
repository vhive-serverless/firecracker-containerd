# Step 3 implementation status

Step 3 is incomplete. Steps 1 and 2 retain their accepted branches. This branch
currently adds a containerd CRI adapter and worker tests; it does not yet satisfy
the full CRI, Kubernetes RuntimeClass, or native Pod checkpoint/restore gates.
The [API audit](step-3-api-audit.md) records the v1.38 alpha target and the missing
workflow in the released builds tested so far. Step 4 has not started.

This branch also carries the validated task creation cleanup correction from
Step 1 commit `6585eba` through Step 2 commit `d959a78`. Their receipts are linked
from [Step 1](step-1.md) and [Step 2](step-2.md). The results below identify their
tested source revisions; earlier results do not establish acceptance of the
merged Step 3 runtime.

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
paths. The Pod's containers share a guest tmpfs for `/dev/shm`. Other bind mounts
are explicitly rejected until coherent volume sharing is implemented.

The runtime configuration adds `cri_cni_conf_dir` (default `/etc/cni/net.d`),
`cri_cni_cache_dir` (default `/var/lib/cni`) and `cri_container_count` (default 8,
including the sandbox). The x86 MMIO profile permits 2–14 slots, reserving
interrupts for root, network, vsock and ACPI devices. Host network, host PID and host IPC requests are
rejected rather than interpreted as guest-host namespace requests.

## Host volume export layer

`internal/volfs` supplies the host descriptor table for future guest volume
sharing. An export authorizes a directory or one regular file. Descendant
operations take inode identifiers and single byte-sequence names, and never
resolve a caller-supplied host path. Symlinks are returned for resolution by
the guest VFS. Regular-file reads and writes refer to the same host inode,
including after rename or unlink. The layer includes creation, directory
listing, hard links, ownership, mode, timestamps, truncation, synchronization
and backing-filesystem capacity. Descriptor limits, read-only exports and
cleanup apply independently of the transport.

This layer is not connected to the shim or guest agent yet. Other host bind
mounts remain rejected, and its filesystem tests do not establish guest volume
support. Guest permission checks and ID mapping, the FUSE client, bounded
reconnectable transport, cache coherence, mount propagation, extended attributes
and checkpoint serialization still require implementation and validation.

Run the host filesystem checks on the worker:

```sh
go test -mod=readonly -race -count=1 ./internal/volfs
go test -mod=readonly -race -c -o bin/volfs.test ./internal/volfs
sudo -n bin/volfs.test -test.v -test.timeout=120s
```

The root invocation also checks the host service's hard-link capability and
guest UID/GID ownership with setgid directory inheritance. All fixtures live
in temporary directories; the tests do not mount a filesystem.

Clean committed checkout `37f68f172a9f9994768ceb8fe2f01ad8cf1bbbed` passed
both module linters and the host filesystem checks on the worker. The ordinary
race run passed eight tests and skipped the root-only ownership case; the
privileged invocation passed all nine tests with no skips. The
[host receipt](evidence/37f68f1-volfs-host-receipt.log) records source and test
binary hashes, build metadata and raw log hashes. This validates the export
layer independently; it does not establish guest mount support.

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
