# Migrated Step 2 worker attempts: incomplete

These are revision-specific worker results, including every collected failed
attempt. **No final Step 2 or published-vHive acceptance is established.** This
documentation checkout retains runtime code
`1c009d16649aed2e864b6e0330b740ba2802cb5f` unchanged. The latest worker attempt,
`82a1c43cc971`, is **diagnostic only, not a release candidate**, and still fails
clone shutdown. Its console markers show that the agent successfully exits and
guest PID 1 waits for remaining workload processes. The speculative `cf0d9708`
bounded drain did not address that observed failure. Agent-owned task kill/wait
correction and fresh acceptance remain pending.

## Recorded sources and outcomes

| Runtime source | Runtime tree | Recorded result |
| --- | --- | --- |
| `b0498f3a598e06fa54f7bf0089cb8c7e6b01a7c1` | `86c0398751842e6b58ca630f843f4ef88e5b80de` | Lint/build/private install/full `test-stack.sh` passed, ending in `COMMITTED_STACK_ACCEPTANCE_OK`: 127 verbose top-level passes and one explicit SDK `TestLogAndMetrics` skip. The subsequent vHive attempt failed before its first VM because `route` was missing; the overall pipeline exited 2. [Original summary](b0498f3a598e/summary.json). |
| `718f305bcf2a481e98e854ca5960cda5880ce362` | `eb3fc9847735c046692dca04540999aacf5f09c8` | Build/install/test-image preparation passed after adding the vHive image prerequisite. `TestPauseSnapResume` passed; both clone tests failed before clone boot because private mount validation rejected the normal `/dev/mapper` alias with `ELOOP`. Pipeline exited 1. No full-stack rerun is recorded for this revision. [Original summary](718f305bcf2a/summary.json). |
| `1c009d16649aed2e864b6e0330b740ba2802cb5f` | `02f863ae454e5976edeb0150726a79e2ddefab26` | Build/install/test-image preparation passed after the device-alias correction. `TestPauseSnapResume` passed. The two clone tests reached graceful StopVM but failed because the VMM did not exit before the host deadline and was forcefully terminated. Pipeline exited 1; no full-stack rerun is recorded at this revision. [Original summary](1c009d16649a/summary.json). |
| `cf0d9708bb3412507ddcc5110fccb1b4545f6b75` | `6ddf649bcb5877b5b16da5e8daca02b788f9f9d8` | Build/install/test-image preparation passed with the bounded final agent drain. `TestPauseSnapResume` passed in 16.27 s; `TestSnapshotConcurrentClones` failed in 64.49 s and `TestSnapshotSourceMemoryAndDiskIsolation` failed in 45.43 s during graceful clone shutdown. Pipeline exited 1; no full-stack rerun is recorded. **The bounded drain did not fix worker acceptance.** [Original summary](cf0d9708bb34/summary.json). |
| `82a1c43cc971a8e0e4100709a9e4e4d12c11a1b3` | `d59d6a654e4e1b1843e310e82283e74a03dadb64` | **Diagnostic instrumentation only; not a release candidate.** `TestPauseSnapResume` passed in 15.73 s; concurrent clones failed in 64.37 s and memory/disk isolation failed in 46.89 s at graceful clone shutdown. Pipeline exited 1. Agent and PID 1 console evidence below localizes the remaining shutdown wait; it does not establish acceptance. [Original summary](82a1c43cc971/summary.json). |

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
The next corrective work is bounded agent-owned shutdown of managed guest tasks,
including kill/wait/event/IO correctness. It is not yet validated by this bundle;
raising the acceptance timeout is not a correction. Both the speculative `cf0`
implementation and the `82` diagnostic instrumentation are archived outside the
intended corrective release code ancestry. These receipts remain as failed and
diagnostic evidence regardless of later results.

## Inputs and provisional consumer boundary

All five source locks select official, unmodified Firecracker/jailer v1.18.0
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
of future success of the provisional harness.

## Integrity and scope

The five attempt subdirectories are byte-identical copies of the original collections
`stage2-worker-evidence`, `stage2-718-worker-evidence`,
`stage2-1c009-worker-evidence`, `stage2-cf0-worker-evidence` and
`stage2-82-diagnostic-evidence`. Their original
`summary.json` files and all raw logs/provenance are unchanged; the cf0 summary
includes its completed post-run cleanup check. The 76 files listed by the original summaries
were independently checked against their recorded lengths and SHA-256 hashes.
[SHA256SUMS](SHA256SUMS) additionally covers all five original summary files:
**81 original collection files**, plus two separately collected cf0 agent/rootfs
provenance checks (**83 byte-preserved files total**). All prior 67 files remain
unchanged; this append adds only the 82 diagnostic collection and cf0 test-image
proof. Previous historical receipts were left untouched.
Empty or repeated-content logs retain their per-attempt identity; Git deduplicates
identical blob content.

The successful full-stack result belongs only to `b0498f3a598e`; it must not be
reported as a rerun of `718f305b`, `1c009d16`, `cf0d9708`, diagnostic `82a1c43c`, or
this documentation tip. No runtime native restore, CRI conformance, Kubernetes restore,
Step 4 storage, PMEM, reclamation or memory-savings acceptance follows from this
bundle. Fresh exact-source full validation and all required graceful clone
shutdown checks remain pending. Historical evidence elsewhere remains historical.
