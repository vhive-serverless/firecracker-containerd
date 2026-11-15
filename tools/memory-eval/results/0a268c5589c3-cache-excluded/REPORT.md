# Step 4 memory overhead excluding host filesystem page cache

This supersedes the cache-inclusive report's overhead interpretation for this project.
Reanalysis of the same 21 historical trials at `0a268c5589c3`; no new VM runs.

Primary metric: `memory.current - (memory.stat.file - memory.stat.shmem)`.
This excludes host disk-backed file cache, including mapped/dirty file pages,
while retaining anonymous memory, tmpfs/shared memory, and kernel charges.
Guest filesystem cache resident inside guest RAM remains counted. The kernel's
[file counter includes shmem](https://docs.kernel.org/admin-guide/cgroup-v2.html),
so subtracting all of `file` would incorrectly discard RAM-backed allocations.
These are sampled accounting estimates, not exact allocation attribution; counters
are read separately. Kernel/slab charges remain included. This is workload footprint,
not incremental hypervisor overhead above a workload-free baseline.

Means use the original 20-second common-idle window, three trials per arm,
with two VMs plus runtime per trial. All units below are MiB across both VMs.

| Arm | Cache-excluded idle memory |
| --- | ---: |
| original | 832.11 |
| disk | 855.84 |
| reclaim | 841.62 |
| ext4-block | 844.49 |
| pmem | 744.12 |
| ext4-reclaim | 834.08 |
| combined | 736.43 |

| Target − baseline | Mean change | Paired range |
| --- | ---: | ---: |
| pmem − ext4-block | -100.38 | [-102.34, -98.51] |
| reclaim − disk | -14.21 | [-15.97, -12.64] |
| combined − pmem | -7.69 | [-8.81, -6.90] |
| combined − disk | -119.41 | [-121.63, -116.62] |
| combined − original | -95.68 | [-98.21, -94.30] |

Negative changes mean lower overhead under the revised definition. Results are
from one worker and three paired blocks, not general performance guarantees.
The earlier +320.69 MiB combined-versus-SquashFS figure includes excluded cache
and must not be described as overhead under this definition. CPU/readback costs
and correctness observations in the original report are unaffected.

Reproduce with `python3 tools/memory-eval/cache_excluded.py` from the repository.
The script reads the frozen archive without extraction, verifies original total
means against the strict summary, and writes per-trial idle/full-window metrics.
Original observations, tables, figures and checksums remain unchanged as historical
total-charge evidence; their memory figures are not cache-excluded results.
