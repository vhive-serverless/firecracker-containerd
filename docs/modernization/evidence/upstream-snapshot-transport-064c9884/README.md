# Supplemental snapshot transport acceptance at 064c

This separately recorded supplement passed with exact test-only source
`064c9884a0a34a52c1e648847a4bb5f336fa2dde`, tree
`5c094243ecaf1d6ea7891d2021d78ff634be513b`, against the unchanged accepted
Step 3 runtime/image from `332da2a7065baa0050037250bbf2ff316a1a8519`.
The [original worker summary](worker/summary.json) records exit 0 and
`SUPPLEMENTAL_SNAPSHOT_TRANSPORT_ACCEPTANCE_OK`;
[command statuses](worker/command-status.tsv),
[terminal exit](worker/exit-status) and [marker](worker/success-marker) retain
the original observations. This is a new witness of common transport behavior,
not a claim that the original Step 3 tests already exercised unread input.

The [earlier accepted Step 3 receipt](../upstream-step3-332da2a7065b/README.md)
and all Step 1–3 tracked source/docs/receipts remain unchanged. This archive is
being prepared in a later documentation clone; it does not establish acceptance
of that clone's code or replace the failed Step 4 transcript.

## Local admission and final test-source checks

Two original local race logs each contain 50 actual passes of
`TestSnapshotAdmissionDrainsHeldMutation`:
[initial race run](local/admission-race.log) and
[final race run](local/admission-race-final.log). The recorded command was
`DISABLE_ROOT_TESTS=1 go test -mod=readonly -race -short -count=50 -v ./runtime -run '^TestSnapshotAdmissionDrainsHeldMutation$'`.
These are two local development phases; the first must not be presented as a
separately recorded clean-commit VM run. The focused unit holds a real runtime
service mutation in its fake backend, verifies an exclusive snapshot deadline
while the mutation remains admitted, lets backend completion drain it, then
verifies exclusive acquisition and rejection of a late mutation after pause.
It does not prove remote delivery of an outer RPC response before capture.

The [initial lint output](local/lint.log) preserves the failed builtin-name
warning; [final scoped lint](local/lint-final.log) records zero issues after
the test-only naming correction. Both [initial](local/runtime-compile.log) and
[final compile output](local/runtime-compile-final.log) are retained. The
[final patch](local/supplement.patch) adds exactly two tests; the
[local production-equality proof](local/production-equality.json) records all
433 non-test mode/type/object/path entries and all existing tests unchanged
from accepted `332da`. The [prepared worker command](local/worker-command.sh)
is planning history; the actual executed launcher and command files under
`worker/` are the execution record.

## Actual unread-input VM witness

The [VM log](worker/buffered-stdin-vm.log) records
`TestNativeBufferedStdinSnapshot_Isolated` PASS in both jail modes:
nonjailed 14.04 s and jailed 14.22 s. Each positively observes **47 bytes
accepted by the guest stdin receiver while the application is held**, then
checks the actual checkpoint cut reports that same accepted offset. This is
a positive receiver/cursor witness, rather than assuming an unwritten or
already-consumed host-side buffer exists. Pre-capture Prepare/Describe/Resume
is observation/preconditioning; ordinary CheckpointVM performs the capture.

Both source and clone consume the captured sentinel exactly once with the
captured identity/counter, then accept independent input. The checks require
exact stdout/stderr and real EOF with no trailing output; the log records
`source_once=true clone_once=true captured_counter=0 stdout_stderr_exact=true eof_no_tail=true`.
These observations supplement the original stream/cut tests. The test-source
[hash receipt](worker/test-sources.sha256) matches the two files in exact `064c`;
no production change, old-test edit, rebuilt runtime or fresh image is hidden
in this attribution.

## Accepted image and actual binary provenance

The [inputs](worker/inputs), [image identity](worker/image-identity.log),
[image proof](worker/image-proof.log) and
[image inspection](worker/image-inspect.log) name existing image
`localhost/firecracker-containerd-test:332da2a7065b`, ID
`sha256:17046a2aeb5c501161f0761094b96b3f5347602888759d56ac8cf9d6c40ab64d`.
The [actual VM command](worker/commands/buffered-stdin-vm.sh) uses
`--pull=never`, that accepted image, and the exact supplemental checkout as
test source. It compiles/runs only the new isolated Go test client inside the
image; it does not run build-stack, install-stack or make test-images.

The [binary proof](worker/binary-proof.log) and
[image binary hashes](worker/image-binaries.log) match the accepted runtime
artifacts:

| Actual image artifact | SHA-256 |
| --- | --- |
| containerd | `47a157ebf66d55921ea81b937957ba6e91dbe63372f474cd33e166490f7a5c74` |
| shim | `4c0c5e1dc13e130bdec45e9115f207a5105e9ed93ffe49ee8a4b1ac8058eafad` |
| official Firecracker | `b33f46cd6d88834cf6a4f8d184f08f219809c636236bffd286d233a60602a2ba` |
| accepted test-image guest rootfs | `b8455888c02d3eab83d9ea7ca1000d9978f46830eefbdb7b09d9d67166f56c72` |

The [worker source proof](worker/production-equality.json) independently records
the unchanged 433 production entries and the two added tests. This rootfs is
the accepted Step 3 test-image artifact; it is not a claim of byte identity
with Step 3's earlier installed-prefix rootfs or final installed-vHive artifacts.
Runtime/guest detail is retained in the full
[containerd log](worker/runtime-logs/containerd.out), without trimming.

## Cleanup, integrity and remaining limits

The [cleanup status](worker/cleanup-status.tsv) records cleanup exit 0. The
[owned containers](worker/owned-containers-after-cleanup.txt),
[owned mappings](worker/owned-mappings-after-cleanup.txt) and
[loop inventory](worker/loops-after-cleanup.json) retain the post-cleanup
records. The original summary reports a fresh worker read showing only the
unrelated existing Kubernetes container. Named supplemental resources were
removed; this is not permission to remove other worker resources.

The original worker's 54 summary-listed files total 929,878 bytes; the largest
is the 891,836-byte runtime log. That size does not justify dropping provenance
or supposedly redundant output. **All 54 originals plus their unchanged summary
are preserved**, including empty logs and repeated content. All nine local
evidence files are also copied unchanged: **64 original files** total, covered
by [SHA256SUMS](SHA256SUMS). The worker files were checked against recorded
lengths/SHA-256, and every copied local file was compared byte for byte.

The production-equality and image proofs attribute this result to accepted
`332da` production artifacts plus exact `064c` test source. They do not make a
later assembled source automatically tested, retroactively expand original
receipts, or complete Step 4 storage/stargz corrective validation. Full CRI
remains WIP; Kubernetes Pod checkpoint/restore is deferred to the 1.38 interface
work; final published-runtime/installed-vHive consumer acceptance remains
pending. No new memory or performance claim follows from this supplement.
