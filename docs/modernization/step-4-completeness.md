# Step 4 implementation completeness

## Current status

At candidate revision `7588b07`, the stargz storage matrix passed all four
profiles (disk, reclaim, pmem, combined) under both jailer modes. The guest
stargz store and overlay upper/work are bound to `/rom/overlay/stargz` on the
private guest-upper filesystem. These integration tests use a static owned TAP
setup; they do not establish default CNI coverage or cold lazy-fetch behavior.
Stargz workload lower layers remain FUSE-backed.

Native capture/restore acceptance is still open. After correcting CRI
fixture annotations, development tests passed disk with both jailers and
ext4-block without the jailer. The ext4-block jailed case failed on a
post-restore image-layer read after six registry header-timeout attempts.
The cause remains under investigation; this is a real acceptance failure,
not a waived test. Therefore this report does not mark Step 4 complete. The 21-trial memory evaluation uses the standard guest image and does
not measure the stargz change.

Kubernetes Pod checkpoint/restore integration is deferred to the 1.38
interface work because the required endpoints were absent from the tested
releases. This is separate from observed CRI conformance failures and the Step
4 storage/restore work. It does not claim that a particular 1.38 build already
implements those APIs.

## Historical audit at `8340f72`

The matrix below preserves the earlier source/evidence audit of
`damon-virtio-pmem` against `goal.md:148-226`. Its stargz tmpfs findings and
open follow-up describe that audited revision only; they are superseded by the
current status above. The original requirements are restated for traceability.

## Historical requirement matrix

| Goal requirement | Finding and evidence | Status |
| --- | --- | --- |
| Verify actual initial storage; distinguish guest overlay, workload layer, temporary mounts (`goal.md:156-170`) | Baseline worker audit records SquashFS + tmpfs guest upper, devmapper/ext4 workload roots, and `/dev`, `/run`, `/dev/shm` tmpfs (`docs/modernization/step-4.md:18-38`). Step 1 separately documents the guest stargz store/upper as tmpfs (`step-1.md:63-69`). | Verified for the audited revision; the stargz finding is historical. |
| Move persistent writes to per-VM host-disk filesystem with capacity, ownership, cleanup, ENOSPC handling (`goal.md:160-175,218-220`) | `runtime/guest_storage.go:38-97` attaches a per-VM ext4 upper disk by default (512 MiB; 64–65536 MiB range), checks immutable root and UUID boot selection. `:100-198` verifies host filesystem type, formats/reserves capacity, cleans failed allocation. Tests/evidence cover mount/write/copy-up/rename/delete, ENOSPC recovery and cleanup (`step-4.md:83-145`, clean `8f8d00f` receipt). Container roots remain devmapper-backed. | Accepted for default devmapper and ordinary runtime paths in the audited revision; later stargz storage acceptance is summarized above. |
| Preserve only appropriate temporary tmpfs (`goal.md:170`) | At the audited revision, `/dev`, `/run`, `/dev/shm` are explicitly temporary and remain tmpfs (`step-4.md:29-31`), while the stargz service used tmpfs (`step-1.md:67-69`). | Historical open finding; current stargz backing is covered by the storage matrix above. |
| Independently configurable pmem/DAX and DAMON/free-page reporting; verify actual operation and document deviations (`goal.md:177-186`) | `runtime/pmem_root.go:75-134` configures verified read-only ext4 image as pmem/DAX; `:137-245` digest cache and explicit immutable sharing. `runtime/guest_reclamation.go:24-72` independently configures DAMON reclaim and VMM free-page reporting. Pinned-kernel behavior/deviations and config contract are described in `step-4-pmem.md:25-63`, `step-4-reclamation.md:25-70`. Actual DAX and reclaim/reporting checked in component and restore tests; clean receipts at `438c2cb`, `9706231`, `020f85b`, `72f4fc9`. | Accepted with documented constraints: immutable cache persists and requires admin GC (`step-4-pmem.md:32-39`); pmem snapshots depend on fixed path contract (`:56-63`); reporting counters can lag flush. |
| Snapshot consistency: quiesce, flush/copy, prevent advanced source backing, private writable restores (`goal.md:188-196`) | `runtime/native_checkpoint.go:198-223` pauses tasks and VMM, freezes/saves exported volumes, then saves VMM files; `:233-255` copies every configured drive and immutable root. `:282-295` thaws before atomic publication. Restore validations and ordering/limitations are in `step-4.md:221-259`. Storage matrix advances source data, stops source VMM/removes its directory, validates captured state, hashes, private clone inodes and restored I/O (`native_restore_integ_test.go:60-72,~120-283`). | Accepted for direct same-host runtime API and tested layouts. External writers to exported volumes need independent quiescence; networked/cross-host and pmem path relocation are out of scope and disclosed (`step-4.md:227-250,270-278`). |
| Interrupted checkpoint cleanup and continued source usability | New `TestNativeCheckpointInterrupt_Isolated` observes nonempty staged memory, disconnects the control transport, checks stage/destination removal, task running/exec, and a successful subsequent capture in both jailer modes (`native_checkpoint_interrupt_integ_test.go:37-46,100-145`). Clean committed receipt at `5186d40`, both modes/lint (`step-4.md:423-444`). | Accepted for transport interruption after quiescing. This is not an injected storage-I/O failure test; do not broaden the claim. |
| Evaluate original, disk, pmem, reclaim, combined using identical workload/resources and required metrics (`goal.md:198-226`) | `REPORT.md` records 21/21 trials, seven arms × three randomized blocks, two VMs/trial, same guest tree/workload/resources. It reports cgroup peak/time, idle anon/file, PSS, CPU, fault, guest reactivation latency, create/readback latency, disk allocation counters, correctness and observed tradeoffs. `trials_compact.csv` includes per-file `st_blocks`/thin-pool and cumulative physical I/O; `DATA_DICTIONARY.md:4-16` defines limits. | Accepted with explicit missing views: `missing_metrics.csv` lists unavailable full-trial mapped PSS (sampler lacks brackets) and full-trial physical-I/O deltas (initial bracket unavailable); report uses valid idle PSS and cumulative end counters instead. The reporting-frame freshness limitation is also explicit (`REPORT.md` reporting paragraph; `step-4.md:72-75`). Measurements are one worker, three blocks, warmed backing, not general estimates. |
| Step 3 lifecycle and restore tests with Step 4 storage/mechanisms; reproducible branch (`goal.md:222-226`) | Clean `72f4fc9` passes 14 direct native restore cases, source termination, task lifecycle and six restored reclaim/reporting paths; Step 4 ordinary Kubernetes tests pass four profiles on a pinned runtime image. Later runtime test-harness fixes were targeted, not one uninterrupted full-suite run (`step-4.md:204-225,288-391`). | Historical status: direct runtime/storage scope accepted; CRI suite had 16 failures/29 skips. Pod checkpoint/restore is deferred to 1.38 per current clarification, not a Step 4 storage gate. |

## Historical follow-up at `8340f72`

1. The audited source had an unresolved guest stargz tmpfs path. The current storage matrix addresses its disk-backing coverage; native restore acceptance remains in progress.
2. Preserve the distinction between Step 4 completion and inherited Step 3 status. CRI conformance remains work in progress. Kubernetes Pod checkpoint/restore is deferred to 1.38 interface availability.
