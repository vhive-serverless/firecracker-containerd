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
    clang libclang-dev flex bison libelf-dev libssl-dev dwarves

# Match Firecracker's musl builder: Linux UAPI headers are required by bindgen
# and the userfaultfd C helper. Preserve any existing header layout.
for header in linux asm-generic; do
    if [ ! -e "/usr/include/x86_64-linux-musl/$header" ]; then
        sudo -n ln -s "../$header" "/usr/include/x86_64-linux-musl/$header"
    fi
done
if [ ! -e /usr/include/x86_64-linux-musl/asm ]; then
    sudo -n ln -s ../x86_64-linux-gnu/asm /usr/include/x86_64-linux-musl/asm
fi

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

rustup_dir="$stack_dir/toolchains/rustup"
cargo_dir="$stack_dir/toolchains/cargo"
rustup_init="$stack_dir/downloads/rustup-init-1.28.2"
if [ ! -x "$cargo_dir/bin/rustup" ]; then
    curl --fail --location --retry 3 --output "$rustup_init" \
        https://static.rust-lang.org/rustup/archive/1.28.2/x86_64-unknown-linux-gnu/rustup-init
    echo "20a06e644b0d9bd2fbdbfd52d42540bdde820ea7df86e92e533c073da0cdd43c  $rustup_init" | sha256sum --check -
    chmod +x "$rustup_init"
    RUSTUP_HOME="$rustup_dir" CARGO_HOME="$cargo_dir" "$rustup_init" \
        -y --no-modify-path --profile minimal --default-toolchain 1.97.0
fi
RUSTUP_HOME="$rustup_dir" CARGO_HOME="$cargo_dir" "$cargo_dir/bin/rustup" \
    target add --toolchain 1.97.0 x86_64-unknown-linux-musl
RUSTUP_HOME="$rustup_dir" CARGO_HOME="$cargo_dir" "$cargo_dir/bin/rustup" \
    component add --toolchain 1.97.0 rustfmt
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
