#!/usr/bin/env python3
# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
"""Reproduce descriptive tables and figures from the frozen strict Step 4 summary."""
import argparse
from bisect import bisect_left, bisect_right
import csv
import hashlib
import json
import math
from pathlib import Path
import statistics

import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
from matplotlib.lines import Line2D
from matplotlib.ticker import ScalarFormatter
import numpy as np

REVISION = '0a268c5589c37832e1c3dc89a7b60ed2701e0b9e'
ARMS = ['original', 'disk', 'reclaim', 'ext4-block', 'pmem', 'ext4-reclaim', 'combined']
LABEL = dict(zip(ARMS, ['Original (RAM upper)', 'Disk (SquashFS)', 'Reclaim (SquashFS)',
                       'Ext4 block', 'Pmem / DAX', 'Ext4 block + reclaim', 'Combined']))
COLOR = dict(zip(ARMS, ['#667580', '#2C5F8A', '#168A8D', '#8C6BA4', '#C98317', '#AF5D8E', '#347A4B']))
PAIRS = [('original', 'disk'), ('disk', 'reclaim'), ('ext4-block', 'pmem'), ('ext4-reclaim', 'combined')]
MIB, GIB = 2**20, 2**30
METRICS = {
    'idle_cgroup_MiB': ('common_idle.dedicated_cgroup.memory_current.mean_bytes', MIB),
    'idle_pss_MiB': ('common_idle.vm_aggregate_pss_mapped_view.mean_bytes', MIB),
    'full_peak_MiB': ('full_trial.dedicated_cgroup.memory_current.sampled_peak_bytes', MIB),
    'full_memory_GiB_s': ('full_trial.dedicated_cgroup.memory_current.trapezoid_byte_seconds', GIB),
    'full_cpu_s': ('full_trial.dedicated_cgroup.cpu_delta_usec.usage_usec', 1e6),
    'idle_cpu_s': ('common_idle.dedicated_cgroup.cpu_delta_usec.usage_usec', 1e6),
    'full_duration_s': ('full_trial.duration_ns', 1e9),
    'vm_create_mean_ms': ('latency_ns.vm_ready.sum', 2e6),
    'workload_ready_mean_ms': ('latency_ns.vm_workload_ready.sum', 2e6),
    'readback_mean_ms': ('latency_ns.workload_readback.sum', 2e6),
    'physical_write_cumulative_MiB': ('full_trial.dedicated_cgroup.physical_io_cumulative_at_window_end.counters.wbytes', MIB),
    'physical_read_cumulative_MiB': ('full_trial.dedicated_cgroup.physical_io_cumulative_at_window_end.counters.rbytes', MIB),
    'idle_physical_write_delta_MiB': ('common_idle.dedicated_cgroup.physical_io_delta.wbytes', MIB),
    'full_host_major_faults': ('full_trial.dedicated_cgroup.memory_fault_deltas.pgmajfault', 1),
    'full_host_minor_faults': ('full_trial.dedicated_cgroup.memory_fault_deltas.pgfault', 1),
    'input_logical_MiB': ('input_image_file.logical_bytes', MIB),
    'input_allocated_MiB': ('input_image_file.allocated_bytes', MIB),
    'lower_cache_allocated_MiB': ('disk_file_allocation.immutable_lower_cache.sum_st_blocks_bytes', MIB),
    'thinpool_data_allocated_MiB': ('pool_backing.data.allocated_bytes', MIB),
    'thinpool_metadata_allocated_MiB': ('pool_backing.metadata.allocated_bytes', MIB),
}


def load(path):
    return json.loads(path.read_text())


def get(value, path):
    for key in path.split('.'):
        value = value[key]
    assert isinstance(value, (int, float)) and math.isfinite(value), path
    return value


def csv_file(path, rows):
    assert rows
    with path.open('w', newline='') as stream:
        writer = csv.DictWriter(stream, fieldnames=list(rows[0]))
        writer.writeheader()
        writer.writerows(rows)


def clipped(samples, start, end, getter):
    times = [row['monotonic_ns'] for row in samples]
    left, right = bisect_right(times, start)-1, bisect_left(times, end)
    assert left >= 0 and right < len(times)
    chosen = samples[left:right+1]
    assert all(0 < b['monotonic_ns']-a['monotonic_ns'] <= 1.5e9 for a,b in zip(chosen, chosen[1:]))
    times = np.array([r['monotonic_ns']-start for r in chosen], dtype=np.float64)/1e9
    values = np.array([getter(r) for r in chosen], dtype=np.float64)
    assert np.isfinite(values).all() and (values >= 0).all()
    duration = (end-start)/1e9
    inside = (times > 0) & (times < duration)
    x = np.r_[0, times[inside], duration]
    y = np.interp(x, times, values)
    return x, y


def sample_rows(path):
    return [row for row in map(json.loads, path.read_text().splitlines()) if row.get('type') == 'sample']


def save_figure(fig, output, name):
    fig.savefig(output/(name+'.svg'), metadata={'Date': None, 'Creator': 'Step 4 matrix results; source 0a268c5589c3'})
    fig.savefig(output/(name+'.png'), dpi=300, metadata={'Software': 'Matplotlib '+matplotlib.__version__})
    plt.close(fig)


def style(axis, xlabel, labels=True):
    axis.set_xlabel(xlabel)
    axis.set_yticks(range(7), [LABEL[a] for a in ARMS] if labels else [])
    axis.set_ylim(6.6, -0.6)
    axis.grid(axis='x', color='#E5E8EB', linewidth=.65)
    axis.set_axisbelow(True)
    axis.spines[['top','right','left']].set_visible(False)
    axis.tick_params(axis='y', length=0)
    axis.xaxis.set_major_formatter(ScalarFormatter())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--summary', required=True, type=Path)
    parser.add_argument('--raw', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=False)
    meta, paired = load(args.summary/'summary.json'), load(args.summary/'paired.json')
    trials = load(args.summary/'trials.json')['trials']
    assert meta['label'] == 'COMMITTED OBSERVATIONS'
    assert len(trials) == 21 and len(paired['blocks']) == 12 and not paired['unpaired']
    assert meta['configuration']['idle_offset_s'] == 5 and meta['configuration']['idle_window_s'] == 20
    assert {t['provenance_id'] for t in trials} == {meta['manifests'][0]['provenance_id']}
    assert meta['manifests'][0]['identity']['source_revision'] == REVISION
    assert not meta['manifests'][0]['development_issues']
    for block in range(3):
        selected = [t for t in trials if t['block']==block]
        assert len(selected)==7 and {t['arm'] for t in selected}==set(ARMS)
    rows, vm_rows, curves, verification, missing = [], [], [], [], []
    for trial in trials:
        assert not trial['development_issues'] and trial['label']=='COMMITTED OBSERVATIONS'
        directory = args.raw/trial['trial']
        for name, expected in trial['input_sha256'].items():
            assert hashlib.sha256((directory/name).read_bytes()).hexdigest()==expected, (trial['trial'],name)
        recorded_trial=load(directory/'trial.json')
        assert recorded_trial['status']=='pass' and recorded_trial['exit_code']==recorded_trial['container_exit_code']==0 and not recorded_trial.get('cleanup_errors')
        phase_records=[json.loads(line) for line in (directory/'phases.jsonl').read_text().splitlines()]
        completed=[r for r in phase_records if r.get('phase')=='workload_complete']
        assert len(completed)==1 and completed[0]['correctness'] is True
        for vm_index in range(2):
            probe=load(directory/f'vm{vm_index}-probe.json')
            phases={r['phase']:r for r in probe if r.get('phase')}
            assert all(phases[k]['verified'] is True for k in ['file_retouch','anon_verify','complete'])
            assert phases['file_touch']['sha256']==phases['file_retouch']['sha256']
        m=trial['metrics']
        row={key:trial[key] for key in ['trial','arm','block','ordinal']}
        row.update({name:get(m,path)/scale for name,(path,scale) in METRICS.items()})
        for key in ['anon','file','kernel','shmem']:
            row['idle_end_cgroup_'+key+'_MiB']=get(m,'common_idle.dedicated_cgroup.memory_stat_bytes.end.'+key)/MIB
        for metric,reason in trial['details'].get('unavailable',{}).items():
            missing.append({'trial':trial['trial'],'metric':metric,'reason':reason})
        unavailable=m['full_trial']['dedicated_cgroup']['physical_io_delta']
        if unavailable.get('status')=='unavailable':
            missing.append({'trial':trial['trial'],'metric':'full_trial.physical_io_delta','reason':unavailable['reason']})
        row['full_physical_delta_available']='status' not in m['full_trial']['dedicated_cgroup']['physical_io_delta']
        row['guest_payload_and_canary_verified']=True
        rows.append(row)
        host=sample_rows(directory/'host.jsonl')
        vms=sample_rows(directory/'vm-processes.jsonl')
        pids=trial['details']['vmm_pids']
        def cgroup(r): return r['cgroup']['metrics']['memory.current']['data']
        def pss(r):
            processes={p['pid']:p for p in r['processes']}
            assert set(processes)==set(pids)
            assert all(processes[p]['identity_verified'] for p in pids)
            assert all(processes[p]['smaps_rollup']['status']=='ok' and processes[p]['smaps_rollup']['data']['units']['Pss']=='kB' for p in pids)
            return sum(processes[p]['smaps_rollup']['data']['values']['Pss']*1024 for p in pids)
        for phase in ['full_trial','common_idle']:
            window=trial['details']['windows'][phase]
            a,b=window['start_host_monotonic_ns'],window['end_host_monotonic_ns']
            x,y=clipped(host,a,b,cgroup)
            integral=float(np.trapezoid(y,x))
            strict=get(m,phase+'.dedicated_cgroup.memory_current.trapezoid_byte_seconds')
            assert math.isclose(integral,strict,rel_tol=1e-11), trial['trial']
            verification.append({'trial':trial['trial'],'metric':phase+'.cgroup_byte_seconds','recomputed':integral,'strict_summary':strict})
            if phase=='common_idle':
                px,py=clipped(vms,a,b,pss)
                strict_pss=get(m,'common_idle.vm_aggregate_pss_mapped_view.trapezoid_byte_seconds')
                assert math.isclose(float(np.trapezoid(py,px)),strict_pss,rel_tol=1e-11)
                for at in np.linspace(0,20,81):
                    curves.append({'trial':trial['trial'],'arm':trial['arm'],'block':trial['block'],
                                   'seconds_after_later_idle_start':float(at+5),
                                   'cgroup_MiB':float(np.interp(at,x,y)/MIB),'vmm_pss_MiB':float(np.interp(at,px,py)/MIB)})
        for vm in ['vm0','vm1']:
            guest=m['guest'][vm]
            assert trial['details']['guest'][vm]['dax']['statx_dax'] == (trial['arm'] in ['pmem','combined'])
            v={'trial':trial['trial'],'arm':trial['arm'],'block':trial['block'],'vm':vm,
               'retouch_ms':get(guest,'phase_timings.file_retouch.phase_duration_ns')/1e6,
               'touch_ms':get(guest,'phase_timings.file_touch.phase_duration_ns')/1e6,
               'statx_dax':trial['details']['guest'][vm]['dax']['statx_dax'],
               'nominal_pmem_metadata_MiB':8 if trial['arm'] in ['pmem','combined'] else 0,
               'guest_major_faults_probe':get(guest,'rusage_deltas.whole_probe.major_faults'),
               'guest_minor_faults_probe':get(guest,'rusage_deltas.whole_probe.minor_faults')}
            for phase in ['idle_start','idle_end']:
                for key in ['MemTotal','MemAvailable','Cached','AnonPages','MemFree']:
                    v[phase+'_'+key+'_MiB']=get(guest,'memory_bytes.'+phase+'.'+key)/MIB
            v.update({'recorded_reporting_'+k:value for k,value in m['reporting'][vm].items()})
            v['reporting_counters_may_lag_final_flush']=True
            vm_rows.append(v)
    rows.sort(key=lambda r:(r['block'],r['ordinal']))
    csv_file(args.output/'trials_compact.csv',rows)
    csv_file(args.output/'guests.csv',vm_rows)
    csv_file(args.output/'idle_curves.csv',curves)
    csv_file(args.output/'integral_verification.csv',verification)
    csv_file(args.output/'missing_metrics.csv',missing)
    summaries=[]
    numeric=[key for key,value in rows[0].items() if isinstance(value,(int,float)) and not isinstance(value,bool) and key not in ['block','ordinal']]
    for arm in ARMS:
        own=[r for r in rows if r['arm']==arm]
        for key in numeric:
            vals=[r[key] for r in own]
            summaries.append({'arm':arm,'metric':key,'n_blocks':len(vals),'mean':statistics.mean(vals),
                              'min':min(vals),'max':max(vals),'sample_sd':statistics.stdev(vals)})
    csv_file(args.output/'arm_statistics.csv',summaries)
    contrast_rows=[]
    for a,b in PAIRS:
        for block in range(3):
            baseline=next(r for r in rows if r['arm']==a and r['block']==block)
            target=next(r for r in rows if r['arm']==b and r['block']==block)
            r={'baseline':a,'target':b,'block':block}
            for key in numeric:
                r[key+'_delta']=target[key]-baseline[key]
            contrast_rows.append(r)
    csv_file(args.output/'paired_block_differences.csv',contrast_rows)
    # Verify selected paired deltas against the separately validated strict analyzer.
    for strict in paired['blocks']:
        row=next(r for r in contrast_rows if r['baseline']==strict['baseline'] and r['target']==strict['target'] and r['block']==strict['block'])
        for metric in ['idle_cgroup_MiB','idle_pss_MiB','full_peak_MiB','full_memory_GiB_s','full_cpu_s','readback_mean_ms']:
            path,scale=METRICS[metric]
            assert math.isclose(row[metric+'_delta'],strict['target_minus_baseline'][path]/scale,abs_tol=1e-8)
    plt.rcParams.update({'font.family':'DejaVu Sans','font.size':9,'axes.titlesize':10.5,
                         'axes.labelsize':9,'xtick.labelsize':8.5,'ytick.labelsize':9,
                         'axes.edgecolor':'#6B7280','axes.linewidth':.6,'svg.hashsalt':'0a268c5589c3',
                         'savefig.facecolor':'white','figure.facecolor':'white'})
    fig,axes=plt.subplots(2,2,figsize=(11.8,7.3),layout='constrained')
    specs=[('idle_cgroup_MiB','A  Common-idle cgroup mean','MiB; two VMs + runtime + charged cache'),
           ('idle_pss_MiB','B  Common-idle VMM PSS','MiB; mapped-memory view, two VMMs'),
           ('full_memory_GiB_s','C  Full-trial memory-time','GiB·s; setup through verified readback'),
           ('full_cpu_s','D  Full-trial CPU','CPU-seconds; dedicated cgroup')]
    for axis,(metric,title,xlabel) in zip(axes.flat,specs):
        for i,arm in enumerate(ARMS):
            vals=[r[metric] for r in rows if r['arm']==arm]
            axis.hlines(i,min(vals),max(vals),color=COLOR[arm],lw=2)
            for block,val in enumerate(vals):
                axis.scatter(val,i+(block-1)*.13,c=COLOR[arm],marker=['o','s','^'][block],s=24,zorder=3,edgecolors='white',linewidths=.35)
            mean=statistics.mean(vals)
            axis.plot(mean,i,'|',c='#19212A',ms=14,mew=1.4,zorder=4)
            axis.annotate(f'{mean:,.1f}',(max(vals),i),xytext=(7,0),textcoords='offset points',va='center',fontsize=8,color='#28313B')
        style(axis,xlabel)
        axis.set_xlim(0,max(r[metric] for r in rows)*1.18)
        axis.set_title(title,loc='left',weight='bold',pad=9)
    fig.suptitle('Measured memory and CPU across seven configurations',fontsize=13,weight='bold')
    fig.supxlabel('Circle/square/triangle = blocks 0/1/2; black tick = arithmetic mean; horizontal line = observed range.\nCommon idle: seconds 5–25 after later guest idle start. Cgroup charges and VMM PSS are separate views.',fontsize=8.5)
    save_figure(fig,args.output,'01_memory_and_cpu')
    fig,axes=plt.subplots(1,4,figsize=(12.8,4.1),layout='constrained')
    short=['Disk − original','Reclaim − disk','Pmem − ext4 block','Combined − ext4 reclaim']
    for axis,metric,title,unit in zip(axes,['idle_cgroup_MiB','full_memory_GiB_s','full_cpu_s','readback_mean_ms'],
                                    ['Idle cgroup mean','Full memory-time','Full CPU','Readback RPC / VM'],
                                    ['Δ MiB','Δ GiB·s','Δ CPU-s','Δ ms']):
        for i,(a,b) in enumerate(PAIRS):
            vals=[r[metric+'_delta'] for r in contrast_rows if r['baseline']==a and r['target']==b]
            axis.hlines(i,min(vals),max(vals),color=COLOR[b],lw=2)
            for block,val in enumerate(vals):axis.scatter(val,i+(block-1)*.12,c=COLOR[b],marker=['o','s','^'][block],s=30,zorder=3)
            axis.plot(statistics.mean(vals),i,'|',c='#19212A',ms=15,mew=1.5)
        axis.set_yticks(range(4),short if axis is axes[0] else [])
        axis.set_ylim(3.55,-.55);axis.axvline(0,c='#58616B',lw=.8);axis.grid(axis='x',color='#E5E8EB',lw=.6)
        axis.spines[['top','right','left']].set_visible(False);axis.tick_params(axis='y',length=0)
        axis.set_title(title,weight='bold',fontsize=10);axis.set_xlabel(unit)
    fig.suptitle('Within-block comparisons preserve filesystem controls',fontsize=13,weight='bold')
    fig.supxlabel('Target minus baseline; left of zero = lower observed cost. Circle/square/triangle = paired blocks 0/1/2; black tick = mean.\nPmem comparisons hold ext4 format and reclamation state fixed. Ranges are observed variation, not confidence intervals.',fontsize=8.5)
    save_figure(fig,args.output,'02_paired_tradeoffs')
    fig,axes=plt.subplots(1,2,figsize=(11.5,4.9),layout='constrained')
    for i,arm in enumerate(ARMS):
        own=[r for r in vm_rows if r['arm']==arm]
        for j,row in enumerate(own):
            offset=(row['block']-1)*.14+(-.035 if row['vm']=='vm0' else .035)
            axes[0].scatter(row['retouch_ms'],i+offset,c=COLOR[arm],marker=['o','s','^'][row['block']],s=28,edgecolors='white',linewidths=.3)
            axes[1].scatter(row['idle_end_Cached_MiB'],i+offset,c=COLOR[arm],marker=['o','s','^'][row['block']],s=28,edgecolors='white',linewidths=.3)
        axes[0].plot(statistics.median(r['retouch_ms'] for r in own),i,'|',color='#19212A',ms=14,mew=1.4)
        axes[1].plot(statistics.mean(r['idle_end_Cached_MiB'] for r in own),i,'|',color='#19212A',ms=14,mew=1.4)
    style(axes[0],'Instrumented immutable-file retouch (ms; log scale)')
    axes[0].set_xscale('log');axes[0].set_xticks([10,30,100,300,1000],[10,30,100,300,1000]);axes[0].set_xlim(9,1000)
    axes[0].set_title('A  Retouch includes a 605 ms observation',loc='left',weight='bold')
    style(axes[1],'Guest Cached at idle end (MiB per VM)',False);axes[1].set_xlim(0,300)
    axes[1].set_title('B  Guest cache falls more than host charges',loc='left',weight='bold')
    worst=max(vm_rows,key=lambda r:r['retouch_ms'])
    axes[0].annotate(f'{worst["retouch_ms"]:.0f} ms\nblock {worst["block"]}, {worst["vm"]}',(worst['retouch_ms'],ARMS.index(worst['arm'])),xytext=(-10,-30),textcoords='offset points',ha='right',fontsize=8,arrowprops={'arrowstyle':'-','color':'#606A75'})
    fig.suptitle('Per-VM observations retain reactivation cost and guest cache effects',fontsize=12.5,weight='bold')
    fig.supxlabel('Circle/square/triangle = blocks 0/1/2, two VMs each. Black ticks: retouch median (A), cache mean (B).\nProbe intervals include instrumentation and hashing; they are not pure storage latency. VM observations within a trial are not independent replications.',fontsize=8.5)
    save_figure(fig,args.output,'03_reactivation_and_guest_cache')
    fig,axes=plt.subplots(1,2,figsize=(11.8,5.5))
    fig.subplots_adjust(left=.065,right=.99,top=.80,bottom=.25,wspace=.20)
    for arm in ARMS:
        own=[r for r in curves if r['arm']==arm]
        grid=sorted({r['seconds_after_later_idle_start'] for r in own})
        for axis,key in zip(axes,['cgroup_MiB','vmm_pss_MiB']):
            by_time=[[r[key] for r in own if r['seconds_after_later_idle_start']==at] for at in grid]
            assert all(len(values)==3 for values in by_time)
            axis.fill_between(grid,[min(v) for v in by_time],[max(v) for v in by_time],color=COLOR[arm],alpha=.12,lw=0)
            axis.plot(grid,[statistics.mean(v) for v in by_time],c=COLOR[arm],lw=1.5,label=LABEL[arm])
        axes[0].set_title('A  Dedicated-cgroup memory.current',loc='left',weight='bold')
        axes[1].set_title('B  Sum of identity-verified VMM PSS',loc='left',weight='bold')
    for axis in axes:
        axis.set_xlabel('Seconds after later guest idle start');axis.set_ylabel('MiB')
        axis.set_xlim(5,25);axis.grid(color='#E5E8EB',lw=.6);axis.spines[['top','right']].set_visible(False)
    # Explicit zoom is useful for within-idle evolution; absolute levels appear in Figure 1.
    axes[0].set_ylim(1400,2250);axes[1].set_ylim(620,800)
    fig.suptitle('Memory evolution within the pre-specified common idle window',fontsize=13,weight='bold',y=.98)
    fig.text(.5,.915,'Mean lines and observed-range bands across three blocks; axes are zoomed',ha='center',fontsize=9.5)
    handles,labels=axes[0].get_legend_handles_labels()
    fig.legend(handles,labels,loc='lower center',bbox_to_anchor=(.5,.045),ncol=4,frameon=False,fontsize=8.5)
    fig.text(.5,.015,'Linear interpolation within valid sample brackets; no extrapolation',ha='center',fontsize=8)
    save_figure(fig,args.output,'04_idle_evolution')
    (args.output/'verification.json').write_text(json.dumps({'source_revision':REVISION,'label':meta['label'],
      'trials':len(trials),'blocks':3,'arms':7,'guest_vms':len(vm_rows),'predefined_paired_comparisons':len(contrast_rows),
      'input_file_hashes_verified':sum(len(t['input_sha256']) for t in trials),'cgroup_integrals_recomputed':len(verification),
      'vmm_idle_pss_integrals_recomputed':21,'paired_metric_differences_checked':12*6,
      'all_guest_payload_and_canary_checks_passed':True,'plots':4,
      'reporting_freshness_limitation':'recorded Firecracker reporting frames may precede final flush consumption'},indent=2)+'\n')
    print(json.dumps(load(args.output/'verification.json'),sort_keys=True))


if __name__=='__main__': main()
