# Failed Step 4 targeted remainder at 535e

This separate, uncommitted receipt preserves the failed targeted retry after
the earlier [9c3 attempt](../upstream-step4-9c3ea8e618a4/README.md). The
[original summary](worker/summary.json) records **terminal exit 1 and cleanup
exit 0**. The [command TSV](worker/command-status.tsv) records
`stargz-native=1` and `cleanup-on-exit=0`; the remaining checks were not reached.
This is not current Step 4 acceptance or a final documentation verdict.

Runtime source is `9c3ea8e618a46d6d59e64a5812eff4f1bd382cdc`; exact test-only
fixture source is `535e094d36ff2689d254bdefd2d0452ea37c9ef0`. The
[source proof](worker/source-before.log) identifies the sole changed test file,
`snapshotter/stargz_restore_integ_test.go`, with 944 other mode/type/object
entries unchanged. The existing image is
`sha256:70b8de8f4d8250a4a7d2919607dae67c8949094268569fadc58ee321cd1c66e6`.
[Image inspection](worker/image-inspect.log),
[image artifact hashes](worker/image-artifacts.log),
[host artifacts before the attempt](worker/artifacts-before.log) and the
[executed test command](worker/commands/stargz-native.sh) retain provenance;
no runtime/image rebuild is attributed to this run.

## Actual twelve-leaf outcome

All twelve leaves positively log native manifest version 2 and transport cut
version 1, then fail at restore. The corrected manifest/transport assertions
therefore do not provide a successful restore result.

| Profile | Nonjailed failure | Jailed failure |
| --- | --- | --- |
| `disk` | mapping overlap at `ctrstub0` | `openat ctrstub0: file exists` |
| `ext4-block` | mapping overlap at `ctrstub0` | `openat ctrstub0: file exists` |
| `pmem` | mapping overlap at `ctrstub0` | `openat ctrstub0: file exists` |
| `reclaim` | mapping overlap at `guest-overlay.ext4` | `openat ctrstub0: file exists` |
| `combined` | mapping overlap at `guest-overlay.ext4` | `openat ctrstub0: file exists` |
| `ext4-reclaim` | mapping overlap at `guest-overlay.ext4` | `openat ctrstub0: file exists` |

Thus **three nonjailed stub overlaps, three nonjailed guest-overlay overlaps and
six jailed stub collisions** are preserved. Mapping errors say the target
overlaps another mount or live shim files; they are distinct from the jailed
VM startup collision. The [full test log](worker/stargz-native.log) and
[full containerd log](worker/runtime-logs/containerd.out) are untrimmed. No
leaf passed or skipped; this receipt does not infer an unproven root cause.

The twelve-pass guard, thinpool/SDK tail and all unique follow-ups were not
reached: unit race, both shell checks, Python evaluation tests, probe builds,
namespace/API, private ENOSPC and five host FUSE checks. `artifacts-after`, its
identity guard and final source/image completion steps were also not reached.
The immutable summary describes the planned before/after scope, but only the
before receipt exists; no before/after artifact equality is claimed here.

## Independent cleanup and byte preservation

The collector waited for the original terminal summary and owned-container
absence before copying. Its separate read-only
[cleanup verification](collection/post-cleanup-verification.json) confirms
named/labelled owned containers, dedicated pool mappings, backing loops and
probe/VM mounts are absent. Both runtime and fixture checkouts are clean; the
VM directory and created bind-target record are gone. The unrelated existing
Kubernetes control-plane container remains running. The original worker
summary is unchanged; this collector record is additional provenance.

All **35 original worker files (5,770,731 bytes)** were remotely hashed and
locally verified; [remote inventory](collection/remote-inventory.json) records
every size/hash. There were no exclusions: large probes/backing images did not
remain after authoritative cleanup, so every remaining regular raw file was
preserved, including complete runtime logs and original prior-failure copies.
Five collector-provenance files are also copied exactly: inventory, cleanup
verification, the [collection report](collection/collection-report.json),
original worker-hash manifest and collector README. That preserved README's
relative links use the original collection-root layout now represented under
`worker/`; use this receipt's links for navigation.

[SHA256SUMS](SHA256SUMS) covers all **40 byte-preserved worker/provenance files**.
The earlier failed attempt, accepted transport supplement and all inherited
Step 1–3 tracked source/docs/receipts remain unchanged. No final documentation
commit is made while corrective Step 4 validation remains pending. Full CRI
remains WIP; Kubernetes Pod restore stays deferred to the 1.38 interface work;
final published-runtime/installed-vHive consumer acceptance remains separate.

## Later accepted corrected source

The subsequent [exact bbcaf full-stack and unique-check receipt](../upstream-step4-bbcaf820056d/README.md)
records successful corrective acceptance separately. Pending preparation notes
above describe this earlier archive's phase; its failed worker outcome, raw
bytes and original summary remain unchanged. Use the current Step 4 procedure
for the later accepted source and remaining CRI/consumer limits.
