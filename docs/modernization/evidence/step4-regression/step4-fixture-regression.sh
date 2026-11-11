#!/usr/bin/env bash
set -euo pipefail
root=/users/nehalem/fc-ctrd-mod
cd "$root/step4-regression-fix"
export PATH="$root/toolchains/go1.26.11/bin:$PATH"
export STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
export DOCKER_IMAGE_TAG=72f4fc9173be FICD_DM_VOLUME_GROUP=''
export FICD_DM_POOL=fc-step4-fixtures-d5739e5
export FICD_VM_STORAGE="$root/.ficd-test-vms/step4-fixtures-d5739e5"
test "$(git rev-parse HEAD)" = d5739e5d8cbb0be38c97b64260fc044e4dcddc6c
test -z "$(git status --porcelain)"
test ! -e "$FICD_VM_STORAGE"
if sudo -n dmsetup info "$FICD_DM_POOL" >/dev/null 2>&1; then exit 1; fi
mkdir -p "$FICD_VM_STORAGE"
image=localhost/firecracker-containerd-test:$DOCKER_IMAGE_TAG
docker image inspect "$image" --format '{{.Id}}'
set +e
tools/modernize/run-isolated-vm-test.sh "$FICD_VM_STORAGE" --rm --entrypoint /bin/sh "$image" -c 'touch /srv/firecracker_containerd_tests/fixture; exit 37'
code=$?
set -e
test "$code" = 37
test -z "$(find "$FICD_VM_STORAGE" -mindepth 1 -print -quit)"
echo EXPECTED_FAILURE_STATUS_AND_CLEANUP_OK
set +e
timeout --signal=TERM 5s tools/modernize/run-isolated-vm-test.sh "$FICD_VM_STORAGE" --rm --entrypoint /bin/sh "$image" -c 'touch /srv/firecracker_containerd_tests/fixture; sleep 60'
code=$?
set -e
test "$code" = 124
test -z "$(find "$FICD_VM_STORAGE" -mindepth 1 -print -quit)"
echo INTERRUPTED_CONTAINER_AND_STORAGE_CLEANUP_OK
cp --sparse=always "$root/step4-native-final/tools/image-builder/rootfs-dax.img" tools/image-builder/rootfs-dax.img
cp "$root/step4-native-final/tools/memory-probe/fc-memory-probe" tools/memory-probe/fc-memory-probe
sha256sum tools/image-builder/rootfs-dax.img tools/memory-probe/fc-memory-probe
cleanup() {
    tools/thinpool.sh remove "$FICD_DM_POOL"
    sudo -n rm -rf -- "$FICD_VM_STORAGE"
}
tools/thinpool.sh create "$FICD_DM_POOL"
trap cleanup EXIT
for name in DriveMount JailerCPUSet Jailer AttachBlockDevice PmemRoot NativeRestore; do
    make -C runtime "integ-test-Test${name}_Isolated" EXTRAGOARGS='-v -count=1 -timeout=1200s'
    test -z "$(find "$FICD_VM_STORAGE" -mindepth 1 -print -quit)"
    echo "PER_INVOCATION_STORAGE_REMOVED $name"
done
make -C snapshotter integ-test-TestGuestVolumeFrom_Isolated EXTRAGOARGS='-v -count=1 -timeout=300s'
test -z "$(find "$FICD_VM_STORAGE" -mindepth 1 -print -quit)"
test -z "$(git status --porcelain)"
echo COMMITTED_PER_INVOCATION_FIXTURE_REGRESSION_OK
