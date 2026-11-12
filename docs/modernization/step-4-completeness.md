# Step 4 acceptance

This preserves the original revision-specific assessment and measurements.
It does not certify the migrated candidate; see [current Step 4 status](step-4.md).

**Step 4 is complete for the authorized disk-backed storage and memory scope.**
Clean source `06b55e3b78cc6bedfe3f0671fcdc49df73046eb2` passed build, install,
and the full stack test on the designated worker. The checkout was clean, all
test-owned pools and VM files were removed, and no test containers remained.
The [verification record](evidence/step4-stargz/clean-06b55e3/verification.json)
contains commands, source and dependency hashes, pass counts, skips, and links
to raw output. The [CI run](https://github.com/vhive-serverless/firecracker-containerd/actions/runs/43172572356)
also passed.

The verbose full run reported 47 runtime, 7 snapshotter, and 79 SDK top-level
passes. The nonverbose package and CRI race checks and generation checks passed.
Three explicit skips remain documented: `TestMemoryEvaluation_Isolated` needs
one evaluation-trial configuration and the frozen 21-trial matrix is separately
validated; `TestLogAndMetrics` is an existing SDK skip; and
`TestNativeSnapshotVsockPolicy` is routed by the SDK to
`tools/test-native-snapshot.sh`, while native restore matrices ran separately.

## Requirement coverage

| Goal area | Evidence and scope | Status |
| --- | --- | --- |
| Per-VM disk-backed persistent writes (`goal.md`, Step 4A) | The default guest upper uses a private host-disk ext4 image. The retained guest stargz store and its container overlay upper/work paths use that same per-VM filesystem. `TestStargzGuestDiskStorage_Isolated` passed four profiles (disk, reclaim, pmem, combined) under both jailer modes in 84.80 s. It checks private backing, ownership/capacity, ext4 store and overlay paths, copy-up/rename/delete, ENOSPC recovery, synced persistence, and cleanup. Default non-stargz container writable roots remain devmapper-backed. | Accepted for tested storage paths and profiles. |
| Snapshot/restore with new storage (`goal.md`, Step 4C) | `TestStargzNativeRestore_Isolated` passed 12 cases: six storage profiles under both jailer modes, in 308.74 s. The full run also passed 14 standard native restore cases. Tests verify source termination, restored task state and I/O, private writable clone state, lower-file access, and cleanup. | Accepted for the tested same-host runtime API and storage layouts. |
| Pmem/DAX and DAMON/free-page reporting (`goal.md`, Step 4B) | Independent mechanism configuration and runtime checks are documented in [Step 4](step-4.md). Stargz `pmem` cases use the DAX-capable guest base image and check DAX on `/rom`; stargz workload lowers remain FUSE-backed. Reclamation-enabled profiles check the guest reclaim/reporting path. | Accepted in the documented configurations; this does not establish pmem/DAX for FUSE lowers. |
| Controlled memory evaluation (`goal.md`, Step 4D) | The frozen 21-trial evaluation uses the standard guest image: seven arms across three randomized blocks. It does not exercise the retained stargz storage path and supports no stargz-specific memory-savings claim. `missing_metrics.csv` records unbracketed full-trial mapped-PSS windows and unavailable full-trial physical-I/O deltas; the report presents idle PSS and cumulative end counters with those limits. | Accepted as a bounded evaluation of the measured standard-image workload; full-trial PSS/I/O views remain unavailable. |
| Ordinary Kubernetes and inherited Step 3 behavior | Ordinary Pod profile results are recorded in [Step 4](step-4.md). The unfiltered CRI conformance suite still has 16 failures and 29 skips, so Step 3 CRI compliance remains WIP. Kubernetes Pod checkpoint/restore is deferred to the 1.38 interface work under the current user clarification. | Step 3 remains WIP; this does not invalidate Step 4's storage and runtime-level acceptance. |

## Stargz test limits

The stargz integration matrices use a purpose-built static bridge and TAP
devices; they do not validate default CNI for that path. The image wrapper
prefetches 10 MiB, so lower-file reads are not a cold lazy-fetch measurement.
The container image lowers remain FUSE-backed. The pmem check is for the guest
base image at `/rom`, not those FUSE lowers. Restore exercises the direct runtime
API with replacement TAPs on one host; it does not validate CRI conformance,
Kubernetes Pod restore, or preservation of arbitrary network connections.

The [stargz evidence README](evidence/step4-stargz/README.md) records the
fixture investigation and component history. The final clean run's
[verification JSON](evidence/step4-stargz/clean-06b55e3/verification.json),
[test log](evidence/step4-stargz/clean-06b55e3/test.log), and
[cleanup receipt](evidence/step4-stargz/clean-06b55e3/cleanup.txt) provide the
committed acceptance record.
