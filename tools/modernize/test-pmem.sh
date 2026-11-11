#!/usr/bin/env bash
# Worker pmem/DAX component acceptance; native restore and Step 4 remain WIP.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
export PATH="$stack_dir/toolchains/go1.26.11/bin:$stack_dir/toolchains/cargo/bin:$PATH"
export STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
export FICD_DM_VOLUME_GROUP=''
cd "$repo_dir"
test -z "$(git status --porcelain)"
export DOCKER_IMAGE_TAG=$(git rev-parse --short=12 HEAD)
pool=fc-pmem-${DOCKER_IMAGE_TAG}
if sudo -n dmsetup info "$pool" >/dev/null 2>&1; then exit 1; fi
cleanup() {
    if sudo -n dmsetup info "$pool" >/dev/null 2>&1; then tools/thinpool.sh remove "$pool"; fi
}
trap cleanup EXIT
python3 tools/modernize/verify-inputs.py
make lint
go test -mod=readonly -race -count=1 -v -run '^TestImmutableRoot' ./runtime
make -C proto clean
make proto
git diff --exit-code
make image-dax
sha256sum tools/image-builder/rootfs-dax.img tools/memory-probe/fc-memory-probe
make test-images
tools/thinpool.sh create "$pool"
for name in BindImmutableImageToJail PmemRoot_Isolated PmemNativeCheckpoint_Isolated PmemBlockNativeCheckpoint_Isolated; do
    FICD_DM_POOL="$pool" make -C runtime "integ-test-Test${name}" \
        EXTRAGOARGS='-v -count=1 -timeout=600s'
done
cleanup
test -z "$(git status --porcelain)"
echo COMMITTED_PMEM_DAX_AND_CAPTURE_OK
