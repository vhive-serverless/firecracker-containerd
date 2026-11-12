# Maintained runtime branches

`modernize-runtime` is the maintained base for this fork. The incremental
branches build on it in this order:

```text
modernize-runtime -> vhive-compatible -> cri-native-snapshot-restore -> damon-virtio-pmem
```

| Step | Branch | Scope and acceptance |
| --- | --- | --- |
| 1 | `modernize-runtime` | Modernized OCI runtime; committed worker build and lifecycle acceptance. |
| 2 | `vhive-compatible` | Adds the pinned standalone vHive deployment and function snapshot/restore profile. |
| 3 | `cri-native-snapshot-restore` | Adds CRI integration, ordinary RuntimeClass Pods and native checkpoint components; **WIP**, with CRI failures and Kubernetes Pod restore deferred to the 1.38 interface work. |
| 4 | `damon-virtio-pmem` | Adds disk-backed guest storage, pmem/DAX, DAMON/reporting and memory evaluation; direct runtime restore is accepted in its documented scope. The retained guest stargz store is migrated to disk, with VM acceptance pending. Inherited Step 3 limitations remain disclosed. |

Read the milestone document on its own branch: [Step 1](step-1.md),
[Step 2](https://github.com/vhive-serverless/firecracker-containerd/blob/vhive-compatible/docs/modernization/step-2.md),
[Step 3](https://github.com/vhive-serverless/firecracker-containerd/blob/cri-native-snapshot-restore/docs/modernization/step-3.md),
and [Step 4](https://github.com/vhive-serverless/firecracker-containerd/blob/damon-virtio-pmem/docs/modernization/step-4.md).
Later branches retain earlier milestone documents as descriptions of those
milestones, not as descriptions of every later branch's storage or dependencies.

## Build and evidence

Use this checkout's [versions.json](versions.json), module manifests and gitlinks.
`selected_inputs` describes this branch's dependency selection; `existing`
describes the historical pre-modernization baseline. Validation entries name the
exact tested source and scope. Later documentation, test-only maintenance, and
production fixes may not have repeated broader acceptance; use each maintenance
receipt to distinguish focused checks from full VM acceptance. Never substitute a
later branch's dependency manifest when building an earlier one.

The scripts in `tools/modernize/` prepare the worker, build and privately install
the stack, and run acceptance. Follow the branch-specific milestone procedure
and its tested revision. VM acceptance requires the designated KVM worker;
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

## Recorded branch-chain audit

The [forward-merge receipt](evidence/branch-chain-audit.json) records the original
and reconciled tips, all three ancestor checks and unchanged source/dependency
selections. The [agent footprint audit](agent-footprint.md) answers the DSec and
Go/Rust comparison using a pinned Step 4 image.

The user confirmed on 2026-11-12 that Kubernetes Pod checkpoint/restore is
deferred to 1.38 interface availability. The [recorded API audit](step-3-api-audit.md)
distinguishes that external dependency from the observed CRI failures and the
independent Step 4 stargz storage gap.
