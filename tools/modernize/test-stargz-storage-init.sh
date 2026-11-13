#!/usr/bin/env bash
# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
# Exercise the guest stargz storage initializer in an isolated privileged
# container. No VM or host mount is created by this test.
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
image=${1:-${FICD_TEST_IMAGE:-}}
helper=${2:-${STARGZ_STORAGE_HELPER:-}}
if [[ -z "$helper" ]]; then
    helper="$script_dir/../image-builder/files_stargz/usr/local/bin/prepare-stargz-storage"
fi

if [[ -z "$image" ]]; then
    echo "usage: $0 TEST_IMAGE [PREPARE_STARGZ_STORAGE]" >&2
    echo "or set FICD_TEST_IMAGE and optionally STARGZ_STORAGE_HELPER" >&2
    exit 2
fi
if [[ ! "$helper" = /* ]]; then
    echo "helper path must be absolute: $helper" >&2
    exit 2
fi
if [[ ! -f "$helper" ]]; then
    echo "stargz storage helper not found: $helper" >&2
    exit 2
fi

docker image inspect "$image" >/dev/null
container="fc-stargz-storage-init-$$-${RANDOM}"
if docker container inspect "$container" >/dev/null 2>&1; then
    echo "refusing to replace existing container: $container" >&2
    exit 1
fi
container_started=0
run_log=$(mktemp "${TMPDIR:-/tmp}/stargz-storage-init.XXXXXX")
cleanup() {
    status=$?
    if (( container_started )); then
        docker rm -f "$container" >/dev/null 2>&1 || true
    fi
    rm -f -- "$run_log"
    exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

echo "test_image=$image"
echo "helper=$helper"
sha256sum "$helper"
container_started=1
if ! docker run --rm --interactive --name "$container" --privileged --network none \
    --env "STARGZ_HOST_MNT_NS=$(readlink /proc/self/ns/mnt)" \
    --mount "type=bind,source=$helper,target=/test/prepare-stargz-storage,readonly" \
    --entrypoint /bin/bash "$image" -s >"$run_log" 2>&1 <<'CONTAINER'
set -euo pipefail

helper=/test/prepare-stargz-storage
test -x "$helper"
container_mnt_ns=$(readlink /proc/self/ns/mnt)
[[ "$container_mnt_ns" != "$STARGZ_HOST_MNT_NS" ]] ||
    { echo 'container shares the caller mount namespace' >&2; exit 1; }
test "$(findmnt -n -o PROPAGATION /)" = private
mount --make-rprivate /
test "$(findmnt -n -o PROPAGATION /)" = private
echo "MOUNT_NAMESPACE_PRIVATE host=$STARGZ_HOST_MNT_NS container=$container_mnt_ns propagation=$(findmnt -n -o PROPAGATION /)"

tmp=$(mktemp -d /tmp/stargz-storage-init.XXXXXX)
backing=/rom/overlay
store=/var/lib/containerd-stargz-grpc
loopdev=
loop_image=$tmp/guest-upper.ext4
proc_cmdline_bound=0

cleanup() {
    status=$?
    trap - EXIT INT TERM HUP
    cleanup_failed=0
    set +e
    if (( proc_cmdline_bound )); then
        umount /proc/cmdline || cleanup_failed=1
    fi
    if mountpoint -q "$store"; then
        umount "$store" || cleanup_failed=1
    fi
    if mountpoint -q "$backing"; then
        umount "$backing" || cleanup_failed=1
    fi
    if [[ -n "$loopdev" ]]; then
        associated=$(losetup --noheadings --output NAME --associated "$loop_image" 2>/dev/null | sed '/^[[:space:]]*$/d')
        if [[ -z "$associated" ]]; then
            echo LOOP_AUTOCLEAR_CLEANUP_OK
        elif [[ "$associated" == "$loopdev" ]]; then
            # Safety fallback for a failure before normal autoclear verification.
            losetup -d "$loopdev" || cleanup_failed=1
            associated=$(losetup --noheadings --output NAME --associated "$loop_image" 2>/dev/null | sed '/^[[:space:]]*$/d')
            [[ -z "$associated" ]] || cleanup_failed=1
        else
            cleanup_failed=1
        fi
        loopdev=
    fi
    rm -rf -- "$tmp"
    [[ ! -e "$loop_image" ]] || cleanup_failed=1
    rmdir "$store" 2>/dev/null || true
    rmdir "$backing" 2>/dev/null || true
    rmdir /rom 2>/dev/null || true
    if (( cleanup_failed )); then
        echo 'FAIL: private mount or loop cleanup failed' >&2
        status=1
    else
        echo STARGZ_STORAGE_INIT_CLEANUP_OK
    fi
    exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }
expect_failure() {
    label=$1
    expected=$2
    shift 2
    output="$tmp/$label.log"
    if "$@" >"$output" 2>&1; then
        cat "$output" >&2
        fail "$label unexpectedly succeeded"
    fi
    if ! grep -Fq "$expected" "$output"; then
        cat "$output" >&2
        fail "$label failed without expected diagnostic: $expected"
    fi
    pass "$label rejected ($expected)"
}
store_mount_count() {
    awk -v target="$store" '$5 == target { count++ } END { print count + 0 }' \
        /proc/self/mountinfo
}
write_cmdline() {
    printf '%s\n' "$1" >"$tmp/cmdline"
    if (( proc_cmdline_bound )); then
        umount /proc/cmdline
        proc_cmdline_bound=0
    fi
    mount --bind "$tmp/cmdline" /proc/cmdline
    proc_cmdline_bound=1
}

mkdir -p "$backing" "$store"
expect_failure absent_backing 'guest upper is not mounted at /rom/overlay' "$helper"
pass 'absent backing is fail-closed'

# An unrelated filesystem must never be accepted as the guest upper.
mount -t proc proc "$backing"
expect_failure unsupported_backing 'unsupported guest-upper filesystem' "$helper"
umount "$backing"
pass 'unsupported backing filesystem is fail-closed'

# tmpfs is accepted only for the explicit RAM-layout control. Override the
# container's view of /proc/cmdline with a private bind mount for both cases.
mount -t tmpfs -o size=16m stargz-test-tmpfs "$backing"
write_cmdline 'console=ttyS0 stargz_storage_init_test=1'
expect_failure implicit_tmpfs 'RAM guest upper requires explicit overlay_root=ram' "$helper"
write_cmdline 'console=ttyS0 overlay_root=ram stargz_storage_init_test=1'
"$helper"
source_id=$(stat -c '%d:%i' "$backing/stargz")
store_id=$(stat -c '%d:%i' "$store")
[[ "$source_id" == "$store_id" ]] || fail 'RAM store is not bound to the upper source'
echo "RAM_BIND_ID source=$source_id store=$store_id"
printf 'ram-control-ok\n' >"$store/ram-control"
test -f "$backing/stargz/ram-control"
[[ "$(store_mount_count)" == 1 ]] || fail 'RAM setup did not create exactly one store mount'
"$helper"
[[ "$(store_mount_count)" == 1 ]] || fail 'repeated RAM setup stacked store mounts'
pass 'explicit overlay_root=ram control binds correctly and is idempotent'
umount /proc/cmdline
proc_cmdline_bound=0
umount "$store"
umount "$backing"

# Create one container-owned ext4 loop image. mount -o loop asks util-linux to
# use autoclear, so forced container removal cannot strand a persistent loop.
# Mounts remain in this private container namespace.
mkdir -p "$backing" "$store"
loop_image=$tmp/guest-upper.ext4
truncate -s 64M "$loop_image"
mkfs.ext4 -q -F "$loop_image"
mount -t ext4 -o loop "$loop_image" "$backing"
loop_devices=$(losetup --noheadings --output NAME --associated "$loop_image" | sed '/^[[:space:]]*$/d')
[[ -n "$loop_devices" && "$loop_devices" != *$'\n'* ]] ||
    fail "expected exactly one autoclear loop for $loop_image, found: $loop_devices"
loopdev=$loop_devices
loop_autoclear=$(losetup --noheadings --output AUTOCLEAR --associated "$loop_image" | tr -d '[:space:]')
[[ "$loop_autoclear" == 1 ]] || fail "loop $loopdev is not autoclear (flag=$loop_autoclear)"
upper_info=$(findmnt -n -o FSTYPE,SOURCE,MAJ:MIN --target "$backing")
read -r upper_fs upper_source upper_majmin <<<"$upper_info"
[[ "$upper_fs" == ext4 ]] || fail "expected ext4 backing, found $upper_fs"
loop_majmin=$(lsblk -dn -o MAJ:MIN "$loopdev" | tr -d '[:space:]')
[[ "$upper_majmin" == "$loop_majmin" ]] ||
    fail "mounted upper device number $upper_majmin does not match loop $loopdev ($loop_majmin)"
echo "EXT4_UPPER fstype=$upper_fs source=$upper_source majmin=$upper_majmin loop=$loopdev autoclear=$loop_autoclear image=$loop_image"

"$helper"
source_id=$(stat -c '%d:%i' "$backing/stargz")
store_id=$(stat -c '%d:%i' "$store")
[[ "$source_id" == "$store_id" ]] || fail 'ext4 store is not bound to the upper source'
echo "EXT4_BIND_ID source=$source_id store=$store_id"
printf 'ext4-control-ok\n' >"$store/ext4-control"
test -f "$backing/stargz/ext4-control"
[[ "$(store_mount_count)" == 1 ]] || fail 'ext4 setup did not create exactly one store mount'
"$helper"
[[ "$(store_mount_count)" == 1 ]] || fail 'repeated ext4 setup stacked store mounts'
pass 'ext4 backing matches the exact loop device; source identity, write-through, and idempotence verified'

# A pre-existing mount from another directory on the same ext4 device must be
# rejected. Device equality alone is insufficient: the bind source must match.
umount "$store"
mkdir -p "$backing/other-store"
mount --bind "$backing/other-store" "$store"
expect_failure mismatched_existing_store \
    'existing store mount does not reference the private guest upper' "$helper"
umount "$store"
pass 'mismatched existing store mount is fail-closed'
umount "$backing"
remaining_loops=$(losetup --noheadings --output NAME --associated "$loop_image" | sed '/^[[:space:]]*$/d')
[[ -z "$remaining_loops" ]] || fail "autoclear loop remained after unmount: $remaining_loops"
[[ ! -e "$loop_image" ]] && fail 'owned loop image unexpectedly disappeared before cleanup'
pass 'owned autoclear loop detached after unmount'
loopdev=

echo STARGZ_STORAGE_INIT_ACCEPTANCE_OK
CONTAINER
then
    cat "$run_log" >&2
    exit 1
fi
cat "$run_log"
if ! grep -Fq STARGZ_STORAGE_INIT_ACCEPTANCE_OK "$run_log"; then
    echo "container exited without the acceptance marker" >&2
    exit 1
fi
if ! grep -Fq STARGZ_STORAGE_INIT_CLEANUP_OK "$run_log"; then
    echo "container exited without the private mount/loop cleanup marker" >&2
    exit 1
fi
container_started=0
echo STARGZ_STORAGE_INIT_CONTAINER_OK
