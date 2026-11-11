#!/usr/bin/env bash
# Run Step 1 acceptance from a committed worker checkout after build-stack.sh.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
export PATH="$stack_dir/toolchains/go1.26.11/bin:$stack_dir/toolchains/cargo/bin:$PATH"
export RUSTUP_HOME="$stack_dir/toolchains/rustup"
export CARGO_HOME="$stack_dir/toolchains/cargo"
export STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
cd "$repo_dir"
test -z "$(git status --porcelain)"
export DOCKER_IMAGE_TAG=$(git rev-parse --short=12 HEAD)
runtime_pool=fc-test-${DOCKER_IMAGE_TAG}-runtime
snapshotter_pool=fc-test-${DOCKER_IMAGE_TAG}-stargz
export FICD_DM_VOLUME_GROUP=''
cleanup() {
    for pool in "$runtime_pool" "$snapshotter_pool"; do
        if sudo -n dmsetup info "$pool" >/dev/null 2>&1; then
            tools/thinpool.sh remove "$pool"
        fi
    done
}
trap cleanup EXIT
python3 tools/modernize/verify-inputs.py
make lint
make test-images
env -u ENABLE_ISOLATED_TESTS DISABLE_ROOT_TESTS=1 go test -short -timeout=180s ./...
(cd cri && go test -mod=readonly -race -count=1 -timeout=180s ./...)
sudo -n env -u ENABLE_ISOLATED_TESTS PATH="$PATH" GOMODCACHE="$(go env GOMODCACHE)" \
    GOCACHE="$(go env GOCACHE)" go test -timeout=180s ./...
make tidy
make -C proto clean
make proto
make -C cri generate
git diff --exit-code
# The storage restore matrix alone takes about eight minutes on the worker.
FICD_DM_POOL="$runtime_pool" make -k -C runtime integ-test EXTRAGOARGS='-v -count=1 -timeout=1200s'
FICD_DM_POOL="$snapshotter_pool" make -k -C snapshotter integ-test EXTRAGOARGS='-v -count=1 -timeout=300s'
tools/modernize/test-thinpool.sh
tools/modernize/test-sdk.sh
cleanup
test -z "$(git status --porcelain)"
mkdir -p bin/receipts/acceptance
git rev-parse HEAD > bin/receipts/acceptance/runtime-revision
go list -m -json all > bin/receipts/acceptance/go-modules.json
(cd cri && go list -m -json all) > bin/receipts/acceptance/cri-go-modules.json
go version -m agent/agent > bin/receipts/acceptance/agent-build-info
go version -m cri/firecracker-cri > bin/receipts/acceptance/cri-build-info
sha256sum bin/firecracker _submodules/runc/runc agent/agent \
    _submodules/stargz-snapshotter/out/containerd-stargz-grpc volume/volume-init \
    runtime/containerd-shim-aws-firecracker \
    firecracker-control/cmd/containerd/firecracker-containerd \
    firecracker-control/cmd/containerd/firecracker-ctr \
    cri/firecracker-cri \
    default-vmlinux.bin tools/image-builder/rootfs.img tools/image-builder/rootfs-stargz.img \
    > bin/receipts/acceptance/binaries.sha256
docker image inspect "localhost/firecracker-containerd-test:$DOCKER_IMAGE_TAG" \
    > bin/receipts/acceptance/test-image.json
echo COMMITTED_STACK_ACCEPTANCE_OK
