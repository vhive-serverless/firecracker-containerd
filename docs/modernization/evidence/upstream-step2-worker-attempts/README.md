# Migrated Step 2 worker attempts: incomplete

These are revision-specific worker results, including every collected failed
attempt. **No final Step 2 or published-vHive acceptance is established.** This
documentation checkout retains runtime code
`1c009d16649aed2e864b6e0330b740ba2802cb5f` unchanged. The latest recorded worker
attempt, `cf0d9708bb34`, includes the bounded final agent drain but **still fails
clone shutdown**. The proposed drain root-cause explanation remains unconfirmed;
local regression reproduction did not establish or resolve the worker failure.

## Recorded sources and outcomes

| Runtime source | Runtime tree | Recorded result |
| --- | --- | --- |
| `b0498f3a598e06fa54f7bf0089cb8c7e6b01a7c1` | `86c0398751842e6b58ca630f843f4ef88e5b80de` | Lint/build/private install/full `test-stack.sh` passed, ending in `COMMITTED_STACK_ACCEPTANCE_OK`: 127 verbose top-level passes and one explicit SDK `TestLogAndMetrics` skip. The subsequent vHive attempt failed before its first VM because `route` was missing; the overall pipeline exited 2. [Original summary](b0498f3a598e/summary.json). |
| `718f305bcf2a481e98e854ca5960cda5880ce362` | `eb3fc9847735c046692dca04540999aacf5f09c8` | Build/install/test-image preparation passed after adding the vHive image prerequisite. `TestPauseSnapResume` passed; both clone tests failed before clone boot because private mount validation rejected the normal `/dev/mapper` alias with `ELOOP`. Pipeline exited 1. No full-stack rerun is recorded for this revision. [Original summary](718f305bcf2a/summary.json). |
| `1c009d16649aed2e864b6e0330b740ba2802cb5f` | `02f863ae454e5976edeb0150726a79e2ddefab26` | Build/install/test-image preparation passed after the device-alias correction. `TestPauseSnapResume` passed. The two clone tests reached graceful StopVM but failed because the VMM did not exit before the host deadline and was forcefully terminated. Pipeline exited 1; no full-stack rerun is recorded at this revision. [Original summary](1c009d16649a/summary.json). |
| `cf0d9708bb3412507ddcc5110fccb1b4545f6b75` | `6ddf649bcb5877b5b16da5e8daca02b788f9f9d8` | Build/install/test-image preparation passed with the bounded final agent drain. `TestPauseSnapResume` passed in 16.27 s; `TestSnapshotConcurrentClones` failed in 64.49 s and `TestSnapshotSourceMemoryAndDiskIsolation` failed in 45.43 s during graceful clone shutdown. Pipeline exited 1; no full-stack rerun is recorded. **The bounded drain did not fix worker acceptance.** [Original summary](cf0d9708bb34/summary.json). |

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

## Inputs and provisional consumer boundary

All four source locks select official, unmodified Firecracker/jailer v1.18.0
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
This checks built artifacts; it does **not** extract the actual test Docker image
layer or guest memory, prove which bytes a restored guest was executing, or
establish the clone-shutdown root cause. The original failed-run collection and
summary remain unchanged; the diagnostic is a separate additional record.

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

The four subdirectories are byte-identical copies of the original collections
`stage2-worker-evidence`, `stage2-718-worker-evidence`,
`stage2-1c009-worker-evidence` and `stage2-cf0-worker-evidence`. Their original
`summary.json` files and all raw logs/provenance are unchanged; the cf0 summary
includes its completed post-run cleanup check. The 62 files listed by the original summaries
were independently checked against their recorded lengths and SHA-256 hashes.
[SHA256SUMS](SHA256SUMS) additionally covers all four original summary files:
**66 original collection files**, plus the separately collected agent/rootfs
diagnostic (**67 byte-preserved files total**). The first three attempt directories are preserved
unchanged from their initial evidence commit; only the new cf0 attempt was
appended. Previous historical receipts were left untouched.
Empty or repeated-content logs retain their per-attempt identity; Git deduplicates
identical blob content.

The successful full-stack result belongs only to `b0498f3a598e`; it must not be
reported as a rerun of `718f305b`, `1c009d16`, the later shutdown correction, or
this documentation tip. No runtime native restore, CRI conformance, Kubernetes restore,
Step 4 storage, PMEM, reclamation or memory-savings acceptance follows from this
bundle. Fresh exact-source full validation and all required graceful clone
shutdown checks remain pending. Historical evidence elsewhere remains historical.
