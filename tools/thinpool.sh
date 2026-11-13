#! /bin/bash
#
# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License"). You may
# not use this file except in compliance with the License. A copy of the
# License is located at
#
# 	http://aws.amazon.com/apache2.0/
#
# or in the "license" file accompanying this file. This file is distributed
# on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
# express or implied. See the License for the specific language governing
# permissions and limitations under the License.

set -euo pipefail

FICD_DM_VOLUME_GROUP=${FICD_DM_VOLUME_GROUP:-}
subcommand=${1:?usage: thinpool.sh create|remove|reset POOL_NAME}
name=${2:?usage: thinpool.sh create|remove|reset POOL_NAME}
if [[ ! "$name" =~ ^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$ ]]; then
    echo "Invalid thin-pool name: $name" >&2
    exit 2
fi

# Each pool owns different backing files and loop devices. Never reuse the
# backing of a running pool when creating another pool in the same checkout.
DIR="$(cd "$(dirname "$BASH_SOURCE")/.." && pwd)/tmp/devmapper/$name"
mkdir -p "$DIR"
exec 9>"$DIR/lock"
flock -x 9

create_loopback_device() {
    local path=$1
    local size=$2

    if [[ ! -f "$path" ]]; then
        touch "$path"
        truncate -s "$size" "$path"
    fi

    local dev=$(sudo losetup --output NAME --noheadings --associated "$path")
    if [[ -z "$dev" ]]; then
        dev=$(sudo losetup --find --show "$path")
    fi
    echo "$dev"
}

if [[ -z "$FICD_DM_VOLUME_GROUP" ]]; then
    pool_create() {
        mkdir -p "$DIR"

        if sudo dmsetup info "$name" >/dev/null 2>&1; then
            echo "Thin pool already exists: $name" >&2
            return 1
        fi
        local datadev=$(create_loopback_device "$DIR/data" '10G')
        local metadev=$(create_loopback_device "$DIR/metadata" '1G')

        local sectorsize=512
        local datasize="$(sudo blockdev --getsize64 -q ${datadev})"
        local length_sectors=$(bc <<< "${datasize}/${sectorsize}")
        local thinp_table="0 ${length_sectors} thin-pool ${metadev} ${datadev} 128 32768 1 skip_block_zeroing"
        sudo dmsetup create "$name" --table "${thinp_table}"
    }

    pool_remove() {
        for snapshot in $(sudo dmsetup ls | awk "/^$name-snap-/ { print \$1 }"); do
            sudo dmsetup remove $snapshot
        done

        # The complete pool and its private metadata backing are discarded;
        # enumerating thin IDs until the first gap neither finds all IDs nor
        # adds any cleanup beyond removing the pool itself.
        sudo dmsetup remove "$name"
        for path in "$DIR/data" "$DIR/metadata"; do
            while read -r dev; do
                if [[ -n "$dev" ]]; then sudo losetup --detach "$dev"; fi
            done < <(sudo losetup --output NAME --noheadings --associated "$path")
            rm -f "$path"
        done
    }

    pool_reset() {
        if sudo dmsetup info "$name"; then
            pool_remove
        fi
        pool_create
    }
else
    dm_device="/dev/mapper/$(echo ${FICD_DM_VOLUME_GROUP} | sed -e s/-/--/g)-$name"

    pool_create() {
        echo sudo lvcreate --type thin-pool \
             --poolmetadatasize 16MiB \
             --size 1GiB \
             -n "$name" "$FICD_DM_VOLUME_GROUP"
        sudo lvcreate --type thin-pool \
             --poolmetadatasize 16MiB \
             --size 1GiB \
             -n "$name" "$FICD_DM_VOLUME_GROUP"
    }

    pool_remove() {
        # Remove only this pool's snapshot mappings. Busy mappings cause failure
        # rather than forcing removal from a running workload.
        while read -r snapshot; do
            if [[ -n "$snapshot" ]]; then sudo dmsetup remove "$snapshot"; fi
        done < <(sudo dmsetup ls | awk -v prefix="$(basename "$dm_device")-snap-" 'index($1, prefix) == 1 { print $1 }')
        sudo lvremove -f "$dm_device"
    }

    pool_reset() {
        if [ -e "$dm_device" ]; then
            pool_remove
        fi
        pool_create
    }

fi

case "$subcommand" in
    'create')
        pool_create
        ;;
    'remove')
        pool_remove
        ;;
    'reset')
        pool_reset
        ;;
    *)
        echo "This script doesn't support $subcommand"
        exit 1
esac
