# Step 3 prerequisite audit

Step 3 is incomplete. This branch starts from the accepted Step 2 commit
`122d7084043a1e0b237fea9eb57de8d2eb48c486`, whose
[worker receipt](evidence/122d7084043a-acceptance-receipt.log) records all
runtime and vHive acceptance gates. Step 4 has not started.

The [committed worker probe receipt](evidence/f649858-kubernetes-api-audit.log)
records the binary hashes, gate errors, raw log hashes, and audit exit status.
The script was tested from clean commit
`f649858` (the receipt records the complete commit). The earlier
two-build audit remains in `evidence/579da231cefb-kubernetes-api-audit.log`.

## Released binaries tested on the worker

| Input | Exact input | Result |
| --- | --- | --- |
| Required k3s | `v1.37.2+k3s1`, binary reports `f6973cb8`; SHA-256 `265b3ee6a3198ef14033c63ef46cbb32370d83795199fb6d1b01f21575eddbda` | The API server rejects `PodLevelCheckpointRestore` as an unrecognized feature gate and shuts down. |
| Same-version upstream Kubernetes | `v1.37.2`, source `c50e5395f1ae2d4a2c05f28fdcc38c0916cc1a32`; API server SHA-256 `b87e5a91107cf994bfa38c6c1b28eb39f01fc040734ec9942c22738bc8b8f34b` | The upstream binary also rejects the gate and exits with status 1. This is not specific to k3s packaging. |
| Newer upstream release | Kubernetes `v1.38.0-beta.0`, source `1ca7499c9a5669c1f025dbe14f1da7dd9a0eb58a`; API server binary SHA-256 `24c8161f0e983a3134002c7f32866dc8fe36f706a4e645a94bb2c0c81984afa0` | The API server rejects the same gate and exits with status 1. |
| CRI API source | `k8s.io/cri-api v0.37.2` | Defines `CheckpointPod` and `RestorePod`. Their presence in the protocol does not implement the Kubernetes workflow. |
| Runtime's current CRI dependency | `k8s.io/cri-api v0.36.3` | Does not define those two RPCs. |

The source registries in Kubernetes v1.37.2 and v1.38.0-beta.0 also lack
`PodLevelCheckpointRestore`. The v1.37.2 kubelet has wrappers for the new
CRI calls but no Pod checkpoint object/watch or restore execution path.
As of this audit, the k3s repository exposes no v1.38 or v1.37.3 release
tags. The newer upstream beta was checked explicitly instead.

This differs from the
[Kubernetes 1.37 release article](https://kubernetes.io/blog/2026/08/26/kubernetes-v1-37-release/),
which describes Pod checkpoint/restore as alpha. The actual checked binaries
and source determine this audit result.

The current KEP resolves the release-target ambiguity: its metadata targets
alpha in **v1.38**. The upstream
[milestone update](https://github.com/kubernetes/enhancements/commit/7672f60a388a323ee8ec8a2e02232b7eeefc7e57)
changed the target on September 17, 2026. The
[v1.38 schedule](https://www.kubernetes.dev/resources/release/) plans the final
release for December 16, 2026; a target milestone does not imply that every
prerelease already contains the implementation. At the November 11 audit,
`latest-1.38.txt` and upstream release tags still identified
`v1.38.0-beta.0` as the newest published 1.38 build. That exact beta was tested
above and lacks the gate. No unmerged PR build has been substituted.

This is not inferred from the unrecognized gate alone. In the released
v1.37.2 source, the only callers of `.CheckpointPod(` and `.RestorePod(`
under `pkg/` are the instrumentation wrappers in
`pkg/kubelet/kuberuntime/instrumented_services.go`. The kubelet HTTP checkpoint
route in `pkg/kubelet/server/server.go` still requires
`/{podNamespace}/{podID}/{containerName}` and uses `ContainerCheckpoint`.
That existing container-only route does not implement whole-Pod restore.

A [plain upstream single-node baseline](one-node-baseline.md) separately
checks Kubernetes v1.37.2 without the k3s packaging. Default API discovery
alone would not disprove an opt-in alpha feature; it is recorded alongside
the rejected gate and missing source call paths, rather than treated as a
replacement for those checks.

## Upstream implementation status

The [checkpoint implementation PR](https://github.com/kubernetes/kubernetes/pull/140186)
and [restore implementation PR](https://github.com/kubernetes/kubernetes/pull/143579)
remain open. The restore series builds on the checkpoint series. Heads
observed during the audit were:

- Checkpoint: `27d02988e4fb3008c4d29e9db6ad844f58cb8d1b`.
- Restore: `5533040d9fce30bc30ed3c07e8b187ed788c2f8a`.
- Kubernetes master: `c41dac6b0f518f2068c98b323964949fd015e760`.

Those are implementation candidates, not released Kubernetes interfaces.
The current PRs also differ from older KEP text: the checkpoint resource
is in `node.k8s.io/v1alpha1`, and restore uses a structured
`Pod.spec.restoreFrom`. A custom CRD or a Firecracker-only snapshot
endpoint would not satisfy the requested Kubernetes acceptance gate.

## Reproduction

On the designated worker after reconstructing the Step 2 test image:

```sh
tools/kubernetes/audit-interfaces.sh
```

The script downloads the checksummed binaries, runs each probe in an isolated
Docker container, captures revisions, hashes and gate errors, and removes its
containers and temporary server data. It prints `AUDIT_LOGS` for the raw logs
and receipt. Exit status 2 with
`KUBERNETES_POD_CHECKPOINT_API_UNAVAILABLE` means all three tested builds
reject the required gate. A changed result needs discovery and kubelet
verification; the script does not automatically claim full API support.

The released-build requirement remains in force. No experimental Kubernetes
build has been substituted. The Kubernetes acceptance portion of Step 3 remains
unavailable until a compatible release exists or the user changes that
requirement explicitly.

Work can continue on CRI integration, ordinary Firecracker RuntimeClass Pods
and the native VM checkpoint/restore engine independently of that release gate.
Checkpoint/restore has not been removed from Step 3. Runtime-level restoration
must work, and Kubernetes must initiate Pod checkpoint and restore through the
supported interfaces before this milestone is complete. The current runtime
still lacks that complete native Pod restore path; the newer CRI method
definitions and the earlier vHive snapshot path do not establish it.

This audit does not claim CRI conformance, native Pod restore, or completion of
Step 3. The current implementation and validation are tracked in
[Step 3 implementation status](step-3.md).
