# Migrated Step 1 worker acceptance

The recorded four-command pipeline passed on `amd218.utah.cloudlab.us` for
clean committed runtime source `1074bdc8f2b602c9f986d3975d5c94b40bd1cac7`,
tree `d786b6116dd3b855c2b2bc57ac727ecb354ce534`. This is the tested source;
the later commits adding these receipts are documentation/evidence only and were
not rebuilt. The original [summary](summary.json), logs and generated provenance
files are copied byte for byte from the worker evidence collection.

## Recorded procedure and result

The pipeline ran, in order:

```sh
make lint
tools/modernize/build-stack.sh
tools/modernize/install-stack.sh
tools/modernize/test-stack.sh
```

The collected pipeline exit code is `0`. [Lint](stage1-lint-1074bdc8f2b6.log)
reported zero issues; [build](stage1-build-1074bdc8f2b6.log) and
[private installation](stage1-install-1074bdc8f2b6.log) succeeded. The
[acceptance log](stage1-acceptance-1074bdc8f2b6.log) ends with
`PINNED_SDK_PRIVILEGED_SUITE_OK` and `COMMITTED_STACK_ACCEPTANCE_OK`.

At the tested source, `test-stack.sh` requires a clean checkout before and after
its checks. It verifies inputs, runs lint and test-image builds, nonroot short
and privileged package checks, module tidy and protobuf regeneration with a
clean source diff, isolated runtime and snapshotter integration, thin-pool
isolation/cleanup, and the pinned SDK privileged suite. Its successful final
marker follows explicit removal of its runtime/snapshotter thin pools and its
final clean-source check.

The verbose integration output records 127 top-level passes: 36 runtime, five
snapshotter and 86 SDK tests across SDK packages. Subtests are not included in
those counts. The sole explicit top-level skip is SDK `TestLogAndMetrics`;
this receipt does not establish that test. Package checks run without verbose
output are separate from those counts. The runtime matrix includes both jailer
modes in `TestCreateVM_Isolated`, ordinary pause/resume, exec, event, IO,
networking and cleanup checks. The raw log retains all emitted warnings and
diagnostics, including an SDK CNI cleanup warning in a passing test.

## Installed-stack lifecycle and crash recovery

Separate follow-ups passed on the same clean source `1074bdc8f2b6` using
installed prefix `/users/nehalem/fc-ctrd-mod/install/1074bdc8f2b6` and run name
`fcmod-step1-1074bdc8f2b6-lifecycle`. The
[lifecycle log](stage1-lifecycle-1074bdc8f2b6.log) records the exact runtime/client
revision and ends with `MODERNIZED_CONTAINER_OK`. The
[controller/shim recovery log](stage1-controller-recovery-1074bdc8f2b6.log)
records process identities, `CONTROLLER_RESTART_RECONNECT_OK` and
`SHIM_CRASH_RECOVERY_OK`: controller restart preserved workload files and
reconnected, then shim crash reaped the VMM and removed task/VM directories.

The [lifecycle summary](lifecycle-summary.json) records the dedicated service
stopped, its thinpool absent after cleanup, and the source checkout clean. The
[cleanup log](stage1-lifecycle-cleanup-1074bdc8f2b6.log) is empty;
the summary records the successful cleanup checks. Its launcher note preserves
that the initial SSH stdin-fed launcher returned after lifecycle without running
recovery/cleanup. The missing recovery receipt was detected and the remaining
steps ran through an uploaded script, exiting zero with
`STAGE1_INSTALLED_LIFECYCLE_AND_CRASH_RECOVERY_OK`. Lifecycle was not rerun and
no test retry is hidden.

These follow-ups used the installed rootfs
`/users/nehalem/fc-ctrd-mod/install/1074bdc8f2b6/share/runtime/default-rootfs.img`.
At the tested source, `run-worker.sh` selects that path when `INSTALL_DIR` is
set. This exercises the installed-prefix image, distinct from the subsequently
rebuilt final `test-stack.sh` image described below. The separate follow-ups
are not part of the original four-command pipeline or a rebuild of this later
documentation/evidence tip.

## Binary and input provenance

The selected stack uses official unmodified Firecracker/jailer v1.18.0 release
binaries, published vHive SDK
`dd9db322432989b96cff7a38a64703d60b6cabf9`, containerd v2.3.7 and runc v1.4.2.
The raw logs record input verification, the SDK replacement and equal
VMM/SDK schema SHA-256
`3693a55005be2b9c81bf14e9908bd894e361adedff4abcf8c1ac24a8c40adb0c`.
Use the tested source's `versions.json` and gitlinks for the complete input lock.

- [Runtime revision](acceptance/runtime-revision) identifies the tested commit.
- [Agent build information](acceptance/agent-build-info) records Go 1.26.11,
  the SDK replacement, that same VCS revision and `vcs.modified=false`.
- [Module graph](acceptance/go-modules.json) records the selected module graph.
- [Binary hashes](acceptance/binaries.sha256) identify artifacts at the end of
  acceptance; [test-image inspection](acceptance/test-image.json) identifies the
  revision-tagged test image.
- The install log separately records installed artifact hashes and the private
  prefix `/users/nehalem/fc-ctrd-mod/install/1074bdc8f2b6`. Installed runtime,
  shim, Firecracker, jailer, runc and kernel hashes match the final acceptance
  hash list. The installed rootfs SHA-256 is
  `aaf822def29df7331b8431a552b08a78e456368049df7fddc672c55343e00468`;
  the final acceptance rootfs SHA-256 is
  `dd1239a2a68ada110a2ffc4d8065e8ce918d34dc5e556f28c1505f3f0bb93324`.
  Test-image construction rebuilds guest images after installation. These
  records identify different image artifacts; the receipt does not assert that
  the installed rootfs was the final acceptance image.

The install log retains a nonfatal runc submodule Git metadata diagnostic;
the subsequent build explicitly supplies the pinned runc `COMMIT`, installs
successfully and records its binary hash. No raw output was removed or repaired.

## Scope and limits

This is acceptance of the recorded Step 1 pipeline at the exact tested source.
It establishes no legacy vHive coordinated snapshot/restore, runtime native
restore, CRI conformance, Kubernetes Pod restore, Step 4 storage, PMEM,
reclamation or memory-savings result. SDK snapshot tests are SDK coverage only.
Separate `run-worker.sh` and `test-crash-recovery.py` follow-ups are included
under their own lifecycle logs and summary above; they did not run as part of
the four-command pipeline. Later-stage validation remains pending;
CRI remains WIP and Kubernetes Pod checkpoint/restore is deferred to the 1.38
interface work.

## Evidence integrity

`summary.json` records the byte length and SHA-256 of each of the nine original
log/provenance files. All were independently rehashed and matched before this
receipt was prepared. The summary's own SHA-256 is recorded separately in
`SHA256SUMS`, together with those nine original files. The separate
`lifecycle-summary.json` records byte lengths and SHA-256 hashes for its three
raw logs; its own hash and those three log hashes are appended to `SHA256SUMS`.
The original pipeline summary/log/provenance bytes are unchanged. This README
and `SHA256SUMS` are explanatory metadata; all collected evidence is preserved
byte for byte.
