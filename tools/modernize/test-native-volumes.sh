#!/usr/bin/env bash
# Build and run only on the designated worker, using its pinned stack artifacts.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
cd "$repo_dir"
: "${FC_NATIVE_VMM:?set FC_NATIVE_VMM to the pinned Firecracker binary}"
: "${FC_NATIVE_KERNEL:?set FC_NATIVE_KERNEL to the pinned guest kernel}"
FC_NATIVE_VMM=$(realpath "$FC_NATIVE_VMM")
FC_NATIVE_KERNEL=$(realpath "$FC_NATIVE_KERNEL")
fixture=$(mktemp -d /tmp/fc-volume-fixture.XXXXXX)
trap 'rm -rf "$fixture"' EXIT
CGO_ENABLED=0 go build -mod=readonly -trimpath -o "$fixture/init" ./tools/native-volume-guest
python3 - "$fixture/init" "$fixture/initrd.cpio" <<'PY'
from pathlib import Path
import stat
import sys

with open(sys.argv[2], "wb") as archive:
    def entry(name, mode, data=b"", major=0, minor=0):
        filename = name.encode() + b"\0"
        fields = (1, mode, 0, 0, 1, 0, len(data), 0, 0, major, minor, len(filename), 0)
        header = b"070701" + b"".join(f"{value:08x}".encode() for value in fields)
        archive.write(header + filename)
        archive.write(b"\0" * (-len(header + filename) % 4))
        archive.write(data)
        archive.write(b"\0" * (-len(data) % 4))

    for name in ("dev", "proc", "sys"):
        entry(name, stat.S_IFDIR | 0o755)
    entry("dev/console", stat.S_IFCHR | 0o600, major=5, minor=1)
    entry("init", stat.S_IFREG | 0o755, Path(sys.argv[1]).read_bytes())
    entry("TRAILER!!!", 0)
PY
go test -mod=readonly -c -o "$fixture/native-volume.test" ./internal/volfs
sha256sum "$FC_NATIVE_VMM" "$FC_NATIVE_KERNEL" "$fixture/init" "$fixture/initrd.cpio" "$fixture/native-volume.test"
sudo -n env FICD_VOLFS_VM_TEST=1 FC_NATIVE_VMM="$FC_NATIVE_VMM" \
    FC_NATIVE_KERNEL="$FC_NATIVE_KERNEL" FC_NATIVE_INITRD="$fixture/initrd.cpio" \
    "$fixture/native-volume.test" -test.v -test.count=1 -test.timeout=120s \
    -test.run='^TestVolumeCheckpointRealVM$'
