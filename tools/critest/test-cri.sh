#!/usr/bin/env bash
# Exercise the real containerd CRI in a private daemon, network and thin pool.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
cd "$repo_dir"
revision=$(git rev-parse HEAD)
dirty=false
if test -n "$(git status --porcelain)"; then dirty=true; fi
if test -z "${CRI_BASE_IMAGE:-}" && test "$dirty" = true; then
    echo 'Default CRI validation requires a clean committed checkout.' >&2
    exit 1
fi
image=${CRI_BASE_IMAGE:-"localhost/firecracker-containerd-test:${revision:0:12}"}
runtime_revision=${CRI_RUNTIME_REVISION:-$revision}
image_id=$(docker image inspect --format '{{.Id}}' "$image")
mkdir -p "$stack_dir/evidence"
logs=$(mktemp -d "$stack_dir/evidence/cri-${revision:0:12}.XXXXXXXX")
suffix=$(basename "$logs")
suffix=${suffix//./-}
name="fcmod-$suffix"
export FICD_DM_VOLUME_GROUP=
export FICD_DM_POOL="fc-$suffix"
container_owned=0
pool_owned=0
cleanup() {
    if test "$container_owned" = 1; then
        docker rm --force "$name" >/dev/null
        container_owned=0
    fi
    if test "$pool_owned" = 1; then
        tools/thinpool.sh remove "$FICD_DM_POOL"
        pool_owned=0
    fi
}
trap cleanup EXIT
printf 'CRI_LOGS=%s\n' "$logs"
{
    printf 'script_revision=%s\nruntime_revision=%s\nimage=%s\nfocus=%s\n' \
        "$revision" "$runtime_revision" "$image_id" "${CRI_FOCUS:-all}"
    printf 'source_dirty=%s\nimage_override=%s\n' "$dirty" "${CRI_BASE_IMAGE:+true}"
    sha256sum tools/critest/test-cri.sh tools/critest/run-in-container.sh
} > "$logs/inputs.log"
tools/thinpool.sh create "$FICD_DM_POOL"
pool_owned=1
docker create --init --name "$name" --privileged --network bridge --ipc=host \
    --volume /dev:/dev --volume /run/udev/control:/run/udev/control \
    --volume "$repo_dir/tools/critest/run-in-container.sh:/run-cri:ro" \
    --volume "$logs:/var/log/firecracker-containerd-test" \
    --env FICD_DM_POOL --env "CRI_RUNTIME_REVISION=$runtime_revision" \
    --env "CRI_FOCUS=${CRI_FOCUS:-}" --entrypoint /bin/bash \
    "$image" /run-cri > "$logs/container-id"
container_owned=1
set +e
docker start --attach "$name" 2>&1 | tee "$logs/critest.log"
run_code=${PIPESTATUS[0]}
set -e
test_code=$(docker inspect --format '{{.State.ExitCode}}' "$name")
printf 'docker_attach_exit=%s\ncontainer_exit=%s\n' "$run_code" "$test_code" | tee "$logs/exit-status"
cleanup
test "$run_code" = 0
test "$test_code" = 0
grep -qx CRI_TESTS_PASSED "$logs/critest.log"
echo CRI_WORKER_TESTS_PASSED
