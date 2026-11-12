# Migrated Step 2 worker evidence

These are revision-specific worker results, including every collected failed
attempt. **No final Step 2 or published-vHive acceptance is established.** This
documentation checkout retains runtime code
`1c009d16649aed2e864b6e0330b740ba2802cb5f` unchanged. The latest recorded source,
`7386ab5c5e8c`, corrects the dependency classification after the managed-task
shutdown fix and **passes all three unchanged provisional vHive VM cases**.
Its full regression is running separately and is not claimed passed. The older
`9000` full-regression cleanliness failure remains recorded, without a runtime
VM verdict. Final published runtime
dependency and installed-vHive consumer acceptance remain pending. Earlier
failed and diagnostic attempts retain their original outcomes.

## Recorded sources and outcomes

| Runtime source | Runtime tree | Recorded result |
| --- | --- | --- |
| `b0498f3a598e06fa54f7bf0089cb8c7e6b01a7c1` | `86c0398751842e6b58ca630f843f4ef88e5b80de` | Lint/build/private install/full `test-stack.sh` passed, ending in `COMMITTED_STACK_ACCEPTANCE_OK`: 127 verbose top-level passes and one explicit SDK `TestLogAndMetrics` skip. The subsequent vHive attempt failed before its first VM because `route` was missing; the overall pipeline exited 2. [Original summary](b0498f3a598e/summary.json). |
| `718f305bcf2a481e98e854ca5960cda5880ce362` | `eb3fc9847735c046692dca04540999aacf5f09c8` | Build/install/test-image preparation passed after adding the vHive image prerequisite. `TestPauseSnapResume` passed; both clone tests failed before clone boot because private mount validation rejected the normal `/dev/mapper` alias with `ELOOP`. Pipeline exited 1. No full-stack rerun is recorded for this revision. [Original summary](718f305bcf2a/summary.json). |
| `1c009d16649aed2e864b6e0330b740ba2802cb5f` | `02f863ae454e5976edeb0150726a79e2ddefab26` | Build/install/test-image preparation passed after the device-alias correction. `TestPauseSnapResume` passed. The two clone tests reached graceful StopVM but failed because the VMM did not exit before the host deadline and was forcefully terminated. Pipeline exited 1; no full-stack rerun is recorded at this revision. [Original summary](1c009d16649a/summary.json). |
| `cf0d9708bb3412507ddcc5110fccb1b4545f6b75` | `6ddf649bcb5877b5b16da5e8daca02b788f9f9d8` | Build/install/test-image preparation passed with the bounded final agent drain. `TestPauseSnapResume` passed in 16.27 s; `TestSnapshotConcurrentClones` failed in 64.49 s and `TestSnapshotSourceMemoryAndDiskIsolation` failed in 45.43 s during graceful clone shutdown. Pipeline exited 1; no full-stack rerun is recorded. **The bounded drain did not fix worker acceptance.** [Original summary](cf0d9708bb34/summary.json). |
| `82a1c43cc971a8e0e4100709a9e4e4d12c11a1b3` | `d59d6a654e4e1b1843e310e82283e74a03dadb64` | **Diagnostic instrumentation only; not a release candidate.** `TestPauseSnapResume` passed in 15.73 s; concurrent clones failed in 64.37 s and memory/disk isolation failed in 46.89 s at graceful clone shutdown. Pipeline exited 1. Agent and PID 1 console evidence below localizes the remaining shutdown wait; it does not establish acceptance. [Original summary](82a1c43cc971/summary.json). |
| `9000f53fb03d23699b1a88fe167d6f4aa5a0a707` | `c7cf1a852be45366dd85e44ca703c88d6349d9b3` | Build/install/test-image preparation and all three unchanged provisional vHive cases **passed**: source pause/snapshot/resume 16.00 s, concurrent clones 27.57 s, source-memory/disk isolation 21.72 s. That pipeline exited 0. The separate full regression exited 1 at dependency cleanliness before runtime VM cases; final published-consumer acceptance remains open. [Original provisional summary](9000f53fb03d/summary.json), [completed regression summary](9000f53fb03d-regression/summary.json). |
| `7386ab5c5e8c3519ad1698c27f548803e714e304` | `45c82884ea40ff22e5407da36911cf344d6f9e6c` | After the metadata-only direct-dependency correction, build/install/test-images and the same three VM cases **passed**: 15.50 s, 28.12 s and 21.73 s respectively. Pipeline exited 0. Full regression is running separately; final published-consumer/installed-stack acceptance is pending. [Original summary](7386ab5c5e8c/summary.json). |

At `1c009d16`, the continuity test reached its final clone stop after checking
captured exec/counter continuity, independent source/clone writes, and clone
operation after deleting the source. The concurrent-clone test reached its
first clone stop after both clones served requests with distinct endpoints and
disks. These are partial observations before fatal assertions, **not passes of
either test**. The raw test and containerd output retain the shutdown failures.
The precise cause was still under investigation when this evidence was collected;
an unbounded final agent RPC drain is a separate source-level diagnosis, not a
stack trace captured by these logs. The later `cf0d9708` attempt retains the same
two failing graceful-shutdown cases despite implementing that bounded drain.
Its original summary also records completed cleanup: clean runtime checkout,
dedicated vHive pool/container absent, and the unrelated Kubernetes container
still running. This cleanup result does not turn the failed tests into passes.

## Diagnostic shutdown finding, not acceptance

The `82a1c43cc971` [containerd/guest-console log](82a1c43cc971/vhive-82a1c43cc971.iDUM5rch/containerd.out)
records each restored clone's drain returning successfully, Serve returning
`ttrpc: server closed`, `group.Wait` returning cancellation, and main returning
success rapidly. Guest systemd proceeds to global shutdown and broadcasts
SIGTERM, then reports `Waiting for process: 1026 (python)` for both concurrent
clones; the continuity clone reports remaining sleep/python processes. Thus the
observed VMM wait happens **after successful agent main exit**, rather than inside
the speculative blocked RPC drain. The `cf0` local blocked-write regression does
not explain these worker failures.

The source vHive path deletes its task with `WithProcessKill` before StopVM;
restored clones have no corresponding host task handle and skip that deletion.
The corrective work is bounded agent-owned shutdown of managed guest tasks,
including kill/wait/event/IO correctness. Its provisional worker result is
recorded below; broader validation remains separately scoped. Both the speculative `cf0`
implementation and the `82` diagnostic instrumentation are archived outside the
intended corrective release code ancestry. These receipts remain as failed and
diagnostic evidence regardless of later results.

## Corrective provisional acceptance at 9000

Exact source `9000f53fb03d23699b1a88fe167d6f4aa5a0a707` descends directly from
`1c009d16`, excluding the speculative `cf0` drain and `82` instrumentation from
implementation ancestry. Managed guest tasks are stopped before final agent
cancellation. With that correction, the same three tests now complete all their
existing source/clone continuity, isolation and graceful-stop assertions. No
acceptance timeout, test source or assertion was changed to obtain the result;
the runtime shutdown timeout and harness files match the `1c009d16` source.

The [command-status receipt](9000f53fb03d/stage2-command-status-9000f53fb03d.tsv)
records exit 0 for build, install, test-images and vHive. The
[test log](9000f53fb03d/vhive-9000f53fb03d.eNO7tGXN/vhive-tests.log) records all
three PASS lines and the final continuity observation after successful clone
stop. This establishes that the managed-task correction fixed the observed
clone-stop failure in this provisional worker profile. The original provisional
summary correctly records that the full regression was still running when it
was collected; it is preserved unchanged. The completed result follows below.
Published-module and installed-consumer acceptance remain open.

## Separate full-regression dependency cleanliness failure

The [final regression log](9000f53fb03d-regression/stage2-acceptance-9000f53fb03d.log)
and [terminal status](9000f53fb03d-regression/stage2-full-regression-status-9000f53fb03d.txt)
record exit 1 at the generated dependency/protobuf cleanliness gate, **before
the runtime VM regression tests**. `go mod tidy` promotes the already-selected
`github.com/containerd/errdefs/pkg v0.3.0` from indirect to direct because the new
shutdown implementation imports `errgrpc`. The resulting `go.mod` diff is one
insertion and one deletion, without a version change.

This is a failed cleanliness gate, not a runtime VM regression result. It does
not invalidate or broaden the separate three-case provisional vHive pass. The
dependency classification correction is recorded at `7386` below; its full
regression retry remains separately in progress, using unchanged acceptance
checks. Only the frozen final summary and its two named
hashed files are archived here; intermediate `*.initial-running-copy` files
are deliberately excluded.

## Corrected metadata provisional acceptance at 7386

Exact source `7386ab5c5e8c3519ad1698c27f548803e714e304`, tree
`45c82884ea40ff22e5407da36911cf344d6f9e6c`, is a direct child of `9000`.
Its only source-tree change promotes the existing `errdefs/pkg v0.3.0` entry
from indirect to direct in `go.mod`; runtime code, selected versions, sums and
the acceptance harness remain unchanged.

The [command-status receipt](7386ab5c5e8c/stage2-command-status-7386ab5c5e8c.tsv)
records exit 0 for build, install, test-images and vHive. The
[VM test log](7386ab5c5e8c/vhive-7386ab5c5e8c.zVF8PyYI/vhive-tests.log) records
`TestPauseSnapResume` PASS in 15.50 s, `TestSnapshotConcurrentClones` PASS in
28.12 s and `TestSnapshotSourceMemoryAndDiskIsolation` PASS in 21.73 s. This
repeats the provisional profile's source continuation, independent clones and
graceful stops at the corrected exact revision. Full Step 2 regression is
running separately when this collection is frozen; it is **not claimed passed**.
Published runtime dependency and installed-vHive consumer acceptance remain open.

## Inputs and provisional consumer boundary

All seven source locks select official, unmodified Firecracker/jailer v1.18.0
release binaries, published SDK
`dd9db322432989b96cff7a38a64703d60b6cabf9`
(`v0.0.0-20261112180021-dd9db3224329`), containerd v2.3.7 and runc v1.4.2.
Use each recorded runtime's `docs/modernization/versions.json` for its complete
input lock. Each attempt retains installed runtime/shim/kernel/rootfs hashes in
its install log, plus vHive test binary hashes/metadata, selected module graph
and base-image identity. Only `b0498f3a598e` also has the full-stack generated
agent/build provenance collection. Image preparation can rebuild guest images
after installation; no installed/test-rootfs byte identity is asserted here.
The separately collected [cf0 agent/rootfs diagnostic](diagnostics/cf0-agent-rootfs-provenance.txt)
records matching SHA-256
`d4693c4811a178cfd3865c8ef8e8a1db3e9427a048bdff0c8d653955b20a29f9`
for the built agent, the staged guest agent and the agent extracted from the built
SquashFS. Its build information names `cf0d9708bb34` and `vcs.modified=false`.
That first receipt checks built artifacts. The separately collected
[cf0 actual test-image proof](diagnostics/cf0-test-image-agent-provenance.json)
additionally records extraction from the original run's exact stored test image:
image ID `sha256:afee504b391cf0b83f0e268763e32b12eec9d05b44de9f759f33b38351c5ad71`
matches the manifest-list export in the original cf0 vHive image-build log, and its extracted
rootfs contains the same cf0 agent hash. This verifies stored test-image bytes;
neither receipt is a live guest-memory extraction or a successful shutdown test.
Original failed-run collections and summaries remain unchanged; both provenance
checks are separately collected additional records.

The vHive test source is exactly
`89593103ac667cc096f89770c94a77b2b93c3a7d`. The selected function image is
`ghcr.io/ease-lab/helloworld@sha256:12dc6715ed1a8306f246ceaf7742c09e38a52a79d17421e4a50d7e0e09fdbc25`.
The tested legacy profile is standalone/unjailed, read-only guest root, one
writable devmapper workload drive and File memory.

These harness builds replace `github.com/firecracker-microvm/firecracker-containerd`
with the exact clean runtime archive at local path `/runtime`; each `inputs`
receipt explicitly says `purpose=provisional-local-module-candidate`, and each
`modules.json` records that replacement. The SDK remains the published dd9db32
module. This is **provisional local-replacement coverage**, not validation of
vHive consuming a published runtime module through its finalized installation
manifest. That published-consumer/installed-stack gate remains open, independently
of the successful `9000` and `7386` provisional harness runs.

## Integrity and scope

The eight attempt/phase subdirectories preserve the selected original collections
`stage2-worker-evidence`, `stage2-718-worker-evidence`,
`stage2-1c009-worker-evidence`, `stage2-cf0-worker-evidence` and
`stage2-82-diagnostic-evidence`, `stage2-9000-worker-evidence` and the final records
from `stage2-9000-regression-evidence`, plus `stage2-7386-worker-evidence`. Their original
`summary.json` files and all raw logs/provenance are unchanged; the cf0 summary
includes its completed post-run cleanup check. The 108 files listed by the original summaries
were independently checked against their recorded lengths and SHA-256 hashes.
[SHA256SUMS](SHA256SUMS) additionally covers all eight original summary files:
**116 original collection files**, plus two separately collected cf0 agent/rootfs
provenance checks (**118 byte-preserved files total**). All prior 102 files remain
unchanged; this append adds only the 7386 provisional acceptance collection.
Previous historical receipts were left untouched.
Empty or repeated-content logs retain their per-attempt identity; Git deduplicates
identical blob content.

The successful full-stack result belongs only to `b0498f3a598e`; it must not be
reported as a rerun of `718f305b`, `1c009d16`, `cf0d9708`, diagnostic `82a1c43c`, `9000`, `7386`, or
this documentation tip. No runtime native restore, CRI conformance, Kubernetes restore,
Step 4 storage, PMEM, reclamation or memory-savings acceptance follows from this
bundle. Successful 9000/7386 provisional clone checks do not certify the older
failed full-regression gate, the separately running corrected-source full
regression, or the final published/installed vHive consumer.
Historical evidence elsewhere remains historical.
