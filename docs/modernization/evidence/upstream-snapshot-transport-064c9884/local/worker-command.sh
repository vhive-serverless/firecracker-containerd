set -euo pipefail
supplement_dir=/users/nehalem/fc-ctrd-mod/upstream-migration/stage3-transport-supplement
evidence_dir=/users/nehalem/fc-ctrd-mod/evidence/native-buffered-stdin-064c9884
cd "$supplement_dir"
test "$(git rev-parse HEAD)" = 064c9884a0a34a52c1e648847a4bb5f336fa2dde
test -z "$(git status --porcelain)"
test "$(git diff --name-status 332da2a7065baa0050037250bbf2ff316a1a8519 HEAD | wc -l)" = 2
# Equality must also be verified against the supplied production-equality proof:
# only the two new *_test.go additions are allowed; no old entry may differ.
git diff --name-status 332da2a7065baa0050037250bbf2ff316a1a8519 HEAD
mkdir "$evidence_dir"
export PATH=/users/nehalem/fc-ctrd-mod/toolchains/go1.26.11/bin:$PATH
export FIRECRACKER_CONTAINERD_TEST_IMAGE=localhost/firecracker-containerd-test
export DOCKER_IMAGE_TAG=332da2a7065b
image="$FIRECRACKER_CONTAINERD_TEST_IMAGE:$DOCKER_IMAGE_TAG"
test "$(docker image inspect --format '{{.Id}}' "$image")" = sha256:17046a2aeb5c501161f0761094b96b3f5347602888759d56ac8cf9d6c40ab64d
docker image inspect "$image" > "$evidence_dir/accepted-image.json"
git rev-parse HEAD HEAD^{tree} > "$evidence_dir/test-source.txt"
git diff --name-status 332da2a7065baa0050037250bbf2ff316a1a8519 HEAD > "$evidence_dir/test-only-diff.txt"
sha256sum runtime/snapshot_admission_supplement_test.go \
    runtime/native_buffered_stdin_supplement_integ_test.go > "$evidence_dir/test-sources.sha256"
# Record actual accepted-image binaries, without starting its service entrypoint.
docker run --rm --network=none --entrypoint=sha256sum "$image" \
    /usr/local/bin/firecracker-containerd \
    /usr/local/bin/containerd-shim-aws-firecracker \
    /usr/local/bin/firecracker \
    /var/lib/firecracker-containerd/runtime/default-rootfs.img \
    > "$evidence_dir/accepted-binaries.sha256"
DISABLE_ROOT_TESTS=1 go test -mod=readonly -race -short -count=50 -v \
    ./runtime -run '^TestSnapshotAdmissionDrainsHeldMutation$' \
    > "$evidence_dir/admission-race.log" 2>&1
export FICD_DM_VOLUME_GROUP=''
export FICD_DM_POOL=fc-buffered-stdin-064c9884
if sudo -n dmsetup info "$FICD_DM_POOL" >/dev/null 2>&1; then
    echo 'Dedicated supplement pool already exists' >&2
    exit 1
fi
cleanup() {
    if sudo -n dmsetup info "$FICD_DM_POOL" >/dev/null 2>&1; then
        tools/thinpool.sh remove "$FICD_DM_POOL"
    fi
}
trap cleanup EXIT
tools/thinpool.sh create "$FICD_DM_POOL"
make -C runtime integ-test-TestNativeBufferedStdinSnapshot_Isolated \
    EXTRAGOARGS='-mod=readonly -v -count=1 -timeout=1200s' \
    > "$evidence_dir/buffered-stdin-vm.log" 2>&1
cleanup
test -z "$(git status --porcelain)"
