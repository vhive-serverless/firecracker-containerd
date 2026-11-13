#!/usr/bin/env bash
# Full seven-arm controlled matrix; build all stack artifacts before observing.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
export STACK_DIR="$stack_dir"
export PATH="$stack_dir/toolchains/go1.26.11/bin:$PATH"
cd "$repo_dir"
test -z "$(git status --porcelain)"
revision=$(git rev-parse HEAD)
short_revision=$(git rev-parse --short=12 HEAD)
test "$(cat bin/receipts/build/runtime-revision)" = "$revision"
export DOCKER_IMAGE_TAG="$short_revision" STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
# This step may rebuild/install test-image files; it precedes all observations.
make test-images
tools/modernize/build-memory-eval.sh
sudo -n python3 tools/memory-eval/test_controller_worker.py \
    --image "localhost/firecracker-containerd-test:$short_revision"
output=${EVAL_OUTPUT:-"$stack_dir/evidence/memory-matrix-$short_revision"}
sudo -n python3 tools/memory-eval/run_matrix.py \
    --output "$output" --squash "$repo_dir/tools/image-builder/rootfs.img" \
    --image "localhost/firecracker-containerd-test:$short_revision" \
    --builder-image "fc-image-builder:$short_revision" --runtime-revision "$revision" \
    --repeats 3 --idle 30 --interval 0.5 --seed 5823
test -z "$(git status --porcelain)"
echo COMMITTED_MEMORY_MATRIX_OK
