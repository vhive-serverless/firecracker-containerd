#!/usr/bin/env bash
# Run prepare-worker.sh first. All compilation and guest image builds run here.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
export RUSTUP_HOME="$stack_dir/toolchains/rustup"
export CARGO_HOME="$stack_dir/toolchains/cargo"
export PATH="$stack_dir/toolchains/go1.26.11/bin:$CARGO_HOME/bin:$PATH"
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
git submodule update --init --recursive
python3 tools/modernize/verify-inputs.py
rustc --version
export DOCKER_IMAGE_TAG="$(git rev-parse --short=12 HEAD)"

# Force Go linking so revision metadata cannot come from an earlier checkout.
make -B agent runtime examples firecracker-control/cmd/containerd snapshotter \
    docker-credential-mmds volume cni-bins test-cni-bins
make firecracker runc kernel default-vmlinux image image-stargz
mkdir -p bin/receipts
go list -m -json all > bin/receipts/go-modules.json
git rev-parse HEAD > bin/receipts/runtime-revision
git submodule status > bin/receipts/submodules
for binary in bin/firecracker _submodules/runc/runc \
    agent/agent runtime/containerd-shim-aws-firecracker \
    firecracker-control/cmd/containerd/firecracker-containerd \
    firecracker-control/cmd/containerd/firecracker-ctr; do
    sha256sum "$binary"
done > bin/receipts/binaries.sha256
go version -m agent/agent > bin/receipts/agent-build-info
bin/firecracker --version
_submodules/runc/runc --version
firecracker-control/cmd/containerd/firecracker-containerd --version
