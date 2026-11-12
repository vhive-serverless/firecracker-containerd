#!/usr/bin/env bash
# Exercise the pinned SDK, with test networking isolated from worker interfaces.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
export PATH="$stack_dir/toolchains/go1.26.11/bin:$PATH"
sdk_revision=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["selected_inputs"]["sdk"]["commit"])' "$repo_dir/docs/modernization/versions.json")
sdk_src="$repo_dir/tmp/sdk-tests/$sdk_revision"
fixtures="$repo_dir/tmp/sdk-testdata/$sdk_revision"
image=${SDK_TEST_IMAGE:-"localhost/firecracker-containerd-test:$(git -C "$repo_dir" rev-parse --short=12 HEAD)"}
vmm_build="$repo_dir/bin"
test -x "$repo_dir/bin/firecracker"
test -x "$vmm_build/jailer"
mkdir -p "$(dirname "$sdk_src")" "$fixtures"
if [ ! -d "$sdk_src/.git" ]; then
    git clone --no-checkout https://github.com/vhive-serverless/firecracker-go-sdk.git "$sdk_src"
fi
git -C "$sdk_src" checkout --detach "$sdk_revision"
test -z "$(git -C "$sdk_src" status --porcelain)"
cd "$sdk_src"
FC_TEST_DATA_PATH="$fixtures" make all-tests EXTRAGOARGS='-run=^$'
go mod download
FC_TEST_DATA_PATH="$fixtures" make lint
# The native jailer test owns this exact cgroup. Refuse to reuse an existing one.
sudo -n test ! -e /sys/fs/cgroup/firecracker/b
cleanup() {
    if sudo -n test -d /sys/fs/cgroup/firecracker/b; then
        test -z "$(sudo -n cat /sys/fs/cgroup/firecracker/b/cgroup.procs)"
        sudo -n rmdir /sys/fs/cgroup/firecracker/b
    fi
}
trap cleanup EXIT
docker run --rm --privileged --cgroupns=host --network=none --init \
    --entrypoint /bin/bash \
    --volume "$sdk_src:/sdk" --volume "$fixtures:/fixtures" \
    --volume "$repo_dir/bin/firecracker:/selected-vmm/firecracker:ro" \
    --volume "$vmm_build/jailer:/selected-vmm/jailer:ro" \
    --volume "$(go env GOMODCACHE):/go/pkg/mod" \
    --workdir /sdk --env CGO_ENABLED=0 --env GOPROXY=off --env GOSUMDB=off \
    --env FC_TEST_DATA_PATH=/fixtures --env FC_TEST_BIN=/selected-vmm/firecracker \
    --env FC_TEST_JAILER_BIN=/selected-vmm/jailer --env FC_TEST_RELEASE_API=1 \
    "$image" -c 'go test -v -count=1 -timeout=15m ./...'
cleanup
git diff --exit-code
git diff --cached --exit-code
echo PINNED_SDK_PRIVILEGED_SUITE_OK
