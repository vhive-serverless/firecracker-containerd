# Reclamation integration notes

Step 3 remains WIP by explicit user instruction. Reclamation component,
controlled evaluation and direct runtime restore results are recorded below;
they do not complete the inherited Kubernetes/CRI integration gates.
The runtime configuration below passed clean committed worker acceptance at
`97062314463adb53ee6534afc5d8eafd82bbf509`: full build, private installation,
lint, race units, forced protobuf regeneration, and isolated reporting, DAMON
and ordinary balloon tests. The
[receipt](evidence/9706231-damon-acceptance-receipt.log) and
[raw acceptance log](evidence/9706231-damon-acceptance.log) identify the source,
dependencies, actual binaries and image. The checkout stayed clean.
The first clean run at `fe2562f` completed build and private installation but
stopped at lint because the integration test needed gofmt. The
[failure log](evidence/fe2562f-damon-acceptance-failed.log) is retained; this
attempt does not count as acceptance.

The corrected committed run reclaimed 41943040 bytes while retaining the
128 MiB cold file. VMM RSS fell from 287388 to 251532 KiB and returned to
286208 KiB after reread. Reporting processed 76 descriptors without failures,
with balloon target/actual zero. Both the file checksum and memory-only token
survived reuse. This run accepts the mechanism; the later controlled evaluation,
pmem integration and restore checks have separate acceptance records below.

`CreateVMRequest.Reclamation` overrides administrator `guest_reclamation`
configuration. Nil inherits the administrator value; explicit `Enabled=false`
disables it for that VM. Enabled mode activates DAMON and free-page reporting;
pmem selection is separate. Defaults are five-second cold age, 16 MiB/s size
quota, 5 ms/s action quota and reporting order nine. `MinAgeSeconds`,
`QuotaMibPerSecond`, `QuotaTimeMs` and optional `ReportingOrder` expose bounded
overrides. Native restore retains its captured kernel/device state.

The [pinned kernel implementation](https://github.com/amazonlinux/linux/blob/7483450dec64b4f923037de31d8c28298a7c3359/mm/damon/reclaim.c)
supports built-in DAMON_RECLAIM. Anonymous folios can be excluded with
`skip_anon=Y`; this is not a filesystem selector and does not specifically
exclude tmpfs/shmem. Use the name “non-anonymous LRU reclaim.” The cold-age
threshold is in microseconds. Configure while disabled, then enable; stopping
is synchronous. Runtime changes require the documented commit-input protocol.

For the controlled cold-file experiment, the implemented initial policy is a
five-second cold age, 5 ms / 16 MiB per second action quotas, 5 ms sampling and
100 ms aggregation. Free-memory watermarks of 1000/1000/0 per thousand remove
pressure gating; PSI/user-feedback quota autotuning stays disabled. These
choices are our experiment settings, not disclosed paper settings. Test their
actual activation and reclaim behavior before relying on them.

The first development VM test retained a 128 MiB ext4 file across idle. DAMON
reclaimed 49467392 bytes; VMM RSS moved from 288976 to 247264 KiB, then to
289472 KiB after reading the file again. Reporting processed 78 descriptors
without failures, with balloon target/actual zero. The file checksum and a
random token held only in process memory survived page reuse. This is one
mechanism check, not the controlled evaluation or a claimed paper reproduction.

Record `bytes_reclaim_tried_regions`, `bytes_reclaimed_regions`,
`nr_quota_exceeds`, and `kdamond_pid` from the module parameters. Tried bytes
include candidates later rejected by filters; do not call them exact scanned
bytes. The selected kernel disables FTRACE, so exact vmscan trace counts are
unavailable without a separately pinned kernel configuration change.

The [reporting order implementation](https://github.com/amazonlinux/linux/blob/7483450dec64b4f923037de31d8c28298a7c3359/mm/page_reporting.c)
accepts `page_reporting.page_reporting_order` at boot and exposes the same
parameter through sysfs. Order 9 means 2 MiB on this 4 KiB-page guest; lower
orders can report fragmented memory with more work. Keep the order explicit in
experiments and account for its cost. Reporting is deferred and subject to zone
watermarks, so an enabled flag does not prove release.

The selected guest configuration disables both initialization-on-free by default
and page poisoning. Changing those settings requires rechecking negotiation:
the [virtio-balloon driver](https://github.com/amazonlinux/linux/blob/7483450dec64b4f923037de31d8c28298a7c3359/drivers/virtio/virtio_balloon.c)
can disable reporting when the VMM lacks the required poison feature.

The runtime's reporting development test compares enabled/disabled VMs with
balloon target zero, processes VMM metrics, checks RSS release after freeing
resident pages, then verifies retained data after fresh allocations. A separate
DAMON test retains a cold disk file across idle, observes reclaim/reporting
and a host release, and verifies its checksum after refault. Neither test replaces
the controlled five-configuration memory/performance evaluation.

The later [controlled evaluation](../../tools/memory-eval/results/0a268c5589c3/REPORT.md)
passed all 21 trials. Reclamation's fixed-window host-memory benefit was small
in this workload and added CPU/readback costs. Its report distinguishes guest
cache endpoints, mapped PSS and cgroup memory, and discloses the original
reporting-frame freshness limitation.

The later [native runtime restore acceptance](step-4.md#native-runtime-storage-restore)
passed at `72f4fc9`. Six restored reclamation-enabled VMs demonstrated RSS
release, zero balloon target/actual size and retained-data correctness after
fresh allocations. This extends the runtime checks without accepting Step 3's
remaining Kubernetes/CRI integration.
