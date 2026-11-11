#!/usr/bin/env bash
# Build the pinned vHive source and exercise it against a dedicated test stack.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
export PATH="$stack_dir/toolchains/go1.26.11/bin:$PATH"
export GOWORK=off GOTOOLCHAIN=local
cd "$repo_dir"
revision=$(git rev-parse --short=12 HEAD)
base_image="localhost/firecracker-containerd-test:$revision"
if [ "${ALLOW_DIRTY:-0}" = 1 ]; then
    base_image=${VHIVE_BASE_IMAGE:-"$base_image"}
else
    test -z "$(git status --porcelain)"
fi
base_image_id=$(docker image inspect --format '{{.Id}}' "$base_image")
mapfile -t vhive_inputs < <(python3 -c 'import json; v=json.load(open("docs/modernization/versions.json"))["selected_inputs"]["vhive"]; print(v["repository"],v["commit"],v["function_image"],v["patch"],sep="\n")')
vhive_remote=${vhive_inputs[0]}
vhive_revision=${vhive_inputs[1]}
vhive_patch=${vhive_inputs[3]}
export VHIVE_FUNCTION_IMAGE=${vhive_inputs[2]}
export VHIVE_RUNTIME_REVISION=$(git rev-parse HEAD)
mkdir -p tmp "$stack_dir/evidence"
build_dir=$(mktemp -d "$repo_dir/tmp/vhive-build.XXXXXXXX")
logs=$(mktemp -d "$stack_dir/evidence/vhive-$revision.XXXXXXXX")
mkdir -p "$build_dir/src" "$build_dir/image"
printf 'VHIVE_LOGS=%s\n' "$logs"
git -C "$build_dir/src" init --quiet
git -C "$build_dir/src" remote add origin "$vhive_remote"
git -C "$build_dir/src" fetch --depth 1 origin "$vhive_revision"
git -C "$build_dir/src" checkout --detach "$vhive_revision"
test "$(git -C "$build_dir/src" rev-parse HEAD)" = "$vhive_revision"
git -C "$build_dir/src" apply "$repo_dir/$vhive_patch"
mkdir -p "$build_dir/src/cmd/fcmod-probe"
cp tools/vhive/probe.go.in "$build_dir/src/cmd/fcmod-probe/main.go"
install -m 755 tools/vhive/run-in-container.sh "$build_dir/image/run-in-container.sh"
go_builder=public.ecr.aws/docker/library/golang:1.26.11-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0
mkdir -p "$stack_dir/toolchains/vhive-go-cache" "$(go env GOMODCACHE)"
docker run --rm --user "$(id -u):$(id -g)" \
    --volume "$build_dir:/build" --volume "$(go env GOMODCACHE):/go/pkg/mod" \
    --volume "$stack_dir/toolchains/vhive-go-cache:/build-cache" \
    --workdir /build/src --env HOME=/tmp --env GOCACHE=/build-cache \
    --env GOWORK=off --env GOTOOLCHAIN=local --env CGO_ENABLED=1 \
    "$go_builder" bash -ec 'go build -mod=readonly -trimpath -o /build/image/vhive . &&
        go build -mod=readonly -trimpath -o /build/image/vhive-probe ./cmd/fcmod-probe'
printf 'runtime=%s\nvhive=%s\nfunction=%s\nbase_image=%s\n' \
    "$VHIVE_RUNTIME_REVISION" "$vhive_revision" "$VHIVE_FUNCTION_IMAGE" "$base_image_id" > "$logs/inputs"
sha256sum "$build_dir/image/vhive" "$build_dir/image/vhive-probe" "$vhive_patch" > "$logs/binaries.sha256"
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
grep -qx VHIVE_DEPLOY_INVOKE_SNAPSHOT_RESTORE_OK "$logs/acceptance.log"
if sudo -n dmsetup info "$FICD_DM_POOL" >/dev/null 2>&1; then
    echo "vHive test pool still exists: $FICD_DM_POOL" >&2
    exit 1
fi
echo VHIVE_WORKER_ACCEPTANCE_OK
