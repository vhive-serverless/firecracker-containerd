#!/usr/bin/env bash
set -euo pipefail
if [[ $# != 2 || ! $1 =~ ^[0-9a-f]{40}$ || $2 != /* ]]; then
    echo "usage: $0 REVIEWED_COMMIT /absolute/corrected-checkout" >&2
    exit 2
fi
expected_revision=$1
launcher_source=$(realpath -- "${BASH_SOURCE[0]}")
cd -- "$2"
test "$(git rev-parse --show-toplevel)" = "$(pwd -P)"
git merge-base --is-ancestor 332da2a7065baa0050037250bbf2ff316a1a8519 HEAD
test -z "$(git status --porcelain)"
test "$(git rev-parse HEAD)" = "$expected_revision"
export STACK_DIR=/users/nehalem/fc-ctrd-mod
export PATH="$STACK_DIR/toolchains/go1.26.11/bin:$PATH"
export STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
export GOTOOLCHAIN=local GOWORK=off
unset DISABLE_ROOT_TESTS ENABLE_ISOLATED_TESTS VOLFS_MOUNT_TESTS FICD_VOLFS_VM_TEST FC_TEST_BIN
revision=$(git rev-parse --short=12 HEAD)
evidence=$(cd .. && pwd)/evidence
mkdir -p "$evidence"
status_file="$evidence/stage3-command-status-$revision.tsv"
test ! -e "$status_file"
(set -o noclobber; printf 'step\texit_code\n' > "$status_file")
cp -- "$launcher_source" "$evidence/stage3-launcher-$revision.sh"
git rev-parse HEAD HEAD^{tree} > "$evidence/stage3-tested-source-$revision.txt"
run_logged() {
    local step=$1 status
    shift
    test ! -e "$evidence/stage3-$step-$revision.log"
    if "$@" > "$evidence/stage3-$step-$revision.log" 2>&1; then
        printf '%s\t0\n' "$step" >> "$status_file"
    else
        status=$?
        printf '%s\t%s\n' "$step" "$status" >> "$status_file"
        return "$status"
    fi
}
run_logged build tools/modernize/build-stack.sh
run_logged install tools/modernize/install-stack.sh
run_logged native tools/modernize/test-native-restore.sh
run_logged volumes env FC_NATIVE_VMM="$PWD/bin/firecracker" FC_NATIVE_KERNEL="$PWD/default-vmlinux.bin" \
    tools/modernize/test-native-volumes.sh
run_logged cri-focused env CRI_INTEGRATION_TESTS=1 CRI_FOCUS='Runtime info|starting container with volume' \
    tools/critest/test-cri.sh
grep -qx COMMITTED_NATIVE_RUNTIME_RESTORE_OK "$evidence/stage3-native-$revision.log"
grep -q '^--- PASS: TestVolumeCheckpointRealVM ' "$evidence/stage3-volumes-$revision.log"
grep -qx CRI_WORKER_TESTS_PASSED "$evidence/stage3-cri-focused-$revision.log"
test -z "$(git status --porcelain)"
echo STAGE3_SAME_ID_NATIVE_VOLUME_AND_FOCUSED_CRI_OK
