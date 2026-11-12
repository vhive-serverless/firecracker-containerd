#!/usr/bin/env bash
# Accept the guest stargz disk path after build-stack.sh/install-stack.sh.
# Native runtime restore only; Kubernetes Pod restore remains deferred.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
export PATH="$stack_dir/toolchains/go1.26.11/bin:$stack_dir/toolchains/cargo/bin:$PATH"
export STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
export FICD_DM_VOLUME_GROUP=''
cd "$repo_dir"
test -z "$(git status --porcelain)"
export DOCKER_IMAGE_TAG=$(git rev-parse --short=12 HEAD)
export FIRECRACKER_CONTAINERD_TEST_IMAGE=${FIRECRACKER_CONTAINERD_TEST_IMAGE:-localhost/firecracker-containerd-test}
export FICD_VM_STORAGE=${FICD_VM_STORAGE:-"$(dirname "$repo_dir")/.ficd-test-vms/$(basename "$repo_dir")"}
pool=fc-stargz-${DOCKER_IMAGE_TAG}
if sudo -n dmsetup info "$pool" >/dev/null 2>&1; then
    echo "Stargz acceptance pool already exists: $pool" >&2
    exit 1
fi
cleanup() {
    if sudo -n dmsetup info "$pool" >/dev/null 2>&1; then
        tools/thinpool.sh remove "$pool"
    fi
}
trap cleanup EXIT
python3 tools/modernize/verify-inputs.py
make lint
sh -n tools/image-builder/files_stargz/usr/local/bin/prepare-stargz-storage
make test-images image-stargz-dax
tools/modernize/test-stargz-storage-init.sh "$FIRECRACKER_CONTAINERD_TEST_IMAGE:$DOCKER_IMAGE_TAG"
tools/thinpool.sh create "$pool"
for name in StargzGuestDiskStorage StargzNativeRestore; do
    timeout=1200s
    # Twelve serial restore cases retain their individual 420-second bounds.
    # Restored stargz HTTP connections can need minutes to time out and retry.
    if [ "$name" = StargzNativeRestore ]; then timeout=5400s; fi
    FICD_DM_POOL="$pool" make -C snapshotter "integ-test-Test${name}_Isolated" \
        EXTRAGOARGS="-v -count=1 -failfast -timeout=$timeout"
done
cleanup
test -z "$(git status --porcelain)"
receipt=bin/receipts/stargz-storage
mkdir -p "$receipt"
git rev-parse HEAD > "$receipt/runtime-revision"
git submodule status > "$receipt/submodules"
findmnt -T "$FICD_VM_STORAGE" > "$receipt/host-backing"
sha256sum tools/image-builder/files_stargz/usr/local/bin/prepare-stargz-storage \
    tools/image-builder/rootfs-stargz.img tools/image-builder/rootfs-stargz-dax.img \
    runtime/containerd-shim-aws-firecracker agent/agent bin/firecracker \
    default-vmlinux.bin > "$receipt/inputs.sha256"
docker image inspect "$FIRECRACKER_CONTAINERD_TEST_IMAGE:$DOCKER_IMAGE_TAG" \
    > "$receipt/test-image.json"
echo COMMITTED_STARGZ_DISK_STORAGE_AND_NATIVE_RESTORE_OK
