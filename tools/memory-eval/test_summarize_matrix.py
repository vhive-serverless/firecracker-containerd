# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
import contextlib
import copy
import csv
import io
import json
import os
from pathlib import Path
import tempfile
import unittest

import summarize_matrix as analysis

NS = analysis.NS


def save(path, value):
    path.write_text(json.dumps(value))


def save_lines(path, values):
    path.write_text(''.join(json.dumps(value) + '\n' for value in values))


def host_row(t):
    metrics = {'memory.current': 100 + 2 * t,
               'cpu.stat': {'usage_usec': 100 * t, 'user_usec': 60 * t, 'system_usec': 40 * t},
               'memory.stat': {'anon': 20 + t, 'file': 10 + t, 'kernel': 8, 'shmem': 0},
               'io.stat': {'8:0': {'rbytes': 100 + 3 * t, 'wbytes': 200 + 5 * t},
                           '7:1': {'rbytes': 100 + 7 * t, 'wbytes': 200 + 11 * t}}}
    return {'type': 'sample', 'sequence': t, 'monotonic_ns': t * NS,
            'read_started_monotonic_ns': t * NS - 100, 'read_finished_monotonic_ns': t * NS,
            'cgroup': {'status': 'ok', 'path': '/owned', 'expected_device_inode': [28, 45],
                       'metrics': {key: {'status': 'ok', 'data': value} for key, value in metrics.items()}},
            'host_meminfo': {'status': 'ok', 'data': {
                'values': {'MemTotal': 100000, 'MemAvailable': 1000 - t, 'Cached': 500 + t,
                           'AnonPages': 300 + 2 * t, 'MemFree': 1000 - t},
                'units': {key: 'kB' for key in ('MemTotal', 'MemAvailable', 'Cached', 'AnonPages', 'MemFree')}}}}


def vm_row(t):
    return {'type': 'sample', 'sequence': t - 6, 'monotonic_ns': t * NS,
            'processes': [{'pid': pid, 'status': 'ok', 'identity_verified': True,
                           'expected_starttime_ticks': pid + 100, 'observed_starttime_ticks': pid + 100,
                           'stat': {'starttime': pid + 100}, 'stat_after': {'starttime': pid + 100},
                           'smaps_rollup': {'status': 'ok', 'data': {'values': {'Pss': pss, 'Rss': 99999},
                                                                 'units': {'Pss': 'kB', 'Rss': 'kB'}}}}
                          for pid, pss in ((11, 100), (12, 200))]}


def sampler_file(path, rows, pids, cgroup=None):
    metadata = {'type': 'metadata', 'schema_version': 1, 'pids': pids, 'cgroup': cgroup,
                'boot_id': {'status': 'ok', 'data': 'same-boot'}, 'interval_s': 1}
    save_lines(path, [metadata, *rows, {'type': 'end', 'samples': len(rows)}])


def fixture(root, source='1' * 40):
    root.mkdir(parents=True)
    info = {'source_revision': source, 'source_dirty': False, 'source_digest': 'a' * 64}
    build = {'build_info': info, 'binary_sha256': 'b' * 64,
             'receipt': {'build_info': info, 'binary_sha256': 'b' * 64}}
    manifest = {'source_revision': source, 'runtime_revision': source, 'runtime_version': 'runtime ' + source,
                'source_dirty': False, 'source_diff': '', 'committed_acceptance': True,
                'evaluation_build': build, 'test_binary_sha256': 'b' * 64, 'sampler_sha256': 'c' * 64,
                'runtime_image': 'sha256:' + 'd' * 64, 'builder_image': 'sha256:' + 'e' * 64,
                'backing_device': {'filesystem_major_minor': '8:3', 'physical_major_minor': '8:0'},
                'images': {'squash': {'sha256': 'f' * 64}, 'ext4': {'sha256': '9' * 64}},
                'arguments': {'arms': 'original', 'repeats': 1}}
    save(root / 'manifest.json', manifest)
    save(root / 'matrix-result.json', {'status': 'pass', 'trials': 1, 'committed_acceptance': True})
    trial = root / 'b00-00-original'
    trial.mkdir()
    save(trial / 'trial.json', {'status': 'pass', 'exit_code': 0, 'container_exit_code': 0,
                                'committed_acceptance': True, 'arm': 'original', 'block': 0, 'ordinal': 0,
                                'image_sha256': 'f' * 64, 'cgroup': '/owned', 'vmm_pids': [11, 12],
                                'pool_backing': {'data': {'allocated_bytes': 64, 'logical_bytes': 1000}}})
    save(trial / 'vmm-pids.json', [11, 12])
    save(trial / 'disk-allocation.json', {'/cache/image.img': {'allocated_bytes': 100, 'logical_bytes': 120}})
    phases = [{'phase': 'start', 'monotonic_ns': 2 * NS, 'vms': 2,
               'arm': {'Name': 'original', 'Upper': 'ram', 'Ext4': False, 'Pmem': False, 'Reclaim': False}},
              {'phase': 'input_ready', 'monotonic_ns': 3 * NS, 'probe_sha256': '8' * 64},
              {'phase': 'vm_ready', 'monotonic_ns': 4 * NS, 'vm': 0, 'create_ns': 20},
              {'phase': 'vm_ready', 'monotonic_ns': 5 * NS, 'vm': 1, 'create_ns': 30},
              {'phase': 'payloads_created', 'monotonic_ns': 7 * NS},
              {'phase': 'vm_workload_ready', 'monotonic_ns': 8 * NS, 'vm': 0, 'duration_ns': 40},
              {'phase': 'vm_workload_ready', 'monotonic_ns': 9 * NS, 'vm': 1, 'duration_ns': 50},
              {'phase': 'workload_ready', 'monotonic_ns': 9 * NS + 1},
              {'phase': 'probes_complete', 'monotonic_ns': 22 * NS}]
    for index in range(2):
        phases.extend([{'phase': 'workload_readback', 'monotonic_ns': (23 + index) * NS,
                        'vm': index, 'duration_ns': 60 + index},
                       {'phase': 'reporting', 'monotonic_ns': (23 + index) * NS + 1,
                        'vm': index, 'descriptors': 0, 'traffic_bytes': 0, 'failures': 0,
                        'reclamation_enabled': False}])
    phases.append({'phase': 'workload_complete', 'monotonic_ns': 30 * NS, 'correctness': True})
    save_lines(trial / 'phases.jsonl', phases)
    for index in range(2):
        times = [7 + index, 8 + index, 9 + index, 10 + index, 20 + index,
                 20.1 + index, 20.2 + index, 20.3 + index]
        records = [{'statx_dax_supported': True, 'statx_dax': False}]
        for i, (name, time) in enumerate(zip(analysis.PROBE_PHASES, times)):
            records.append({'phase': name, 'received_host_monotonic_ns': int(time * NS),
                            'elapsed_ns': int((time - times[0]) * NS),
                            'phase_duration_ns': int((time - times[i - 1]) * NS) if i else 0,
                            'verified': True, 'sha256': '7' * 64,
                            'rusage_total': {key: 10 + i for key in analysis.RUSAGE},
                            'meminfo': {'AnonPages': '100 kB', 'Cached': '200 kB', 'MemTotal': '512000 kB'}})
        save(trial / f'vm{index}-probe.json', records)
    sampler_file(trial / 'host.jsonl', [host_row(t) for t in range(33)], [9], '/owned')
    sampler_file(trial / 'vm-processes.jsonl', [vm_row(t) for t in range(6, 33)], [11, 12])
    return trial


class InterpolationTests(unittest.TestCase):
    def setUp(self):
        self.rows = [{'monotonic_ns': t * NS, 'value': 10 * t} for t in (0, 2, 4)]

    def test_exact_trapezoid_and_interpolated_edges(self):
        result = analysis.gauge(self.rows, NS, 3 * NS, lambda r: r['value'], 2 * NS)
        self.assertEqual(result['start_bytes'], 10)
        self.assertEqual(result['end_bytes'], 30)
        self.assertEqual(result['trapezoid_byte_seconds'], 40)
        self.assertEqual(result['mean_bytes'], 20)
        self.assertEqual(result['sampled_peak_bytes'], 30)

    def test_no_extrapolation_or_gap_interpolation(self):
        for start, end, gap in [(-NS, 3 * NS, 2 * NS), (NS, 5 * NS, 2 * NS), (NS, 3 * NS, NS)]:
            with self.subTest(start=start, end=end), self.assertRaises(analysis.AnalysisError):
                analysis.gauge(self.rows, start, end, lambda r: r['value'], gap)

    def test_missing_interior_value_is_not_filtered_or_zero_filled(self):
        self.rows[1]['value'] = None
        with self.assertRaises(analysis.AnalysisError):
            analysis.gauge(self.rows, NS, 3 * NS, lambda r: r['value'], 2 * NS)

    def test_counter_reset_rejected(self):
        for row, value in zip(self.rows, (100, 80, 120)):
            row['value'] = value
        with self.assertRaises(analysis.AnalysisError):
            analysis.counter(self.rows, 0, 4 * NS, lambda r: r['value'], 2 * NS)

    def test_vm_identity_cannot_change_between_valid_rows(self):
        rows = [vm_row(t) for t in (12, 13, 14)]
        for row in rows[1:]:
            process = row['processes'][0]
            process['expected_starttime_ticks'] += 1
            process['observed_starttime_ticks'] += 1
            process['stat']['starttime'] += 1
            process['stat_after']['starttime'] += 1
        with self.assertRaisesRegex(analysis.AnalysisError, 'identity changed'):
            analysis.vm_window(rows, 12 * NS, 14 * NS, [11, 12], 2 * NS)


class IOTests(unittest.TestCase):
    def test_layers_not_summed_and_new_layer_not_zero_filled(self):
        rows = [host_row(t) for t in range(4)]
        del rows[0]['cgroup']['metrics']['io.stat']['data']['7:1']
        result, details = analysis.cgroup_window(rows, 0, 3 * NS, 2 * NS, '8:0')
        self.assertEqual(result['physical_io_delta']['rbytes'], 9)
        self.assertEqual(result['physical_io_delta']['wbytes'], 15)
        self.assertEqual(details['io_stat_layers_not_summed']['7:1']['status'], 'unavailable')
        self.assertNotIn('deltas', details['io_stat_layers_not_summed']['7:1'])
        with self.assertRaises(analysis.AnalysisError):
            analysis.cgroup_window(rows, 0, 3 * NS, 2 * NS, '7:1')

    def test_recorded_backing_precedes_current_analyzer_filesystem(self):
        result = analysis.backing_device(Path('/does-not-exist'), {'backing_device': {
            'filesystem_major_minor': '8:3', 'physical_major_minor': '8:0'}})
        self.assertEqual(result['physical_major_minor'], '8:0')
        self.assertEqual(result['selection'], 'recorded manifest')

    def test_partition_ascends_to_parent(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            disk, sysfs = root / 'devices/sda', root / 'sys'
            (disk / 'sda3').mkdir(parents=True)
            sysfs.mkdir()
            (disk / 'dev').write_text('8:0\n')
            (disk / 'sda3/partition').write_text('3\n')
            value = root.stat().st_dev
            (sysfs / f'{os.major(value)}:{os.minor(value)}').symlink_to(disk / 'sda3')
            result = analysis.backing_device(root, {}, sys_root=sysfs)
            self.assertEqual(result['physical_major_minor'], '8:0')


class MatrixTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name) / 'matrix'
        self.trial = fixture(self.root)

    def analyze(self, **kwargs):
        return analysis.analyze([self.root], idle_offset=1, idle_window=2, **kwargs)

    def test_full_and_common_idle_are_distinct_with_exact_metrics(self):
        result = self.analyze()
        trial = result['trials'][0]
        full = trial['metrics']['full_trial']['dedicated_cgroup']
        idle = trial['metrics']['common_idle']['dedicated_cgroup']
        self.assertEqual(full['memory_current']['trapezoid_byte_seconds'], 3696)
        self.assertEqual(idle['memory_current']['trapezoid_byte_seconds'], 252)
        self.assertEqual(full['cpu_delta_usec']['usage_usec'], 2800)
        self.assertEqual(idle['physical_io_delta']['rbytes'], 6)
        self.assertEqual(trial['metrics']['common_idle']['vm_aggregate_pss_mapped_view']['mean_bytes'], 300 * 1024)
        self.assertNotIn('vm_aggregate_pss_mapped_view', trial['metrics']['full_trial'])
        self.assertIn('full_trial.vm_aggregate_pss_mapped_view', trial['details']['unavailable'])
        self.assertEqual(trial['details']['windows']['common_idle']['start_host_monotonic_ns'], 12 * NS)
        self.assertEqual(trial['metrics']['guest']['vm0']['memory_bytes']['idle_start']['AnonPages'], 102400)
        self.assertEqual(trial['metrics']['latency_ns']['vm_ready']['sum'], 50)
        self.assertEqual(trial['metrics']['latency_ns']['vm_workload_ready']['sum'], 90)
        self.assertEqual(trial['metrics']['latency_ns']['workload_readback']['sum'], 121)
        output = Path(self.temporary.name) / 'summary'
        analysis.write_outputs(output, result)
        self.assertEqual(len(list(output.iterdir())), 7)
        with (output / 'trials.csv').open() as stream:
            rows = list(csv.DictReader(stream))
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]['label'], 'COMMITTED OBSERVATIONS')
        self.assertEqual(rows[0]['physical_device'], '8:0')
        self.assertIn('full_trial.vm_aggregate_pss_mapped_view', json.loads(rows[0]['unavailable_json']))
        self.assertEqual(int(rows[0]['idle_start_host_ns']), 12 * NS)
        self.assertEqual(float(rows[0]['common_idle.dedicated_cgroup.memory_current.trapezoid_byte_seconds']), 252)

    def test_default_window_and_equal_guest_end_are_rejected(self):
        with self.assertRaisesRegex(analysis.AnalysisError, 'idle window'):
            analysis.analyze([self.root])
        with self.assertRaisesRegex(analysis.AnalysisError, 'idle window'):
            analysis.analyze([self.root], idle_offset=1, idle_window=8)

    def test_dirty_or_legacy_requires_explicit_development_label(self):
        manifest = analysis.load(self.root / 'manifest.json')
        manifest['source_dirty'] = True
        manifest.pop('evaluation_build')
        save(self.root / 'manifest.json', manifest)
        with self.assertRaisesRegex(analysis.AnalysisError, 'provenance rejected'):
            self.analyze()
        result = self.analyze(allow_development=True)
        self.assertEqual(result['label'], 'DEVELOPMENT ONLY')
        self.assertTrue(result['manifests'][0]['development_issues'])

    def test_failed_trial_never_accepted_even_in_development(self):
        value = analysis.load(self.trial / 'trial.json')
        value['status'] = 'fail'
        save(self.trial / 'trial.json', value)
        with self.assertRaises(analysis.AnalysisError):
            self.analyze(allow_development=True)

    def test_manifest_build_receipt_mismatch_rejected(self):
        manifest = analysis.load(self.root / 'manifest.json')
        manifest['evaluation_build']['receipt']['binary_sha256'] = '0' * 64
        save(self.root / 'manifest.json', manifest)
        with self.assertRaisesRegex(analysis.AnalysisError, 'digest/receipt mismatch'):
            self.analyze()

    def test_missing_or_permission_denied_sample_rejected(self):
        path = self.trial / 'host.jsonl'
        rows = analysis.lines(path)
        rows[14]['cgroup']['metrics']['memory.current'] = {'status': 'permission_denied'}
        save_lines(path, rows)
        with self.assertRaisesRegex(analysis.AnalysisError, 'memory.current unavailable'):
            self.analyze()

    def test_missing_sample_sequence_rejected(self):
        path = self.trial / 'host.jsonl'
        rows = analysis.lines(path)
        del rows[14]
        rows[-1]['samples'] -= 1
        save_lines(path, rows)
        with self.assertRaisesRegex(analysis.AnalysisError, 'missing sample sequence'):
            self.analyze()

    def test_idle_vm_pss_gap_rejected(self):
        path = self.trial / 'vm-processes.jsonl'
        rows = analysis.lines(path)
        del rows[8]
        for i, record in enumerate(rows[1:-1]):
            record['sequence'] = i
        rows[-1]['samples'] -= 1
        save_lines(path, rows)
        with self.assertRaisesRegex(analysis.AnalysisError, 'sample gap'):
            self.analyze()

    def test_changed_cgroup_inode_rejected(self):
        path = self.trial / 'host.jsonl'
        rows = analysis.lines(path)
        rows[14]['cgroup']['expected_device_inode'] = [28, 46]
        save_lines(path, rows)
        with self.assertRaisesRegex(analysis.AnalysisError, 'cgroup identity changed'):
            self.analyze()

    def test_mixed_sources_rejected_or_kept_in_separate_development_groups(self):
        other = Path(self.temporary.name) / 'other'
        fixture(other, source='2' * 40)
        with self.assertRaisesRegex(analysis.AnalysisError, 'mixed matrix provenance'):
            analysis.analyze([self.root, other], idle_offset=1, idle_window=2)
        result = analysis.analyze([self.root, other], idle_offset=1, idle_window=2, allow_development=True)
        self.assertEqual(len(result['arms']), 2)
        self.assertTrue(all(arm['trials'] == 1 for arm in result['arms']))

    def test_cli_rejection_creates_no_output(self):
        output = Path(self.temporary.name) / 'rejected'
        with contextlib.redirect_stderr(io.StringIO()):
            code = analysis.main(['--input', str(self.root), '--output', str(output)])
        self.assertEqual(code, 2)
        self.assertFalse(output.exists())


class PairTests(unittest.TestCase):
    def test_pairs_are_within_block_and_pmem_uses_ext4_controls(self):
        trials = []
        levels = {'original': 100, 'disk': 80, 'reclaim': 75, 'ext4-block': 70,
                  'pmem': 60, 'ext4-reclaim': 65, 'combined': 55}
        for block in (0, 1):
            for arm, value in levels.items():
                if block == 1 and arm == 'combined':
                    continue
                trials.append({'matrix': 'matrix', 'block': block, 'arm': arm, 'provenance_id': 'same',
                               'metrics': {'common_idle': {'dedicated_cgroup': {'peak_bytes': value + 100 * block}}}})
        result = analysis.paired(trials)
        self.assertEqual(len(result['blocks']), 7)
        self.assertFalse(any(row['baseline'] == 'disk' and row['target'] == 'pmem' for row in result['blocks']))
        pmem = next(row for row in result['summaries'] if row['baseline'] == 'ext4-block')
        self.assertEqual(pmem['target_minus_baseline']['common_idle.dedicated_cgroup.peak_bytes'],
                         {'count': 2, 'median': -10, 'min': -10, 'max': -10})
        combined = next(row for row in result['summaries'] if row['target'] == 'combined')
        self.assertEqual(combined['target_minus_baseline']['common_idle.dedicated_cgroup.peak_bytes']['count'], 1)
        self.assertEqual(len(result['unpaired']), 1)

    def test_missing_metrics_do_not_become_zeros_in_arm_statistics(self):
        trials = [{'arm': 'disk', 'provenance_id': 'same', 'metrics': {'measured': 10}},
                  {'arm': 'disk', 'provenance_id': 'same', 'metrics': {}}]
        result = analysis.aggregate(trials)[0]
        self.assertEqual(result['trials'], 2)
        self.assertEqual(result['metrics']['measured'], {'count': 1, 'median': 10, 'min': 10, 'max': 10})


if __name__ == '__main__':
    unittest.main()
