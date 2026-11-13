#!/usr/bin/env bash
# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
# Convert the exact SquashFS baseline tree to an optional uncompressed ext4 root.
set -euo pipefail
source_image=${1:?SquashFS source image required}
output_image=${2:?ext4 output image required}
image_bytes=536870912
image_uuid=30b5df10-6ad9-4c79-9d64-06b319188e41
export SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-1794268800}
export E2FSPROGS_FAKE_TIME="$SOURCE_DATE_EPOCH"
work_dir=$(mktemp -d)
temporary_image=$(mktemp "${output_image}.tmp.XXXXXXXX")
cleanup() {
    rm -rf "$work_dir"
    rm -f "$temporary_image"
}
trap cleanup EXIT

unsquashfs -no-progress -d "$work_dir/rootfs" "$source_image"
# Compare contents, modes, owners, symlink/hardlink targets and special devices.
# Archive timestamps are normalized; filesystem inode timestamps remain intact.
(cd "$work_dir/rootfs" && tar --sort=name --numeric-owner --format=gnu \
    --mtime=@0 -cf - .) | sha256sum > "${output_image}.tree.sha256"
sha256sum "$source_image" > "${output_image}.source.sha256"
truncate -s "$image_bytes" "$temporary_image"
mke2fs -F -t ext4 -b 4096 -I 256 -m 0 -L fc-root-dax \
    -U "$image_uuid" -O ^has_journal \
    -E "lazy_itable_init=0,hash_seed=$image_uuid,root_owner=0:0" \
    -d "$work_dir/rootfs" "$temporary_image"
# ext4 creates this empty recovery directory; remove it to retain the exact tree.
debugfs -w -R 'rmdir /lost+found' "$temporary_image"
e2fsck -f -n "$temporary_image"
dumpe2fs -h "$temporary_image" > "${output_image}.superblock"
grep -Eq '^Filesystem state: +clean$' "${output_image}.superblock"
if grep '^Filesystem features:' "${output_image}.superblock" | grep -qw has_journal; then
    echo 'DAX root image unexpectedly has a journal' >&2
    exit 1
fi
test "$(stat -c %s "$temporary_image")" = "$image_bytes"
test "$((image_bytes % 2097152))" = 0
chmod 0444 "$temporary_image"
mv -f "$temporary_image" "$output_image"
sha256sum "$output_image" > "${output_image}.sha256"
printf 'DAX_ROOT_IMAGE_READY bytes=%s block_size=4096 nominal_pmem_metadata_bytes=8388608\n' "$image_bytes"
