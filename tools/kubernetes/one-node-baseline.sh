#!/usr/bin/env bash
# Provision and probe released upstream Kubernetes without changing host services.
# This validates the cluster baseline, not Firecracker or Pod checkpoint/restore.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
state_dir="$stack_dir/kubernetes/upstream-one-node"
cluster=fcmod-upstream-137
node="$cluster-control-plane"
export KIND_EXPERIMENTAL_PROVIDER=docker
cd "$repo_dir"
mapfile -t inputs < <(python3 - <<'PY'
import json
v = json.load(open("docs/modernization/versions.json"))["selected_inputs"]["kubernetes_one_node"]
for key in ("kind_version", "kind_sha256", "kubernetes_version", "server_archive_sha256", "base_image", "smoke_image"):
    print(v[key])
PY
)
kind_version=${inputs[0]}
kind_sha=${inputs[1]}
kube_version=${inputs[2]}
archive_sha=${inputs[3]}
base_image=${inputs[4]}
smoke_image=${inputs[5]}
kind="$stack_dir/toolchains/kind-$kind_version/kind-linux-amd64"
archive="$stack_dir/toolchains/kubernetes-$kube_version/kubernetes-server-linux-amd64.tar.gz"
image="localhost/fcmod-kubernetes-node:$kube_version"

download() {
    local url=$1 path=$2 digest=$3
    mkdir -p "$(dirname "$path")"
    if ! test -f "$path" || ! echo "$digest  $path" | sha256sum --check --status; then
        curl --fail --location --retry 3 "$url" -o "$path.partial"
        echo "$digest  $path.partial" | sha256sum --check
        mv "$path.partial" "$path"
    fi
    echo "$digest  $path" | sha256sum --check
}

owned_node() {
    test -f "$state_dir/node-id"
    test "$(cat "$state_dir/node-id")" = "$(docker inspect --format '{{.Id}}' "$node")"
    test "$(docker inspect --format '{{index .Config.Labels "io.x-k8s.kind.cluster"}}' "$node")" = "$cluster"
}

kubectl() {
    docker exec "$node" kubectl --kubeconfig=/etc/kubernetes/admin.conf "$@"
}

probe() {
    owned_node
    mkdir -p "$stack_dir/evidence"
    logs=$(mktemp -d "$stack_dir/evidence/upstream-one-node.XXXXXXXX")
    printf 'ONE_NODE_LOGS=%s\n' "$logs"
    {
        printf 'source_revision=%s\n' "$(git rev-parse HEAD)"
        printf 'source_dirty=%s\n' "$(test -z "$(git status --porcelain)" && echo false || echo true)"
        sha256sum "$kind" "$archive" tools/kubernetes/one-node-baseline.sh
        docker image inspect "$image" --format 'node_image={{.Id}}'
        docker exec "$node" kubelet --version
        docker exec "$node" containerd --version
    } | tee "$logs/inputs.log"
    kubectl wait --for=condition=Ready node --all --timeout=120s
    kubectl get nodes -o json > "$logs/nodes.json"
    python3 - "$logs/nodes.json" "$kube_version" <<'PY'
import json, sys
nodes = json.load(open(sys.argv[1]))["items"]
assert len(nodes) == 1, len(nodes)
assert nodes[0]["status"]["nodeInfo"]["kubeletVersion"] == sys.argv[2]
assert any(c["type"] == "Ready" and c["status"] == "True" for c in nodes[0]["status"]["conditions"])
PY
    kubectl api-resources -o wide > "$logs/api-resources.log"
    kubectl get --raw /openapi/v3/api/v1 > "$logs/core-openapi.json"
    python3 - "$logs" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
resources = (p / "api-resources.log").read_text()
schemas = json.loads((p / "core-openapi.json").read_text())["components"]["schemas"]
report = {
    "podcheckpoint_resource_advertised": "podcheckpoints" in resources,
    "pod_spec_restoreFrom_advertised": "restoreFrom" in schemas["io.k8s.api.core.v1.PodSpec"]["properties"],
    "feature_gate_configuration": "released defaults; PodLevelCheckpointRestore was rejected in the separate binary audit",
}
(p / "pod-workflow-discovery.json").write_text(json.dumps(report, indent=2) + "\n")
print(json.dumps(report))
PY
    # Use the stock runtime to establish that this released cluster works.
    # Firecracker RuntimeClass acceptance is a separate, mandatory Step 3 gate.
    smoke_ns="fcmod-one-node-smoke-$$"
    smoke_created=0
    cleanup_smoke() {
        if test "$smoke_created" = 1; then
            kubectl delete namespace "$smoke_ns" --wait=true --timeout=60s
            smoke_created=0
        fi
    }
    trap cleanup_smoke EXIT
    kubectl create namespace "$smoke_ns"
    smoke_created=1
    kubectl --namespace "$smoke_ns" run smoke --image="$smoke_image" --restart=Never \
        --command -- /bin/sh -ec 'echo UPSTREAM_POD_STARTED; sleep 600'
    kubectl --namespace "$smoke_ns" wait --for=condition=Ready pod/smoke --timeout=120s
    kubectl --namespace "$smoke_ns" logs smoke | tee "$logs/pod.log"
    grep -qx UPSTREAM_POD_STARTED "$logs/pod.log"
    kubectl --namespace "$smoke_ns" exec smoke -- /bin/sh -ec \
        'echo disk-write-ok > /tmp/probe; test "$(cat /tmp/probe)" = disk-write-ok; nslookup kubernetes.default.svc.cluster.local' \
        | tee "$logs/exec-dns.log"
    cleanup_smoke
    trap - EXIT
    echo UPSTREAM_ONE_NODE_BASELINE_OK
}

case ${1:-probe} in
    create)
        download "https://github.com/kubernetes-sigs/kind/releases/download/$kind_version/kind-linux-amd64" "$kind" "$kind_sha"
        chmod +x "$kind"
        download "https://dl.k8s.io/release/$kube_version/kubernetes-server-linux-amd64.tar.gz" "$archive" "$archive_sha"
        if "$kind" get clusters | grep -qx "$cluster"; then
            echo "Cluster already exists; use probe or delete: $cluster" >&2
            exit 1
        fi
        test ! -e "$state_dir/node-id"
        test ! -e "$state_dir/kubeconfig"
        mkdir -p "$state_dir" "$stack_dir/evidence"
        chmod 700 "$state_dir"
        "$kind" build node-image --type file "$archive" --base-image "$base_image" --image "$image" \
            > "$state_dir/node-build.log" 2>&1
        cat > "$state_dir/config.yaml" <<YAML
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: $cluster
networking:
  apiServerAddress: 127.0.0.1
nodes:
- role: control-plane
  image: $image
YAML
        "$kind" create cluster --config "$state_dir/config.yaml" \
            --kubeconfig "$state_dir/kubeconfig" --wait 180s
        chmod 600 "$state_dir/kubeconfig"
        docker inspect --format '{{.Id}}' "$node" > "$state_dir/node-id"
        probe
        ;;
    probe) probe ;;
    delete)
        owned_node
        "$kind" delete cluster --name "$cluster" --kubeconfig "$state_dir/kubeconfig"
        rm -f "$state_dir/node-id" "$state_dir/kubeconfig"
        ;;
    *) echo "Usage: $0 {create|probe|delete}" >&2; exit 1 ;;
esac
