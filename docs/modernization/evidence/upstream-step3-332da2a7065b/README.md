# Migrated Step 3 native/volume and focused CRI acceptance

Clean source `332da2a7065baa0050037250bbf2ff316a1a8519`, tree
`ca3b1392749b9337479a79c6a7c9c39883ccc507`, passed this bounded worker pipeline.
The [original summary](summary.json) records exit 0 and
`STAGE3_NATIVE_VOLUME_AND_FOCUSED_CRI_OK`. The
[source receipt](stage3-tested-source-332da2a7065b.txt) identifies the exact
tested commit/tree; the [command statuses](stage3-command-status-332da2a7065b.tsv)
record successful build, installation, native, volumes and focused-CRI commands.
This documentation tip was not built or tested. Full CRI remains WIP;
Kubernetes Pod checkpoint/restore is deferred to the 1.38 interface work.
Final published-runtime-module and installed-vHive consumer acceptance remain
pending. This collection does not establish those gates or Step 4 acceptance.

## Commands and observed results

The recorded command order is the current [Step 3 procedure](../../step-3.md):
`build-stack.sh`, `install-stack.sh`, `test-native-restore.sh`,
`test-native-volumes.sh` with the checkout's Firecracker/kernel paths, and
`test-cri.sh` with `CRI_INTEGRATION_TESTS=1` and
`CRI_FOCUS='Runtime info|starting container with volume'`.

The [native log](stage3-native-332da2a7065b.log) records lint and the runner's
focused race/unit checks, test-image creation, and all eight native VM leaves:

| Group | Nonjailed | Jailed |
| --- | --- | --- |
| `TestNativeCheckpoint_Isolated` | PASS 32.67 s | PASS 32.89 s |
| `TestNativeForkTransport_Isolated` | PASS 36.67 s | PASS 36.99 s |
| `TestNativeCheckpointInterrupt_Isolated` | PASS 18.81 s | PASS 19.33 s |
| `TestNativeRestore_Isolated` | PASS 18.52 s | PASS 19.19 s |

The log ends with `COMMITTED_NATIVE_RUNTIME_RESTORE_OK`. These cases cover the
runner's checkpoint, source/two-clone streams/events, cancellation recovery and
restore profile with immutable guest root and private workload storage/volumes.
They do not exercise the later Step 4 guest-upper or PMEM profile.

The [volume log](stage3-volumes-332da2a7065b.log) records
`TestVolumeCheckpointRealVM` PASS (2.46 s), captured counter 41 and restored
counter 42 after advancement, logical inode 2, shared counter 42 and retained
orphan contents `orphan42`. This purpose-built volume VM fixture separately
checks host volume table/data continuity; it does not provide CRI Pod restore.

The [focused CRI log](stage3-cri-focused-332da2a7065b.log) and original
[critest output](cri-332da2a7065b.q9oGzdxZ/critest.log) record **four passed,
zero failed and 138 skipped** of 142 specs using critest v1.37.0: runtime
version/conditions and volume startup with ordinary/symlink host paths.
`TestPodVMResources` passed (13.11 s), including `declared_limits`,
`unspecified_CPU_period` and `unbounded_uses_requests_and_fallback`;
`TestPodVolumes` passed (6.55 s). The log ends with `CRI_WORKER_TESTS_PASSED`.
The [inputs](cri-332da2a7065b.q9oGzdxZ/inputs.log) identify clean exact source,
`websocket_streaming=0` and `runtime_integration_tests=1`. The
[JSON report](cri-332da2a7065b.q9oGzdxZ/report.json),
[runtime version](cri-332da2a7065b.q9oGzdxZ/runtime-version.log),
[CRI version](cri-332da2a7065b.q9oGzdxZ/cri-version.log) and
[containerd output](cri-332da2a7065b.q9oGzdxZ/containerd.out) preserve detail.
This is focused CRI coverage, not full CRI conformance or Kubernetes validation.

## Exact inputs and actual artifacts

The [build log](stage3-build-332da2a7065b.log) and native log record the verified
published SDK selection
`github.com/vhive-serverless/firecracker-go-sdk v0.0.0-20261112180021-dd9db3224329`,
commit `dd9db322432989b96cff7a38a64703d60b6cabf9`, containerd 2.3.7 and runc
1.4.2. The source [versions.json](../../versions.json) remains unchanged and
retains exact release/schema/submodule inputs. The build output identifies the
co-versioned CRI frontend's selected modules separately from the parent runtime.
These logs are not a replacement for a separately collected compiled SDK module
graph or the final installed-vHive consumer's module receipt.

The [install log](stage3-install-332da2a7065b.log) records official unmodified
Firecracker and jailer v1.18.0 with actual SHA-256 hashes:

| Artifact | SHA-256 |
| --- | --- |
| Firecracker | `b33f46cd6d88834cf6a4f8d184f08f219809c636236bffd286d233a60602a2ba` |
| jailer | `ddadbaa15b5c942271a83b7040ca7259c8ccf92a9e08d44cce0b86fa407ba460` |
| installed guest rootfs | `67be06be4790c196d710b4d24ca2b396431112fd00b79d101b6d253f655e8bd0` |
| rebuilt native-test rootfs | `b8455888c02d3eab83d9ea7ca1000d9978f46830eefbdb7b09d9d67166f56c72` |

The install rootfs and rebuilt test rootfs are distinct artifacts; no byte
identity or final installed-consumer acceptance is inferred. The generated
[native binary manifest](native-restore/binaries.sha256),
[runtime revision](native-restore/runtime-revision),
[submodule identities](native-restore/submodules) and
[test-image inspection](native-restore/test-image.json) retain actual native
test provenance. The CRI image ID in its inputs is
`sha256:17046a2aeb5c501161f0761094b96b3f5347602888759d56ac8cf9d6c40ab64d`.
The summary records a clean worker checkout, removal of owned native/CRI
containers and pool, and preservation of shared pools and the unrelated
existing Kubernetes control-plane container.

## Integrity and limits

[SHA256SUMS](SHA256SUMS) covers the 23 original summary-listed files and the
original summary itself: **24 byte-preserved files**. Each named file was
checked against the frozen input's recorded length and SHA-256 before copying;
the original summary and raw output were not edited. Prior historical receipts
elsewhere remain unchanged and retain their original dependency/source scope.

This acceptance covers only exact `332da2a7065b` and the commands above. It does
not claim a repeated full Step 1/2 regression, a published vHive consumer run,
full CRI compliance, Kubernetes Pod restore, Step 4 storage/reclaim acceptance
or new performance/memory-savings measurements. Earlier approved milestone
documents and receipts will be carried forward during cumulative assembly.
