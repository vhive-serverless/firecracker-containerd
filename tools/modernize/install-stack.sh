#!/usr/bin/env bash
# Install the built milestone into a dedicated prefix, without replacing host Docker tools.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
revision=$(git -C "$repo_dir" rev-parse --short=12 HEAD)
install_dir=${INSTALL_DIR:-"$stack_dir/install/$revision"}
export PATH="$stack_dir/toolchains/go1.26.11/bin:$stack_dir/toolchains/cargo/bin:$PATH"
export RUSTUP_HOME="$stack_dir/toolchains/rustup"
export CARGO_HOME="$stack_dir/toolchains/cargo"
export STATIC_AGENT=on
export STATIC_DOCKER_CREDENTIAL_MMDS=on
export DOCKER_IMAGE_TAG="$revision"
cd "$repo_dir"
if [ "${ALLOW_DIRTY:-0}" != 1 ]; then
    test -z "$(git status --porcelain)"
fi
python3 tools/modernize/verify-inputs.py
sudo -n env PATH="$PATH" RUSTUP_HOME="$RUSTUP_HOME" CARGO_HOME="$CARGO_HOME" \
    STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on make \
    INSTALLROOT="$install_dir" \
    FIRECRACKER_CONTAINERD_RUNTIME_DIR="$install_dir/share/runtime" \
    ETC_CONTAINERD="$install_dir/etc" \
    DEFAULT_RUNC_JAILER_CONFIG_INSTALLPATH="$install_dir/etc/firecracker-runc-config.json" \
    CNI_BIN_ROOT="$install_dir/lib/cni" \
    install install-firecracker install-runc install-kernel \
    install-default-rootfs install-default-runc-jailer-config install-cni-bins
sudo -n sha256sum "$install_dir/bin/firecracker-containerd" \
    "$install_dir/bin/firecracker-cri" \
    "$install_dir/bin/containerd-shim-aws-firecracker" "$install_dir/bin/firecracker" \
    "$install_dir/bin/runc" "$install_dir/share/runtime/default-vmlinux.bin" \
    "$install_dir/share/runtime/default-rootfs.img"
printf 'INSTALL_DIR=%s\n' "$install_dir"
