# Corrected Step 2 segmented coverage at 2e — CNI flake unfixed

Exact source `2e03843816cf95d184ad4ad429adf6ea2ccc1412`, tree
`76c862aa60162a0b510d7626d523f047709e48d9`, has complete required check coverage
across the receipts below. **The original full run failed with exit 2 and is
not rewritten as an uninterrupted pass.** The existing pinned CNI chain race
remains unfixed. This later documentation/cumulative-history tip was not tested.

## Original failure and unchanged repeats

The [original terminal authority](full-attempt/lead-terminal-result.json),
[phase TSV](full-attempt/stage2-command-status-2e03843816cf.tsv) and
[full log](full-attempt/stage2-acceptance-2e03843816cf.log) retain build/install/
test-images/provisional-vHive passes followed by acceptance exit 2. Input
verification, lint, short/root package tests, tidy/proto and clean-diff gates
completed before all 36 runtime and five snapshotter cases were attempted.
There were 40 top-level passes: all 36 runtime tests and four snapshotter tests.
The sole failure was `TestLaunchMultipleContainersWithRemoteSnapshotter_Isolated`
in 0.31 s: iptables 1.8.9 `nf_tables` returned exit 4,
`CHAIN_USER_ADD failed (File exists): chain CNI-FORWARD`. The snapshotter
`make -k` continued and recorded `TestGuestVolumeFrom_Isolated` passing afterward.
Eight existing nested StopVM skips remain in the original log.

The [three diagnostic results](fresh-repeats/summary.json) preserve three
serial fresh-container observations of that same unchanged test: PASS in
7.93/7.45/7.76 s. Each retained five concurrent VMs, fresh network namespace,
owned pool, exact source and unchanged image; no warm chain or reduced
concurrency was substituted. All three cleanup results are 0. The
[input identities](fresh-repeats/source-inputs.json), actual per-run commands,
full daemon logs and [file hashes](fresh-repeats/files.json) remain unchanged.

The [source diagnosis](reports/cni-race-diagnosis.json) records identical CNI
config/test/plugin pins to accepted Step 1 and earlier Step 2. Pinned plugin
v1.9.1 checks `ChainExists` then calls `NewChain`, suppressing only status 1;
the observed nftables status 4 escapes. This supports an existing check/create
race explanation, not proof of a new same-ID regression. The exact failing
interleaving remains an inference. Nothing in these repeats fixes the race,
erases the failure or constitutes a clean full-stack run.

## Previously unreached tail and check coverage

The [tail terminal authority](remaining-tail/lead-terminal-result.json) and
[report](reports/tail-collection-report.json) record exit 0 with
`STAGE2_REMAINING_THINPOOL_AND_SDK_CHECKS_OK`. Its
[thinpool log](remaining-tail/thinpool.log) and [SDK log](remaining-tail/sdk.log)
show the unchanged required scripts passing: isolation/cleanup marker plus
86 SDK top-level passes, no failures and the sole `TestLogAndMetrics` skip.
The [tail TSV](remaining-tail/status.tsv) records both 0. It does not emit or
invent `COMMITTED_STACK_ACCEPTANCE_OK` for the original failed invocation.

The [coverage assessment](reports/combined-coverage-assessment.json) maps every
original `test-stack.sh` check: earlier input/build/package/generation gates,
36 runtime identities, five snapshotter identities, thinpool, SDK, clean source,
final artifacts/modules/image and cleanup. No required check is unobserved or
weakened. Coverage is segmented: do not sum repeated invocations into a synthetic
single-run pass count. The source coverage gate can be closed with this flake
disclosed, while the operational risk of concurrent CNI ADD failure remains.

The original three provisional legacy vHive tests passed at exact `2e` in
16.29/28.34/21.30 s; [their log](full-attempt/vhive/vhive-tests.log) and
[inputs](full-attempt/vhive/inputs) record explicit local runtime replacement.
This is not final published-runtime or installed-vHive consumer acceptance.

## Exact artifact and source boundaries

The tail [module graph](remaining-tail/go-modules.json) is byte-identical to
the original build graph; SDK replacement remains published dd9db32. The
[agent metadata](remaining-tail/agent-build-info.txt) identifies clean exact
`2e`. All 12 [tail binary/artifact hashes](remaining-tail/binaries.sha256)
match the frozen original source artifacts. Original and tail image identity is
`sha256:97f3745973e8856190db513d2d1def3a84a603eb3337bc4ccac655e554b27117`;
see [tail image receipt](remaining-tail/test-image.json). The original collector's
missing full-acceptance-receipt flag stays unchanged; this tail supplies separate
provenance, not a retroactively generated original acceptance receipt.

Installed rootfs hash is
`e4ab2bd242a1d3ce5157c99d3c9c1cf38228263c0b8c35a37a6081ffa17d6ca4`;
the rebuilt full-stack test rootfs is
`5454922651a3fd2507f15b7e079e5d34288141189cf9a43ab70c3896cfebe4d1`.
The earlier provisional vHive image is a third distinct artifact. No byte
identity or final installed-consumer acceptance is inferred. The
[kernel reuse receipt](full-attempt/kernel-seed-lead-receipt.json) explicitly
records reuse of accepted `7386` kernel/config after equality checks of pinned
build inputs; no new kernel compilation is claimed. Runtime/guest images were
built for the corrected source and retain actual provenance.

Independent observations in the original/repeat/tail collection reports verify
clean source and owned containers/pools/loops/backings/SDK cgroup removed. The
unrelated Kubernetes control plane was preserved; later Step 3 build resources
in the tail observation are separately owned and were not touched.

## Integrity and limits

[SHA256SUMS](SHA256SUMS) preserves **94 original collection/provenance files**:
36 original-attempt files, 37 diagnostic files, 12 tail files, five local
correction records and four reports. All copied bytes match their frozen
sources, including original hashes/metadata. Local correction race/lint and
patch-equivalence records retain their own scope; they are not worker VM runs.
Earlier [7386 and failed-attempt evidence](../upstream-step2-worker-attempts/README.md)
remains untouched. No criteria, source, image or concurrency were changed to
replace the failing case. No current clean full-run claim is made.

Full CRI remains WIP, Kubernetes Pod restore is deferred to the 1.38 interface
work, and final published-module/installed-vHive consumer acceptance remains
separate. No new native/Step 4 or memory/performance result follows here.
