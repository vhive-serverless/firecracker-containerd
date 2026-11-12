#!/usr/bin/env python3
# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
"""Fingerprint the evaluation sources and validate a precompiled worker receipt."""
import hashlib
import json
from pathlib import Path
import subprocess
import os

INPUTS = ('runtime/memory_evaluation_integ_test.go', 'tools/memory-eval/run_matrix.py',
          'tools/memory-eval/sample_host.py', 'tools/memory-eval/provenance.py',
          'tools/modernize/build-memory-eval.sh', 'go.mod', 'go.sum')


def source_digest(repo):
    value = hashlib.sha256()
    for name in INPUTS:
        value.update(name.encode() + b'\0')
        value.update((repo / name).read_bytes())
        value.update(b'\0')
    return value.hexdigest()


def binary_digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def binary_info(path):
    environment = dict(os.environ, FICD_EVAL_PROVENANCE='1')
    output = subprocess.run([str(path), '-test.run=^TestMemoryEvaluationBuildInfo$', '-test.v'],
                            check=True, capture_output=True, text=True, env=environment).stdout
    records = [json.loads(line.removeprefix('EVAL_BUILD_INFO ')) for line in output.splitlines()
               if line.startswith('EVAL_BUILD_INFO ')]
    if len(records) != 1:
        raise ValueError('evaluation binary lacks its build identity; use build-memory-eval.sh')
    return records[0]


def verify(repo, head, allow_dirty):
    binary = repo / 'bin/memory-eval.test'
    receipt = json.loads((repo / 'bin/memory-eval-build.json').read_text())
    info = binary_info(binary)
    checksum = binary_digest(binary)
    if receipt['binary_sha256'] != checksum or receipt['build_info'] != info:
        raise ValueError('evaluation binary does not match its worker build receipt')
    if info['source_digest'] != source_digest(repo):
        raise ValueError('evaluation sources changed since compilation')
    if not allow_dirty and (info['source_revision'] != head or info['source_dirty']):
        raise ValueError('evaluation binary must come from this exact clean committed revision')
    return {'binary_sha256': checksum, 'build_info': info, 'receipt': receipt}


if __name__ == '__main__':
    print(source_digest(Path(__file__).resolve().parents[2]))
