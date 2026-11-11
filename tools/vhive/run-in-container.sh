#!/usr/bin/env bash
# Run actual vHive orchestration and function RPCs within the test network.
set -euo pipefail
export FICD_LOG_DIR=${FICD_LOG_DIR:-/var/log/firecracker-containerd-test}
image=${VHIVE_FUNCTION_IMAGE:?missing pinned function image}
socket=/run/firecracker-containerd/containerd.sock
for attempt in $(seq 1 30); do
    if ctr --address "$socket" version >/dev/null 2>&1; then break; fi
    sleep 1
done
ctr --address "$socket" version | tee "$FICD_LOG_DIR/vhive-runtime-version"
test "$(awk -v rev="$VHIVE_RUNTIME_REVISION" '$1 == "Revision:" && $2 == rev {n++} END {print n+0}' "$FICD_LOG_DIR/vhive-runtime-version")" = 2
cat > /tmp/vhive-stock.toml <<'CONFIG'
version = 4
root = "/var/lib/vhive-stock"
state = "/run/vhive-stock"
[plugins."io.containerd.server.v1.grpc"]
  address = "/run/containerd/containerd.sock"
[plugins."io.containerd.server.v1.ttrpc"]
  address = "/run/containerd/containerd.sock.ttrpc"
CONFIG
/usr/local/libexec/vhive-stock/containerd --config /tmp/vhive-stock.toml \
    > "$FICD_LOG_DIR/vhive-stock.out" 2>&1 &
stock_pid=$!
cleanup() {
    for pid in "${vhive_pid:-}" "$stock_pid"; do
        if [ -n "$pid" ]; then
            kill "$pid" 2>/dev/null || true
            wait "$pid" || true
        fi
    done
}
trap cleanup EXIT
for attempt in $(seq 1 30); do
    if ctr --address /run/containerd/containerd.sock version >/dev/null 2>&1; then break; fi
    sleep 1
done
ctr --address /run/containerd/containerd.sock version
vhive -snapshots -ss=devmapper -upf=false -dbg -hostIface eth0 -netPoolSize 2 -vethPrefix 198.18 -clonePrefix 198.19 \
    > "$FICD_LOG_DIR/vhive.out" 2>&1 &
vhive_pid=$!
for attempt in $(seq 1 30); do
    if ( : </dev/tcp/127.0.0.1/3333 ) 2>/dev/null; then break; fi
    if ! kill -0 "$vhive_pid" 2>/dev/null; then
        tail -40 "$FICD_LOG_DIR/vhive.out" >&2
        exit 1
    fi
    sleep 1
done

capture_vm() {
    python3 - "$1" <<'PY'
import json
from pathlib import Path
import subprocess
import sys
files = list(Path("/srv/firecracker_containerd_tests").glob("*/recovery.json"))
assert len(files) == 1, files
state = json.loads(files[0].read_text())
pid = state["vmm"]["pid"]
assert pid > 1 and Path(f"/proc/{pid}").exists(), state
assert state["netns"] and Path(state["netns"]).exists() and not state["own_netns"], state
config = json.loads(subprocess.check_output([
    "curl", "--fail", "--silent", "--show-error", "--unix-socket",
    str(files[0].parent / "firecracker.sock"), "http://localhost/vm/config",
]))
disk = next(drive["path_on_host"] for drive in config["drives"] if not drive["is_root_device"])
assert disk.startswith("/dev/mapper/"), disk
result = {
    "vm_id": state["vm_id"],
    "vmm_pid": pid,
    "start_ticks": state["vmm"]["start_ticks"],
    "disk": disk,
    "netns": state["netns"],
}
Path(sys.argv[1]).write_text(json.dumps(result, indent=2) + "\n")
print(json.dumps(result))
PY
}

assert_stopped() {
    python3 - "$1" <<'PY'
import json
from pathlib import Path
import sys
import time

state = json.loads(Path(sys.argv[1]).read_text())
stat = Path(f"/proc/{state['vmm_pid']}/stat")
deadline = time.monotonic() + 20
while True:
    try:
        fields = stat.read_text().rsplit(")", 1)[1].split()
        alive = fields[0] != "Z" and fields[19] == str(state["start_ticks"])
    except FileNotFoundError:
        alive = False
    metadata = list(Path("/srv/firecracker_containerd_tests").glob("*/recovery.json"))
    disk_exists = Path(state["disk"]).exists()
    if not alive and not metadata and not disk_exists:
        break
    assert time.monotonic() < deadline, (state, alive, metadata, disk_exists)
    time.sleep(0.2)
print(f"removed VM {state['vm_id']} (PID {state['vmm_pid']}) and {state['disk']}")
PY
}

assert_no_host_tasks() {
    local tasks containers
    tasks=$(ctr --address "$socket" --namespace firecracker-containerd tasks list --quiet)
    containers=$(ctr --address "$socket" --namespace firecracker-containerd containers list --quiet)
    test -z "$tasks"
    test -z "$containers"
}

function_id=901
vhive-probe start "$function_id" "$image"
vhive-probe invoke "$function_id" "$image" before-one false
vhive-probe invoke "$function_id" "$image" before-two false
capture_vm "$FICD_LOG_DIR/vhive-source.json"
find /fccd/snapshots -name snap_file -size +0c | tee "$FICD_LOG_DIR/vhive-snapshot-paths"
test -s "$FICD_LOG_DIR/vhive-snapshot-paths"
test -s "/fccd/snapshots/$function_id/mem_file"
test -s "/fccd/snapshots/$function_id/info_file"
test -s "/fccd/snapshots/$function_id/disk_file"
ls -l "/fccd/snapshots/$function_id" | tee "$FICD_LOG_DIR/vhive-artifacts"
vhive-probe stop "$function_id"
assert_stopped "$FICD_LOG_DIR/vhive-source.json"
assert_no_host_tasks

vhive-probe invoke "$function_id" "$image" after-restore true
vhive-probe invoke "$function_id" "$image" after-warm false
capture_vm "$FICD_LOG_DIR/vhive-restored.json"
# This is a resumed guest, not an OCI cold boot under the same function ID.
assert_no_host_tasks
python3 - "$FICD_LOG_DIR" <<'PY'
import json
from pathlib import Path
import sys
logs = Path(sys.argv[1])
source, restored = [json.loads((logs / f"vhive-{phase}.json").read_text()) for phase in ("source", "restored")]
assert source["vm_id"] != restored["vm_id"], (source, restored)
assert source["vmm_pid"] != restored["vmm_pid"], (source, restored)
assert source["disk"] != restored["disk"], (source, restored)
PY
vhive-probe stop "$function_id"
assert_stopped "$FICD_LOG_DIR/vhive-restored.json"
assert_no_host_tasks
kill "$vhive_pid"
wait "$vhive_pid"
vhive_pid=
test ! -e /fccd/snapshots
for netns in /run/netns/uvmns*; do
    test ! -e "$netns"
done
echo VHIVE_DEPLOY_INVOKE_SNAPSHOT_RESTORE_OK
