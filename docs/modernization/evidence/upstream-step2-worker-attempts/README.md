# Migrated Step 2 worker attempts: incomplete

These are revision-specific worker results, including every collected failed
attempt. **No final Step 2 or published-vHive acceptance is established.** This
documentation-only addition is based on runtime
`1c009d16649aed2e864b6e0330b740ba2802cb5f`; that revision's cloned guests still
failed graceful shutdown. The separately proposed bounded final agent drain is
not tested by any receipt in this directory.

## Recorded sources and outcomes

| Runtime source | Runtime tree | Recorded result |
| --- | --- | --- |
| `b0498f3a598e06fa54f7bf0089cb8c7e6b01a7c1` | `86c0398751842e6b58ca630f843f4ef88e5b80de` | Lint/build/private install/full `test-stack.sh` passed, ending in `COMMITTED_STACK_ACCEPTANCE_OK`: 127 verbose top-level passes and one explicit SDK `TestLogAndMetrics` skip. The subsequent vHive attempt failed before its first VM because `route` was missing; the overall pipeline exited 2. [Original summary](b0498f3a598e/summary.json). |
| `718f305bcf2a481e98e854ca5960cda5880ce362` | `eb3fc9847735c046692dca04540999aacf5f09c8` | Build/install/test-image preparation passed after adding the vHive image prerequisite. `TestPauseSnapResume` passed; both clone tests failed before clone boot because private mount validation rejected the normal `/dev/mapper` alias with `ELOOP`. Pipeline exited 1. No full-stack rerun is recorded for this revision. [Original summary](718f305bcf2a/summary.json). |
| `1c009d16649aed2e864b6e0330b740ba2802cb5f` | `02f863ae454e5976edeb0150726a79e2ddefab26` | Build/install/test-image preparation passed after the device-alias correction. `TestPauseSnapResume` passed. The two clone tests reached graceful StopVM but failed because the VMM did not exit before the host deadline and was forcefully terminated. Pipeline exited 1; no full-stack rerun is recorded at this revision. [Original summary](1c009d16649a/summary.json). |

At `1c009d16`, the continuity test reached its final clone stop after checking
captured exec/counter continuity, independent source/clone writes, and clone
operation after deleting the source. The concurrent-clone test reached its
first clone stop after both clones served requests with distinct endpoints and
disks. These are partial observations before fatal assertions, **not passes of
either test**. The raw test and containerd output retain the shutdown failures.
The precise cause was still under investigation when this evidence was collected;
an unbounded final agent RPC drain is a separate source-level diagnosis, not a
stack trace captured by these logs.

## Inputs and provisional consumer boundary

All three source locks select official, unmodified Firecracker/jailer v1.18.0
release binaries, published SDK
`dd9db322432989b96cff7a38a64703d60b6cabf9`
(`v0.0.0-20261112180021-dd9db3224329`), containerd v2.3.7 and runc v1.4.2.
Use each recorded runtime's `docs/modernization/versions.json` for its complete
input lock. Each attempt retains installed runtime/shim/kernel/rootfs hashes in
its install log, plus vHive test binary hashes/metadata, selected module graph
and base-image identity. Only `b0498f3a598e` also has the full-stack generated
agent/build provenance collection. Image preparation can rebuild guest images
after installation; no installed/test-rootfs byte identity is asserted here.

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

The three subdirectories are byte-identical copies of the original collections
`stage2-worker-evidence`, `stage2-718-worker-evidence` and
`stage2-1c009-worker-evidence`. Their original `summary.json` files and all raw
logs/provenance are unchanged. The 48 files listed by the original summaries
were independently checked against their recorded lengths and SHA-256 hashes.
[SHA256SUMS](SHA256SUMS) additionally covers all three original summary files:
**51 original files total**. No nonempty file from these collections was already
tracked at the base revision; previous historical receipts were left untouched.
Empty or repeated-content logs retain their per-attempt identity; Git deduplicates
identical blob content.

The successful full-stack result belongs only to `b0498f3a598e`; it must not be
reported as a rerun of `718f305b`, `1c009d16`, the later shutdown correction, or
this documentation tip. No runtime native restore, CRI conformance, Kubernetes restore,
Step 4 storage, PMEM, reclamation or memory-savings acceptance follows from this
bundle. Fresh exact-source full validation and all required graceful clone
shutdown checks remain pending. Historical evidence elsewhere remains historical.
