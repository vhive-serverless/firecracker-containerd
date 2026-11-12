# Step 4 memory evaluation: frozen source `0a268c5589c3`

**Pmem/DAX reduced host cgroup memory against matched ext4 controls, while the full combined configuration used more memory than either SquashFS baseline.** Reclamation reduced guest cache and late-idle mapped memory, but its common-idle average benefit was small and CPU/readback costs increased. These are descriptive observations from three randomized blocks on one worker, not confidence intervals or general performance guarantees.

The completed clean-source matrix contains **21/21 passed trials and 42/42 verified VM workloads**. Each trial has two simultaneous unjailed VMs, one vCPU and 512 MiB configured RAM per VM; three fixed-seed randomized blocks use seven arms. Each VM writes 64 MiB to its 128 MiB upper and 128 MiB to its workload disk, warms both, maps the immutable agent and retains a 32 MiB anonymous canary across 30 seconds idle. All arms use the same guest tree and Step 4 binary; “original” is a RAM-upper storage-layout control, not an earlier binary. The [protocol](../../README.md#controlled-matrix) defines the workload and measurement domains.

Primary memory is the trial’s fresh dedicated cgroup, including runtime, both VMMs, kernel and owned cache charges. Common idle is the same 20-second window (seconds 5–25 after the later guest idle-start record). Full trial spans phase start through verified workload completion, including setup. Table values below are arithmetic means of three trials. Sampled peaks need not equal instantaneous peaks. All individual trials/ranges are shown in [Figure 1](01_memory_and_cpu.svg) and [trials_compact.csv](trials_compact.csv).

| Arm | Idle charge (MiB) | Full sampled peak (MiB) | Full memory-time (GiB·s) | Full CPU (s) |
| --- | ---: | ---: | ---: | ---: |
| Original (RAM upper) | 1480.4 | 1488.0 | 61.16 | 18.45 |
| Disk (SquashFS) | 1760.7 | 1768.1 | 75.32 | 19.53 |
| Reclaim (SquashFS) | 1746.5 | 1760.6 | 75.66 | 22.54 |
| Ext4 block | 2189.5 | 2197.1 | 99.58 | 21.10 |
| Pmem/DAX | 2088.9 | 2096.3 | 95.00 | 21.13 |
| Ext4 + reclaim | 2179.2 | 2190.3 | 98.28 | 23.36 |
| Combined | 2081.4 | 2090.3 | 95.03 | 23.20 |

Within-block contrasts preserve the same filesystem for pmem comparisons. Entries below are **target minus baseline: mean [minimum, maximum] across the three paired blocks**. Negative values indicate lower observed cost. [Figure 2](02_paired_tradeoffs.svg) displays every block; the complete differences are in [paired_block_differences.csv](paired_block_differences.csv).

| Contrast | Idle charge Δ MiB | Full memory-time Δ GiB·s | Full CPU Δ s | Readback RPC Δ ms/VM |
| --- | ---: | ---: | ---: | ---: |
| Disk (SquashFS) − Original (RAM upper) | +280.27 [+278.14, +283.71] | +14.16 [+13.81, +14.35] | +1.084 [+1.073, +1.099] | +1.32 [+0.91, +1.81] |
| Reclaim (SquashFS) − Disk (SquashFS) | -14.18 [-15.84, -12.55] | +0.34 [-0.10, +1.08] | +3.005 [+2.570, +3.323] | +86.37 [+74.78, +103.19] |
| Pmem/DAX − Ext4 block | -100.55 [-102.63, -98.55] | -4.58 [-4.96, -4.15] | +0.039 [-0.002, +0.070] | +7.97 [+5.01, +12.73] |
| Combined − Ext4 + reclaim | -97.81 [-101.13, -91.89] | -3.24 [-4.56, -1.50] | -0.161 [-0.279, -0.041] | +10.22 [-6.39, +22.05] |

Pmem versus ext4 block lowered idle cgroup mean by 100.55 MiB (4.59%), full memory-time by 4.60%, and mean two-VMM PSS by 39.52 MiB. Combined versus ext4 + reclaim lowered idle cgroup mean by 97.81 MiB (4.49%) and full memory-time by 3.30%. PSS is a separate mapped-memory view and is never added to cgroup charges.

The combined arm nevertheless remained 320.69 MiB (18.21%) above disk/SquashFS and 600.96 MiB (40.59%) above the RAM-upper original. Disk itself increased idle charge relative to the RAM-upper control. Ext4 block versus disk/SquashFS increased idle charge by 428.81 MiB; their idle-end cgroup file counters differed by 440.16 MiB. The published SquashFS input is 72 MiB apparent / 70.71 MiB allocated, versus 512 MiB apparent / 207.47 MiB allocated for ext4. The larger logical image and its verified/warmed host cache are part of the measured format/preparation cost; this comparison does not isolate DAX.

Reclaim versus disk/SquashFS lowered common-idle charge by only 14.18 MiB (0.81%), increased full CPU by 15.38%, and increased full memory-time by 0.45%. Full-trial duration rose from 51.71 to 52.79 seconds. Idle cgroup CPU rose from 0.046 to 0.874 CPU-seconds over the fixed 20 seconds. Exploratory paired comparison of combined with pmem alone gives only 7.56 MiB lower idle mean and essentially unchanged full memory-time (+0.035 GiB·s), with more CPU. This exploratory contrast is additional to the analyzer’s four predefined contrasts.

[Figure 4](04_idle_evolution.svg) shows that reclamation develops late within the chosen idle window. This explains why endpoint reductions can exceed the fixed-window mean benefit; it does not justify extrapolating an unmeasured steady state. Guest cache drops are not automatically host-memory savings.

| Arm | VM creation RPC mean ms/VM | Readback RPC mean ms/VM | Physical writes, cumulative MiB: mean [min, max] | Guest probe major faults [min, max] |
| --- | ---: | ---: | ---: | ---: |
| Original (RAM upper) | 2310.2 | 894.4 | 268.8 [145.2, 367.0] | [1, 2] |
| Disk (SquashFS) | 2893.3 | 895.7 | 484.1 [455.5, 502.8] | [0, 2] |
| Reclaim (SquashFS) | 2951.4 | 982.1 | 608.6 [595.6, 615.9] | [10, 94] |
| Ext4 block | 3372.7 | 891.4 | 811.1 [748.6, 856.9] | [23, 26] |
| Pmem/DAX | 3302.4 | 899.4 | 723.8 [693.8, 760.6] | [0, 0] |
| Ext4 + reclaim | 3373.2 | 977.6 | 785.4 [694.0, 893.9] | [55, 92] |
| Combined | 3439.5 | 987.8 | 778.2 [702.3, 841.3] | [0, 0] |

Physical I/O selects device **8:0** only. Full-trial I/O deltas are unavailable in all 21 trials because the device lacks an initial local bracket; none are zero-filled. The table instead reports the explicitly named cumulative counter at the full-window end, since fresh cgroup creation, including pre-phase setup and writeback. Its wide block ranges are retained. Loop/thin/physical layers and VMM `/proc/io` are separate observations, never summed. All full-window cgroup major-fault deltas were zero; guest major faults above are a different scope. DAX arms had more guest minor faults (see [guests.csv](guests.csv)); low major-fault counts do not imply zero mapping cost.

Reactivation costs remain visible in [Figure 3](03_reactivation_and_guest_cache.svg), which uses a labelled log scale and shows all six VM observations per arm. In SquashFS reclaim, instrumented immutable-file retouch durations were:

| Block | VM0 retouch ms | VM1 retouch ms |
| --- | ---: | ---: |
| 0 | 105.831 | 68.477 |
| 1 | 605.046 | 45.555 |
| 2 | 51.821 | 50.040 |

The 605.046 ms observation is retained, not trimmed or treated as a percentile estimate. Probe phases include hashing, mmap/canary checks and sampling overhead; RPC timings are also instrumented. These are not pure storage-request latencies. Two VMs within one trial are not independent block replications.

All 42 guest probes verified immutable-file retouch and the retained anonymous canary, and all 21 trial completions verified writable payloads/copy-up/rename/delete. Observed `STATX_ATTR_DAX` matched all 12 pmem/combined VMs and was false for all 30 block-backed VMs. This establishes correctness for the exercised data and idle/reactivation path, not every workload or native restore. This matrix did not test native restore; at the evaluated revision it was inherited Step 3 WIP.

Nominal pmem page metadata is **8 MiB per 512 MiB device**, assuming 64-byte `struct page` and 4 KiB pages (16 MiB across two devices). Observed guest `MemTotal` was **481.6328125 MiB in every VM**, including controls. Unchanged `MemTotal` does not establish zero metadata cost: normal-page allocations for vmemmap can reduce free/available memory without changing total RAM. Overall measured costs include those allocations, but exact metadata cost was not isolated; do not add or subtract the nominal estimate from measured cgroup/PSS values.

Recorded Firecracker free-page-reporting counters may lag the last `FlushMetrics`: the harness’s ready flag can remain true after an earlier frame. The raw counters are retained and explicitly flagged in `guests.csv`; they cannot certify complete final traffic/failure totals or unique freed memory. In `b00-06-reclaim` VM1 the recorded descriptor count is zero despite guest DAMON being enabled and its idle-end counter reporting 99,958,784 reclaimed bytes. Neither counter proves an equal amount of host-memory saving. Primary cgroup/PSS/CPU and guest correctness observations do not depend on that reporting-frame readiness flag.

This is one host, one fixed workload, two VMs per trial and three randomized blocks (seed 5823). The host is Linux 6.8.0-147-generic; guest kernel is the pinned 6.18.56 build. KSM was inactive. Input hashing warms host backing; ext3 upper capacity reservation writes the full capacity. No global cache drop or forced host reclaim occurred. Thus these results characterize fresh guest execution with verified/warmed host backing, not cold-host I/O. Cgroup ownership/cache charging and shared backing are not a global physical-memory census; whole-host meminfo includes unrelated workloads. [missing_metrics.csv](missing_metrics.csv) records unavailable views, including full-trial VMM PSS before its sampler starts. No unsupported extrapolation is used.

Analysis used the strict 5-second offset / 20-second window summary, then independently verified **168 raw input-file hashes**, recomputed **42 full/idle cgroup integrals and 21 idle VMM-PSS integrals**, and checked **72 paired metric differences** against the validated analyzer. Only valid local brackets (maximum gap 1.5 seconds) were used. See [verification.json](verification.json), [integral_verification.csv](integral_verification.csv), and the [evaluation receipt](../../../../docs/modernization/evidence/0a268c5-evaluation-receipt.log). Figures are standalone SVG and 300-dpi PNG; all points and observed ranges are retained, with no hypothesis tests or confidence claims.

Exact evaluation source: `0a268c5589c37832e1c3dc89a7b60ed2701e0b9e`. The result archive contains original observations, strict summaries, manifest and driver logs; the large image binaries are excluded and remain at the exact worker locations and hashes in [provenance.json](provenance.json). `SHA256SUMS` covers result artifacts. Reproduction commands and the isolated pinned analysis environment are in [REPRODUCE.md](REPRODUCE.md).
