#!/usr/bin/env bash
# Provision build dependencies on the designated, otherwise unused worker.
set -euo pipefail

stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
go_version=1.26.11
go_sha256=9849f4e04ee35303714e34fbc3ac8ab44ea3e448a95aa182f91e3b9b79c710ec
test "$(uname -m)" = x86_64
mkdir -p "$stack_dir/toolchains" "$stack_dir/downloads" "$stack_dir/evidence"
sudo -n apt-get update
sudo -n env DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
    ca-certificates curl git rsync jq python3 build-essential pkg-config \
    libseccomp-dev e2fsprogs squashfs-tools debootstrap dmsetup \
    thin-provisioning-tools iproute2 iptables socat musl-tools \
    protobuf-compiler bc docker.io docker-buildx \
    flex bison libelf-dev libssl-dev dwarves

go_dir="$stack_dir/toolchains/go$go_version"
if [ ! -x "$go_dir/bin/go" ]; then
    archive="$stack_dir/downloads/go$go_version.linux-amd64.tar.gz"
    curl --fail --location --retry 3 --output "$archive" \
        "https://go.dev/dl/go$go_version.linux-amd64.tar.gz"
    echo "$go_sha256  $archive" | sha256sum --check -
    staging=$(mktemp -d "$stack_dir/toolchains/.go.XXXXXXXX")
    tar -xzf "$archive" -C "$staging"
    mv "$staging/go" "$go_dir"
    rmdir "$staging"
fi
test "$("$go_dir/bin/go" version)" = "go version go$go_version linux/amd64"
"$go_dir/bin/go" version

if ! id -nG "$USER" | tr " " "\n" | grep -q '^docker$'; then
    sudo -n usermod -aG docker "$USER"
    echo "Docker group added; run subsequent commands in a new SSH session."
fi
sudo -n docker version
sudo -n test -r /dev/kvm
findmnt -no SOURCE,FSTYPE,TARGET -T "$stack_dir"
findmnt -no FSTYPE /sys/fs/cgroup
dpkg-query -W -f='${Package}\t${Version}\n' \
    gcc make libseccomp-dev squashfs-tools debootstrap dmsetup docker.io protobuf-compiler
