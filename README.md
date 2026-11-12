# firecracker-containerd

This branch uses official Firecracker/jailer v1.18.0 release binaries, the
published vHive SDK dd9db32, and containerd v2.3.7. Exact Step 3 source
`332da2a7065b` passed native restore in both jail modes, real-volume VM continuity
and focused CRI checks. CRI remains WIP; Kubernetes Pod restore is deferred to
1.38, and final published/installed vHive acceptance remains pending. See the
[Step 3 procedure and acceptance scope](docs/modernization/step-3.md). This later
documentation tip was not built or tested.

This Step 4 candidate adds private guest storage, PMEM/DAX, bounded DAMON and
free-page reporting. See [Step 4 status](docs/modernization/step-4.md), the
[historical memory evaluation](tools/memory-eval/results/0a268c5589c3/REPORT.md)
and [reference acceptance scopes](docs/modernization/upstream-migration.md).
Fresh acceptance of this exact migrated tree remains pending. CRI remains WIP;
Kubernetes whole-Pod checkpoint/restore is deferred to the 1.38 interface work.
The upstream overview below is historical; use the branch guide for current
build and reproduction commands.

[![BuildKite](https://badge.buildkite.com/aab4ae547d5e5079a5915522e8cdb18492349aef67aae5a8c5.svg?branch=main)](https://buildkite.com/firecracker-microvm/firecracker-containerd)
[![GitHub Actions](https://github.com/firecracker-microvm/firecracker-containerd/actions/workflows/build.yaml/badge.svg)](https://github.com/firecracker-microvm/firecracker-containerd/actions)

This repository enables the use of a container runtime,
[containerd](https://containerd.io), to manage
[Firecracker](https://github.com/firecracker-microvm/firecracker) microVMs.
Like traditional containers, Firecracker microVMs offer fast start-up and
shut-down and minimal overhead.  Unlike traditional containers, however, they
can provide an additional layer of isolation via the KVM hypervisor.

Potential use cases of Firecracker-based containers include:

* Sandbox a partially or fully untrusted third party container
  in its own microVM.  This would reduce the likelihood of
  leaking secrets via the third party container, for example.
* Bin-pack disparate container workloads on the same host,
  while maintaining a high level of isolation between containers.  Because
  the overhead of Firecracker is low, the achievable container
  density per host should be comparable to
  running containers using kernel-based container runtimes,
  without the isolation compromise of such solutions.  Multi-tenant
  hosts would particularly benefit from this use case.
  * Please note that multi-tenant use cases are at the user's own risk.

To maintain compatibility with the container ecosystem, where possible, we use
container standards such as the OCI image format.

There are several components in this repository that enable containerd to use
Firecracker microVMs to run containers:

* A [control plugin](firecracker-control) managing the lifecycle of the
  runtime and implementing our [control API](proto/firecracker.proto) to
  manage the lifecycle of microVMs. The control plugin is compiled in to the
  containerd binary since [building a Go plugin out-of-tree is hard](https://github.com/golang/go/issues/20481),
  which requires us to build a specialized containerd binary for
  firecracker-containerd.
* A [runtime](runtime) linking containerd (outside the microVM) to the
  Firecracker virtual machine monitor (VMM).  The runtime is implemented as an
  out-of-process
  [shim runtime](https://github.com/containerd/containerd/issues/2426)
  communicating over ttrpc.
* An [agent](agent) running inside the microVM, which invokes
  [runC](https://runc.io) via containerd's embedded runc-v2 task service
  to create standard Linux containers inside the microVM.
* A [root file filesystem image builder](tools/image-builder) that
  constructs a firecracker microVM root filesystem containing runc and
  the firecracker-containerd agent.

For more detailed information on the components and how they work, see
[architecture.md](docs/architecture.md).

## Maintained branches and usage

Use the [Step 1–4 branch guide](docs/modernization/README.md) for build,
installation, validation and forward-merge maintenance.
`firecracker-v1.18-containerdv2` is the maintained base;
`firecracker-v1.18-containerdv2-vhive-integration` adds vHive. Later branches
add CRI/native checkpoint components and disk-backed storage/memory mechanisms
in order. Each branch pins its own
stack. Step 3 and inherited Kubernetes/CRI acceptance gates remain WIP.

The original general getting-started and quickstart pages are historical
references. The milestone procedures and their committed evidence identify
what has actually been built and tested.

## Questions?

Please use [GitHub
issues](https://github.com/firecracker-microvm/firecracker-containerd/issues) to
report problems, discuss roadmap items, or make feature requests.

If you've discovered an issue that may have security implications to
users or developers of this software, please do not report it using
GitHub issues, but instead follow
[Firecracker's security reporting
guidelines](https://github.com/firecracker-microvm/firecracker/blob/main/SECURITY-POLICY.md).

Other discussion: For general discussion, please join us in the `#containerd`
channel on the [Firecracker Slack](https://join.slack.com/t/firecracker-microvm/shared_invite/zt-oxbm7tqt-GLlze9zZ7sdRSDY6OnXXHg).

## License

This library is licensed under the Apache 2.0 License.
