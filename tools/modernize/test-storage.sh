#!/usr/bin/env bash
# Run after build-stack.sh/install-stack.sh on the designated worker.
# This validates disk storage and native capture; inherited restore remains WIP.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
export PATH="$stack_dir/toolchains/go1.26.11/bin:$stack_dir/toolchains/cargo/bin:$PATH"
export STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
cd "$repo_dir"
test -z "$(git status --porcelain)"
export DOCKER_IMAGE_TAG=$(git rev-parse --short=12 HEAD)
export FICD_DM_VOLUME_GROUP=''
pool=fc-storage-${DOCKER_IMAGE_TAG}
if sudo -n dmsetup info "$pool" >/dev/null 2>&1; then
    echo "Storage test pool already exists: $pool" >&2
    exit 1
fi
fixture=''
cleanup() {
    if [ -n "$fixture" ]; then
        if mountpoint -q "$fixture/mount"; then sudo -n umount "$fixture/mount"; fi
        test -z "$(sudo -n losetup --output NAME --noheadings --associated "$fixture/image")"
        rm -f "$fixture/image"
        rmdir "$fixture/mount" "$fixture"
        fixture=''
    fi
    if sudo -n dmsetup info "$pool" >/dev/null 2>&1; then
        tools/thinpool.sh remove "$pool"
    fi
}
trap cleanup EXIT
python3 tools/modernize/verify-inputs.py
make lint
go test -mod=readonly -race -count=1 -run '^TestGuestOverlay' -v ./runtime
sh -n tools/image-builder/files_debootstrap/sbin/overlay-init
make -C proto clean
make proto
git diff --exit-code

# Exhaust a private filesystem instead of filling the worker's shared disk.
mkdir -p tmp
fixture=$(mktemp -d "$repo_dir/tmp/storage-exhaustion.XXXXXX")
mkdir "$fixture/mount"
truncate -s 32M "$fixture/image"
mkfs.ext4 -q -F "$fixture/image"
sudo -n mount -o loop "$fixture/image" "$fixture/mount"
sudo -n env PATH="$PATH" FICD_OVERLAY_ENOSPC_DIR="$fixture/mount" \
    GOMODCACHE="$(go env GOMODCACHE)" GOCACHE="$(go env GOCACHE)" \
    go test -mod=readonly -count=1 -v -run '^TestGuestOverlayHostENOSPC$' ./runtime
cleanup

make test-images
tools/thinpool.sh create "$pool"
for name in GuestDiskStorage StorageBaseline NativeCheckpoint JailerTelemetryCrossFilesystem CreateVM; do
    FICD_DM_POOL="$pool" make -C runtime "integ-test-Test${name}_Isolated" \
        EXTRAGOARGS='-v -count=1 -timeout=360s'
done
cleanup
test -z "$(git status --porcelain)"
mkdir -p bin/receipts/storage
git rev-parse HEAD > bin/receipts/storage/runtime-revision
git submodule status > bin/receipts/storage/submodules
go list -mod=readonly -m -json all > bin/receipts/storage/go-modules.json
findmnt -T runtime/vm-storage > bin/receipts/storage/host-backing
sha256sum runtime/containerd-shim-aws-firecracker agent/agent bin/firecracker \
    default-vmlinux.bin tools/image-builder/rootfs.img \
    > bin/receipts/storage/binaries.sha256
docker image inspect "localhost/firecracker-containerd-test:$DOCKER_IMAGE_TAG" \
    > bin/receipts/storage/test-image.json
echo COMMITTED_GUEST_STORAGE_AND_CAPTURE_OK
