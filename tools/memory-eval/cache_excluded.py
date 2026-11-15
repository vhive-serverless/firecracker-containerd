#!/usr/bin/env python3
"""Reanalyze frozen observations, excluding disk-backed host file cache."""
import csv
import json
from pathlib import Path
import statistics
import tarfile
from summarize_matrix import gauge, metric

ROOT = Path(__file__).resolve().parent
SOURCE = ROOT / 'results/0a268c5589c3'
OUT = ROOT / 'results/0a268c5589c3-cache-excluded'

def main():
    OUT.mkdir(exist_ok=True)
    rows = []
    with tarfile.open(SOURCE / 'raw-observations.tar.gz') as archive:
        trials = json.load(archive.extractfile('memory-summary-0a268c5589c3/trials.json'))['trials']
        for trial in trials:
            samples = [json.loads(line) for line in archive.extractfile(
                'memory-matrix-0a268c5589c3/' + trial['trial'] + '/host.jsonl')]
            samples = [r for r in samples if r.get('type') == 'sample']
            def overhead(r):
                stat = metric(r, 'memory.stat')
                assert 0 <= stat['shmem'] <= stat['file']
                value = metric(r, 'memory.current') - stat['file'] + stat['shmem']
                assert value >= 0
                return value
            row = {k: trial[k] for k in ('trial', 'arm', 'block')}
            for phase in ('common_idle', 'full_trial'):
                window = trial['details']['windows'][phase]
                start, end = window['start_host_monotonic_ns'], window['end_host_monotonic_ns']
                total = gauge(samples, start, end, lambda r: metric(r, 'memory.current'), 2_000_000_000)
                reference = trial['metrics'][phase]['dedicated_cgroup']['memory_current']['mean_bytes']
                assert abs(total['mean_bytes'] - reference) < 0.01
                result = gauge(samples, start, end, overhead, 2_000_000_000)
                row[phase + '_mean_MiB'] = result['mean_bytes'] / 2**20
                row[phase + '_peak_MiB'] = result['sampled_peak_bytes'] / 2**20
                row[phase + '_GiB_s'] = result['trapezoid_byte_seconds'] / 2**30
            rows.append(row)
    assert len(rows) == 21
    with (OUT / 'trials.csv').open('w') as stream:
        writer = csv.DictWriter(stream, fieldnames=rows[0]); writer.writeheader(); writer.writerows(rows)
    arms = ('original', 'disk', 'reclaim', 'ext4-block', 'pmem', 'ext4-reclaim', 'combined')
    means = {arm: statistics.mean(r['common_idle_mean_MiB'] for r in rows if r['arm'] == arm) for arm in arms}
    text = '''# Step 4 memory overhead excluding host filesystem page cache

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
'''
    for arm in arms: text += f'| {arm} | {means[arm]:.2f} |\n'
    text += '\n| Target − baseline | Mean change | Paired range |\n| --- | ---: | ---: |\n'
    for baseline, target in [('ext4-block','pmem'),('disk','reclaim'),('pmem','combined'),('disk','combined'),('original','combined')]:
        differences = [next(r['common_idle_mean_MiB'] for r in rows if r['arm']==target and r['block']==b) - next(r['common_idle_mean_MiB'] for r in rows if r['arm']==baseline and r['block']==b) for b in range(3)]
        text += f'| {target} − {baseline} | {statistics.mean(differences):+.2f} | [{min(differences):+.2f}, {max(differences):+.2f}] |\n'
    text += '''
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
'''
    (OUT / 'REPORT.md').write_text(text)
    print(text)

if __name__ == '__main__':
    main()
