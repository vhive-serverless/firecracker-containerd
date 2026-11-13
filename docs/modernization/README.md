# Maintained runtime branches

`firecracker-v1.18-containerdv2` is the maintained base for the migrated stack.
The incremental branches build on it in this order:

```text
firecracker-v1.18-containerdv2
  -> firecracker-v1.18-containerdv2-vhive-integration
  -> cri-native-snapshot-restore
  -> damon-virtio-pmem
```

| Step | Branch | Scope and acceptance |
| --- | --- | --- |
| 1 | `firecracker-v1.18-containerdv2` | Modernized OCI runtime, acknowledged events and framed IO; the worker pipeline and separate installed-prefix lifecycle/controller-restart/shim-crash checks passed at `1074bdc8f2b6`, with one explicit SDK skip in the pipeline. [Receipt and scope](evidence/upstream-step1-1074bdc8f2b6/README.md). |
| 2 | `firecracker-v1.18-containerdv2-vhive-integration` | Exact `2e03843816cf` has complete segmented check coverage: original full run failed on an unfixed existing CNI flake; unchanged five-VM repeats and remaining thinpool/SDK checks passed separately. Three provisional vHive VM cases passed. [Receipt and limits](evidence/upstream-step2-2e03843816cf/README.md). Final published/installed consumer acceptance remains pending. |
| 3 | `cri-native-snapshot-restore` | Exact `751c03c6f9a9` passed eight jailed/nonjailed native leaves, real-volume continuity and focused CRI checks (4 specs passed, 138 skipped) plus resource/volume integration. [Receipt and scope](evidence/upstream-step3-751c03c6f9a9/README.md). Full CRI remains **WIP**; Kubernetes Pod restore is deferred to 1.38. |
| 4 | `damon-virtio-pmem` | Adds disk-backed guest storage, pmem/DAX, DAMON/reporting and memory evaluation; historical direct runtime acceptance is revision-specific and does not validate the migrated candidate. Fresh migrated validation and inherited Step 3 gates remain open. |

Read the milestone document on its own branch: [Step 1](step-1.md),
[Step 2](https://github.com/vhive-serverless/firecracker-containerd/blob/firecracker-v1.18-containerdv2-vhive-integration/docs/modernization/step-2.md),
[Step 3](https://github.com/vhive-serverless/firecracker-containerd/blob/cri-native-snapshot-restore/docs/modernization/step-3.md),
and [Step 4](https://github.com/vhive-serverless/firecracker-containerd/blob/damon-virtio-pmem/docs/modernization/step-4.md).
Later branches retain earlier milestone documents as descriptions of those
milestones, not as descriptions of every later branch's storage or dependencies.
The old names `modernize-runtime` and `vhive-compatible` identify historical
milestones and archived receipts; use the new names for current builds.

## Build and evidence

The migrated stack uses official Firecracker and jailer v1.18.0 release
archives, verified against checksums in `versions.json`, and the published
`vhive-serverless/firecracker-go-sdk` branch `firecracker-v1.18.0`, commit
`dd9db322432989b96cff7a38a64703d60b6cabf9`. Runtime builds fetch the official
binaries rather than compiling a custom VMM. Exact Step 3 source `751c03c6f9a9`
passed the native/volume/focused-CRI worker procedure documented in
[Step 3](step-3.md). Its [receipt](evidence/upstream-step3-751c03c6f9a9/README.md)
identifies the tested source and actual artifacts;
this later documentation tip was not tested. Earlier milestones retain their own
exact-source receipts and procedures below. Full CRI and final published/installed-vHive
acceptance remain separate open gates; later-stage acceptance requires its own
source and receipts.

Exact Step 2 `2e03843816cf` has complete segmented check coverage; the original
full run remains exit 2 on the unfixed CNI race, while unchanged five-VM repeats
and remaining thinpool/SDK checks passed separately. See the
[Step 2 receipt and limits](evidence/upstream-step2-2e03843816cf/README.md).
Earlier `7386` clean acceptance retains its own receipt. No uninterrupted
corrected-source full-stack pass or final installed-vHive result is inferred.

The inherited Step 1 worker pipeline passed at exact source `1074bdc8f2b6`;
its [receipt](evidence/upstream-step1-1074bdc8f2b6/README.md) preserves the SDK
skip and the distinction between the installed and final test rootfs. Separate
lifecycle/controller-restart/shim-crash checks passed at the same source using
the installed-prefix rootfs, with dedicated service and thinpool cleanup
verified. See the [Step 1 procedure](step-1.md) and
[migration scope](upstream-migration.md) for that earlier milestone.

Use this checkout's [versions.json](versions.json), module manifests and gitlinks.
`selected_inputs` describes this branch's dependency selection; `existing`
describes the historical pre-modernization baseline. `modules.txt` is the
historical captured module inventory, not the current selected graph. Generate
the current graph with `go list -m -json all` from the exact checkout.
Validation entries name the exact tested source and scope. Later documentation, test-only maintenance, and
production fixes may not have repeated broader acceptance; use each maintenance
receipt to distinguish focused checks from full VM acceptance. Never substitute a
later branch's dependency manifest when building an earlier one.

The scripts in `tools/modernize/` prepare the worker, build and privately install
the stack, and run acceptance. Follow the branch-specific milestone procedure
and the exact committed migrated revision under review. Historical reproduction
commands stay under explicitly historical headings. VM acceptance requires
the designated KVM worker;
GitHub Actions build/unit checks alone do not establish VM or Kubernetes acceptance.
The old general quickstart and image-builder examples retain historical commands
and are not the reproduction procedure for these branches.

## Propagating maintenance

Land common fixes on the earliest affected branch, validate them there, then
merge forward one branch at a time. Preserve each branch's extra functionality,
dependency pins and acceptance scope while resolving conflicts. Do not merge
Step 4 storage changes backwards into the Step 1 baseline. Prefer forward
merges to separate cherry-picks so Git records that the earlier fixes are present.
Historical maintenance was cherry-picked; patch equivalence is different from
commit ancestry and can explain different hashes for the same fix.

For each adjacent pair, `git merge-base --is-ancestor <lower> <upper>` must
succeed after propagation. Inspect the resulting source diff, run the checks
appropriate to that diff and preserve receipts before pushing. Use separate
checkouts, install prefixes, test storage and thin pools for concurrent builds.
The branch chain does not make Step 3 complete: Step 4 was started from its WIP
baseline by explicit exception on 2026-11-11.
