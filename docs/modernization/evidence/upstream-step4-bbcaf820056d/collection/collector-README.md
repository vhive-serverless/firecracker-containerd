# Exact bbcaf terminal acceptance

Exact source `bbcaf820056d78e33887059465504c6647f3d983`, tree
`9dcfa565a91171b00af8bd5fb271dfc47fd4c388`, passed the full launcher with exit 0
and `STAGE4_STACK_AND_UNIQUE_COMPONENT_CHECKS_OK`. The lead reported the
authoritative session 54045 result (chunk `60c7f2`); the collector separately
required launcher disappearance and checked source/image/owned cleanup before
freezing terminal files. [summary.json](summary.json) is collector-generated;
the original worker TSV/logs/receipts remain unchanged.

The full-stack phase recorded **142 top-level passes: 49 runtime, seven
snapshotter and 86 SDK**, with two top-level skips (`TestMemoryEvaluation_Isolated`,
`TestLogAndMetrics`) and eight existing nested StopVM skips. No failure was
observed. The selected 24-case native/reporting group consists of 22 native
jailed/nonjailed leaves plus two reporting-enabled/disabled cases; twelve
additional stargz native leaves passed in 315.75 s. The four stargz storage
profiles passed. This is exact-source functional validation, not new performance
or memory-evaluation trial evidence.

All unique follow-ups passed: unit race, two shell checks, 36 Python tests,
two namespace/API tests, private HostENOSPC and five host FUSE tests. The
[command TSV](stage4-command-status-bbcaf820056d.tsv) records every phase 0;
the [full acceptance log](stage4-acceptance-bbcaf820056d.log) ends with its
committed-stack marker. The [collector summary](collector-summary.json)
retains detailed passes/skips and independent cleanup observations.

Actual test image ID is
`sha256:f6becb18f0df98ffb0d5086b54740328c91a8df697d811abb598a2fc95eeaaa8`,
matching [the generated inspection](acceptance/test-image.json) and read-only
inspection of the final tag. The installed rootfs is
`8ed46c277f6fa36e4a822ffee857a4e603d8871add1fd883d853f701fd36217d`;
the rebuilt final test rootfs is
`b638c52bf69348f2f43d070878068182f088d179f75d74b0a834fc5366ed22a1`.
Their byte identity is not asserted. Build/acceptance module graphs, agent/CRI
metadata, official release receipt, actual kernel config and binary manifests
are preserved.

The read-only [inventory and cleanup observations](remote-inventory.json)
confirm clean source, owned containers/pool mappings/loops/private fixtures and
SDK cgroup absent, with the unrelated Kubernetes control plane still running.
All 16 earlier frozen build/install files remain unchanged.
[SHA256SUMS](SHA256SUMS) covers 41 original worker files (1,666,372 bytes),
remotely and locally hash-verified. Inventory, both JSON summaries and this
README are collector metadata, distinct from original worker records.

Corrected Step 2/3 exact-source acceptance remains separately pending. Full CRI
is WIP, Kubernetes Pod restore is deferred to 1.38, and final published-runtime/
installed-vHive consumer acceptance remains separate. No worker tests, builds,
cleanup or environment changes were performed by the collector.
