# Read-only host sampler

Run `sample_host.py` with the explicit host PIDs to observe. It reads `/proc`
and, optionally, an explicitly selected cgroup v2 directory. It does not change
cgroups, drop caches, request reclaim, signal sampled processes, or launch a
workload. Root owns the workload and evaluation design.

```bash
python3 tools/memory-eval/sample_host.py \
  --pid "$vmm_pid" --pid "$shim_pid" \
  --cgroup /sys/fs/cgroup/your-existing-group \
  --interval 0.5 --duration 60 --output /absolute/new-observations.jsonl
```

`--pid` is required and repeatable; duplicate PIDs are sampled once. The interval
must be finite and between 0.1 and 3600 seconds. Duration must be finite, positive
and at most 3600 seconds. Output defaults to stdout; `--output` creates a new file
exclusively and never overwrites an existing file. Its parent must already exist.
Allow the process owner or root to read the requested proc files; unavailable
metrics are explicit JSON status objects, never zero-filled observations.

JSONL contains metadata, sample and end records. Every record has UTC time and
`monotonic_ns` at emission. Samples also contain collection start/end monotonic
nanoseconds, elapsed seconds and a sequence number; each process has its own
read window. These are sequential observations, not an atomic system snapshot.
Sampling starts immediately, skips missed interval slots rather than catching
up in a burst, and stops scheduling at the duration limit. An in-flight kernel
file read can finish after that limit. Ctrl-C emits an interrupted end record.
There are no summary integrals, totals, interpolated samples or savings claims.

Each process reports raw `/proc/PID/stat` identity, state, CPU ticks, minor/major
fault counters (own and waited-for children), virtual size and RSS pages; all
`smaps_rollup` fields including PSS/RSS/private/shared; and all `/proc/PID/io`
counters. Metadata records clock ticks/second, base page size and boot ID.
The `kB` units reported by Linux proc memory files mean 1024 bytes. Proc I/O
`syscr`/`syscw` count syscalls; other proc I/O fields are bytes. Cgroup CPU
`*_usec` fields are microseconds. Cgroup memory/current is bytes; `memory.stat`
has mixed byte, event and page counters, retained in raw kernel units. Cgroup
`io.stat` is keyed by device major:minor and preserves its raw named counters.
Host `meminfo` keeps explicit native units, including unitless page counts.

A PID's first observed `starttime` is pinned for the run. PID reuse is reported
and the new identity is never adopted. Reads are checked against stat again;
metrics collected across a reuse, exit or unverifiable identity are discarded.
An initially absent PID is `not_found`; disappearance after observation is
`exited`, or `exited_during_sample`. Permission failures and parsing/read failures
are explicit. A zombie may still have stat state `Z` and unavailable smaps;
absence or hidepid restrictions can be indistinguishable as `not_found`.
The optional cgroup is pinned by device/inode; replacements are rejected.
Individual unavailable controller files yield `partial` cgroup observations.

Do not sum shared RSS as physical memory. PSS apportions mapped shared pages
among current mappers; it does not include all unmapped page cache or attribute
all host ownership. Shared backing can appear in several processes' RSS, host
page cache and a cgroup's charge at once. Cgroup memory counters cover that
cgroup and descendants, with ownership/cache-charge rules that may charge a
shared file to another group or retain its charge after the initiating process
exits. They do not cover all globally shared backing. The explicitly selected
cgroup need not contain every selected PID and can include other processes.
Host meminfo includes unrelated workloads. Proc I/O is not device latency or a
complete account of DAX/mmap activity. Keep these observation domains separate.

Run validation only on the designated Cloudlab worker, from a clean committed
checkout with Python's standard library:

```bash
python3 -m unittest discover -s tools/memory-eval -p 'test_sample_host.py' -v
python3 tools/memory-eval/test_worker_sample.py --output-dir "$new_evidence_dir"
```

The worker smoke check alone launches a temporary owned process, allocates and
touches 16 MiB, writes/fsyncs a private 1 MiB file, then terminates/reaps that
process during observation to check exit handling. It samples its existing
explicit cgroup if the v2 interface is available, changes no cgroups, and
cleans its temporary input file. The produced JSONL remains in the new output
directory for review. This check is validation, not an evaluation workload.

The [committed worker receipt](evidence/dfb45bd-receipt.log) records twelve passing
tests and a six-sample capture from source `dfb45bd7bdec2d59184f4fc077e654f8d3589857`.
The owned process was observed alive and then exited. The explicit existing
session cgroup exposed memory and CPU counters; its absent `io.stat` was
recorded as unavailable, yielding partial cgroup observations. Raw JSONL and
validation logs are retained alongside the receipt.

## Controlled matrix

`tools/modernize/evaluate-memory.sh` runs the five requested configurations plus
matched filesystem controls. Run a clean `build-stack.sh` and private install
first. It prepares the test image, precompiles the harness on the worker, checks
its build receipt, checks owned failure cleanup, and then performs three
fixed-seed randomized blocks. No compilation occurs during observations.

| Arm | Guest writable upper | Immutable OS lower | Reclamation |
| --- | --- | --- | --- |
| original | tmpfs | SquashFS via block | off |
| disk | host-disk ext4 | SquashFS via block | off |
| pmem | host-disk ext4 | ext4 via pmem/DAX | off |
| reclaim | host-disk ext4 | SquashFS via block | DAMON/reporting |
| combined | host-disk ext4 | ext4 via pmem/DAX | DAMON/reporting |
| ext4-block | host-disk ext4 | ext4 via block | off |
| ext4-reclaim | host-disk ext4 | ext4 via block | DAMON/reporting |

“Original” selects the original storage layout on the common Step 4 binary and
guest tree, with the same input verification as other arms. It does not claim a
bit-for-bit Step 3 binary comparison. The controller derives both immutable
formats from one published SquashFS tree. SquashFS is padded to 2 MiB alignment;
this changes its file digest, not its guest tree. Conversion uses the pinned
image-builder image and the committed ext4 conversion helper. Runtime, shim,
guest-agent and precompiled-test identities are checked before observation;
image tags are resolved to immutable local image IDs. Default execution rejects
dirty or mixed revisions and stale test receipts. `--allow-dirty` is an explicit
development mode and marks the manifest, trials and result as non-acceptance.

Each trial uses two simultaneous unjailed VMs, each with one vCPU and 512 MiB
RAM. Each guest upper has 128 MiB nominal capacity; the tmpfs control is explicitly
remounted to that limit. Ext4 consumes some of its capacity for metadata, which
is part of the storage cost. Each VM writes 64 MiB of nonzero data to its guest
upper and 128 MiB to its existing disk-backed workload root. Patterns differ
between VMs and remain identical across arms. Checksums, copy-up, rename/delete,
the actual lower-file DAX attribute, and the installed probe hash are checked.
Both payloads are reread immediately before idle to establish warm working data.
The probe maps the immutable guest agent, holds a 32 MiB anonymous canary across
30 seconds idle, then rereads and verifies. Both writable payloads are checked
again. Reclamation uses the documented bounded policy and balloon target zero.

The primary memory view is the fresh private Docker cgroup containing this
trial's runtime, VMMs, kernel charges and owned file-cache charges. A sampler
outside the cgroup starts before runtime/VM startup. A second sampler observes
both VMMs after boot, reporting PSS separately. The controller creates a private
immutable input inside the trial, explicitly publishes/reuses its digest cache,
and deletes the temporary input after both VMs use the cache. The private cache
outlives VM cleanup during observation; it is removed only after the owned
container has stopped. Private writable disks and thin-pool backing are also
cleaned up. No global cache drop, forced host reclaim, or change to unrelated
workloads is performed. KSM must be inactive; the controller does not change it.

Full-image hashing warms host backing and adds startup CPU/I/O. Ext3 capacity
reservation also writes the full upper capacity and can leave host page cache.
These costs remain in the measurements. The experiment therefore measures fresh
guest execution with verified/warmed host backing, rather than cold-host I/O.
Whole-host meminfo is retained to expose unrelated activity, but is not added to
cgroup charges or VM PSS. Physical I/O selects the underlying filesystem device;
loop, thin-device and physical-device counters must not be added together.

Raw artifacts include the manifest, worker build receipt, Docker identity,
phase clocks, guest probe records, host and per-VMM JSONL, disk allocation,
reporting counters and complete workload logs. Full-trial memory-time covers
host phase `start` through `workload_complete`, including setup and measured
work. Report it separately from a fixed common idle window, normally seconds
5–25 after the later guest idle-start record, strictly before either idle ends.
No full-trial PSS integral is extrapolated over the unsampled boot interval.
Probe phase timings include hashing and instrumentation; they are not pure
storage request latency. VM creation and workload-readback RPC timings are
recorded separately. Snapshot/capture checks run separately from these trials.
Direct native runtime restore subsequently passed separate
[storage restore acceptance](../../docs/modernization/step-4.md#native-runtime-storage-restore)
at `72f4fc9`. The frozen evaluation does not test restore, and Kubernetes/CRI
Pod restore remains the inherited Step 3 WIP limitation.

Analyze a completed committed matrix using the separately worker-tested analyzer:

```bash
python3 tools/memory-eval/summarize_matrix.py \
  --input /absolute/completed-matrix --output /absolute/new-summary
```

The default requires clean matching provenance and passed trials. It writes
per-trial and aggregate CSV/JSON, paired block comparisons and explicit missing
metric reasons. Development smoke data requires `--allow-development` and is
labeled **DEVELOPMENT ONLY**. The short smoke protocol uses `--idle-offset 2
--idle-window 5`; the final protocol uses the default 5-second offset and
20-second common idle window. Counter layers are retained separately. Host and
guest faults, guest memory endpoints, instrumented probe timings and VMM
process I/O remain distinct from primary cgroup memory and physical-device I/O.

The [committed 21-trial results](results/0a268c5589c3/REPORT.md) preserve raw
observations, matching filesystem comparisons, costs and missing-data reasons.
That revision waited for any metrics frame after requesting the final flush,
so its reporting counters may omit the final frame. They must not be used as
complete-through-flush traffic or failure totals. Host memory/CPU and guest
correctness observations use independent data sources.

Subsequent runs use a FIFO byte watermark taken after the synchronous
`FlushMetrics` response and wait until every byte through that point belongs
to a fully parsed and aggregated record. This includes scanner-buffered bytes
and avoids treating an earlier frame or a partial record as acknowledgment.
It uses neither timestamps nor a settling delay. The reader's readiness poll
only bounds cancellation latency. Reporting counters still represent cumulative
descriptor traffic, rather than unique physical-memory savings.
