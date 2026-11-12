#!/usr/bin/env bash
# Runtime storage/restore acceptance, including original-layout and disk-upper forks.
# Kubernetes/CRI Pod restore remains WIP. This target selects 24 VM leaf cases.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
export PATH="$stack_dir/toolchains/go1.26.11/bin:$PATH"
export STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
export FICD_DM_VOLUME_GROUP=''
cd "$repo_dir"
test -z "$(git status --porcelain)"
export DOCKER_IMAGE_TAG=$(git rev-parse --short=12 HEAD)
pool=fc-restore-${DOCKER_IMAGE_TAG}
if sudo -n dmsetup info "$pool" >/dev/null 2>&1; then
    echo "Restore test pool already exists: $pool" >&2
    exit 1
fi
cleanup() {
    if sudo -n dmsetup info "$pool" >/dev/null 2>&1; then tools/thinpool.sh remove "$pool"; fi
}
trap cleanup EXIT
python3 tools/modernize/verify-inputs.py
make lint
unit_tests='^Test(CheckpointTransportRejectsInvalidVersionAndThawSet|SealTransportPersistsAcknowledgedEventCursor|ReportingMetricsAwaitFlushed|BalloonBootOptionsForNativeRestore|NativeRestore(DiskBackingAndReservation|RejectsRAMBacking|StorageFailureCleanup|ExecStateIdentity|CopyFailureCleanup|CopiesPrivateArtifacts|MountPlan|PmemMountPlan|CreateVM(SelectsArtifact|DeadlinePreservesRequestIdentity))|GuestOverlay(Allocation|RejectsRAMAndCleansFailure))'
go test -mod=readonly -race -count=1 -v -run "$unit_tests" ./runtime
make image-dax
make test-images
tools/thinpool.sh create "$pool"
for name in NativeCheckpoint NativeForkTransport NativeForkTransportStorage NativeCheckpointInterrupt NativeRestore NativeRestoreStorage FreePageReporting; do
    FICD_DM_POOL="$pool" make -C runtime "integ-test-Test${name}_Isolated" \
        EXTRAGOARGS='-v -count=1 -timeout=1200s'
done
cleanup
test -z "$(git status --porcelain)"
mkdir -p bin/receipts/native-restore
git rev-parse HEAD > bin/receipts/native-restore/runtime-revision
git submodule status > bin/receipts/native-restore/submodules
sha256sum firecracker-control/cmd/containerd/firecracker-containerd \
    runtime/containerd-shim-aws-firecracker agent/agent bin/firecracker \
    default-vmlinux.bin tools/image-builder/rootfs.img \
    tools/image-builder/rootfs-dax.img runtime/native_restore_integ_test.go \
    > bin/receipts/native-restore/binaries.sha256
docker image inspect "localhost/firecracker-containerd-test:$DOCKER_IMAGE_TAG" \
    > bin/receipts/native-restore/test-image.json
echo COMMITTED_NATIVE_RUNTIME_STORAGE_RESTORE_OK
