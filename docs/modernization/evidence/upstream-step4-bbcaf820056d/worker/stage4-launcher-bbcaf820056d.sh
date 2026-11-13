#!/usr/bin/env bash
set -euo pipefail

# Prepared successor: exact reviewed source and a fresh checkout are required.
# Rebuilds runtime artifacts because same-ID restore corrections change production.
if [[ $# != 2 || ! $1 =~ ^[0-9a-f]{40}$ || $2 != /* ]]; then
    echo "usage: $0 REVIEWED_COMMIT /absolute/corrected-checkout" >&2
    exit 2
fi
expected_revision=$1
launcher_source=$(realpath -- "${BASH_SOURCE[0]}")
cd -- "$2"
test "$(git rev-parse --show-toplevel)" = "$(pwd -P)"
git merge-base --is-ancestor 535e094d36ff2689d254bdefd2d0452ea37c9ef0 "$expected_revision"
test -z "$(git status --porcelain)"
test "$(git rev-parse HEAD)" = "$expected_revision"
export STACK_DIR=/users/nehalem/fc-ctrd-mod
export PATH="$STACK_DIR/toolchains/go1.26.11/bin:$PATH"
export STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
unset ENABLE_ISOLATED_TESTS VOLFS_MOUNT_TESTS FICD_VOLFS_VM_TEST
unset FICD_EVAL_ARM FICD_EVAL_PROVENANCE FC_TEST_BIN DISABLE_ROOT_TESTS
revision=$(git rev-parse --short=12 HEAD)
evidence=$(cd .. && pwd)/evidence
mkdir -p "$evidence"
status_file="$evidence/stage4-command-status-$revision.tsv"
if test -e "$status_file"; then
    echo "Existing Stage 4 status/evidence for $revision; refusing to overwrite" >&2
    exit 2
fi
(set -o noclobber; printf 'step\texit_code\n' > "$status_file")
git rev-parse HEAD HEAD^{tree} > "$evidence/stage4-tested-source-$revision.txt"
git submodule status > "$evidence/stage4-submodules-$revision.txt"
cp -- "$launcher_source" "$evidence/stage4-launcher-$revision.sh"
sha256sum "$evidence/stage4-launcher-$revision.sh" > "$evidence/stage4-launcher-$revision.sha256"

run_logged() {
    local step=$1 status log
    shift
    log="$evidence/stage4-$step-$revision.log"
    if test -e "$log"; then
        echo "Existing Stage 4 log $log; refusing to overwrite" >&2
        printf '%s\t2\n' "$step" >> "$status_file"
        return 2
    fi
    if "$@" > "$log" 2>&1; then
        printf '%s\t0\n' "$step" >> "$status_file"
    else
        status=$?
        printf '%s\t%s\n' "$step" "$status" >> "$status_file"
        return "$status"
    fi
}

require_passes() {
    python3 - "$@" <<'PY'
from pathlib import Path
import re
import sys
output = Path(sys.argv[1]).read_text()
for name in sys.argv[2:]:
    if not re.search(r"^--- PASS: " + re.escape(name) + r" \(", output, re.MULTILINE):
        raise SystemExit(f"Required component test did not pass: {name}")
PY
}

run_logged build tools/modernize/build-stack.sh
run_logged install tools/modernize/install-stack.sh
# Includes lint, ordinary default-disk regressions, all dynamically enumerated
# runtime/snapshotter VM cases, DAX/stargz init, CRI units and the published SDK.
run_logged acceptance tools/modernize/test-stack.sh

# Preserve unique component checks without repeating the five VM matrices.
run_logged unit-race env DISABLE_ROOT_TESTS=1 \
    go test -mod=readonly -short -race -count=1 -timeout=180s ./...
run_logged overlay-syntax sh -n tools/image-builder/files_debootstrap/sbin/overlay-init
run_logged stargz-syntax sh -n tools/image-builder/files_stargz/usr/local/bin/prepare-stargz-storage
run_logged evaluation-unit python3 -m unittest discover -s tools/memory-eval -p 'test_*.py' -v

mkdir -p tmp
checks=$(mktemp -d "$PWD/tmp/step4-extra.XXXXXXXX")
cleanup_checks() {
    local status=${1:-0} cleanup_failed=0 devices
    if mountpoint -q "$checks/fs"; then
        if ! sudo -n umount "$checks/fs"; then cleanup_failed=1; fi
    fi
    if test -f "$checks/enospc.ext4"; then
        if devices=$(sudo -n losetup --output NAME --noheadings --associated "$checks/enospc.ext4"); then
            if test -n "$devices"; then cleanup_failed=1; fi
        else
            cleanup_failed=1
        fi
    fi
    if (( cleanup_failed )); then
        echo "Step 4 private fixture cleanup failed; retaining $checks" >&2
        return 1
    fi
    rm -rf -- "$checks"
    return "$status"
}
trap 'cleanup_checks "$?"' EXIT

run_logged runtime-probe-build env CGO_ENABLED=0 \
    go test -mod=readonly -c -o "$checks/runtime.test" ./runtime
run_logged volfs-probe-build go test -mod=readonly -race -c \
    -o "$checks/volfs.test" ./internal/volfs
sha256sum "$checks/runtime.test" "$checks/volfs.test" bin/firecracker \
    > "$evidence/stage4-extra-binaries-$revision.sha256"
go version -m "$checks/runtime.test" > "$evidence/stage4-runtime-probe-buildinfo-$revision.txt"
go version -m "$checks/volfs.test" > "$evidence/stage4-volfs-probe-buildinfo-$revision.txt"

# Static namespace isolation and official Firecracker API path-open probe.
# These start no guest and must show actual passes rather than optional skips.
run_logged namespace-api sudo -n env FC_TEST_BIN="$PWD/bin/firecracker" \
    "$checks/runtime.test" -test.v -test.count=1 -test.timeout=120s \
    -test.run='^TestRestoreNamespace(Isolation|ReleaseAPI)$'
run_logged namespace-pass-guard require_passes "$evidence/stage4-namespace-api-$revision.log" \
    TestRestoreNamespaceIsolation TestRestoreNamespaceReleaseAPI

# Exhaust only a private 32 MiB filesystem, never the worker's shared storage.
mkdir "$checks/fs"
truncate -s 32M "$checks/enospc.ext4"
mkfs.ext4 -q -F "$checks/enospc.ext4" > "$evidence/stage4-enospc-fixture-$revision.log" 2>&1
sudo -n mount -o loop "$checks/enospc.ext4" "$checks/fs" \
    >> "$evidence/stage4-enospc-fixture-$revision.log" 2>&1
run_logged host-enospc sudo -n env FICD_OVERLAY_ENOSPC_DIR="$checks/fs" \
    "$checks/runtime.test" -test.v -test.count=1 -test.timeout=120s \
    -test.run='^TestGuestOverlayHostENOSPC$'
run_logged enospc-pass-guard require_passes "$evidence/stage4-host-enospc-$revision.log" \
    TestGuestOverlayHostENOSPC
sudo -n umount "$checks/fs" >> "$evidence/stage4-enospc-fixture-$revision.log" 2>&1

# Actual host FUSE mounts are private; the separate Stage 3 volume VM runner
# covers the unchanged real-VM volfs component and keeps its own receipt.
run_logged host-fuse sudo -n env VOLFS_MOUNT_TESTS=1 \
    unshare --mount --propagation private "$checks/volfs.test" \
    -test.v -test.count=1 -test.timeout=120s \
    -test.run='^TestGuest(FilesystemLifecycleAndHostCoherence|FilesystemDirectoryCookiesAndReconnect|SingleFileAndReadOnlyMount|MountCloseDetachesBorrowedFile|KernelPermissionsAndOwnership)$'
run_logged fuse-pass-guard require_passes "$evidence/stage4-host-fuse-$revision.log" \
    TestGuestFilesystemLifecycleAndHostCoherence TestGuestFilesystemDirectoryCookiesAndReconnect \
    TestGuestSingleFileAndReadOnlyMount TestGuestMountCloseDetachesBorrowedFile \
    TestGuestKernelPermissionsAndOwnership

cleanup_checks 0
trap - EXIT
test -z "$(git status --porcelain)"
echo STAGE4_STACK_AND_UNIQUE_COMPONENT_CHECKS_OK
