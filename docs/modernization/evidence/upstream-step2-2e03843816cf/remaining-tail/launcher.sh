#!/usr/bin/env bash
# Supplemental checks not reached by the failed full-stack run. Not a full-run PASS.
set -euo pipefail
root=/users/nehalem/fc-ctrd-mod/upstream-migration
cd "$root/stage2-same-id-runtime-fix"
revision=2e03843816cf95d184ad4ad429adf6ea2ccc1412
test "$(git rev-parse HEAD)" = "$revision"
test -z "$(git status --porcelain)"
export STACK_DIR=/users/nehalem/fc-ctrd-mod
export PATH="$STACK_DIR/toolchains/go1.26.11/bin:$PATH"
export GOTOOLCHAIN=local GOWORK=off STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
unset ENABLE_ISOLATED_TESTS DISABLE_ROOT_TESTS FC_TEST_BIN
export DOCKER_IMAGE_TAG=${revision:0:12}
test "$(docker image inspect --format '{{.Id}}' localhost/firecracker-containerd-test:$DOCKER_IMAGE_TAG)" = sha256:97f3745973e8856190db513d2d1def3a84a603eb3337bc4ccac655e554b27117
out="$root/evidence/stage2-remaining-$DOCKER_IMAGE_TAG"
mkdir "$out"
cp -- "$(realpath "${BASH_SOURCE[0]}")" "$out/launcher.sh"
printf 'step\texit_code\n' > "$out/status.tsv"
run_logged() {
    local name=$1 code
    shift
    if "$@" > "$out/$name.log" 2>&1; then
        printf '%s\t0\n' "$name" >> "$out/status.tsv"
    else
        code=$?
        printf '%s\t%s\n' "$name" "$code" >> "$out/status.tsv"
        return "$code"
    fi
}
run_logged thinpool tools/modernize/test-thinpool.sh
run_logged sdk tools/modernize/test-sdk.sh
test -z "$(git status --porcelain)"
git rev-parse HEAD HEAD^{tree} > "$out/runtime-source.txt"
go list -m -json all > "$out/go-modules.json"
go version -m agent/agent > "$out/agent-build-info.txt"
sha256sum bin/firecracker bin/jailer _submodules/runc/runc agent/agent \
    _submodules/stargz-snapshotter/out/containerd-stargz-grpc volume/volume-init \
    runtime/containerd-shim-aws-firecracker \
    firecracker-control/cmd/containerd/firecracker-containerd \
    firecracker-control/cmd/containerd/firecracker-ctr \
    default-vmlinux.bin tools/image-builder/rootfs.img tools/image-builder/rootfs-stargz.img \
    > "$out/binaries.sha256"
docker image inspect localhost/firecracker-containerd-test:$DOCKER_IMAGE_TAG > "$out/test-image.json"
echo STAGE2_REMAINING_THINPOOL_AND_SDK_CHECKS_OK
