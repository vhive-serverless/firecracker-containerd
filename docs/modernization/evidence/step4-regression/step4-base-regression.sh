#!/usr/bin/env bash
set -euo pipefail
root=/users/nehalem/fc-ctrd-mod
cd "$root/step4-native-final"
export STACK_DIR="$root"
export PATH="$root/toolchains/go1.26.11/bin:$PATH"
export STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
export DOCKER_IMAGE_TAG=72f4fc9173be FICD_DM_VOLUME_GROUP=''
export FICD_DM_POOL=fc-step4-base-72f4fc9
export FICD_VM_STORAGE="$root/.ficd-test-vms/step4-native-final"
test "$(git rev-parse HEAD)" = 72f4fc9173bef33cc1c981ad285758716f635e02
test -z "$(git status --porcelain)"
if sudo -n dmsetup info "$FICD_DM_POOL" >/dev/null 2>&1; then exit 1; fi
cleanup() { tools/thinpool.sh remove "$FICD_DM_POOL"; }
tools/thinpool.sh create "$FICD_DM_POOL"
trap cleanup EXIT
mapfile -t tests < "$root/evidence/step4-baseline-tests.txt"
test "${#tests[@]}" = 35
targets=()
for name in "${tests[@]}"; do targets+=("integ-test-$name"); done
set +e
make -k -C runtime "${targets[@]}" EXTRAGOARGS='-v -count=1 -timeout=1200s'
code=$?
set -e
printf 'BASE_RUNTIME_EXIT=%s\n' "$code"
test -z "$(git status --porcelain)"
exit "$code"
