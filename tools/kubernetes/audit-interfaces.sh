#!/usr/bin/env bash
# Verify released Kubernetes Pod checkpoint API support on the worker.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
cd "$repo_dir"
base_revision=$(python3 -c 'import json; print(json.load(open("docs/modernization/versions.json"))["step_2_validation"]["completion_commit"][:12])')
base_image=${AUDIT_BASE_IMAGE:-"localhost/firecracker-containerd-test:$base_revision"}
base_image_id=$(docker image inspect --format '{{.Id}}' "$base_image")
mkdir -p "$stack_dir/evidence" "$stack_dir/tmp" "$stack_dir/toolchains"
logs=$(mktemp -d "$stack_dir/evidence/kubernetes-api-audit.XXXXXXXX")
data=$(mktemp -d "$stack_dir/tmp/k3s-api-audit.XXXXXXXX")
name=fcmod-k3s-api-audit-$$
container_created=0
printf 'AUDIT_LOGS=%s\n' "$logs"
cleanup() {
    if test "$container_created" = 1; then
        docker rm --force "$name" >/dev/null
        container_created=0
    fi
    if test -d "$data"; then
        sudo -n rm -rf -- "$data"
    fi
}
trap cleanup EXIT

download() {
    local url=$1 path=$2 digest=$3
    mkdir -p "$(dirname "$path")"
    if ! test -f "$path" || ! echo "$digest  $path" | sha256sum --check --status; then
        curl --fail --location --retry 3 "$url" -o "$path.partial"
        echo "$digest  $path.partial" | sha256sum --check
        mv "$path.partial" "$path"
    fi
    echo "$digest  $path" | sha256sum --check
    chmod +x "$path"
}

k3s="$stack_dir/toolchains/k3s-v1.37.2/k3s"
upstream_stable="$stack_dir/toolchains/kubernetes-v1.37.2/kube-apiserver"
apiserver="$stack_dir/toolchains/kubernetes-v1.38.0-beta.0/kube-apiserver"
download 'https://github.com/k3s-io/k3s/releases/download/v1.37.2%2Bk3s1/k3s' "$k3s" \
    265b3ee6a3198ef14033c63ef46cbb32370d83795199fb6d1b01f21575eddbda
download 'https://dl.k8s.io/release/v1.37.2/bin/linux/amd64/kube-apiserver' "$upstream_stable" \
    b87e5a91107cf994bfa38c6c1b28eb39f01fc040734ec9942c22738bc8b8f34b
download 'https://dl.k8s.io/release/v1.38.0-beta.0/bin/linux/amd64/kube-apiserver' "$apiserver" \
    24c8161f0e983a3134002c7f32866dc8fe36f706a4e645a94bb2c0c81984afa0
{
    printf 'runtime_revision=%s\nbase_image=%s\n' "$(git rev-parse HEAD)" "$base_image_id"
    "$k3s" --version
    "$upstream_stable" --version
    "$apiserver" --version
    sha256sum "$k3s" "$upstream_stable" "$apiserver" tools/kubernetes/audit-interfaces.sh
} > "$logs/inputs.log"

docker run --detach --name "$name" --privileged --network bridge \
    --volume "$k3s:/usr/local/bin/k3s:ro" --volume "$data:/var/lib/rancher/k3s" \
    --entrypoint /usr/local/bin/k3s "$base_image" server --disable-agent \
    --data-dir=/var/lib/rancher/k3s --disable=traefik,servicelb,local-storage,metrics-server \
    --kube-apiserver-arg=feature-gates=PodLevelCheckpointRestore=true \
    --kube-controller-manager-arg=feature-gates=PodLevelCheckpointRestore=true \
    > "$logs/container-id"
container_created=1
for attempt in $(seq 1 30); do
    if test "$(docker inspect --format '{{.State.Running}}' "$name")" != true; then break; fi
    if docker exec "$name" /usr/local/bin/k3s kubectl get --raw /apis/node.k8s.io/v1alpha1 \
        > "$logs/k3s-node-api.json" 2>/dev/null &&
        python3 -c 'import json,sys; assert any(r["name"] == "podcheckpoints" for r in json.load(open(sys.argv[1])).get("resources", []))' \
        "$logs/k3s-node-api.json" 2>/dev/null; then
        break
    fi
    sleep 1
done
docker logs "$name" > "$logs/k3s.log" 2>&1
for label in upstream-stable upstream-beta; do
    binary=$apiserver
    if test "$label" = upstream-stable; then binary=$upstream_stable; fi
    set +e
    docker run --rm --network none --volume "$binary:/audit/kube-apiserver:ro" \
        --entrypoint /usr/bin/timeout "$base_image" 15s /audit/kube-apiserver \
        --feature-gates=PodLevelCheckpointRestore=true --etcd-servers=http://127.0.0.1:1 \
        --bind-address=127.0.0.1 --secure-port=16443 --cert-dir=/tmp/fcmod-apiserver-certs \
        > "$logs/$label.log" 2>&1
    code=$?
    set -e
    printf 'exit_code=%s\n' "$code" >> "$logs/$label.log"
done
{
    cat "$logs/inputs.log"
    printf '\nk3s gate result:\n'
    grep -E 'unrecognized feature gate|Failed to set feature-gates' "$logs/k3s.log" || true
    for label in upstream-stable upstream-beta; do
        printf '\n%s gate result:\n' "$label"
        grep -E 'unrecognized feature gate|exit_code=' "$logs/$label.log" || true
    done
    sha256sum "$logs/k3s.log" "$logs/upstream-stable.log" "$logs/upstream-beta.log"
} > "$logs/receipt.log"
cat "$logs/receipt.log"
cleanup
if grep -q 'unrecognized feature gate: PodLevelCheckpointRestore' "$logs/k3s.log" &&
   grep -q 'unrecognized feature gate: PodLevelCheckpointRestore' "$logs/upstream-stable.log" &&
   grep -q 'unrecognized feature gate: PodLevelCheckpointRestore' "$logs/upstream-beta.log"; then
    echo KUBERNETES_POD_CHECKPOINT_API_UNAVAILABLE
    exit 2
fi
# A changed result needs review of discovery and kubelet behavior, not an
# automatic claim that the complete checkpoint/restore integration works.
echo KUBERNETES_API_AUDIT_REQUIRES_REVIEW
exit 3
