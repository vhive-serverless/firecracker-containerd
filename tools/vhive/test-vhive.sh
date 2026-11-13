#!/usr/bin/env bash
# Build the pinned vHive tests against an exact local candidate runtime archive.
# This provisional harness does not validate the published consumer module graph.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
export PATH="$stack_dir/toolchains/go1.26.11/bin:$PATH"
export GOWORK=off GOTOOLCHAIN=local
cd "$repo_dir"
revision=$(git rev-parse --short=12 HEAD)
base_image="localhost/firecracker-containerd-test:$revision"
test -z "$(git status --porcelain)"
base_image_id=$(docker image inspect --format '{{.Id}}' "$base_image")
mapfile -t vhive_inputs < <(python3 -c 'import json; v=json.load(open("docs/modernization/versions.json"))["selected_inputs"]["vhive"]; print(v["repository"],v["commit"],v["function_image"],sep="\n")')
vhive_remote=${VHIVE_SOURCE_REPOSITORY:-${vhive_inputs[0]}}
vhive_revision=${vhive_inputs[1]}
export VHIVE_FUNCTION_IMAGE=${vhive_inputs[2]}
export VHIVE_RUNTIME_REVISION=$(git rev-parse HEAD)
mkdir -p tmp "$stack_dir/evidence"
build_dir=$(mktemp -d "$repo_dir/tmp/vhive-build.XXXXXXXX")
logs=$(mktemp -d "$stack_dir/evidence/vhive-$revision.XXXXXXXX")
mkdir -p "$build_dir/src" "$build_dir/image" "$build_dir/runtime"
git archive HEAD | tar -x -C "$build_dir/runtime"
printf 'VHIVE_LOGS=%s\n' "$logs"
git -C "$build_dir/src" init --quiet
git -C "$build_dir/src" remote add origin "$vhive_remote"
git -C "$build_dir/src" fetch --depth 1 origin "$vhive_revision"
git -C "$build_dir/src" checkout --detach "$vhive_revision"
test "$(git -C "$build_dir/src" rev-parse HEAD)" = "$vhive_revision"
install -m 755 tools/vhive/run-in-container.sh "$build_dir/image/run-in-container.sh"
go_builder=public.ecr.aws/docker/library/golang:1.26.11-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0
mkdir -p "$stack_dir/toolchains/vhive-go-cache" "$(go env GOMODCACHE)"
docker run --rm --user "$(id -u):$(id -g)" \
    --volume "$build_dir:/build" --volume "$build_dir/runtime:/runtime:ro" --volume "$(go env GOMODCACHE):/go/pkg/mod" \
    --volume "$stack_dir/toolchains/vhive-go-cache:/build-cache" \
    --workdir /build/src --env HOME=/tmp --env GOCACHE=/build-cache \
    --env GOWORK=off --env GOTOOLCHAIN=local --env CGO_ENABLED=1 \
    "$go_builder" bash -ec 'go mod edit -replace=github.com/firecracker-microvm/firecracker-containerd=/runtime &&
        go test -mod=mod -c -trimpath -o /build/image/vhive-acceptance.test ./ctriface &&
        go list -mod=mod -m -json all > /build/modules.json &&
        go version -m /build/image/vhive-acceptance.test > /build/test-buildinfo.txt'
printf 'purpose=provisional-local-module-candidate\nruntime=%s\nruntime_tree=%s\nvhive_test_source=%s\nvhive_source_repository=%s\nfunction=%s\nbase_image=%s\n' \
    "$VHIVE_RUNTIME_REVISION" "$(git rev-parse HEAD^{tree})" "$vhive_revision" "$vhive_remote" "$VHIVE_FUNCTION_IMAGE" "$base_image_id" > "$logs/inputs"
cp "$build_dir/modules.json" "$build_dir/test-buildinfo.txt" "$logs/"
sha256sum "$build_dir/image/vhive-acceptance.test" "$build_dir/src/go.mod" "$build_dir/src/go.sum" > "$logs/binaries.sha256"
image="localhost/fcmod-vhive-test:$revision"
docker build --build-arg "BASE_IMAGE=$base_image" -f tools/vhive/Dockerfile -t "$image" "$build_dir/image"
export FICD_DM_VOLUME_GROUP=
export FICD_DM_POOL=fc-vhive-$revision-$$
cleanup() {
    if sudo -n dmsetup info "$FICD_DM_POOL" >/dev/null 2>&1; then
        tools/thinpool.sh remove "$FICD_DM_POOL"
    fi
}
trap cleanup EXIT
tools/thinpool.sh create "$FICD_DM_POOL"
docker run --rm --init --privileged --network bridge --ipc=host \
    --volume /dev:/dev --volume /run/udev/control:/run/udev/control \
    --volume "$logs:/var/log/firecracker-containerd-test" \
    --env FICD_DM_VOLUME_GROUP --env FICD_DM_POOL --env VHIVE_FUNCTION_IMAGE --env VHIVE_RUNTIME_REVISION \
    "$image" 'exec /usr/local/bin/run-vhive-acceptance' 2>&1 | tee "$logs/acceptance.log"
cleanup
grep -qx VHIVE_LEGACY_SNAPSHOT_CANDIDATE_OK "$logs/acceptance.log"
if sudo -n dmsetup info "$FICD_DM_POOL" >/dev/null 2>&1; then
    echo "vHive test pool still exists: $FICD_DM_POOL" >&2
    exit 1
fi
echo VHIVE_WORKER_CANDIDATE_OK
