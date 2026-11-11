#!/usr/bin/env python3
# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
"""Run owned Firecracker trials with raw host observations; never drops caches."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import random
import shutil
import signal
import subprocess
import sys
import time
import uuid

from provenance import verify as verify_provenance

ARMS = ('original', 'disk', 'pmem', 'reclaim', 'combined', 'ext4-block', 'ext4-reclaim')
EXT4 = {'pmem', 'combined', 'ext4-block', 'ext4-reclaim'}
REPO = Path(__file__).resolve().parents[2]


def command(args, **kwargs):
    result = subprocess.run([str(arg) for arg in args], text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kwargs)
    if result.returncode:
        raise RuntimeError(f'command failed ({result.returncode}): {args!r}\n{result.stdout[-4000:]}\n{result.stderr[-4000:]}')
    return result.stdout.strip()


def git(*args):
    return command(['git', '-c', f'safe.directory={REPO}', '-C', REPO, *args])


def save(path, value):
    with path.open('x', encoding='utf-8') as stream:
        json.dump(value, stream, indent=2)
        stream.write('\n')


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def stamp():
    return {'monotonic_ns': time.monotonic_ns(), 'unix_ns': time.time_ns()}


def memory():
    return Path('/proc/meminfo').read_text()


def start_sampler(trial, name, pids, cgroup, interval):
    args = [sys.executable, REPO / 'tools/memory-eval/sample_host.py',
            '--duration', '300', '--interval', str(interval), '--output', trial / f'{name}.jsonl']
    for pid in pids:
        args.extend(['--pid', str(pid)])
    if cgroup is not None:
        args.extend(['--cgroup', str(cgroup)])
    errors = (trial / f'{name}-stderr.log').open('x')
    process = subprocess.Popen([str(arg) for arg in args], stdout=subprocess.DEVNULL,
                               stderr=errors, start_new_session=True)
    return process, errors


def stop_sampler(item):
    process, errors = item
    if process.poll() is None:
        process.send_signal(signal.SIGINT)
    code = process.wait(timeout=10)
    errors.close()
    if code != 0:
        raise RuntimeError(f'owned sampler exited {code}')



def inspect_owned_container(name, token):
    result = subprocess.run(['docker', 'inspect', name], text=True, capture_output=True)
    if result.returncode:
        if 'no such object:' in result.stderr.lower() or 'no such container:' in result.stderr.lower():
            return None
        raise RuntimeError('cannot reconcile owned container: ' + result.stderr)
    info = json.loads(result.stdout)[0]
    if info['Config'].get('Labels', {}).get('fc-memory-eval') != token:
        raise RuntimeError('container ownership label mismatch; preserving resources')
    return info


def remove_owned_container(name, token, trial):
    info = inspect_owned_container(name, token)
    if info is None:
        return
    ident = info['Id']
    logs = subprocess.run(['docker', 'logs', ident], text=True, capture_output=True)
    (trial / 'workload.log').write_text(logs.stdout + logs.stderr)
    if not (trial / 'docker-inspect.json').exists():
        save(trial / 'docker-inspect.json', info)
    command(['docker', 'rm', '-f', ident])


def cleanup_owned_pool(pool):
    directory = REPO / 'tmp/devmapper' / pool
    backing = {key: directory / key for key in ('metadata', 'data')}
    loops = {}
    for key, path in backing.items():
        if path.is_symlink():
            raise RuntimeError('owned pool backing unexpectedly became a symlink')
        loops[key] = command(['losetup', '--noheadings', '--output', 'NAME', '--associated', path]).split() if path.exists() else []
    exists = subprocess.run(['dmsetup', 'info', pool], capture_output=True).returncode == 0
    if exists:
        fields = command(['dmsetup', 'table', pool]).split()
        devices = {key: {f'{os.major(os.stat(dev).st_rdev)}:{os.minor(os.stat(dev).st_rdev)}' for dev in values}
                   for key, values in loops.items()}
        if len(fields) < 5 or fields[2] != 'thin-pool' or fields[3] not in devices['metadata'] or fields[4] not in devices['data']:
            raise RuntimeError('pool does not use the owned private backing; preserving resources')
        command([REPO / 'tools/thinpool.sh', 'remove', pool])
        return
    # A failed create may have attached either loop before dmsetup existed.
    for key, path in backing.items():
        for dev in loops[key]:
            command(['losetup', '--detach', dev])
        if path.exists():
            deadline = time.monotonic() + 5
            while command(['losetup', '--noheadings', '--output', 'NAME', '--associated', path]):
                if time.monotonic() >= deadline:
                    raise RuntimeError('private loop still attached; retaining its backing')
                time.sleep(.05)
            path.unlink()


def trial_run(args, images, arm, block, ordinal):
    trial = args.output / f'b{block:02d}-{ordinal:02d}-{arm}'
    trial.mkdir()
    for name in ('logs', 'vms'):
        (trial / name).mkdir()
    token = uuid.uuid4().hex[:12]
    name = f'fc-eval-{token}'
    pool = f'fc-eval-{token}'
    container = None
    container_attempted = False
    samplers = []
    owned_pool = False
    result = {'arm': arm, 'block': block, 'ordinal': ordinal, 'start': stamp(),
              'committed_acceptance': not args.allow_dirty,
              'host_meminfo_before': memory(), 'container_name': name, 'pool': pool,
              'image_sha256': images['ext4' if arm in EXT4 else 'squash']['sha256']}
    try:
        if (REPO / 'tmp/devmapper' / pool).exists() or subprocess.run(['dmsetup', 'info', pool], capture_output=True).returncode == 0:
            raise RuntimeError('new trial pool name already exists')
        owned_pool = True
        command([REPO / 'tools/thinpool.sh', 'create', pool])
        guest_command = ('while ! test -e /eval/start; do sleep .05; done; '
                         '/entrypoint "/src/bin/memory-eval.test -test.v '
                         '-test.run=^TestMemoryEvaluation_Isolated$ -test.timeout=250s"; '
                         'status=$?; echo "$status" >/eval/exit-code.tmp; mv /eval/exit-code.tmp /eval/exit-code; '
                         'while ! test -e /eval/finish; do sleep .05; done; exit "$status"')
        image_path = images['ext4' if arm in EXT4 else 'squash']['path']
        docker = ['docker', 'run', '-d', '--name', name, '--label', f'fc-memory-eval={token}',
                  '--privileged', '--ipc=host', '--pid=host', '--cgroupns=host', '--network=none',
                  '--volume', '/dev:/dev', '--volume', '/run/udev/control:/run/udev/control',
                  '--volume', f'{REPO}:/src:ro', '--volume', f'{trial}:/eval',
                  '--volume', f'{trial / "vms"}:/srv/firecracker_containerd_tests',
                  '--volume', f'{trial / "logs"}:/var/log/firecracker-containerd-test',
                  '--volume', f'{image_path}:/inputs/root.img:ro',
                  '--env', 'ENABLE_ISOLATED_TESTS=1', '--env', 'FICD_DM_VOLUME_GROUP=',
                  '--env', f'FICD_DM_POOL={pool}', '--env', f'FICD_EVAL_ARM={arm}',
                  '--env', f'FICD_EVAL_TOKEN={token}',
                  '--env', f'FICD_EVAL_IDLE={args.idle}s', '--workdir', '/src/runtime',
                  '--entrypoint', '/bin/bash', args.image, '-c', guest_command]
        container_attempted = True
        container = command(docker)
        result['docker_command'] = docker
        info = json.loads(command(['docker', 'inspect', container]))[0]
        pid = info['State']['Pid']
        relative = next(line.split(':', 2)[2] for line in Path(f'/proc/{pid}/cgroup').read_text().splitlines()
                        if line.startswith('0::'))
        cgroup = Path('/sys/fs/cgroup') / relative.lstrip('/')
        for required in ('memory.current', 'memory.stat', 'cpu.stat'):
            if not (cgroup / required).is_file():
                raise RuntimeError(f'required trial cgroup counter unavailable: {required}')
        result['container_pid'], result['cgroup'] = pid, str(cgroup)
        samplers.append(start_sampler(trial, 'host', [pid], cgroup, args.interval))
        time.sleep(1.2)
        if samplers[0][0].poll() is not None:
            raise RuntimeError('host sampler stopped before trial')
        (trial / 'start').touch(exist_ok=False)
        result['workload_start'] = stamp()
        deadline = time.monotonic() + 270
        pids_started = False
        while not (trial / 'exit-code').exists():
            if (trial / 'vmm-pids.json').exists() and not pids_started:
                pids = json.loads((trial / 'vmm-pids.json').read_text())
                if len(pids) != 2 or len(set(pids)) != 2 or any(not isinstance(p, int) or p <= 1 for p in pids):
                    raise RuntimeError('invalid owned VMM PID record')
                samplers.append(start_sampler(trial, 'vm-processes', pids, None, args.interval))
                result['vmm_pids'], pids_started = pids, True
            if time.monotonic() >= deadline:
                raise RuntimeError('trial timeout')
            if command(['docker', 'inspect', '--format', '{{.State.Running}}', container]) != 'true':
                raise RuntimeError('trial container exited before workload status')
            time.sleep(.25)
        result['workload_exit'] = stamp()
        result['exit_code'] = int((trial / 'exit-code').read_text())
        result['pool_status'] = command(['dmsetup', 'status', pool])
        pool_dir = REPO / 'tmp/devmapper' / pool
        result['pool_backing'] = {p.name: {'logical_bytes': p.stat().st_size,
                                                  'allocated_bytes': p.stat().st_blocks * 512}
                                  for p in (pool_dir / 'data', pool_dir / 'metadata')}
        time.sleep(1.2)
        while samplers:
            stop_sampler(samplers.pop())
        result['observation_end'] = stamp()
        result['host_meminfo_after_workload'] = memory()
        (trial / 'finish').touch(exist_ok=False)
        result['container_exit_code'] = int(command(['docker', 'wait', container], timeout=15))
        if result['exit_code'] != 0 or not pids_started:
            raise RuntimeError(f'workload failed: {result["exit_code"]}')
        result['status'] = 'pass'
    except BaseException as exc:
        result['status'], result['error'] = 'fail', str(exc)
        raise
    finally:
        while samplers:
            try:
                stop_sampler(samplers.pop())
            except Exception as exc:
                result.setdefault('cleanup_errors', []).append(str(exc))
        active_error = sys.exc_info()[0] is not None
        container_removed = False
        try:
            # The known unique name also covers docker-create/start failures
            # which return no container ID to the caller.
            if container_attempted:
                remove_owned_container(name, token, trial)
            container_removed = True
        except Exception as exc:
            result.setdefault('cleanup_errors', []).append(str(exc))
        if owned_pool and container_removed:
            try:
                cleanup_owned_pool(pool)
            except Exception as exc:
                result.setdefault('cleanup_errors', []).append(str(exc))
        # VMs and their container have stopped. Remove only this owned trial's
        # unused cache/disks; retaining them would accumulate host page cache.
        if container_removed:
            shutil.rmtree(trial / 'vms')
        if result.get('cleanup_errors'):
            result['status'] = 'fail'
        # Publish only owned synthetic observation files for the worker user.
        # Core VM state has already been removed with its private directory.
        for artifact in trial.rglob('*'):
            if not artifact.is_symlink() and artifact.is_file():
                artifact.chmod(0o444 if artifact.suffix == '.img' else 0o644)
        result['end'] = stamp()
        result['host_meminfo_after_cleanup'] = memory()
        save(trial / 'trial.json', result)
        if result.get('cleanup_errors') and not active_error:
            raise RuntimeError('owned resource cleanup failed; see trial.json')
    return result



def backing_device(path):
    identity = path.stat().st_dev
    filesystem = f'{os.major(identity)}:{os.minor(identity)}'
    location = Path('/sys/dev/block') / filesystem
    physical = None
    if location.exists():
        resolved = location.resolve()
        physical = (resolved.parent / 'dev').read_text().strip() if (resolved / 'partition').exists() else filesystem
    return {'filesystem_major_minor': filesystem, 'physical_major_minor': physical}



def verify_go_build_info(text, revision, count, allow_dirty):
    revisions = [line.split('vcs.revision=', 1)[1].strip() for line in text.splitlines() if 'vcs.revision=' in line]
    modified = [line.split('vcs.modified=', 1)[1].strip() for line in text.splitlines() if 'vcs.modified=' in line]
    if len(revisions) != count or any(value != revision for value in revisions):
        raise ValueError('actual Go artifact build revisions do not match the pinned runtime')
    if not allow_dirty and (len(modified) != count or any(value != 'false' for value in modified)):
        raise ValueError('committed acceptance requires clean Go artifact builds')


def prepare_images(args):
    directory = args.output / 'images'
    directory.mkdir()
    squash = directory / 'squash.img'
    shutil.copyfile(args.squash, squash)
    with squash.open('r+b') as stream:
        stream.truncate(((squash.stat().st_size + (2 << 20) - 1) // (2 << 20)) * (2 << 20))
    squash.chmod(0o444)
    docker = ['docker', 'run', '--rm', '--network=none', '--entrypoint', '/src/build-dax-rootfs.sh',
              '--volume', f'{REPO / "tools/image-builder"}:/src:ro',
              '--volume', f'{directory}:/out', '--volume', f'{directory}:/inputs:ro',
              args.builder_image, '/inputs/squash.img', '/out/ext4.img']
    logs = command(docker)
    (directory / 'conversion.log').write_text(logs)
    ext4 = directory / 'ext4.img'
    agent = directory / 'guest-agent.bin'
    with agent.open('xb') as stream:
        extracted = subprocess.run(['docker', 'run', '--rm', '--network=none', '--entrypoint', 'unsquashfs',
                                    '--volume', f'{directory}:/inputs:ro', args.builder_image,
                                    '-cat', '/inputs/squash.img', 'usr/local/bin/agent'],
                                   stdout=stream, stderr=subprocess.PIPE)
    if extracted.returncode:
        raise RuntimeError('cannot extract guest agent metadata: ' + extracted.stderr.decode(errors='replace'))
    agent_info = command(['docker', 'run', '--rm', '--network=none', '--volume', f'{directory}:/artifact:ro',
                          '--entrypoint', '/usr/local/go/bin/go', args.image, 'version', '-m', '/artifact/guest-agent.bin'])
    verify_go_build_info(agent_info, args.runtime_revision, 1, args.allow_dirty)
    save(directory / 'guest-agent.json', {'sha256': digest(agent), 'go_build_info': agent_info})
    agent.unlink()
    return {kind: {'path': str(path), 'sha256': digest(path), 'logical_bytes': path.stat().st_size,
                   'allocated_bytes': path.stat().st_blocks * 512}
            for kind, path in (('squash', squash), ('ext4', ext4))}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--squash', type=Path, required=True)
    parser.add_argument('--image', required=True)
    parser.add_argument('--builder-image', required=True)
    parser.add_argument('--runtime-revision', required=True)
    parser.add_argument('--arms', default=','.join(ARMS))
    parser.add_argument('--repeats', type=int, default=3)
    parser.add_argument('--idle', type=int, default=30)
    parser.add_argument('--interval', type=float, default=.5)
    parser.add_argument('--seed', type=int, default=5823)
    parser.add_argument('--allow-dirty', action='store_true', help='development only; never committed acceptance')
    args = parser.parse_args()
    arms = args.arms.split(',')
    if os.geteuid() != 0:
        parser.error('run this owned-resource controller through sudo -n')
    if not args.output.is_absolute() or args.output.exists() or not args.squash.is_absolute():
        parser.error('output must be a new absolute directory and squash must be absolute')
    if not 1 <= args.repeats <= 10 or not 10 <= args.idle <= 120 or not .1 <= args.interval <= 10:
        parser.error('invalid repeat, idle or interval bounds')
    if len(set(arms)) != len(arms) or any(arm not in ARMS for arm in arms):
        parser.error('unknown or repeated arm')
    dirty = git('status', '--porcelain')
    if dirty and not args.allow_dirty:
        parser.error('clean committed source required')
    head = git('rev-parse', 'HEAD')
    if not args.allow_dirty and args.runtime_revision != head:
        parser.error('committed acceptance requires runtime revision equal to source HEAD')
    binary = REPO / 'bin/memory-eval.test'
    if not binary.is_file():
        parser.error('build bin/memory-eval.test on the worker before observation')
    ksm = Path('/sys/kernel/mm/ksm/run')
    if ksm.exists() and ksm.read_text().strip() != '0':
        parser.error('KSM is active; this matrix requires an isolated deduplication baseline')
    requested_images = {'runtime': args.image, 'builder': args.builder_image}
    args.image = json.loads(command(['docker', 'image', 'inspect', args.image]))[0]['Id']
    args.builder_image = json.loads(command(['docker', 'image', 'inspect', args.builder_image]))[0]['Id']
    version = command(['docker', 'run', '--rm', '--network=none', '--entrypoint',
                       '/usr/local/bin/firecracker-containerd', args.image, '--version'])
    if not version.endswith(' ' + args.runtime_revision):
        parser.error('runtime image does not match the explicit pinned revision')
    runtime_info = command(['docker', 'run', '--rm', '--network=none', '--entrypoint', '/usr/local/go/bin/go',
                            args.image, 'version', '-m', '/usr/local/bin/containerd-shim-aws-firecracker',
                            '/usr/local/bin/firecracker-containerd'])
    verify_go_build_info(runtime_info, args.runtime_revision, 2, args.allow_dirty)
    provenance = verify_provenance(REPO, head, args.allow_dirty)
    args.output.mkdir(parents=True)
    images = prepare_images(args)
    metadata = {'source_revision': head, 'source_dirty': bool(dirty),
                'committed_acceptance': not args.allow_dirty, 'evaluation_build': provenance,
                'source_diff': dirty, 'runtime_revision': args.runtime_revision, 'runtime_version': version,
                'runtime_go_build_info': runtime_info, 'requested_image_tags': requested_images, 'guest_agent': json.loads((args.output / 'images/guest-agent.json').read_text()),
                'runtime_image': json.loads(command(['docker', 'image', 'inspect', args.image]))[0]['Id'],
                'builder_image': json.loads(command(['docker', 'image', 'inspect', args.builder_image]))[0]['Id'],
                'test_binary_sha256': digest(binary), 'sampler_sha256': digest(REPO / 'tools/memory-eval/sample_host.py'),
                'images': images, 'arguments': {k: str(v) if isinstance(v, Path) else v for k, v in vars(args).items()},
                'host_uname': command(['uname', '-a']), 'host_meminfo': memory(),
                'backing_device': backing_device(args.output),
                'ksm_run': ksm.read_text().strip() if ksm.exists() else None,
                'cache_state': 'fresh private cache copies; digest verification warms host backing; no global cache drops',
                'start': stamp()}
    save(args.output / 'manifest.json', metadata)
    rng = random.Random(args.seed)
    results = []
    for block in range(args.repeats):
        order = list(arms)
        rng.shuffle(order)
        for ordinal, arm in enumerate(order):
            print(f'RUN block={block} arm={arm}', flush=True)
            result = trial_run(args, images, arm, block, ordinal)
            results.append(result)
            print(f'PASS block={block} arm={arm}', flush=True)
    save(args.output / 'matrix-result.json', {'trials': len(results), 'status': 'pass', 'committed_acceptance': not args.allow_dirty, 'end': stamp()})
    return 0


if __name__ == '__main__':
    sys.exit(main())
