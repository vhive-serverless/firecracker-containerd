#!/usr/bin/env bash
set -euo pipefail
root=/users/nehalem/fc-ctrd-mod
cd "$root/step4-regression-fix"
grep -qx COMMITTED_PER_INVOCATION_FIXTURE_REGRESSION_OK "$root/evidence/d5739e5-fixture-regression.log"
test ! -e "$root/.ficd-test-vms/step4-fixtures-d5739e5"
test -z "$(git status --porcelain)"
git fetch origin damon-virtio-pmem
git merge --ff-only origin/damon-virtio-pmem
test "$(git rev-parse HEAD)" = 7c69cccae0a1f2d0f25d1a0a0ff649307834a73a
git diff --exit-code 72f4fc9173bef33cc1c981ad285758716f635e02..HEAD -- '*.go' ':(exclude)*_test.go' go.mod go.sum _submodules
export PATH="$root/toolchains/go1.26.11/bin:$PATH"
export STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
export DOCKER_IMAGE_TAG=72f4fc9173be FICD_DM_VOLUME_GROUP=''
export FICD_DM_POOL=fc-step4-final-7c69ccc
export FICD_VM_STORAGE="$root/.ficd-test-vms/step4-final-7c69ccc"
test ! -e "$FICD_VM_STORAGE"
if sudo -n dmsetup info "$FICD_DM_POOL" >/dev/null 2>&1; then exit 1; fi
mkdir -p "$FICD_VM_STORAGE"
image=localhost/firecracker-containerd-test:$DOCKER_IMAGE_TAG
test "$(docker image inspect "$image" --format '{{.Id}}')" = sha256:0ed23833cf6043e6f2e0fa6ef3e590cfd9626c03e69d66303f9a693b079021b5
set +e
tools/modernize/run-isolated-vm-test.sh "$FICD_VM_STORAGE" --rm --entrypoint /bin/sh "$image" -c 'touch /srv/firecracker_containerd_tests/fixture; exit 37'
code=$?
set -e
test "$code" = 37
test -z "$(find "$FICD_VM_STORAGE" -mindepth 1 -print -quit)"
echo EXPECTED_FAILURE_STATUS_AND_CLEANUP_OK
start=$SECONDS
set +e
timeout --signal=TERM --kill-after=15s 5s tools/modernize/run-isolated-vm-test.sh "$FICD_VM_STORAGE" --name fcmod-interrupt-7c69ccc --rm --entrypoint /bin/sh "$image" -c 'touch /srv/firecracker_containerd_tests/fixture; sleep 60'
code=$?
set -e
elapsed=$((SECONDS-start))
test "$code" = 124
test "$elapsed" -lt 15
test -z "$(docker ps -aq --filter name=fcmod-interrupt-7c69ccc)"
test -z "$(find "$FICD_VM_STORAGE" -mindepth 1 -print -quit)"
echo "INTERRUPTED_CONTAINER_AND_STORAGE_CLEANUP_OK seconds=$elapsed"
make lint
cleanup() {
    tools/thinpool.sh remove "$FICD_DM_POOL"
    sudo -n rm -rf -- "$FICD_VM_STORAGE"
}
tools/thinpool.sh create "$FICD_DM_POOL"
trap cleanup EXIT
for name in ShimExitsUponContainerDelete StubBlockDevices; do
    make -C runtime "integ-test-Test${name}_Isolated" EXTRAGOARGS='-v -count=1 -timeout=1200s'
    test -z "$(find "$FICD_VM_STORAGE" -mindepth 1 -print -quit)"
    echo "PER_INVOCATION_STORAGE_REMOVED $name"
done
test -z "$(git status --porcelain)"
echo COMMITTED_FINAL_FIXTURE_REGRESSION_OK
