#!/usr/bin/env python3
# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
"""Expose failing Go test names/context in Actions annotations, retaining logs."""
import argparse
from pathlib import Path
import re


def escape(text, property_value=False):
    text = text.replace('%', '%25').replace('\r', '%0D').replace('\n', '%0A')
    return text.replace(',', '%2C').replace(':', '%3A') if property_value else text


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('log', type=Path)
    args = parser.parse_args()
    rows = args.log.read_text(errors='replace').splitlines()
    failures = [(index, match.group(1)) for index, row in enumerate(rows)
                if (match := re.match(r'^\s*--- FAIL: (\S+)', row))]
    if failures:
        for position, (index, name) in enumerate(failures[:20]):
            next_index = failures[position + 1][0] if position + 1 < len(failures) else len(rows)
            context = '\n'.join(rows[index:min(next_index, index + 60)])[:10000]
            print(f'::error title={escape("Go test: " + name, True)}::{escape(context)}')
    else:
        context = '\n'.join(rows[-100:])[-12000:]
        print(f'::error title=Go test command failed::{escape(context)}')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
