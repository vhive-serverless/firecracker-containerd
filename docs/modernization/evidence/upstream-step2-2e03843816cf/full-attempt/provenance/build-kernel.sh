#!/usr/bin/env bash
# Build the guest kernel from an exact Amazon Linux 2023 source revision.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
kernel_tag=kernel6.18-6.18.56-126.188.amzn2023
kernel_revision=7483450dec64b4f923037de31d8c28298a7c3359
kernel_dir="$repo_dir/tmp/kernel-source"
output_dir="$repo_dir/bin"
test "$(uname -m)" = x86_64
mkdir -p "$kernel_dir" "$output_dir"
if [ ! -d "$kernel_dir/.git" ]; then
    git -C "$kernel_dir" init
    git -C "$kernel_dir" remote add origin https://github.com/amazonlinux/linux.git
fi
if ! git -C "$kernel_dir" cat-file -e "$kernel_revision^{commit}"; then
    git -C "$kernel_dir" fetch --depth 1 origin "refs/tags/$kernel_tag"
fi
git -C "$kernel_dir" checkout --detach "$kernel_revision"
test "$(git -C "$kernel_dir" rev-parse HEAD)" = "$kernel_revision"
git -C "$kernel_dir" diff --exit-code
cp "$repo_dir/tools/kernel-configs/microvm-kernel-x86_64-6.18.config" "$kernel_dir/.config"
make -C "$kernel_dir" olddefconfig
make -C "$kernel_dir" -j"${KERNEL_BUILD_JOBS:-16}" LOCALVERSION=-fcmod vmlinux
install -m 644 "$kernel_dir/vmlinux" "$output_dir/vmlinux-6.18.56-fcmod"
cp "$kernel_dir/.config" "$output_dir/vmlinux-6.18.56-fcmod.config"
make -s -C "$kernel_dir" LOCALVERSION=-fcmod kernelrelease
sha256sum "$output_dir/vmlinux-6.18.56-fcmod" "$output_dir/vmlinux-6.18.56-fcmod.config"
