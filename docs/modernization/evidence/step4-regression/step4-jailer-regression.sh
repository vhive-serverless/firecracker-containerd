#!/usr/bin/env bash
set -euo pipefail
root=/users/nehalem/fc-ctrd-mod
cd "$root/step4-regression-fix"
export PATH="$root/toolchains/go1.26.11/bin:$PATH"
export STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
export DOCKER_IMAGE_TAG=72f4fc9173be FICD_DM_VOLUME_GROUP=''
export FICD_DM_POOL=fc-step4-jailer-5fb79f1
export FICD_VM_STORAGE="$root/.ficd-test-vms/step4-jailer-5fb79f1"
test "$(git rev-parse HEAD)" = 5fb79f124f1d00867aec673618d387b006be86de
test -z "$(git status --porcelain)"
test ! -e "$FICD_VM_STORAGE"
if sudo -n dmsetup info "$FICD_DM_POOL" >/dev/null 2>&1; then exit 1; fi
mkdir -p "$FICD_VM_STORAGE/.pmem-images"
sudo -n cp --sparse=always "$root/.ficd-test-vms/step4-native-final/.pmem-images/"*.img "$FICD_VM_STORAGE/.pmem-images/"
find "$FICD_VM_STORAGE/.pmem-images" -type f -exec sha256sum {} \;
cleanup() {
    tools/thinpool.sh remove "$FICD_DM_POOL"
    sudo -n rm -rf -- "$FICD_VM_STORAGE"
}
tools/thinpool.sh create "$FICD_DM_POOL"
trap cleanup EXIT
make -k -C runtime integ-test-TestAttachBlockDevice_Isolated integ-test-TestJailer_Isolated EXTRAGOARGS='-v -count=1 -timeout=1200s'
test -z "$(git status --porcelain)"
echo COMMITTED_JAILER_CACHE_REGRESSION_OK
