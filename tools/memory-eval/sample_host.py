#!/usr/bin/env python3
# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
"""Read explicitly selected Linux processes and an optional cgroup into JSONL."""
import argparse
import contextlib
import datetime
import errno
import json
import math
import os
from pathlib import Path
import sys
import time


def timestamp():
    return {
        "monotonic_ns": time.monotonic_ns(),
        "timestamp_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    }


def failure(exc):
    if isinstance(exc, PermissionError):
        status = "permission_denied"
    elif isinstance(exc, FileNotFoundError) or getattr(exc, "errno", None) == errno.ESRCH:
        status = "not_found"
    elif isinstance(exc, ValueError):
        status = "parse_error"
    else:
        status = "read_error"
    return {"status": status, "errno": getattr(exc, "errno", None), "error": str(exc)}


def parse_stat(text):
    # comm may contain spaces and closing parentheses; fields follow its last ')'.
    begin, end = text.find("("), text.rfind(")")
    if begin < 1 or end < begin:
        raise ValueError("invalid proc stat command field")
    fields = text[end + 1:].split()
    if len(fields) < 22:
        raise ValueError("truncated proc stat")
    result = {"pid": int(text[:begin].strip()), "comm": text[begin + 1:end], "state": fields[0]}
    for name, field in (("minflt", 10), ("cminflt", 11), ("majflt", 12),
                        ("cmajflt", 13), ("utime", 14), ("stime", 15),
                        ("cutime", 16), ("cstime", 17), ("num_threads", 20),
                        ("starttime", 22), ("vsize", 23), ("rss", 24)):
        result[name] = int(fields[field - 3])
    return result


def parse_colon(text):
    """Keep native units, including unitless counts in meminfo."""
    values, units = {}, {}
    for line in text.splitlines():
        if line.endswith("[rollup]") or ":" not in line:  # Mapping header contains a device colon.
            continue
        key, value = line.split(":", 1)
        parts = value.split()
        if not parts or len(parts) > 2:
            raise ValueError("invalid numeric field: " + line)
        values[key] = int(parts[0])
        units[key] = parts[1] if len(parts) == 2 else "native"
    if not values:
        raise ValueError("no numeric fields")
    return {"values": values, "units": units}


def parse_pairs(text):
    result = {}
    for line in text.splitlines():
        key, value = line.split()
        result[key] = int(value)
    return result


def parse_io_stat(text):
    result = {}
    for line in text.splitlines():
        fields = line.split()
        device = fields[0]
        if device in result:
            raise ValueError("duplicate cgroup I/O device: " + device)
        result[device] = {key: int(value) for key, value in (part.split("=", 1) for part in fields[1:])}
    return result


def read_metric(path, parser):
    try:
        return {"status": "ok", "data": parser(path.read_text())}
    except (OSError, ValueError) as exc:
        return failure(exc)


class ProcessSampler:
    def __init__(self, pid, proc_root=Path("/proc")):
        self.pid = pid
        self.directory = proc_root / str(pid)
        self.expected_starttime = None

    def sample(self):
        result = {"pid": self.pid, "expected_starttime_ticks": self.expected_starttime,
                  "identity_verified": False, "read_started_monotonic_ns": time.monotonic_ns()}
        before = read_metric(self.directory / "stat", parse_stat)
        if before["status"] != "ok":
            result.update(status="exited" if before["status"] == "not_found" and
                          self.expected_starttime is not None else before["status"], identity=before)
            return self.finish(result)
        stat = before["data"]
        if stat["pid"] != self.pid:
            result.update(status="identity_mismatch", identity=before)
            return self.finish(result)
        if self.expected_starttime is None:
            self.expected_starttime = stat["starttime"]
        result["expected_starttime_ticks"] = self.expected_starttime
        result["observed_starttime_ticks"] = stat["starttime"]
        if stat["starttime"] != self.expected_starttime:
            result.update(status="pid_reused", identity=before)
            return self.finish(result)
        smaps = read_metric(self.directory / "smaps_rollup", parse_colon)
        io = read_metric(self.directory / "io", parse_colon)
        after = read_metric(self.directory / "stat", parse_stat)
        if after["status"] != "ok":
            result.update(status="exited_during_sample" if after["status"] == "not_found"
                          else "identity_unverified", identity=after)
        elif after["data"]["pid"] != self.pid or after["data"]["starttime"] != self.expected_starttime:
            result.update(status="pid_reused_during_sample", identity=after)
        else:
            result.update(status="ok" if smaps["status"] == io["status"] == "ok" else "partial",
                          identity_verified=True, stat=stat, stat_after=after["data"],
                          smaps_rollup=smaps, io=io)
        # Discard metric reads if the process identity could not be verified twice.
        return self.finish(result)

    @staticmethod
    def finish(result):
        result["read_finished_monotonic_ns"] = time.monotonic_ns()
        return result


class CgroupSampler:
    def __init__(self, directory):
        self.directory = directory
        self.expected_identity = self.identity()

    def identity(self):
        stat = self.directory.stat()
        return [stat.st_dev, stat.st_ino]

    def sample(self):
        result = {"path": str(self.directory), "expected_device_inode": self.expected_identity}
        try:
            identity = self.identity()
            if identity != self.expected_identity:
                return dict(result, status="replaced", observed_device_inode=identity)
            metrics = {}
            for name, parser in (("memory.current", lambda text: int(text.strip())),
                                 ("memory.stat", parse_pairs), ("memory.events", parse_pairs),
                                 ("cpu.stat", parse_pairs), ("io.stat", parse_io_stat)):
                metrics[name] = read_metric(self.directory / name, parser)
            if self.identity() != self.expected_identity:
                return dict(result, status="replaced_during_sample")
            return dict(result, status="ok" if all(value["status"] == "ok" for value in metrics.values())
                        else "partial", metrics=metrics)
        except OSError as exc:
            return dict(result, **failure(exc))


def positive_pid(value):
    pid = int(value)
    if pid <= 0:
        raise argparse.ArgumentTypeError("PID must be positive")
    return pid


def bounded_number(minimum, maximum, minimum_inclusive=True):
    def convert(value):
        number = float(value)
        if not math.isfinite(number) or number > maximum or (
                number < minimum if minimum_inclusive else number <= minimum):
            raise argparse.ArgumentTypeError(f"value must be {'at least' if minimum_inclusive else 'greater than'} {minimum} and at most {maximum}")
        return number
    return convert


def argument_parser():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pid", action="append", required=True, type=positive_pid,
                        help="explicit PID; repeat for each process, identities are never replaced")
    parser.add_argument("--cgroup", type=Path, help="optional absolute cgroup v2 directory")
    parser.add_argument("--interval", type=bounded_number(0.1, 3600), default=1.0, metavar="SECONDS")
    parser.add_argument("--duration", type=bounded_number(0, 3600, False), default=10.0, metavar="SECONDS")
    parser.add_argument("--output", type=Path, help="create this new JSONL file exclusively; otherwise stdout")
    return parser


def run(args, stream):
    processes = [ProcessSampler(pid) for pid in dict.fromkeys(args.pid)]
    cgroup = CgroupSampler(args.cgroup) if args.cgroup is not None else None
    start = time.monotonic()
    end = start + args.duration
    emit = lambda record: print(json.dumps(dict(record, **timestamp()), sort_keys=True), file=stream, flush=True)
    emit({"type": "metadata", "schema_version": 1, "pids": [p.pid for p in processes],
          "cgroup": str(args.cgroup) if args.cgroup else None, "interval_s": args.interval,
          "duration_s": args.duration, "clock_ticks_per_second": os.sysconf("SC_CLK_TCK"),
          "page_size_bytes": os.sysconf("SC_PAGE_SIZE"),
          "boot_id": read_metric(Path("/proc/sys/kernel/random/boot_id"), str.strip),
          "units": {"stat_cpu_and_starttime": "clock_ticks", "stat_rss": "pages",
                    "stat_vsize": "bytes", "proc_kB": "1024 bytes",
                    "proc_io": "bytes except syscr/syscw syscall counts",
                    "cgroup_memory_current": "bytes", "cgroup_cpu_stat_usec": "microseconds",
                    "cgroup_other_fields": "raw kernel-native units"},
          "aggregation": "none; process shared RSS and cgroup charges are not physical-memory totals"})
    sequence, next_due, reason = 0, start, "duration"
    try:
        while time.monotonic() < end:
            delay = min(next_due, end) - time.monotonic()
            if delay > 0:
                time.sleep(delay)
            if time.monotonic() >= end:
                break
            begun = time.monotonic_ns()
            record = {"type": "sample", "sequence": sequence, "read_started_monotonic_ns": begun,
                      "elapsed_s": time.monotonic() - start,
                      "processes": [process.sample() for process in processes],
                      "host_meminfo": read_metric(Path("/proc/meminfo"), parse_colon)}
            if cgroup is not None:
                record["cgroup"] = cgroup.sample()
            record["read_finished_monotonic_ns"] = time.monotonic_ns()
            emit(record)
            sequence += 1
            # Skip missed slots instead of producing a burst of delayed samples.
            next_due = start + max(sequence, math.floor((time.monotonic() - start) / args.interval) + 1) * args.interval
    except KeyboardInterrupt:
        reason = "interrupted"
    emit({"type": "end", "reason": reason, "samples": sequence, "elapsed_s": time.monotonic() - start})
    return 0


def main(argv=None):
    parser = argument_parser()
    args = parser.parse_args(argv)
    if args.cgroup is not None:
        if not args.cgroup.is_absolute():
            parser.error("--cgroup must be an absolute cgroup v2 directory")
        try:
            args.cgroup = args.cgroup.resolve(strict=True)
            (args.cgroup / "cgroup.controllers").stat()
        except OSError as exc:
            parser.error("--cgroup v2 interface unavailable: " + json.dumps(failure(exc)))
    try:
        with args.output.open("x", encoding="utf-8") if args.output else contextlib.nullcontext(sys.stdout) as stream:
            return run(args, stream)
    except (OSError, ValueError) as exc:
        parser.error(str(exc))
    return 2


if __name__ == "__main__":
    sys.exit(main())
