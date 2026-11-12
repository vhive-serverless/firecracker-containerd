#!/usr/bin/env bash
# Provisional candidate checks against the container's exact runtime stack.
set -euo pipefail
export FICD_LOG_DIR=${FICD_LOG_DIR:-/var/log/firecracker-containerd-test}
if ! command -v route >/dev/null 2>&1; then
    echo "vHive networking requires route (net-tools) in the acceptance image" >&2
    exit 1
fi
socket=/run/firecracker-containerd/containerd.sock
for attempt in $(seq 1 30); do
    if ctr --address "$socket" version >/dev/null 2>&1; then break; fi
    sleep 1
done
ctr --address "$socket" version | tee "$FICD_LOG_DIR/vhive-runtime-version"
test "$(awk -v rev="$VHIVE_RUNTIME_REVISION" '$1 == "Revision:" && $2 == rev {n++} END {print n+0}' "$FICD_LOG_DIR/vhive-runtime-version")" = 2
vhive-acceptance.test -test.v -test.count=1 -test.timeout=15m \
    '-test.run=^(TestPauseSnapResume|TestSnapshotConcurrentClones|TestSnapshotSourceMemoryAndDiskIsolation)$' \
    -ss=devmapper -upf=false -img="${VHIVE_FUNCTION_IMAGE:?missing immutable function image}" \
    2>&1 | tee "$FICD_LOG_DIR/vhive-tests.log"
for name in TestPauseSnapResume TestSnapshotConcurrentClones TestSnapshotSourceMemoryAndDiskIsolation; do
    grep -q "^--- PASS: $name (" "$FICD_LOG_DIR/vhive-tests.log"
done
test -z "$(ctr --address "$socket" --namespace firecracker-containerd tasks list --quiet)"
test -z "$(ctr --address "$socket" --namespace firecracker-containerd containers list --quiet)"
echo VHIVE_LEGACY_SNAPSHOT_CANDIDATE_OK
