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
