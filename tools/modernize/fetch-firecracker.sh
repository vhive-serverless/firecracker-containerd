#!/usr/bin/env bash
# Install checksum-pinned, unmodified binaries from the official release.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
version=v1.18.0
arch=$(uname -m)
case "$arch" in
    x86_64) checksum=1ac8eec866dd5eea14efecf6323e22b23eea24054545338a5b34bc5bca6623cd ;;
    aarch64) checksum=2a6130ff0338901ff9d7fbd73ce046c6b1c6123889dc84a34d678aa53bd0c0c2 ;;
    *) echo "Unsupported Firecracker release architecture: $arch" >&2; exit 1 ;;
esac
url="https://github.com/firecracker-microvm/firecracker/releases/download/$version/firecracker-$version-$arch.tgz"
mkdir -p "$repo_dir/bin/receipts/build"
staging=$(mktemp -d "$repo_dir/bin/.firecracker-release.XXXXXXXX")
trap 'rm -rf "$staging"' EXIT
curl --fail --location --retry 3 --output "$staging/release.tgz" "$url"
printf '%s  %s\n' "$checksum" "$staging/release.tgz" | sha256sum --check -
tar -xzf "$staging/release.tgz" -C "$staging"
for binary in firecracker jailer; do
    install -m 0755 "$staging/release-$version-$arch/$binary-$version-$arch" "$repo_dir/bin/$binary"
done
{
    printf 'url=%s\narchive_sha256=%s\n' "$url" "$checksum"
    "$repo_dir/bin/firecracker" --version
    "$repo_dir/bin/jailer" --version
    sha256sum "$repo_dir/bin/firecracker" "$repo_dir/bin/jailer"
} > "$repo_dir/bin/receipts/build/firecracker-release.txt"
cat "$repo_dir/bin/receipts/build/firecracker-release.txt"
