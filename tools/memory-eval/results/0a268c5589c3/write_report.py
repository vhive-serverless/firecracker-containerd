#!/usr/bin/env python3
"""Write the descriptive report from the validated, unit-labelled result CSVs."""
import argparse
import csv
import json
from pathlib import Path
import statistics as st

ARMS=['original','disk','reclaim','ext4-block','pmem','ext4-reclaim','combined']
LABEL={'original':'Original (RAM upper)','disk':'Disk (SquashFS)','reclaim':'Reclaim (SquashFS)',
       'ext4-block':'Ext4 block','pmem':'Pmem/DAX','ext4-reclaim':'Ext4 + reclaim','combined':'Combined'}
PAIRS=[('original','disk'),('disk','reclaim'),('ext4-block','pmem'),('ext4-reclaim','combined')]


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--results',type=Path,required=True)
    args=parser.parse_args(); root=args.results
    rows=list(csv.DictReader((root/'trials_compact.csv').open()))
    guests=list(csv.DictReader((root/'guests.csv').open()))
    assert len(rows)==21 and len(guests)==42
    def values(arm,key):return [float(r[key]) for r in rows if r['arm']==arm]
    def mean(arm,key):return st.mean(values(arm,key))
    def delta(a,b,key):return [float(next(r for r in rows if r['arm']==b and r['block']==str(i))[key])-float(next(r for r in rows if r['arm']==a and r['block']==str(i))[key]) for i in range(3)]
    def describe(v,digits=2):return f'{st.mean(v):+.{digits}f} [{min(v):+.{digits}f}, {max(v):+.{digits}f}]'
    def reduction(a,b,key):return (mean(a,key)-mean(b,key))/mean(a,key)*100
    lines=['# Step 4 memory evaluation: frozen source `0a268c5589c3`','',
'**Pmem/DAX reduced host cgroup memory against matched ext4 controls, while the full combined configuration used more memory than either SquashFS baseline.** Reclamation reduced guest cache and late-idle mapped memory, but its common-idle average benefit was small and CPU/readback costs increased. These are descriptive observations from three randomized blocks on one worker, not confidence intervals or general performance guarantees.','',
'The completed clean-source matrix contains **21/21 passed trials and 42/42 verified VM workloads**. Each trial has two simultaneous unjailed VMs, one vCPU and 512 MiB configured RAM per VM; three fixed-seed randomized blocks use seven arms. Each VM writes 64 MiB to its 128 MiB upper and 128 MiB to its workload disk, warms both, maps the immutable agent and retains a 32 MiB anonymous canary across 30 seconds idle. All arms use the same guest tree and Step 4 binary; “original” is a RAM-upper storage-layout control, not an earlier binary. The [protocol](../../README.md#controlled-matrix) defines the workload and measurement domains.','',
'Primary memory is the trial’s fresh dedicated cgroup, including runtime, both VMMs, kernel and owned cache charges. Common idle is the same 20-second window (seconds 5–25 after the later guest idle-start record). Full trial spans phase start through verified workload completion, including setup. Table values below are arithmetic means of three trials. Sampled peaks need not equal instantaneous peaks. All individual trials/ranges are shown in [Figure 1](01_memory_and_cpu.svg) and [trials_compact.csv](trials_compact.csv).','',
'| Arm | Idle charge (MiB) | Full sampled peak (MiB) | Full memory-time (GiB·s) | Full CPU (s) |',
'| --- | ---: | ---: | ---: | ---: |']
    for arm in ARMS:
        lines.append(f'| {LABEL[arm]} | {mean(arm,"idle_cgroup_MiB"):.1f} | {mean(arm,"full_peak_MiB"):.1f} | {mean(arm,"full_memory_GiB_s"):.2f} | {mean(arm,"full_cpu_s"):.2f} |')
    lines+=['',
'Within-block contrasts preserve the same filesystem for pmem comparisons. Entries below are **target minus baseline: mean [minimum, maximum] across the three paired blocks**. Negative values indicate lower observed cost. [Figure 2](02_paired_tradeoffs.svg) displays every block; the complete differences are in [paired_block_differences.csv](paired_block_differences.csv).','',
'| Contrast | Idle charge Δ MiB | Full memory-time Δ GiB·s | Full CPU Δ s | Readback RPC Δ ms/VM |',
'| --- | ---: | ---: | ---: | ---: |']
    for a,b in PAIRS:
        lines.append('| '+LABEL[b]+' − '+LABEL[a]+' | '+' | '.join(describe(delta(a,b,k),3 if k=='full_cpu_s' else 2) for k in ['idle_cgroup_MiB','full_memory_GiB_s','full_cpu_s','readback_mean_ms'])+' |')
    lines+=['',
f'Pmem versus ext4 block lowered idle cgroup mean by {mean("ext4-block","idle_cgroup_MiB")-mean("pmem","idle_cgroup_MiB"):.2f} MiB ({reduction("ext4-block","pmem","idle_cgroup_MiB"):.2f}%), full memory-time by {reduction("ext4-block","pmem","full_memory_GiB_s"):.2f}%, and mean two-VMM PSS by {mean("ext4-block","idle_pss_MiB")-mean("pmem","idle_pss_MiB"):.2f} MiB. Combined versus ext4 + reclaim lowered idle cgroup mean by {mean("ext4-reclaim","idle_cgroup_MiB")-mean("combined","idle_cgroup_MiB"):.2f} MiB ({reduction("ext4-reclaim","combined","idle_cgroup_MiB"):.2f}%) and full memory-time by {reduction("ext4-reclaim","combined","full_memory_GiB_s"):.2f}%. PSS is a separate mapped-memory view and is never added to cgroup charges.','',
f'The combined arm nevertheless remained {mean("combined","idle_cgroup_MiB")-mean("disk","idle_cgroup_MiB"):.2f} MiB ({-reduction("disk","combined","idle_cgroup_MiB"):.2f}%) above disk/SquashFS and {mean("combined","idle_cgroup_MiB")-mean("original","idle_cgroup_MiB"):.2f} MiB ({-reduction("original","combined","idle_cgroup_MiB"):.2f}%) above the RAM-upper original. Disk itself increased idle charge relative to the RAM-upper control. Ext4 block versus disk/SquashFS increased idle charge by {mean("ext4-block","idle_cgroup_MiB")-mean("disk","idle_cgroup_MiB"):.2f} MiB; their idle-end cgroup file counters differed by {mean("ext4-block","idle_end_cgroup_file_MiB")-mean("disk","idle_end_cgroup_file_MiB"):.2f} MiB. The published SquashFS input is 72 MiB apparent / 70.71 MiB allocated, versus 512 MiB apparent / 207.47 MiB allocated for ext4. The larger logical image and its verified/warmed host cache are part of the measured format/preparation cost; this comparison does not isolate DAX.','',
f'Reclaim versus disk/SquashFS lowered common-idle charge by only {mean("disk","idle_cgroup_MiB")-mean("reclaim","idle_cgroup_MiB"):.2f} MiB ({reduction("disk","reclaim","idle_cgroup_MiB"):.2f}%), increased full CPU by {-reduction("disk","reclaim","full_cpu_s"):.2f}%, and increased full memory-time by {-reduction("disk","reclaim","full_memory_GiB_s"):.2f}%. Full-trial duration rose from {mean("disk","full_duration_s"):.2f} to {mean("reclaim","full_duration_s"):.2f} seconds. Idle cgroup CPU rose from {mean("disk","idle_cpu_s"):.3f} to {mean("reclaim","idle_cpu_s"):.3f} CPU-seconds over the fixed 20 seconds. Exploratory paired comparison of combined with pmem alone gives only {mean("pmem","idle_cgroup_MiB")-mean("combined","idle_cgroup_MiB"):.2f} MiB lower idle mean and essentially unchanged full memory-time ({mean("combined","full_memory_GiB_s")-mean("pmem","full_memory_GiB_s"):+.3f} GiB·s), with more CPU. This exploratory contrast is additional to the analyzer’s four predefined contrasts.','',
'[Figure 4](04_idle_evolution.svg) shows that reclamation develops late within the chosen idle window. This explains why endpoint reductions can exceed the fixed-window mean benefit; it does not justify extrapolating an unmeasured steady state. Guest cache drops are not automatically host-memory savings.','',
'| Arm | VM creation RPC mean ms/VM | Readback RPC mean ms/VM | Physical writes, cumulative MiB: mean [min, max] | Guest probe major faults [min, max] |',
'| --- | ---: | ---: | ---: | ---: |']
    for arm in ARMS:
        io=values(arm,'physical_write_cumulative_MiB')
        faults=[float(r['guest_major_faults_probe']) for r in guests if r['arm']==arm]
        lines.append(f'| {LABEL[arm]} | {mean(arm,"vm_create_mean_ms"):.1f} | {mean(arm,"readback_mean_ms"):.1f} | {st.mean(io):.1f} [{min(io):.1f}, {max(io):.1f}] | [{min(faults):.0f}, {max(faults):.0f}] |')
    lines+=['',
'Physical I/O selects device **8:0** only. Full-trial I/O deltas are unavailable in all 21 trials because the device lacks an initial local bracket; none are zero-filled. The table instead reports the explicitly named cumulative counter at the full-window end, since fresh cgroup creation, including pre-phase setup and writeback. Its wide block ranges are retained. Loop/thin/physical layers and VMM `/proc/io` are separate observations, never summed. All full-window cgroup major-fault deltas were zero; guest major faults above are a different scope. DAX arms had more guest minor faults (see [guests.csv](guests.csv)); low major-fault counts do not imply zero mapping cost.','',
'Reactivation costs remain visible in [Figure 3](03_reactivation_and_guest_cache.svg), which uses a labelled log scale and shows all six VM observations per arm. In SquashFS reclaim, instrumented immutable-file retouch durations were:','',
'| Block | VM0 retouch ms | VM1 retouch ms |', '| --- | ---: | ---: |']
    for block in range(3):
        own=[r for r in guests if r['arm']=='reclaim' and r['block']==str(block)]
        lines.append(f'| {block} | {float(next(r for r in own if r["vm"]=="vm0")["retouch_ms"]):.3f} | {float(next(r for r in own if r["vm"]=="vm1")["retouch_ms"]):.3f} |')
    lines+=['',
'The 605.046 ms observation is retained, not trimmed or treated as a percentile estimate. Probe phases include hashing, mmap/canary checks and sampling overhead; RPC timings are also instrumented. These are not pure storage-request latencies. Two VMs within one trial are not independent block replications.','',
'All 42 guest probes verified immutable-file retouch and the retained anonymous canary, and all 21 trial completions verified writable payloads/copy-up/rename/delete. Observed `STATX_ATTR_DAX` matched all 12 pmem/combined VMs and was false for all 30 block-backed VMs. This establishes correctness for the exercised data and idle/reactivation path, not every workload or native restore. This matrix did not test native restore; at the evaluated revision it was inherited Step 3 WIP.','',
'Nominal pmem page metadata is **8 MiB per 512 MiB device**, assuming 64-byte `struct page` and 4 KiB pages (16 MiB across two devices). Observed guest `MemTotal` was **481.6328125 MiB in every VM**, including controls. Unchanged `MemTotal` does not establish zero metadata cost: normal-page allocations for vmemmap can reduce free/available memory without changing total RAM. Overall measured costs include those allocations, but exact metadata cost was not isolated; do not add or subtract the nominal estimate from measured cgroup/PSS values.','',
'Recorded Firecracker free-page-reporting counters may lag the last `FlushMetrics`: the harness’s ready flag can remain true after an earlier frame. The raw counters are retained and explicitly flagged in `guests.csv`; they cannot certify complete final traffic/failure totals or unique freed memory. In `b00-06-reclaim` VM1 the recorded descriptor count is zero despite guest DAMON being enabled and its idle-end counter reporting 99,958,784 reclaimed bytes. Neither counter proves an equal amount of host-memory saving. Primary cgroup/PSS/CPU and guest correctness observations do not depend on that reporting-frame readiness flag.','',
'This is one host, one fixed workload, two VMs per trial and three randomized blocks (seed 5823). The host is Linux 6.8.0-147-generic; guest kernel is the pinned 6.18.56 build. KSM was inactive. Input hashing warms host backing; ext3 upper capacity reservation writes the full capacity. No global cache drop or forced host reclaim occurred. Thus these results characterize fresh guest execution with verified/warmed host backing, not cold-host I/O. Cgroup ownership/cache charging and shared backing are not a global physical-memory census; whole-host meminfo includes unrelated workloads. [missing_metrics.csv](missing_metrics.csv) records unavailable views, including full-trial VMM PSS before its sampler starts. No unsupported extrapolation is used.','',
'Analysis used the strict 5-second offset / 20-second window summary, then independently verified **168 raw input-file hashes**, recomputed **42 full/idle cgroup integrals and 21 idle VMM-PSS integrals**, and checked **72 paired metric differences** against the validated analyzer. Only valid local brackets (maximum gap 1.5 seconds) were used. See [verification.json](verification.json), [integral_verification.csv](integral_verification.csv), and the [evaluation receipt](../../../../docs/modernization/evidence/0a268c5-evaluation-receipt.log). Figures are standalone SVG and 300-dpi PNG; all points and observed ranges are retained, with no hypothesis tests or confidence claims.','',
'Exact evaluation source: `0a268c5589c37832e1c3dc89a7b60ed2701e0b9e`. The result archive contains original observations, strict summaries, manifest and driver logs; the large image binaries are excluded and remain at the exact worker locations and hashes in [provenance.json](provenance.json). `SHA256SUMS` covers result artifacts. Reproduction commands and the isolated pinned analysis environment are in [REPRODUCE.md](REPRODUCE.md).','']
    (root/'REPORT.md').write_text('\n'.join(lines))


if __name__=='__main__':main()
