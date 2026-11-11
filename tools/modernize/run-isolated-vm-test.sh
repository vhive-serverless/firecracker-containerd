#!/usr/bin/env bash
# Give each Docker test invocation its own disk-backed VM storage lifetime.
set -euo pipefail
base=$1
shift
mkdir -p "$base"
workspace=$(mktemp -d "$base/test.XXXXXXXX")
mkdir "$workspace/vms"
cleanup() {
    status=$?
    if test -s "$workspace/container-id"; then
        container=$(cat "$workspace/container-id")
        # Do not remove backing files while an interrupted test is still live.
        if docker inspect "$container" >/dev/null 2>&1; then
            docker rm --force "$container" >/dev/null || return 1
        fi
    fi
    sudo -n rm -rf -- "$workspace" || return 1
    return "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
docker run --cidfile "$workspace/container-id" \
    --volume "$workspace/vms:/srv/firecracker_containerd_tests" "$@"
