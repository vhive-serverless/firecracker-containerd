# Step 4 evidence draft — acceptance pending

This is an uncommitted evidence draft, not an accepted Step 4 result.
Pipeline 99451 terminated with **acceptance exit 2** after successful build and
private installation. The [original terminal summary](summary.json) and
[command statuses](stage4-command-status-9c3ea8e618a4.tsv) record that failure.
The full stack is not accepted, and the launcher's unique follow-ups were not
reached. No final documentation verdict or commit is prepared here.

The [original source record](stage4-tested-source-9c3ea8e618a4.txt) identifies
worker source `9c3ea8e618a46d6d59e64a5812eff4f1bd382cdc`, tree
`9955efe6cdaf21ba197b2dcc03da891d85ae3ab1`. The documentation preparation clone
starts at cumulative candidate `d050b5fc9a3898a4ae04db722c81ad3e922e5dc2`;
that later cumulative history is not the source built by these worker logs.

The [build log](stage4-build-9c3ea8e618a4.log),
[install log](stage4-install-9c3ea8e618a4.log) and
[submodule record](stage4-submodules-9c3ea8e618a4.txt) retain their original
bytes. The separate original
[collection receipt](build-install-collection.json) records the earlier
build/install-only collection while the pipeline was still running. It is
preserved unchanged as historical phase evidence. The appended
[completed acceptance log](stage4-acceptance-9c3ea8e618a4.log), terminal TSV and
summary preserve the failed outcome. All six files named by the terminal
summary match its recorded lengths/hashes. [SHA256SUMS](SHA256SUMS) now covers
those six originals, the terminal summary itself and the earlier collection
receipt: eight byte-preserved original files. The first five original files
and manifest rows are unchanged.

## Failure scope and remaining checks

The acceptance log contains 55 top-level PASS lines, one top-level
`TestMemoryEvaluation_Isolated` skip, and the failing
`TestStargzNativeRestore_Isolated` matrix. All 12 leaves (six storage profiles
under both jail modes) fail at the fixture's stale manifest-version assertion:
expected 1, actual mandatory migrated version 2. Each stops before restore, so
none of those 12 leaves establishes successful restore. The zero-error checks
before and after that failed matrix do not turn this into a full-stack pass.
The SDK phase and the launcher's unique follow-ups were not reached; no fresh
SDK result or namespace/API/ENOSPC/FUSE/Python/race-follow-up verdict follows.

The lead reports the worker checkout is clean and owned containers/pools are
absent after failure. This is separately reported cleanup, not an additional
field invented in the immutable original summary. A test-only fixture correction
and v2 transport assertions are being prepared separately; the lead plans a
targeted stargz rerun and remaining checks against the exact existing image.
Those future results must retain their own test-source/image provenance and
must not replace this failed transcript or be inferred before execution.

The installed guest rootfs hash is
`ea84f632cbd624c7546d047933e37ae9452943014e6191d19fda292f389ce0a3`.
Final test images may be rebuilt after installation. Their actual hashes and
binary/module provenance must come from terminal receipts; no installed/test
rootfs byte identity is asserted.

The accepted earlier Step 1–3 receipts remain separate and unchanged. The
test-only transport supplement `064c9884a0a34a52c1e648847a4bb5f336fa2dde`
is based on accepted Step 3 source `332da2a7065b`; its separate VM supplement
passed in both jail modes with unchanged accepted-image binaries. Its
[separate receipt](../upstream-snapshot-transport-064c9884/README.md) records the
positive unread-input witness, source/clone once-only consumption and real EOF.
That result does not accept this failed Step 4 matrix or retroactively expand
the original Step 3 transcript. Full CRI
remains WIP, Kubernetes Pod checkpoint/restore is deferred to the 1.38 interface
work, and final published-runtime/installed-vHive consumer acceptance remains
pending. Historical 21-trial measurements are unchanged and supply no new
performance or memory-savings claim for this migrated source.

Await the corrective test source and frozen targeted/follow-up results,
provenance and owned-cleanup evidence before consolidating current Step 4
status documents or creating a signed final documentation commit. Preserve
this failed attempt regardless of any later outcome.

## Later accepted corrected source

The subsequent [exact bbcaf full-stack and unique-check receipt](../upstream-step4-bbcaf820056d/README.md)
records successful corrective acceptance separately. Pending preparation notes
above describe this earlier archive's phase; its failed worker outcome, raw
bytes and original summary remain unchanged. Use the current Step 4 procedure
for the later accepted source and remaining CRI/consumer limits.
