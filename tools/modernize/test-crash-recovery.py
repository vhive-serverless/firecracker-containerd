#!/usr/bin/env python3
"""Crash the dedicated worker stack's shim and verify its VMM is reaped."""
import json
import os
from pathlib import Path
import signal
import subprocess
import time
import uuid

src = Path(os.environ.get("RUNTIME_SRC", str(Path(__file__).resolve().parents[2])))
run_name = os.environ.get("RUN_NAME", "fcmod-modernize")
work = Path(os.environ.get("WORK_DIR", str(src.parent / "modernize-run")))
state = Path("/run") / run_name
ctr = [str(src / "firecracker-control/cmd/containerd/firecracker-ctr"), "--address", str(state / "containerd.sock")]
name = "crash-" + uuid.uuid4().hex[:12]

def command(*args, check=True):
    return subprocess.run(ctr + list(args), check=check, text=True, capture_output=True)

def identity(pid):
    try:
        fields = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()
        return fields[19] if fields[0] != "Z" else None
    except FileNotFoundError:
        return None

try:
    command("run", "--detach", "--runtime", "aws.firecracker", "--snapshotter", "devmapper",
            "docker.io/library/alpine:3.23.0", name, "/bin/sleep", "300")
    vmid = (state / "io.containerd.runtime.v2.task/default" / name / "firecracker-vm-id").read_text().strip()
    directory = work / "shim" / ("default#" + vmid)
    metadata = json.loads((directory / "recovery.json").read_text())
    print(json.dumps(metadata, indent=2), flush=True)
    if os.environ.get("RESTART_CONTROLLER") == "1":
        assert run_name.startswith("fcmod-")
        command("tasks", "exec", "--exec-id", "write-state", name,
                "/bin/sh", "-ec", "echo controller-restart > /restart-state")
        def main_pid():
            return int(subprocess.check_output(
                ["systemctl", "show", run_name, "--property=MainPID", "--value"], text=True))
        previous = main_pid()
        assert previous > 1
        controller_args = Path(f"/proc/{previous}/cmdline").read_bytes()
        assert b"firecracker-containerd" in controller_args and str(work / "config.toml").encode() in controller_args
        subprocess.run(["systemctl", "kill", "--kill-whom=main", "--signal=KILL", run_name], check=True)
        deadline = time.monotonic() + 10
        while main_pid() == previous and time.monotonic() < deadline:
            time.sleep(0.1)
        subprocess.run(["systemctl", "restart", run_name], check=True)
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            if command("version", check=False).returncode == 0:
                break
            time.sleep(0.2)
        assert main_pid() > 1 and main_pid() != previous
        result = command("tasks", "exec", "--exec-id", "read-state", name, "/bin/cat", "/restart-state")
        assert result.stdout.strip() == "controller-restart", result
        print("CONTROLLER_RESTART_RECONNECT_OK", flush=True)
    for key in ("shim", "vmm"):
        process = metadata[key]
        assert process["pid"] > 1 and identity(process["pid"]) == process["start_ticks"], key
    shim = metadata["shim"]
    fd = os.pidfd_open(shim["pid"])
    try:
        assert identity(shim["pid"]) == shim["start_ticks"]
        signal.pidfd_send_signal(fd, signal.SIGKILL)
    finally:
        os.close(fd)
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        gone = all(identity(metadata[key]["pid"]) != metadata[key]["start_ticks"] for key in ("shim", "vmm"))
        if gone and not directory.exists():
            break
        time.sleep(0.2)
    assert gone, "shim or VMM survived crash recovery"
    assert not directory.exists(), "VM directory was retained after cleanup"
    print("SHIM_CRASH_RECOVERY_OK", flush=True)
finally:
    command("tasks", "kill", "--signal", "SIGKILL", name, check=False)
    command("tasks", "delete", "--force", name, check=False)
    result = command("containers", "delete", name, check=False)
    if result.returncode:
        print(result.stderr, flush=True)
