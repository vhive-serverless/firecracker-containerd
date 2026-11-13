# Corrected Step 3 native/volume and focused CRI acceptance at 751c

Exact source `751c03c6f9a98b8726bc4d9066efce78db96952d`, tree
`18f9c8c905eafa8fb86354b5996f68615e542852`, passed the lead-owned launcher
with exit 0 and `STAGE3_SAME_ID_NATIVE_VOLUME_AND_FOCUSED_CRI_OK`. The
[original terminal authority](lead-terminal-result.json),
[phase TSV](stage3-command-status-751c03c6f9a9.tsv) and
[collector summary](collector-summary.json) record build, install, native,
volumes and focused-CRI all 0. The
[concise collection report](collection-report.json) binds the frozen raw files,
actual image/artifact identity and independent cleanup. This later
documentation/cumulative-history tip was not separately built or VM-tested.

## Observed scope

All eight native VM leaves passed with no failures/skips in the
[native log](stage3-native-751c03c6f9a9.log):

| Group | Nonjailed | Jailed |
| --- | --- | --- |
| `TestNativeCheckpoint_Isolated` | 32.80 s | 33.01 s |
| `TestNativeForkTransport_Isolated` | 36.87 s | 37.07 s |
| `TestNativeCheckpointInterrupt_Isolated` | 18.87 s | 19.22 s |
| `TestNativeRestore_Isolated` | 19.14 s | 19.32 s |

The [volume VM log](stage3-volumes-751c03c6f9a9.log) records
`TestVolumeCheckpointRealVM` PASS in 2.43 s: captured counter 41, restored
counter advanced to 42, logical inode 2, shared view 42 and retained orphan
contents `orphan42`, with different source/restored VMM PIDs. This is host-volume
state/data continuity, not a CRI Pod snapshot/restore workflow.

The [focused CRI log](stage3-cri-focused-751c03c6f9a9.log),
[critest output](cri/critest.log) and [JSON report](cri/report.json) record
**four passed, zero failed and 138 skipped of 142 specs**, using focus
`Runtime info|starting container with volume`. These are runtime version/
conditions and ordinary/symlink host-volume startup. `TestPodVMResources`
passed all three subcases (12.62 s); `TestPodVolumes` passed (6.38 s). Actual
guest witnesses report two processors and MemTotal 363988/235348/493012 KiB
for the three resource profiles. The
[CRI inputs](cri/inputs.log) identify exact clean `751c`, runtime integration
tests enabled and websocket streaming disabled. Full CRI remains WIP;
Kubernetes Pod checkpoint/restore is deferred to the 1.38 interface work.

## Corrected implementation and limits

This source contains the inherited unjailed live-directory alias correction,
native recapture output-path translation and native-only jailed owned-stub
reuse. Captured disks retain their original private-mapping identities;
foreign/replaced/symlink/hardlinked stub targets remain rejected. See the
[Step 3 explanation](../../step-3.md) for the exact path/ownership boundaries.
The receipt claims only the actual runner's cases above, not an additional
unrecorded same-ID/recapture or buffered-input VM case. Earlier
[332da acceptance](../upstream-step3-332da2a7065b/README.md) and the separate
`064c` unread-input supplement retain their original source/image attribution.

Inherited [Step 2 combined coverage](../upstream-step2-2e03843816cf/README.md)
still includes its original exit-2 full run, passing unchanged five-VM repeats
and supplemental thinpool/SDK tail. Its known CNI race remains unfixed; this
focused Step 3 run does not turn it into a clean full-stack pass or repair it.
Those inherited receipts are not copied or rewritten by this archive.

## Exact provenance and cleanup

The native [image inspection](native-restore/test-image.json) and CRI input
receipt match actual image ID
`sha256:e0eef7a87dee88f2385d9b9f4159711b4f6a03e596419818396e76455afa7494`.
[Build binaries](build/binaries.sha256),
[native binaries](native-restore/binaries.sha256), agent/CRI build metadata,
parent/CRI module graphs and the
[official release receipt](build/firecracker-release.txt) are retained.
Agent/gateway metadata identifies exact clean `751c`; the selected SDK is
published dd9db32 and VMM/jailer are official unmodified Firecracker 1.18.0.

Installed rootfs SHA-256 is
`316db9b14b3bc177aacfde668a9fdee2dd2e6594e7fcd8072f78c307dd419e5d`;
the rebuilt native-test rootfs is
`6e859297fde4781c5efc5a045276ff10c6eaaa0f1f0dc7e58df8545d4b0fb1cd`.
Their byte identity is not asserted. The
[kernel reuse receipt](kernel-seed-lead-receipt.json) explicitly records use
of accepted `332da` kernel/config after equality checks of pinned build inputs;
no new kernel compilation is claimed.

The original collector's read-only cleanup observations confirm source clean,
owned native/CRI containers, pools, mappings, loops/backings and SDK cgroup
clear, while the unrelated Kubernetes control plane remained running. The
[post-transfer observation](post-transfer-observation.json) preserves the
independent final check. No worker actions were performed for this docs patch.

[SHA256SUMS](SHA256SUMS) covers all 45 byte-preserved worker/collector/report
files: 38 worker originals plus seven provenance records. The original raw
worker manifest is retained as [worker-SHA256SUMS](worker-SHA256SUMS).
Earlier raw evidence remains unchanged. Final published runtime and installed-
vHive acceptance, full CRI and Kubernetes Pod restore remain separate; this
receipt supplies no new memory/performance measurement.
