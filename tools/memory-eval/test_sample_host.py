# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
import contextlib
import errno
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import sample_host as sampler


def stat_line(starttime=1234, command="name with ) spaces", pid=123):
    fields = ["R"] + [str(field) for field in range(4, 53)]
    fields[19] = str(starttime)
    return f"{pid} ({command}) " + " ".join(fields)


class ParsingTests(unittest.TestCase):
    def test_stat_with_parenthesis_and_spaces(self):
        result = sampler.parse_stat(stat_line())
        self.assertEqual(result["pid"], 123)
        self.assertEqual(result["comm"], "name with ) spaces")
        self.assertEqual(result["starttime"], 1234)
        for field, expected in [("minflt", 10), ("majflt", 12), ("utime", 14),
                                ("stime", 15), ("num_threads", 20), ("rss", 24)]:
            self.assertEqual(result[field], expected)

    def test_truncated_stat(self):
        for text in ("broken", "123 (name) R 1 2"):
            with self.subTest(text=text), self.assertRaises(ValueError):
                sampler.parse_stat(text)

    def test_memory_units_and_rollup_header(self):
        values = sampler.parse_colon("00400000-00500000 r--p 0 00:00 0 [rollup]\nRss: 17 kB\nHugePages_Total: 2\n")
        self.assertEqual(values["values"], {"Rss": 17, "HugePages_Total": 2})
        self.assertEqual(values["units"], {"Rss": "kB", "HugePages_Total": "native"})

    def test_cgroup_io_multiple_devices_and_empty(self):
        self.assertEqual(sampler.parse_io_stat("8:0 rbytes=1024 wios=2\n7:1 rios=1\n"),
                         {"8:0": {"rbytes": 1024, "wios": 2}, "7:1": {"rios": 1}})
        self.assertEqual(sampler.parse_io_stat(""), {})
        with self.assertRaises(ValueError):
            sampler.parse_io_stat("8:0 rbytes=1\n8:0 rbytes=2")

    def test_permission_is_explicit(self):
        with patch.object(Path, "read_text", side_effect=PermissionError(errno.EACCES, "denied")):
            result = sampler.read_metric(Path("unreadable"), sampler.parse_colon)
        self.assertEqual(result["status"], "permission_denied")
        self.assertEqual(result["errno"], errno.EACCES)


class IdentityTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.proc = self.root / "123"
        self.proc.mkdir()
        (self.proc / "stat").write_text(stat_line())
        (self.proc / "smaps_rollup").write_text("Rss: 12 kB\nPss: 8 kB\n")
        (self.proc / "io").write_text("rchar: 5\nread_bytes: 4096\n")
        self.sampler = sampler.ProcessSampler(123, self.root)

    def test_reuse_is_not_adopted(self):
        self.assertTrue(self.sampler.sample()["identity_verified"])
        (self.proc / "stat").write_text(stat_line(starttime=5678))
        result = self.sampler.sample()
        self.assertEqual(result["status"], "pid_reused")
        self.assertFalse(result["identity_verified"])
        self.assertNotIn("smaps_rollup", result)
        self.assertEqual(result["expected_starttime_ticks"], 1234)

    def test_exit_after_previous_observation(self):
        self.sampler.sample()
        (self.proc / "stat").unlink()
        self.assertEqual(self.sampler.sample()["status"], "exited")

    def test_reuse_during_collection_discards_metrics(self):
        original = Path.read_text
        def read(path, *args, **kwargs):
            if path.name == "io":
                (self.proc / "stat").write_text(stat_line(starttime=5678))
            return original(path, *args, **kwargs)
        with patch.object(Path, "read_text", read):
            result = self.sampler.sample()
        self.assertEqual(result["status"], "pid_reused_during_sample")
        self.assertNotIn("io", result)
        self.assertNotIn("smaps_rollup", result)

    def test_cgroup_replacement_discards_metrics(self):
        group = self.root / "cgroup"
        group.mkdir()
        subject = sampler.CgroupSampler(group)
        with patch.object(subject, "identity", return_value=[0, 0]):
            result = subject.sample()
        self.assertEqual(result["status"], "replaced")
        self.assertNotIn("metrics", result)


class BoundsTests(unittest.TestCase):
    def test_nonfinite_and_out_of_bounds(self):
        parser = sampler.argument_parser()
        for option, value in [("--interval", "0.09"), ("--interval", "nan"),
                              ("--interval", "inf"), ("--interval", "3601"),
                              ("--duration", "0"), ("--duration", "3600.01"),
                              ("--duration", "nan"), ("--pid", "0")]:
            with self.subTest(option=option, value=value), contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as exc:
                    parser.parse_args(["--pid", "123", option, value])
                self.assertEqual(exc.exception.code, 2)
        args = parser.parse_args(["--pid", "123", "--interval", "0.1", "--duration", "3600"])
        self.assertEqual((args.interval, args.duration), (0.1, 3600.0))

    def test_existing_output_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "existing.jsonl"
            output.write_text("retain this\n")
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                sampler.main(["--pid", "123", "--output", str(output)])
            self.assertEqual(output.read_text(), "retain this\n")

    def test_record_timestamps(self):
        stamp = sampler.timestamp()
        self.assertGreater(stamp["monotonic_ns"], 0)
        self.assertTrue(stamp["timestamp_utc"].endswith("+00:00"))
        json.dumps(stamp)


if __name__ == "__main__":
    unittest.main()
