#!/usr/bin/env python3
# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
"""Offline matrix analysis. No cache drops, process reads, or workload execution.

Example: summarize_matrix.py --input /recorded/matrix --output /new/summary
Legacy smoke only: --allow-development --idle-offset 2 --idle-window 5

Memory-time is the trapezoidal integral of bracketed memory.current samples.
Full-trial start->workload_complete and a fixed common guest-idle window remain
separate. PSS is a mapped-memory view; host meminfo includes unrelated workloads.
I/O device layers are never added. Differences are target-minus-baseline within
a randomized block, with ext4 controls required for pmem comparisons.
"""
import argparse
from bisect import bisect_left, bisect_right
import csv
import hashlib
import json
import math
import os
from pathlib import Path
import re
import statistics
import sys

NS = 1_000_000_000
ARMS = ('original', 'disk', 'reclaim', 'pmem', 'combined', 'ext4-block', 'ext4-reclaim')
EXT4 = {'pmem', 'combined', 'ext4-block', 'ext4-reclaim'}
PMEM = {'pmem', 'combined'}
RECLAIM = {'reclaim', 'combined', 'ext4-reclaim'}
PAIRS = (('original', 'disk', 'writable upper placement; SquashFS unchanged'),
         ('disk', 'reclaim', 'reclamation; SquashFS unchanged'),
         ('ext4-block', 'pmem', 'pmem mapping; ext4 unchanged'),
         ('ext4-reclaim', 'combined', 'pmem mapping with reclamation; ext4 unchanged'))
PROBE_PHASES = ('stat', 'file_touch', 'anon_fill', 'idle_start', 'idle_end',
                'file_retouch', 'anon_verify', 'complete')
RUSAGE = ('user_cpu_us', 'system_cpu_us', 'minor_faults', 'major_faults')


class AnalysisError(ValueError):
    pass


def check(condition, message):
    if not condition:
        raise AnalysisError(message)


def number(value, context):
    check(isinstance(value, (int, float)) and not isinstance(value, bool) and
          math.isfinite(value) and value >= 0, f'{context}: missing/invalid nonnegative number')
    return value


def load(path):
    try:
        return json.loads(path.read_text())
    except (OSError, ValueError) as exc:
        raise AnalysisError(f'{path}: {exc}') from exc


def lines(path):
    try:
        return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
    except (OSError, ValueError) as exc:
        raise AnalysisError(f'{path}: invalid JSONL: {exc}') from exc


def sha(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def digest(value):
    return isinstance(value, str) and re.fullmatch(r'[0-9a-f]{64}', value) is not None


def provenance(manifest, result, allow_development):
    """Validate recorded identities without executing the measured test binary."""
    issues = []
    def expect(condition, reason):
        if not condition:
            issues.append(reason)
    source = manifest.get('source_revision')
    expect(isinstance(source, str) and re.fullmatch(r'[0-9a-f]{40}', source), 'invalid source revision')
    expect(manifest.get('source_dirty') is False and not manifest.get('source_diff'), 'dirty/unknown source')
    expect(manifest.get('committed_acceptance') is True, 'manifest not committed acceptance')
    expect(result.get('committed_acceptance') is True, 'matrix not committed acceptance')
    expect(manifest.get('runtime_revision') == source, 'mixed source/runtime revisions')
    expect(str(manifest.get('runtime_version', '')).endswith(' ' + str(source)), 'runtime version mismatch')
    build = manifest.get('evaluation_build', {})
    info, receipt = build.get('build_info', {}), build.get('receipt', {})
    expect(info.get('source_revision') == source and info.get('source_dirty') is False,
           'evaluation binary source not exact/clean')
    expect(digest(info.get('source_digest')), 'missing evaluation source digest')
    expect(digest(manifest.get('test_binary_sha256')) and
           build.get('binary_sha256') == receipt.get('binary_sha256') == manifest.get('test_binary_sha256'),
           'evaluation binary digest/receipt mismatch')
    expect(bool(info) and receipt.get('build_info') == info, 'evaluation build-info receipt mismatch')
    expect(digest(manifest.get('sampler_sha256')), 'missing sampler digest')
    for key in ('runtime_image', 'builder_image'):
        expect(digest(str(manifest.get(key, '')).removeprefix('sha256:')), f'missing {key} digest')
    if issues and not allow_development:
        raise AnalysisError('provenance rejected: ' + '; '.join(issues))
    return issues


def sampler(path):
    records = lines(path)
    metadata = [r for r in records if r.get('type') == 'metadata']
    end = [r for r in records if r.get('type') == 'end']
    samples = [r for r in records if r.get('type') == 'sample']
    check(len(metadata) == len(end) == 1 and metadata[0].get('schema_version') == 1,
          f'{path}: incomplete/unsupported sampler stream')
    check(len(samples) >= 2 and end[0].get('samples') == len(samples), f'{path}: sample count mismatch')
    check([r.get('sequence') for r in samples] == list(range(len(samples))), f'{path}: missing sample sequence')
    times = [r.get('monotonic_ns') for r in samples]
    check(all(isinstance(t, int) for t in times) and all(a < b for a, b in zip(times, times[1:])),
          f'{path}: non-increasing sample clock')
    return metadata[0], samples


def clip(samples, start, end, getter, max_gap_ns):
    """Require both edge brackets and every intervening observation; no extrapolation."""
    check(start < end, 'window must have positive duration')
    times = [r['monotonic_ns'] for r in samples]
    left, right = bisect_right(times, start) - 1, bisect_left(times, end)
    check(left >= 0 and right < len(times), 'window lacks bracketing samples')
    segment = samples[left:right + 1]
    gaps = [b['monotonic_ns'] - a['monotonic_ns'] for a, b in zip(segment, segment[1:])]
    check(not gaps or max(gaps) <= max_gap_ns, f'sample gap exceeds {max_gap_ns / NS:g}s')
    values = [(r['monotonic_ns'], number(getter(r), 'sample value')) for r in segment]
    def interpolate(at):
        position = bisect_left([p[0] for p in values], at)
        if position < len(values) and values[position][0] == at:
            return values[position][1]
        a, b = values[position - 1], values[position]
        return a[1] + (b[1] - a[1]) * (at - a[0]) / (b[0] - a[0])
    points = [(start, interpolate(start))]
    points.extend((t, value) for t, value in values if start < t < end)
    points.append((end, interpolate(end)))
    return points, segment, max(gaps, default=0)


def gauge(samples, start, end, getter, max_gap_ns):
    points, segment, gap = clip(samples, start, end, getter, max_gap_ns)
    integral = sum((b[0] - a[0]) / NS * (a[1] + b[1]) / 2 for a, b in zip(points, points[1:]))
    return {'start_bytes': points[0][1], 'end_bytes': points[-1][1],
            'sampled_peak_bytes': max(value for _, value in points),
            'sampled_min_bytes': min(value for _, value in points),
            'trapezoid_byte_seconds': integral, 'mean_bytes': integral / ((end - start) / NS),
            'bracketing_samples': len(segment), 'max_sample_gap_ns': gap}


def counter(samples, start, end, getter, max_gap_ns):
    points, _, _ = clip(samples, start, end, getter, max_gap_ns)
    check(all(a[1] <= b[1] for a, b in zip(points, points[1:])), 'counter decreased/reset')
    return points[-1][1] - points[0][1]


def metric(row, key):
    group = row.get('cgroup', {})
    check(group.get('status') in ('ok', 'partial'), 'cgroup identity/read unavailable')
    value = group.get('metrics', {}).get(key, {})
    check(value.get('status') == 'ok', f'cgroup {key} unavailable: {value.get("status")}')
    return value['data']


def device_id(value):
    check(isinstance(value, str) and re.fullmatch(r'\d+:\d+', value), 'device must be MAJOR:MINOR')
    return value


def backing_device(directory, manifest, explicit=None, sys_root=Path('/sys/dev/block')):
    recorded = manifest.get('backing_device', {})
    if explicit:
        return {'physical_major_minor': device_id(explicit), 'selection': 'explicit CLI', 'recorded': recorded}
    if recorded.get('physical_major_minor'):
        return dict(recorded, physical_major_minor=device_id(recorded['physical_major_minor']),
                    selection='recorded manifest')
    # Legacy data only: current filesystem may differ if observations were moved.
    value = directory.stat().st_dev
    filesystem = f'{os.major(value)}:{os.minor(value)}'
    try:
        node = (sys_root / filesystem).resolve(strict=True)
        if (node / 'partition').exists():
            node = node.parent
        check(not (node / 'loop').exists() and not node.name.startswith('dm-') and
              not ((node / 'slaves').exists() and any((node / 'slaves').iterdir())),
              'ambiguous layered backing: supply --physical-device')
        physical = device_id((node / 'dev').read_text().strip())
    except OSError as exc:
        raise AnalysisError('cannot derive physical backing; supply --physical-device') from exc
    return {'filesystem_major_minor': filesystem, 'physical_major_minor': physical,
            'selection': 'current matrix filesystem; verify data was not moved'}


def cgroup_window(samples, start, end, gap, selected):
    points, segment, _ = clip(samples, start, end, lambda r: metric(r, 'memory.current'), gap)
    identities = [(r['cgroup'].get('path'), r['cgroup'].get('expected_device_inode')) for r in segment]
    check(all(item == identities[0] for item in identities) and all(identities[0]),
          'cgroup identity changed/missing')
    result = {'memory_current': gauge(samples, start, end, lambda r: metric(r, 'memory.current'), gap),
              'cpu_delta_usec': {key: counter(samples, start, end,
                   lambda r, key=key: metric(r, 'cpu.stat').get(key), gap)
                   for key in ('usage_usec', 'user_usec', 'system_usec')}}
    memory_endpoints = {}
    for label, point in (('start', points[0][0]), ('end', points[-1][0])):
        memory_endpoints[label] = {}
        for key in ('anon', 'file', 'kernel', 'shmem'):
            try:
                values, _, _ = clip(samples, start, end, lambda r, key=key: metric(r, 'memory.stat').get(key), gap)
                memory_endpoints[label][key] = values[0 if label == 'start' else -1][1]
            except AnalysisError as exc:
                memory_endpoints[label][key] = {'status': 'unavailable', 'reason': str(exc)}
    result['memory_stat_bytes'] = memory_endpoints
    devices = sorted({name for r in segment for name in metric(r, 'io.stat')})
    layers = {}
    for device in devices:
        observed = [metric(r, 'io.stat').get(device) for r in segment]
        keys = sorted({key for value in observed if value is not None for key in value})
        entry = {'observed_start': observed[0], 'observed_end': observed[-1]}
        try:
            entry['deltas'] = {key: counter(samples, start, end,
                lambda r, key=key, device=device: metric(r, 'io.stat').get(device, {}).get(key), gap) for key in keys}
            entry['status'] = 'ok'
        except AnalysisError as exc:
            entry.update(status='unavailable', reason=str(exc))
        layers[device] = entry
    check(selected in layers and layers[selected]['status'] == 'ok',
          f'selected physical io.stat device {selected} lacks complete counter brackets')
    check({'rbytes', 'wbytes'} <= layers[selected]['deltas'].keys(), 'selected physical I/O byte counters missing')
    result['physical_io_delta'] = layers[selected]['deltas']
    return result, {'io_stat_layers_not_summed': layers,
                    'max_read_duration_ns': max(r['read_finished_monotonic_ns'] - r['read_started_monotonic_ns'] for r in segment)}


def vm_pss(row, pids):
    processes = row.get('processes', [])
    check(len(processes) == len(pids) and {p.get('pid') for p in processes} == set(pids), 'VMM PID set changed')
    total = 0
    for process in processes:
        check(process.get('identity_verified') is True and process.get('status') in ('ok', 'partial'), 'VMM identity unavailable')
        expected = process.get('expected_starttime_ticks')
        check(expected is not None and process.get('observed_starttime_ticks') == expected and
              process.get('stat', {}).get('starttime') == process.get('stat_after', {}).get('starttime') == expected,
              'VMM PID reused/unverified')
        rollup = process.get('smaps_rollup', {})
        check(rollup.get('status') == 'ok' and rollup.get('data', {}).get('units', {}).get('Pss') == 'kB', 'PSS missing/unknown units')
        total += number(rollup['data']['values'].get('Pss'), 'VMM PSS') * 1024
    return total


def vm_window(samples, start, end, pids, gap):
    _, segment, _ = clip(samples, start, end, lambda r: vm_pss(r, pids), gap)
    identities = [{p['pid']: p['expected_starttime_ticks'] for p in row['processes']} for row in segment]
    check(all(identity == identities[0] for identity in identities), 'VMM process identity changed across samples')
    return gauge(samples, start, end, lambda r: vm_pss(r, pids), gap)


def guest_mem(record):
    output = {}
    values = record.get('meminfo', {})
    for key in ('AnonPages', 'Cached', 'MemTotal', 'Shmem', 'MemFree', 'MemAvailable', 'Buffers', 'Dirty'):
        if key not in values and key not in ('AnonPages', 'Cached', 'MemTotal'):
            continue
        value = values.get(key, '')
        check(isinstance(value, str) and re.fullmatch(r'\d+ kB', value), f'guest {key} missing/unknown units')
        output[key] = int(value.split()[0]) * 1024
    return output


def probe_records(path, arm):
    records = load(path)
    check(isinstance(records, list), f'{path}: expected probe array')
    phases = {}
    for record in records:
        if record.get('phase'):
            check(record['phase'] not in phases, f'{path}: duplicate probe phase')
            phases[record['phase']] = record
    check(set(phases) == set(PROBE_PHASES), f'{path}: incomplete probe phases')
    check(all(phases[a]['received_host_monotonic_ns'] < phases[b]['received_host_monotonic_ns']
              for a, b in zip(PROBE_PHASES, PROBE_PHASES[1:])), f'{path}: probe clock/order invalid')
    check(all(phases[a].get('elapsed_ns', -1) <= phases[b].get('elapsed_ns', -1)
              for a, b in zip(PROBE_PHASES, PROBE_PHASES[1:])), f'{path}: guest elapsed clock decreased')
    for phase in ('file_retouch', 'anon_verify', 'complete'):
        check(phases[phase].get('verified') is True, f'{path}: probe verification failed')
    check(digest(phases['file_touch'].get('sha256')) and
          phases['file_touch']['sha256'] == phases['file_retouch'].get('sha256'), f'{path}: readback checksum mismatch')
    dax = [r for r in records if 'statx_dax' in r]
    check(len(dax) == 1 and dax[0].get('statx_dax_supported') is True and
          dax[0].get('statx_dax') == (arm in PMEM), f'{path}: DAX support/value inconsistent with arm')
    return phases, dax[0]


def guest_summary(phases):
    summary = {'phase_timings': {}, 'memory_bytes': {}, 'rusage_deltas': {}}
    details = {'phase_host_received_ns': {}, 'damon_idle_phase_endpoints': {}}
    for phase, record in phases.items():
        summary['phase_timings'][phase] = {key: number(record.get(key), key)
                                          for key in ('elapsed_ns', 'phase_duration_ns')}
        details['phase_host_received_ns'][phase] = record['received_host_monotonic_ns']
        check(not record.get('sample_errors'), 'guest probe sampling errors')
    for phase in ('stat', 'idle_start', 'idle_end', 'complete'):
        summary['memory_bytes'][phase] = guest_mem(phases[phase])
    for label, before, after in (('whole_probe', 'stat', 'complete'), ('whole_guest_idle_phase', 'idle_start', 'idle_end')):
        deltas = {}
        for key in RUSAGE:
            begin = number(phases[before].get('rusage_total', {}).get(key), key)
            finish = number(phases[after].get('rusage_total', {}).get(key), key)
            check(finish >= begin, 'guest rusage counter decreased')
            deltas[key] = finish - begin
        summary['rusage_deltas'][label] = deltas
    for phase in ('idle_start', 'idle_end'):
        details['damon_idle_phase_endpoints'][phase] = phases[phase].get('damon_parameters', {})
    return summary, details


def phase_one(phases, name):
    matches = [r for r in phases if r.get('phase') == name]
    check(len(matches) == 1, f'exactly one host phase {name} required')
    return matches[0]


def trial_summary(directory, manifest, physical, offset_ns, window_ns, gap, allow_development):
    trial = load(directory / 'trial.json')
    check(trial.get('status') == 'pass' and trial.get('exit_code') == trial.get('container_exit_code') == 0 and
          not trial.get('cleanup_errors'), f'{directory}: trial did not pass cleanly')
    issues = []
    if trial.get('committed_acceptance') is not True:
        check(allow_development, f'{directory}: trial is development/legacy')
        issues.append('trial not committed acceptance')
    arm = trial.get('arm')
    check(arm in ARMS, 'unknown arm')
    check(all(isinstance(trial.get(key), int) and not isinstance(trial.get(key), bool) and trial[key] >= 0
              for key in ('block', 'ordinal')), 'invalid trial block/ordinal')
    image_kind = 'ext4' if arm in EXT4 else 'squash'
    image = manifest.get('images', {}).get(image_kind, {})
    check(digest(image.get('sha256')) and image['sha256'] == trial.get('image_sha256'), 'trial image differs from manifest')
    phases = lines(directory / 'phases.jsonl')
    times = [r.get('monotonic_ns') for r in phases]
    check(all(isinstance(t, int) for t in times) and all(a < b for a, b in zip(times, times[1:])), 'host phase clock/order invalid')
    beginning, completion = phase_one(phases, 'start'), phase_one(phases, 'workload_complete')
    check(completion.get('correctness') is True and beginning.get('vms') == 2, 'workload correctness/VM count missing')
    config = beginning.get('arm', {})
    check(config.get('Name') == arm and config.get('Ext4') == (arm in EXT4) and
          config.get('Pmem') == (arm in PMEM) and config.get('Reclaim') == (arm in RECLAIM) and
          config.get('Upper') == ('ram' if arm == 'original' else 'disk'), 'observed arm configuration mismatch')
    probes = [probe_records(directory / f'vm{i}-probe.json', arm) for i in range(2)]
    common_start = max(p['idle_start']['received_host_monotonic_ns'] for p, _ in probes)
    idle_start, idle_end = common_start + offset_ns, common_start + offset_ns + window_ns
    check(idle_end < min(p['idle_end']['received_host_monotonic_ns'] for p, _ in probes),
          'common idle window does not end before both guest idle_end records')
    start, end = beginning['monotonic_ns'], completion['monotonic_ns']
    check(start < idle_start < idle_end < end, 'idle window is outside full trial')
    host_meta, host = sampler(directory / 'host.jsonl')
    vm_meta, vms = sampler(directory / 'vm-processes.jsonl')
    pids = trial.get('vmm_pids')
    check(isinstance(pids, list) and len(pids) == len(set(pids)) == 2 and vm_meta.get('pids') == pids and
          load(directory / 'vmm-pids.json') == pids, 'VMM PID metadata mismatch')
    check(host_meta.get('cgroup') == trial.get('cgroup') and host_meta.get('cgroup'), 'dedicated cgroup metadata mismatch')
    check(all(r.get('cgroup', {}).get('path') == trial['cgroup'] for r in host), 'cgroup path differs from trial')
    check(host_meta.get('boot_id', {}).get('status') == vm_meta.get('boot_id', {}).get('status') == 'ok' and
          host_meta['boot_id']['data'] == vm_meta['boot_id']['data'], 'samplers have different/unverified boot identity')
    selected = physical['physical_major_minor']
    metrics, details = {}, {'io_stat': {}, 'windows': {}, 'guest': {}, 'physical_device': physical}
    for label, a, b in (('full_trial', start, end), ('common_idle', idle_start, idle_end)):
        primary, extra = cgroup_window(host, a, b, gap, selected)
        metrics[label] = {'duration_ns': b - a, 'dedicated_cgroup': primary}
        details['io_stat'][label] = extra
        details['windows'][label] = {'start_host_monotonic_ns': a, 'end_host_monotonic_ns': b}
        host_memory = {}
        for key in ('MemTotal', 'MemAvailable', 'Cached', 'AnonPages', 'MemFree'):
            def host_value(row, key=key):
                value = row.get('host_meminfo', {})
                check(value.get('status') == 'ok' and value['data']['units'].get(key) == 'kB', 'host meminfo missing/units invalid')
                return number(value['data']['values'].get(key), key) * 1024
            host_memory[key] = gauge(host, a, b, host_value, gap)
        metrics[label]['host_meminfo_unrelated_workload_view'] = host_memory
        try:
            metrics[label]['vm_aggregate_pss_mapped_view'] = vm_window(vms, a, b, pids, gap)
        except AnalysisError as exc:
            if label == 'common_idle':
                raise AnalysisError(f'common idle PSS rejected: {exc}') from exc
            details.setdefault('unavailable', {})[label + '.vm_aggregate_pss_mapped_view'] = str(exc)
    metrics['guest'] = {}
    for i, (probe, dax) in enumerate(probes):
        metrics['guest'][f'vm{i}'], details['guest'][f'vm{i}'] = guest_summary(probe)
        details['guest'][f'vm{i}']['dax'] = dax
    metrics['latency_ns'], metrics['reporting'] = {}, {}
    for phase, field in (('vm_ready', 'create_ns'), ('workload_readback', 'duration_ns'), ('vm_workload_ready', 'duration_ns')):
        records = [r for r in phases if r.get('phase') == phase]
        if not records:
            continue
        check(len(records) == 2 and {r.get('vm') for r in records} == {0, 1}, f'{phase}: missing/duplicate VM timings')
        metrics['latency_ns'][phase] = {f'vm{r["vm"]}': number(r.get(field), field) for r in records}
        metrics['latency_ns'][phase]['sum'] = sum(metrics['latency_ns'][phase].values())
    check('vm_ready' in metrics['latency_ns'] and 'workload_readback' in metrics['latency_ns'], 'required VM latency records missing')
    reports = [r for r in phases if r.get('phase') == 'reporting']
    check(len(reports) == 2 and {r.get('vm') for r in reports} == {0, 1}, 'reporting records missing')
    for record in reports:
        check(record.get('failures') == 0 and record.get('reclamation_enabled') == (arm in RECLAIM), 'reporting failure/configuration mismatch')
        metrics['reporting'][f'vm{record["vm"]}'] = {key: number(record.get(key), key) for key in ('descriptors', 'traffic_bytes', 'failures')}
    details['disk_allocation'] = load(directory / 'disk-allocation.json') if (directory / 'disk-allocation.json').exists() else {'status': 'unavailable'}
    if 'status' not in details['disk_allocation']:
        metrics['disk_file_allocation'] = {}
        for path, allocation in details['disk_allocation'].items():
            role = 'guest_upper' if Path(path).name == 'guest-overlay.ext4' else 'immutable_lower_cache'
            group = metrics['disk_file_allocation'].setdefault(role, {'files': 0, 'sum_logical_bytes': 0, 'sum_st_blocks_bytes': 0})
            group['files'] += 1
            group['sum_logical_bytes'] += number(allocation.get('logical_bytes'), 'disk logical bytes')
            group['sum_st_blocks_bytes'] += number(allocation.get('allocated_bytes'), 'disk st_blocks bytes')
    metrics['pool_backing'] = trial.get('pool_backing', {})
    details['pool_status_raw'] = trial.get('pool_status')
    details['input_image'] = image
    metrics['input_image_file'] = {key: number(image[key], key) for key in ('logical_bytes', 'allocated_bytes') if key in image}
    details['host_phase_records'] = phases
    probe_digest = phase_one(phases, 'input_ready').get('probe_sha256')
    check(digest(probe_digest), 'missing probe binary digest')
    paths = ['trial.json', 'host.jsonl', 'vm-processes.jsonl', 'phases.jsonl', 'vm0-probe.json', 'vm1-probe.json', 'vmm-pids.json']
    if (directory / 'disk-allocation.json').exists():
        paths.append('disk-allocation.json')
    return {'trial': directory.name, 'arm': arm, 'block': trial['block'], 'ordinal': trial['ordinal'],
            'development_issues': issues, 'probe_sha256': probe_digest, 'metrics': metrics, 'details': details,
            'input_sha256': {name: sha(directory / name) for name in paths}}


def flatten(value, prefix=''):
    result = {}
    for key, item in value.items():
        name = f'{prefix}.{key}' if prefix else key
        if isinstance(item, dict):
            result.update(flatten(item, name))
        elif isinstance(item, (int, float)) and not isinstance(item, bool):
            result[name] = number(item, name)
    return result


def distribution(values):
    return {'count': len(values), 'median': statistics.median(values), 'min': min(values), 'max': max(values)}


def aggregate(trials):
    groups = {}
    for trial in trials:
        group = groups.setdefault((trial['provenance_id'], trial['arm']), [])
        group.append(flatten(trial['metrics']))
    return [{'provenance_id': provenance_id, 'arm': arm, 'trials': len(rows),
             'metrics': {metric: distribution([row[metric] for row in rows if metric in row])
                         for metric in sorted({key for row in rows for key in row})}}
            for (provenance_id, arm), rows in sorted(groups.items())]


def paired(trials):
    blocks = {}
    for trial in trials:
        group = blocks.setdefault((trial['matrix'], trial['block']), {})
        check(trial['arm'] not in group, 'duplicate arm within randomized block')
        group[trial['arm']] = trial
    rows, absent = [], []
    for (matrix, block), arms in sorted(blocks.items()):
        for baseline, target, interpretation in PAIRS:
            if baseline not in arms or target not in arms:
                absent.append({'matrix': matrix, 'block': block, 'baseline': baseline, 'target': target, 'status': 'unpaired'})
                continue
            a, b = arms[baseline], arms[target]
            check(a['provenance_id'] == b['provenance_id'], 'paired trials have mixed provenance')
            before, after = flatten(a['metrics']), flatten(b['metrics'])
            keys = [key for key in before.keys() & after.keys() if
                    ('.dedicated_cgroup.' in key or key.startswith('latency_ns.') or '.vm_aggregate_pss_mapped_view.' in key)
                    and not key.endswith(('bracketing_samples', 'max_sample_gap_ns'))]
            rows.append({'matrix': matrix, 'block': block, 'provenance_id': a['provenance_id'],
                         'baseline': baseline, 'target': target, 'interpretation': interpretation,
                         'target_minus_baseline': {key: after[key] - before[key] for key in sorted(keys)}})
    groups = {}
    for row in rows:
        group = groups.setdefault((row['provenance_id'], row['baseline'], row['target']), {})
        for key, value in row['target_minus_baseline'].items():
            group.setdefault(key, []).append(value)
    summaries = [{'provenance_id': identity, 'baseline': a, 'target': b,
                  'target_minus_baseline': {key: distribution(values) for key, values in group.items()}}
                 for (identity, a, b), group in sorted(groups.items())]
    return {'blocks': rows, 'summaries': summaries, 'unpaired': absent}


def analyze(inputs, idle_offset=5, idle_window=20, max_gap=1.5, allow_development=False, physical_device=None):
    check(all(math.isfinite(value) for value in (idle_offset, idle_window, max_gap)) and
          idle_offset >= 0 and idle_window > 0 and max_gap > 0, 'invalid window/gap arguments')
    trials, manifests, identities = [], [], set()
    for index, directory in enumerate(inputs):
        directory = directory.resolve()
        manifest, result = load(directory / 'manifest.json'), load(directory / 'matrix-result.json')
        check(result.get('status') == 'pass', 'matrix result did not pass')
        issues = provenance(manifest, result, allow_development)
        identity = {key: manifest.get(key) for key in ('source_revision', 'runtime_revision', 'runtime_image',
                    'builder_image', 'test_binary_sha256', 'sampler_sha256', 'evaluation_build')}
        identity['image_sha256'] = {key: value.get('sha256') for key, value in manifest.get('images', {}).items()}
        identity_id = hashlib.sha256(json.dumps(identity, sort_keys=True).encode()).hexdigest()
        identities.add(identity_id)
        device = backing_device(directory, manifest, physical_device)
        directories = sorted(path.parent for path in directory.glob('*/trial.json'))
        check(len(directories) == result.get('trials') and directories, 'matrix trial count mismatch')
        own = [trial_summary(path, manifest, device, int(idle_offset * NS), int(idle_window * NS),
                             int(max_gap * NS), allow_development) for path in directories]
        check(len({t['probe_sha256'] for t in own}) == 1, 'mixed probe binaries in matrix')
        arguments = manifest.get('arguments', {})
        if 'arms' in arguments and 'repeats' in arguments:
            expected = {(block, arm) for block in range(int(arguments['repeats'])) for arm in arguments['arms'].split(',')}
            check({(t['block'], t['arm']) for t in own} == expected and len(own) == len(expected), 'matrix arms/blocks incomplete')
        for trial in own:
            trial.update(matrix=str(directory), matrix_index=index, provenance_id=identity_id,
                         label='DEVELOPMENT ONLY' if allow_development else 'COMMITTED OBSERVATIONS')
        trials.extend(own)
        manifests.append({'matrix': str(directory), 'identity': identity, 'provenance_id': identity_id,
                          'development_issues': issues, 'physical_device': device,
                          'manifest_sha256': sha(directory / 'manifest.json'),
                          'result_sha256': sha(directory / 'matrix-result.json')})
    check(allow_development or len(identities) == 1, 'mixed matrix provenance rejected')
    check(len({t['probe_sha256'] for t in trials}) == 1, 'mixed probe bytes across matrices')
    return {'schema_version': 1, 'label': 'DEVELOPMENT ONLY' if allow_development else 'COMMITTED OBSERVATIONS',
            'configuration': {'idle_offset_s': idle_offset, 'idle_window_s': idle_window, 'max_gap_s': max_gap,
                              'boundary_estimate': 'linear interpolation; no extrapolation or zero fill'},
            'semantics': {'primary': 'dedicated cgroup memory.current; sampled peak and trapezoid byte-seconds',
                          'full_trial': 'host phase start through workload_complete, including startup/preparation',
                          'common_idle': 'max guest idle_start receive time + offset; fixed window before both idle_end',
                          'pss': 'sum of identity-verified VMM PSS only; mapped-memory view, no RSS sum',
                          'host_meminfo': 'whole host; includes unrelated workloads; never added to cgroup or PSS',
                          'io_stat': 'per-device layers never summed; physical device selected independently',
                          'guest_endpoints': 'whole guest phase endpoints; not interpolated into common idle window',
                          'disk_allocation': 'sum of per-file st_blocks by role; not unique physical extents',
                          'reporting': 'descriptor/traffic counters are not unique freed bytes or memory savings'},
            'manifests': manifests, 'trials': trials, 'arms': aggregate(trials), 'paired': paired(trials)}


def write_outputs(output, summary):
    output.mkdir(parents=True, exist_ok=False)
    def write(name, value):
        with (output / name).open('x') as stream:
            json.dump(value, stream, indent=2, allow_nan=False)
            stream.write('\n')
    common = {key: value for key, value in summary.items() if key not in ('trials', 'arms', 'paired')}
    write('summary.json', common)
    write('trials.json', {'label': summary['label'], 'trials': summary['trials']})
    write('arms.json', {'label': summary['label'], 'arms': summary['arms']})
    write('paired.json', {'label': summary['label'], **summary['paired']})
    flat = [flatten(t['metrics']) for t in summary['trials']]
    columns = sorted({key for row in flat for key in row})
    with (output / 'trials.csv').open('x', newline='') as stream:
        keys = ['label', 'matrix', 'trial', 'arm', 'block', 'ordinal', 'provenance_id', 'probe_sha256']
        annotations = ['physical_device', 'physical_device_selection', 'unavailable_json',
                       'unavailable_io_layers_json', 'development_issues_json',
                       'full_start_host_ns', 'full_end_host_ns', 'idle_start_host_ns', 'idle_end_host_ns']
        writer = csv.DictWriter(stream, fieldnames=keys + annotations + columns)
        writer.writeheader()
        for trial, values in zip(summary['trials'], flat):
            detail = trial['details']
            full, idle = detail['windows']['full_trial'], detail['windows']['common_idle']
            unavailable_io = {window: {device: layer.get('reason') for device, layer in data['io_stat_layers_not_summed'].items()
                                       if layer['status'] != 'ok'} for window, data in detail['io_stat'].items()}
            writer.writerow({**{key: trial[key] for key in keys}, **values,
                             'physical_device': detail['physical_device']['physical_major_minor'],
                             'physical_device_selection': detail['physical_device']['selection'],
                             'unavailable_json': json.dumps(detail.get('unavailable', {}), sort_keys=True),
                             'unavailable_io_layers_json': json.dumps(unavailable_io, sort_keys=True),
                             'development_issues_json': json.dumps(trial['development_issues']),
                             'full_start_host_ns': full['start_host_monotonic_ns'],
                             'full_end_host_ns': full['end_host_monotonic_ns'],
                             'idle_start_host_ns': idle['start_host_monotonic_ns'],
                             'idle_end_host_ns': idle['end_host_monotonic_ns']})
    with (output / 'arms.csv').open('x', newline='') as stream:
        writer = csv.writer(stream)
        writer.writerow(['label', 'provenance_id', 'arm', 'trials', 'metric', 'count', 'median', 'min', 'max'])
        for arm in summary['arms']:
            for key, value in arm['metrics'].items():
                writer.writerow([summary['label'], arm['provenance_id'], arm['arm'], arm['trials'], key,
                                 value['count'], value['median'], value['min'], value['max']])
    with (output / 'paired.csv').open('x', newline='') as stream:
        writer = csv.writer(stream)
        writer.writerow(['label', 'matrix', 'block', 'baseline', 'target', 'interpretation', 'metric', 'target_minus_baseline'])
        for row in summary['paired']['blocks']:
            for key, value in row['target_minus_baseline'].items():
                writer.writerow([summary['label'], row['matrix'], row['block'], row['baseline'], row['target'], row['interpretation'], key, value])


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--input', type=Path, action='append', required=True, help='recorded matrix directory; repeat only for compatible provenance')
    parser.add_argument('--output', type=Path, required=True, help='new analysis output directory')
    parser.add_argument('--idle-offset', type=float, default=5, help='seconds after latest guest idle_start receipt (default 5)')
    parser.add_argument('--idle-window', type=float, default=20, help='fixed common idle seconds (default 20)')
    parser.add_argument('--max-gap', type=float, default=1.5, help='maximum bracketing sample gap in seconds (default 1.5)')
    parser.add_argument('--physical-device', help='physical io.stat MAJOR:MINOR; overrides recorded backing device')
    parser.add_argument('--allow-development', action='store_true', help='smoke only; clearly labels dirty/legacy/mixed provenance')
    args = parser.parse_args(argv)
    try:
        check(not args.output.exists(), 'output already exists; refusing overwrite')
        summary = analyze(args.input, args.idle_offset, args.idle_window, args.max_gap,
                          args.allow_development, args.physical_device)
        write_outputs(args.output, summary)
        print(json.dumps({'label': summary['label'], 'trials': len(summary['trials']),
                          'arms': len(summary['arms']), 'paired_blocks': len(summary['paired']['blocks']),
                          'output': str(args.output)}))
        return 0
    except (AnalysisError, OSError, KeyError, TypeError, ValueError) as exc:
        print(json.dumps({'status': 'rejected', 'error': str(exc)}), file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
