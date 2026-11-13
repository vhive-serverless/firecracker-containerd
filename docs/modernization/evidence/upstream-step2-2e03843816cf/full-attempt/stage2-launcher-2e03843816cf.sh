#!/usr/bin/env bash
set -euo pipefail
# Fresh corrected source; never reuse historical recovery mutations.
if [[ $# != 2 || ! $1 =~ ^[0-9a-f]{40}$ || $2 != /* ]]; then
    echo "usage: $0 REVIEWED_COMMIT /absolute/corrected-checkout" >&2
    exit 2
fi
expected_revision=$1
launcher_source=$(realpath -- "${BASH_SOURCE[0]}")
cd -- "$2"
test "$(git rev-parse --show-toplevel)" = "$(pwd -P)"
test -z "$(git status --porcelain)"
test "$(git rev-parse HEAD)" = "$expected_revision"
git merge-base --is-ancestor 7386ab5c5e8c3519ad1698c27f548803e714e304 HEAD
export STACK_DIR=/users/nehalem/fc-ctrd-mod
export PATH="$STACK_DIR/toolchains/go1.26.11/bin:$PATH"
export STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
export GOTOOLCHAIN=local GOWORK=off
unset DISABLE_ROOT_TESTS ENABLE_ISOLATED_TESTS VOLFS_MOUNT_TESTS FICD_VOLFS_VM_TEST FC_TEST_BIN
export VHIVE_SOURCE_REPOSITORY="$STACK_DIR/upstream-migration/vhive-source.git"
revision=$(git rev-parse --short=12 HEAD)
export DOCKER_IMAGE_TAG="$revision"
mkdir -p ../evidence
status_file="../evidence/stage2-command-status-$revision.tsv"
(set -o noclobber; printf 'step\texit_code\n' > "$status_file")
git rev-parse HEAD HEAD^{tree} > "../evidence/stage2-tested-source-$revision.txt"
cp -- "$launcher_source" "../evidence/stage2-launcher-$revision.sh"
run_logged() {
    local step=$1 status logfile
    shift
    logfile="../evidence/stage2-$step-$revision.log"
    test ! -e "$logfile"
    if "$@" > "$logfile" 2>&1; then
        printf '%s\t0\n' "$step" >> "$status_file"
    else
        status=$?
        printf '%s\t%s\n' "$step" "$status" >> "$status_file"
        return "$status"
    fi
}
run_logged build tools/modernize/build-stack.sh
run_logged install tools/modernize/install-stack.sh
run_logged test-images make test-images
run_logged vhive tools/vhive/test-vhive.sh
grep -qx VHIVE_WORKER_CANDIDATE_OK "../evidence/stage2-vhive-$revision.log"
run_logged acceptance tools/modernize/test-stack.sh
grep -qx COMMITTED_STACK_ACCEPTANCE_OK "../evidence/stage2-acceptance-$revision.log"
test -z "$(git status --porcelain)"
echo STAGE2_SAME_ID_VHIVE_AND_STACK_ACCEPTANCE_OK
