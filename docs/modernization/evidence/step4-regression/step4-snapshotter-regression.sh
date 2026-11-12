#!/usr/bin/env bash
set -euo pipefail
root=/users/nehalem/fc-ctrd-mod
cd "$root/step4-regression-fix"
test -z "$(git status --porcelain)"
git fetch origin damon-virtio-pmem
git merge --ff-only origin/damon-virtio-pmem
git rev-parse HEAD
export PATH="$root/toolchains/go1.26.11/bin:$PATH"
export STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
export DOCKER_IMAGE_TAG=72f4fc9173be FICD_DM_VOLUME_GROUP=''
export FICD_DM_POOL=fc-step4-snapshotter-f5b3ba6
export FICD_VM_STORAGE="$root/.ficd-test-vms/step4-snapshotter-f5b3ba6"
test ! -e "$FICD_VM_STORAGE"
if sudo -n dmsetup info "$FICD_DM_POOL" >/dev/null 2>&1; then exit 1; fi
mkdir -p "$FICD_VM_STORAGE"
cleanup() {
    tools/thinpool.sh remove "$FICD_DM_POOL"
    sudo -n rm -rf -- "$FICD_VM_STORAGE"
}
tools/thinpool.sh create "$FICD_DM_POOL"
trap cleanup EXIT
make -k -C snapshotter integ-test EXTRAGOARGS='-v -count=1 -timeout=300s'
test -z "$(git status --porcelain)"
echo COMMITTED_STEP4_SNAPSHOTTER_REGRESSION_OK
