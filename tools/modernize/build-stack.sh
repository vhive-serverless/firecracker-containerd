#!/usr/bin/env bash
# Run prepare-worker.sh first. All compilation and guest image builds run here.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
export PATH="$stack_dir/toolchains/go1.26.11/bin:$PATH"
export GOTOOLCHAIN=local
export STATIC_AGENT=on
export STATIC_DOCKER_CREDENTIAL_MMDS=on
cd "$repo_dir"
if [ "${ALLOW_DIRTY:-0}" != 1 ]; then
    test -z "$(git status --porcelain)" || {
        echo 'Build requires a clean committed checkout (ALLOW_DIRTY=1 is development only).' >&2
        exit 1
    }
fi
git submodule sync --recursive
git submodule update --init --recursive
python3 tools/modernize/verify-inputs.py
export DOCKER_IMAGE_TAG="$(git rev-parse --short=12 HEAD)"

# Force Go linking so revision metadata cannot come from an earlier checkout.
make -B agent runtime examples firecracker-control/cmd/containerd snapshotter \
    docker-credential-mmds volume cni-bins test-cni-bins
# Verify official release checksums and rebuild runc for this revision.
make -B firecracker runc
make kernel default-vmlinux image image-stargz
mkdir -p bin/receipts/build
go list -m -json all > bin/receipts/build/go-modules.json
git rev-parse HEAD > bin/receipts/build/runtime-revision
git submodule status > bin/receipts/build/submodules
for binary in bin/firecracker bin/jailer _submodules/runc/runc \
    _submodules/stargz-snapshotter/out/containerd-stargz-grpc volume/volume-init \
    agent/agent runtime/containerd-shim-aws-firecracker \
    firecracker-control/cmd/containerd/firecracker-containerd \
    firecracker-control/cmd/containerd/firecracker-ctr; do
    sha256sum "$binary"
done > bin/receipts/build/binaries.sha256
go version -m agent/agent > bin/receipts/build/agent-build-info
bin/firecracker --version
_submodules/runc/runc --version
firecracker-control/cmd/containerd/firecracker-containerd --version
