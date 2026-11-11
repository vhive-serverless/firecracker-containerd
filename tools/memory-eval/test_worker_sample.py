#!/usr/bin/env python3
# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
"""Worker-only smoke check using our own temporary process and output files."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    args.output_dir.mkdir(parents=True, exist_ok=False)
    output = args.output_dir / "owned-process.jsonl"
    child_code = """
import os,sys,time
pages = bytearray(16 * 1024 * 1024)
for i in range(0, len(pages), 4096): pages[i] = 1
with open(sys.argv[1], 'wb') as file:
    file.write(bytes(1024 * 1024)); file.flush(); os.fsync(file.fileno())
deadline = time.monotonic() + 0.15
while time.monotonic() < deadline: pass
print('READY', flush=True)
time.sleep(30)
"""
    with tempfile.TemporaryDirectory(prefix="host-sampler-owned-") as temporary:
        child = subprocess.Popen([sys.executable, "-c", child_code, str(Path(temporary) / "io")],
                                 stdout=subprocess.PIPE, text=True)
        timer = None
        try:
            assert child.stdout.readline().strip() == "READY", "owned child failed to initialize"
            command = [sys.executable, str(Path(__file__).with_name("sample_host.py")),
                       "--pid", str(child.pid), "--pid", str(os.getpid()),
                       "--interval", "0.2", "--duration", "1.2", "--output", str(output)]
            relative = next(line.split("::", 1)[1] for line in Path(f"/proc/{child.pid}/cgroup").read_text().splitlines()
                            if line.startswith("0::"))
            cgroup = Path("/sys/fs/cgroup") / relative.lstrip("/")
            if (cgroup / "cgroup.controllers").is_file():
                command += ["--cgroup", str(cgroup)]
            else:
                cgroup = None
            print("COMMAND", json.dumps(command), flush=True)
            print("OWNED_PID", child.pid, "EXPLICIT_CGROUP", str(cgroup), flush=True)
            reaper = threading.Thread(target=child.wait, daemon=True)
            reaper.start()
            timer = threading.Timer(0.65, child.terminate)
            timer.start()
            subprocess.run(command, check=True, timeout=10)
            reaper.join(timeout=5)
            records = [json.loads(line) for line in output.read_text().splitlines()]
            samples = [record for record in records if record["type"] == "sample"]
            assert len(samples) >= 4
            assert records[0]["type"] == "metadata" and records[-1]["type"] == "end"
            assert all(record["timestamp_utc"].endswith("+00:00") for record in records)
            assert all(a["monotonic_ns"] <= b["monotonic_ns"] for a, b in zip(records, records[1:]))
            observed = [record["processes"][0] for record in samples]
            alive = [record for record in observed if record["status"] == "ok"]
            assert alive and any(record["status"] == "exited" for record in observed)
            assert len({record["expected_starttime_ticks"] for record in observed}) == 1
            for record in alive:
                assert record["identity_verified"]
                assert record["smaps_rollup"]["data"]["values"]["Pss"] > 0
                assert record["io"]["data"]["values"]["write_bytes"] >= 1024 * 1024
                assert record["stat"]["utime"] + record["stat"]["stime"] > 0
            assert all(record["host_meminfo"]["status"] == "ok" for record in samples)
            assert all(record["processes"][1]["identity_verified"] for record in samples)
            if cgroup is not None:
                assert all(record["cgroup"]["status"] in ("ok", "partial") for record in samples)
                print("EXPLICIT_CGROUP_READ_OK", flush=True)
            print("OWNED_PROCESS_CPU_IO_PSS_EXIT_AND_TIMESTAMPS_OK", flush=True)
        finally:
            if timer is not None:
                timer.cancel()
            if child.poll() is None:
                child.terminate()
            child.wait(timeout=5)
            child.stdout.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
