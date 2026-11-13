#!/usr/bin/env bash
# Check that named pools own separate files/devices and detach them on removal.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
export FICD_DM_VOLUME_GROUP=''
first=fcmod-pool-audit-$$-a
second=fcmod-pool-audit-$$-b
cleanup() {
    for pool in "$first" "$second"; do
        if sudo -n dmsetup info "$pool" >/dev/null 2>&1; then
            "$repo_dir/tools/thinpool.sh" remove "$pool"
        fi
    done
}
trap cleanup EXIT
"$repo_dir/tools/thinpool.sh" create "$first"
"$repo_dir/tools/thinpool.sh" create "$second"
first_devices=$(sudo -n dmsetup table "$first" | awk '{print $4, $5}')
second_devices=$(sudo -n dmsetup table "$second" | awk '{print $4, $5}')
test "$first_devices" != "$second_devices"
sudo -n dmsetup table "$first"
sudo -n dmsetup table "$second"
"$repo_dir/tools/thinpool.sh" reset "$second"
test "$(sudo -n dmsetup table "$first" | awk '{print $4, $5}')" = "$first_devices"
cleanup
for pool in "$first" "$second"; do
    for backing in data metadata; do
        path="$repo_dir/tmp/devmapper/$pool/$backing"
        test ! -e "$path"
        test -z "$(sudo -n losetup --output NAME --noheadings --associated "$path")"
    done
done
echo THINPOOL_ISOLATION_AND_CLEANUP_OK
