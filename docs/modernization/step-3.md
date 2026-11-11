# Step 3 implementation status

Step 3 is incomplete. Steps 1 and 2 retain their accepted branches. This branch
currently adds a containerd CRI adapter and worker tests; it does not yet satisfy
the full CRI, Kubernetes RuntimeClass, or native Pod checkpoint/restore gates.
The [API audit](step-3-api-audit.md) records the v1.38 alpha target and the missing
workflow in the released builds tested so far. Step 4 has not started.

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
interrupts for root, network, vsock and ACPI devices. VM CPU and memory currently
use the existing defaults; deriving VM capacity from Pod resources remains work
before Kubernetes acceptance. Host network, host PID and host IPC requests are
rejected rather than interpreted as guest-host namespace requests.

## Worker reproduction

The VMM and SDK remain pinned to the accepted Step 2 revisions in `versions.json`.
No dependency source change is required for this adapter. From a clean committed
worker checkout:

```sh
tools/modernize/build-stack.sh
export DOCKER_IMAGE_TAG=$(git rev-parse --short=12 HEAD)
make test-images
CRI_FOCUS='Multiple Containers|Networking|PodSandbox runtime should support basic operations|PodSandbox runtime should support sysctls|Runtime info' \
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
