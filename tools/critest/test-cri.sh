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
extra_mounts=()
if test "${CRI_INTEGRATION_TESTS:-0}" = 1; then
    export PATH="$stack_dir/toolchains/go1.26.11/bin:$PATH"
    make -C cri cri-runtime.test
    extra_mounts+=(--volume "$repo_dir/cri/cri-runtime.test:/cri-runtime.test:ro")
fi
mkdir -p "$stack_dir/evidence"
logs=$(mktemp -d "$stack_dir/evidence/cri-${revision:0:12}.XXXXXXXX")
suffix=$(basename "$logs")
suffix=${suffix//./-}
name="fcmod-$suffix"
export FICD_DM_VOLUME_GROUP=
export FICD_DM_POOL="fc-$suffix"
container_owned=0
pool_owned=0
storage=''
cleanup() {
    if test "$container_owned" = 1; then
        docker rm --force "$name" >/dev/null
        container_owned=0
    fi
    if test "$pool_owned" = 1; then
        tools/thinpool.sh remove "$FICD_DM_POOL"
        pool_owned=0
    fi
    # Only remove the unique directory allocated by this invocation.
    if test -n "$storage"; then sudo -n rm -rf -- "$storage"; fi
}
trap cleanup EXIT
mkdir -p "$stack_dir/.ficd-test-vms"
storage=$(mktemp -d "$stack_dir/.ficd-test-vms/cri-$suffix.XXXXXXXX")
printf 'CRI_LOGS=%s\n' "$logs"
{
    printf 'script_revision=%s\nruntime_revision=%s\nimage=%s\nfocus=%s\n' \
        "$revision" "$runtime_revision" "$image_id" "${CRI_FOCUS:-all}"
    printf 'source_dirty=%s\nimage_override=%s\n' "$dirty" "${CRI_BASE_IMAGE:+true}"
    printf 'websocket_streaming=%s\n' "${CRI_WEBSOCKETS:-0}"
    printf 'runtime_integration_tests=%s\n' "${CRI_INTEGRATION_TESTS:-0}"
    printf 'vm_storage=%s\n' "$storage"
    findmnt -T "$storage" -o TARGET,SOURCE,FSTYPE
    if test "${CRI_INTEGRATION_TESTS:-0}" = 1; then sha256sum cri/cri-runtime.test; fi
    sha256sum tools/critest/test-cri.sh tools/critest/run-in-container.sh
} > "$logs/inputs.log"
tools/thinpool.sh create "$FICD_DM_POOL"
pool_owned=1
docker create --init --name "$name" --privileged --network bridge --ipc=host \
    --volume /dev:/dev --volume /run/udev/control:/run/udev/control \
    --volume "$repo_dir/tools/critest/run-in-container.sh:/run-cri:ro" \
    --volume "$logs:/var/log/firecracker-containerd-test" \
    --volume "$storage:/srv/firecracker_containerd_tests" \
    --env FICD_DM_POOL --env "CRI_RUNTIME_REVISION=$runtime_revision" \
    --env "CRI_FOCUS=${CRI_FOCUS:-}" --entrypoint /bin/bash \
    --env "CRI_WEBSOCKETS=${CRI_WEBSOCKETS:-0}" \
    --env "CRI_INTEGRATION_TESTS=${CRI_INTEGRATION_TESTS:-0}" "${extra_mounts[@]}" \
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
