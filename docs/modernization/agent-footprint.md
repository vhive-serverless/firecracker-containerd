# Agent and runc footprint audit

Audit date: 2026-11-12. This is a read-only inspection of the pinned Step 4 build and existing logs. No VM was launched and no repository artifact was modified.

## Findings

### What runs where

In this Firecracker-containerd configuration, `agent` is a guest service: the guest systemd unit starts `/usr/local/bin/agent --debug`, and the agent sets up task, event, drive and I/O proxy services over vsock. The same source-built `runc` is staged in the guest root filesystem for guest container operations. Separately, host runtime code invokes a configured runc binary through `go-runc` for jail/container lifecycle operations. These are command invocations; runc is not a resident daemon. The DSec paper's “agent sandbox” is a different concept and should not be identified with this repository's guest agent.

### Exact pinned artifacts

Worker checkout `/users/nehalem/fc-ctrd-mod/step4-native-final` was clean at `72f4fc9173bef33cc1c981ad285758716f635e02`, built with Go `go1.26.11 linux/amd64`; runc submodule was `c241c0bb5e60a8e8c1b2e53d4eca8d0068d8d57e`. The pinned test image ID was `sha256:0ed23833cf6043e6f2e0fa6ef3e590cfd9626c03e69d66303f9a693b079021b5`.

| Artifact | File bytes | SHA-256 | Build details |
|---|---:|---|---|
| Guest agent | 18,682,018 (17.82 MiB) | `92eeebb550e0b91741e84ebf59c157c817fdbb46d7ebcb37af3fd2a27e0f5bcf` | Go 1.26.11, Linux amd64, CGO disabled, static, already stripped; revision `72f4fc9…` |
| runc | 17,001,136 (16.21 MiB) | `bc4911748310abf4f0892f704d9d4d11445ee21ee09b7257b10388b50f17de3a` | runc 1.4.2, Go 1.26.11, Linux amd64, static PIE, debug info present, not stripped |

The corresponding staged files under `tools/image-builder/files_ephemeral/usr/local/bin/` have identical bytes and hashes. The built `rootfs.img` is 74,063,872 bytes; extracting its SquashFS entries confirmed the same agent and runc bytes. `rootfs-stargz.img` also contains these exact files. In `rootfs-debug.img`, `/usr/local/bin/agent` is a 488-byte launch wrapper and `/usr/local/bin/agent.real` is the same 18,682,018-byte binary; runc is unchanged. The debug wrapper is not the actual agent executable.

GNU `size` reports agent text/data/BSS as 18,099,929 / 570,891 / 33,862,424 bytes and runc as 10,938,886 / 1,415,389 / 33,811,464 bytes. The `size` totals include BSS and describe ELF sections; they are not measured resident memory.

### Memory evidence and limits

No per-process agent or runc `VmRSS`, `Pss`, or `smaps_rollup` samples were found in the inspected worker evidence. One existing guest log for the pinned image reports `firecracker-agent.service: Consumed 2.426s CPU time, 38M memory peak` ([the retained guest log](evidence/step4-capture-interrupt/clean-5186d40/containerd.out)). This is a systemd service/cgroup peak and may include child processes; it is not agent-only RSS, PSS, heap, or a steady-state measurement.

The separate `evidence/step4-memory-reporting.log` experiment measures the Firecracker VMM process via `/proc/<VMM PID>/smaps_rollup`, not agent or runc. Its reported RSS values therefore provide no per-process evidence for either artifact. No current evidence supports a numeric claim about the guest agent's or runc's RSS/PSS.

### Size reduction experiment

On temporary copies only (GNU strip 2.42), `strip --strip-debug` reduced runc from 17,001,136 to 13,660,736 bytes (3,340,400 bytes saved); `strip --strip-all` reduced it to 12,364,536 bytes (4,636,600 saved). Agent `strip --strip-debug` saved only 1,450 bytes because the Go binary was already link-stripped. Original hashes were rechecked unchanged. `size` text/data/BSS figures were identical for original and stripped copies. These results establish potential image/on-disk savings only; the candidates were not executed and no RSS/PSS impact was measured. Retain detached symbols if runc debugging requires them.

### DSec comparison

DSec describes a node-local edge, a per-sandbox aether proxy (vsock for VMs),
and chronus execution helpers inside the sandbox. Thus it retains guest-side
execution helpers; it does not move all control functionality outside the VM.
The paper does not specify an equivalent guest-runc/containerd stack. Its
“agent sandbox” is an LLM execution environment, not this project's runtime
agent. Its Rust storage implementation is not evidence for a Rust-agent memory
saving. [DSec §§3.3, 6.2 and 7](https://arxiv.org/html/2609.22978v1#S3.SS3).

### Can the agent be made smaller?

Yes, optimization is worth measuring, but the current evidence establishes no
agent-only RSS/PSS baseline. The agent already links with `-s`; repeating strip
is not a material optimization. The runc copy experiment identifies a measurable
file-size opportunity, which still needs startup/lifecycle/restore validation
before changing the shipped binary. It does not establish less resident memory.

For resident memory, first sample guest `/proc/<agent-pid>/smaps_rollup`, Go heap
metrics and the service cgroup at idle and during create/exec/delete and restore.
Measure total host cgroup memory alongside these views without summing overlapping
accounting domains. Profile allocations and retained task/IO state before choosing
a change. Dependency reduction or a smaller task-service implementation may help,
but must preserve OCI behavior, events, statistics, stdio, cancellation and restore.
Go memory-limit/GC settings trade memory against CPU and latency and require
workload testing; they are not hard RSS caps. [Go GC guide](https://go.dev/doc/gc-guide).

Rust avoids a mandatory tracing garbage collector, so a carefully designed
replacement may reduce runtime and heap overhead. It does not guarantee a smaller
binary or lower RSS: dependencies, allocation behavior, buffering and equivalent
features still matter. Replacing the embedded Go task service entails a substantial
compatibility implementation, not a mechanical language translation. Compare a
feature-equivalent prototype against measured Go baselines before selecting it.
[Rust's memory model](https://rust-lang.org/).

## Evidence files

- [artifact-metadata.txt](evidence/agent-footprint/artifact-metadata.txt): worker revision, binary/image hashes, sizes, ELF facts and SquashFS checks.
- [agent-buildinfo.txt](evidence/agent-footprint/agent-buildinfo.txt), [runc-buildinfo.txt](evidence/agent-footprint/runc-buildinfo.txt): Go build metadata.
- [strip-experiment.txt](evidence/agent-footprint/strip-experiment.txt): original and temporary stripped-copy hashes and section sizes.
- [SHA256SUMS](evidence/agent-footprint/SHA256SUMS): integrity hashes for the local audit files.

## Primary sources

- [DSec paper PDF](https://arxiv.org/pdf/2609.22978), especially §§2.2, 5.2, 6.2, 7, 8.4.
- [DSec arXiv record](https://arxiv.org/abs/2609.22978).
- [Pinned agent entry point](https://github.com/vhive-serverless/firecracker-containerd/blob/72f4fc9173bef33cc1c981ad285758716f635e02/agent/main.go), [guest systemd unit](https://github.com/vhive-serverless/firecracker-containerd/blob/72f4fc9173bef33cc1c981ad285758716f635e02/tools/image-builder/files_debootstrap/etc/systemd/system/firecracker-agent.service), and [image-builder Makefile](https://github.com/vhive-serverless/firecracker-containerd/blob/72f4fc9173bef33cc1c981ad285758716f635e02/tools/image-builder/Makefile).
- [Pinned runtime runc invocation](https://github.com/vhive-serverless/firecracker-containerd/blob/72f4fc9173bef33cc1c981ad285758716f635e02/runtime/runc_jailer.go) and [top-level Makefile](https://github.com/vhive-serverless/firecracker-containerd/blob/72f4fc9173bef33cc1c981ad285758716f635e02/Makefile).
- [Pinned runc source](https://github.com/opencontainers/runc/tree/c241c0bb5e60a8e8c1b2e53d4eca8d0068d8d57e).
