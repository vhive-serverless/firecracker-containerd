#!/usr/bin/env python3
# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
"""Actual worker checks for owned failed-start cleanup and binary provenance."""
import argparse
import json
import os
from pathlib import Path
import shutil
import tempfile
import uuid

import provenance
import run_matrix


def rejected(function, *args):
    try:
        function(*args)
    except (ValueError, RuntimeError):
        return
    raise AssertionError('operation unexpectedly accepted invalid ownership/provenance')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True)
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error('worker root privileges required for owned Docker/loop fixtures')
    repo = run_matrix.REPO
    head = run_matrix.git('rev-parse', 'HEAD')
    provenance.verify(repo, head, True)
    print('PASS actual binary matches build receipt and current source digest', flush=True)
    with tempfile.TemporaryDirectory(prefix='fc-eval-guard-') as temporary:
        root = Path(temporary)
        for name in provenance.INPUTS:
            target = root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(repo / name, target)
        (root / 'bin').mkdir()
        shutil.copy2(repo / 'bin/memory-eval.test', root / 'bin/memory-eval.test')
        shutil.copyfile(repo / 'bin/memory-eval-build.json', root / 'bin/memory-eval-build.json')
        provenance.verify(root, head, True)
        source = root / provenance.INPUTS[0]
        source.write_bytes(source.read_bytes() + b'\n')
        rejected(provenance.verify, root, head, True)
        shutil.copyfile(repo / provenance.INPUTS[0], source)
        receipt = json.loads((root / 'bin/memory-eval-build.json').read_text())
        receipt['binary_sha256'] = '0' * 64
        (root / 'bin/memory-eval-build.json').write_text(json.dumps(receipt))
        rejected(provenance.verify, root, head, True)
    print('PASS changed source and stale binary receipt rejected', flush=True)
    token = uuid.uuid4().hex[:12]
    name = 'fc-eval-start-failure-' + token
    with tempfile.TemporaryDirectory(prefix='fc-eval-docker-') as temporary:
        try:
            rejected(run_matrix.command, ['docker', 'run', '-d', '--name', name, '--network=none',
                     '--label', f'fc-memory-eval={token}', '--entrypoint', '/no-such-eval-command', args.image])
            info = run_matrix.inspect_owned_container(name, token)
            assert info is not None and not info['State']['Running']
            rejected(run_matrix.inspect_owned_container, name, 'different-owner')
            assert run_matrix.inspect_owned_container(name, token) is not None
            run_matrix.remove_owned_container(name, token, Path(temporary))
            assert run_matrix.inspect_owned_container(name, token) is None
        finally:
            run_matrix.remove_owned_container(name, token, Path(temporary))
    print('PASS actual failed Docker start reconciled; mismatched ownership preserved', flush=True)
    with tempfile.TemporaryDirectory(prefix='fc-eval-partial-pool-') as temporary:
        fixture = Path(temporary)
        pool = 'fc-eval-partial-' + uuid.uuid4().hex[:12]
        directory = fixture / 'tmp/devmapper' / pool
        directory.mkdir(parents=True)
        try:
            for name in ('data', 'metadata'):
                path = directory / name
                with path.open('xb') as stream:
                    stream.truncate(2 << 20)
                run_matrix.command(['losetup', '--find', '--show', path])
            run_matrix.REPO = fixture
            run_matrix.cleanup_owned_pool(pool)
            assert not (directory / 'data').exists() and not (directory / 'metadata').exists()
        finally:
            run_matrix.REPO = repo
            for name in ('data', 'metadata'):
                path = directory / name
                if path.exists():
                    devices = run_matrix.command(['losetup', '--noheadings', '--output', 'NAME', '--associated', path]).split()
                    for device in devices:
                        run_matrix.command(['losetup', '--detach', device])
    print('PASS two real private loop attachments cleaned without an existing thin pool', flush=True)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
