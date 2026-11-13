# Frozen failed Step 4 targeted remainder at 535e

The [original terminal summary](summary.json) records **exit 1, cleanup 0**.
Collection began only after that summary existed and the named/labelled owned
containers were absent. The [command TSV](command-status.tsv) records
`stargz-native=1` and `cleanup-on-exit=0`; no completion marker exists.
This is a failed targeted run, not Step 4 acceptance.

Runtime source is `9c3ea8e618a46d6d59e64a5812eff4f1bd382cdc`; test-only fixture
source is `535e094d36ff2689d254bdefd2d0452ea37c9ef0`. The
[source proof](source-before.log) verifies the single changed test file and
944 other unchanged mode/type/object entries. Actual existing image is
`sha256:70b8de8f4d8250a4a7d2919607dae67c8949094268569fadc58ee321cd1c66e6`;
[image inspection](image-inspect.log), [artifact hashes](image-artifacts.log)
and the [executed command](commands/stargz-native.sh) preserve provenance.
There was no stack/image rebuild. Original prior-failure logs/TSV/summary are
also retained unchanged and keep their earlier failed status.

All twelve leaves log manifest version 2 and transport cut version 1, then fail
at restore:

- Three nonjailed leaves (`disk`, `ext4-block`, `pmem`) reject a private disk
  mapping target overlapping another mount or live shim files at `ctrstub0`.
- Three nonjailed leaves (`reclaim`, `combined`, `ext4-reclaim`) reject that
  mapping at `guest-overlay.ext4`.
- All six jailed leaves fail starting the VM with `openat ctrstub0: file exists`.

The [complete VM log](stargz-native.log) and untrimmed
[containerd log](runtime-logs/containerd.out) preserve both error classes. Six
leaves fail in each class; none passes or skips. The corrected manifest
assertion does not establish successful restore.

The twelve-pass guard, thinpool/SDK tail and every unique follow-up were not
reached: unit race, shell syntax, Python evaluation tests, probe builds,
namespace/API, private ENOSPC and host FUSE checks. `artifacts-after`, its
identity guard and the final source/image completion steps also were not run.
The immutable summary describes the planned before/after provenance scope;
only [artifacts-before](artifacts-before.log) exists in this failed run, so no
before/after identity assertion is made here.

The collector's separate read-only
[post-cleanup verification](post-cleanup-verification.json) confirms the owned
named/labelled containers, pool mappings, backing loops and probe/VM mounts
are absent; both runtime and test checkouts are clean. The VM directory and
created bind-target record are gone. Only the unrelated existing Kubernetes
control-plane container remains running. This independently supports the
original cleanup result without changing the worker summary.

[remote-inventory.json](remote-inventory.json) records remote lengths/hashes
before transfer. All **35 original regular files (5,770,731 bytes)** were copied
and checked locally against those hashes; [SHA256SUMS](SHA256SUMS) covers these
originals. There are **no exclusions**: large compiled probes and VM/fixture
backing images did not remain after cleanup. Every remaining raw log, command,
launcher and receipt is preserved. The inventory, post-cleanup verification,
this README and the sibling collection report are collector-generated metadata,
distinct from immutable worker files.

No release documentation was edited, and no remote build/test/service/VM action
was performed by the collector. The separate accepted `064c` transport
supplement does not turn this storage restore failure into a pass. Full CRI
remains WIP, Kubernetes Pod restore is deferred to 1.38, and final
published-runtime/installed-vHive acceptance remains separate.
