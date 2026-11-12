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
| 1 | `firecracker-v1.18-containerdv2` | Modernized OCI runtime, acknowledged events and framed IO; the recorded lint/build/install/test-stack worker pipeline passed at `1074bdc8f2b6`, with one explicit SDK skip. [Receipt and scope](evidence/upstream-step1-1074bdc8f2b6/README.md). |
| 2 | `firecracker-v1.18-containerdv2-vhive-integration` | Adds standalone vHive and coordinated legacy snapshot/restore; migrated candidate VM acceptance pending. |
| 3 | `cri-native-snapshot-restore` | Adds CRI integration, ordinary RuntimeClass Pods and native checkpoint components; **WIP**, with historical CRI failures, fresh migrated candidate validation pending, and Kubernetes Pod restore deferred to the 1.38 interface work. |
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
binaries rather than compiling a custom VMM. The recorded Step 1 worker pipeline
passed at exact source `1074bdc8f2b6`; its
[receipt](evidence/upstream-step1-1074bdc8f2b6/README.md) distinguishes the tested
source from this documentation tip, the SDK skip, and installed versus final
test images. Separate lifecycle/crash-runner evidence and later-stage VM
acceptance remain pending. Historical runtime receipts and standalone SDK
acceptance do not establish those results. See [migration scope](upstream-migration.md).

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
