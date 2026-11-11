#!/usr/bin/env bash
# Worker acceptance for DAMON + reporting. Pmem and overall Step 4 remain separate.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
export PATH="$stack_dir/toolchains/go1.26.11/bin:$stack_dir/toolchains/cargo/bin:$PATH"
export STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
export FICD_DM_VOLUME_GROUP=''
cd "$repo_dir"
test -z "$(git status --porcelain)"
export DOCKER_IMAGE_TAG=$(git rev-parse --short=12 HEAD)
pool=fc-reclaim-${DOCKER_IMAGE_TAG}
if sudo -n dmsetup info "$pool" >/dev/null 2>&1; then exit 1; fi
cleanup() {
    if sudo -n dmsetup info "$pool" >/dev/null 2>&1; then tools/thinpool.sh remove "$pool"; fi
}
trap cleanup EXIT
python3 tools/modernize/verify-inputs.py
make lint
go test -mod=readonly -race -count=1 -v -run '^TestGuestReclamation(Configuration|RejectsConflictingAndUnboundedInputs)$' ./runtime
make -C proto clean
make proto
git diff --exit-code
make test-images
tools/thinpool.sh create "$pool"
for name in FreePageReporting GuestReclamation MemoryBalloon; do
    FICD_DM_POOL="$pool" make -C runtime "integ-test-Test${name}_Isolated" \
        EXTRAGOARGS='-v -count=1 -timeout=360s'
done
cleanup
test -z "$(git status --porcelain)"
echo COMMITTED_DAMON_AND_REPORTING_OK
