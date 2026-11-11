#!/usr/bin/env bash
set -euo pipefail
logs=/var/log/firecracker-containerd-test
socket=/run/firecracker-containerd/containerd.sock
cri_socket=/run/firecracker-containerd/cri.sock
test -x /opt/cni/bin/portmap
mkdir -p /etc/cni/net.d "$logs"
rm -f /etc/cni/net.d/10-fcnet.conflist
cat > /etc/cni/net.d/10-cri.conflist <<'JSON'
{
  "cniVersion": "1.0.0",
  "name": "fc-cri",
  "plugins": [
    {"type": "bridge", "bridge": "fc-cri0", "isGateway": true,
     "ipMasq": true, "hairpinMode": true,
     "ipam": {"type": "host-local", "subnet": "198.20.0.0/24"}},
    {"type": "portmap", "capabilities": {"portMappings": true}},
    {"type": "firewall"},
    {"type": "tc-redirect-tap"}
  ]
}
JSON
cat > /tmp/cri.toml <<CONFIG
version = 4
[plugins."io.containerd.server.v1.grpc"]
  address = "$socket"
[plugins."io.containerd.server.v1.ttrpc"]
  address = "$socket.ttrpc"
[plugins."io.containerd.snapshotter.v1.devmapper"]
  pool_name = "$FICD_DM_POOL"
  base_image_size = "1024MB"
[[plugins."io.containerd.transfer.v1.local".unpack_config]]
  platform = "linux/amd64"
  snapshotter = "devmapper"
[plugins."io.containerd.cri.v1.images"]
  snapshotter = "devmapper"
  [plugins."io.containerd.cri.v1.images".pinned_images]
    sandbox = "registry.k8s.io/pause:3.10.2@sha256:f548e0e8e3dc1896ca956272154dde3314e8cc4fde0a57577ee9fa1c63f5baf4"
[plugins."io.containerd.cri.v1.runtime"]
  disable_apparmor = true
  [plugins."io.containerd.cri.v1.runtime".containerd]
    default_runtime_name = "firecracker"
    [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.firecracker]
      runtime_type = "aws.firecracker"
      snapshotter = "devmapper"
      sandboxer = "podsandbox"
  [plugins."io.containerd.cri.v1.runtime".cni]
    bin_dirs = ["/opt/cni/bin"]
    conf_dir = "/etc/cni/net.d"
CONFIG
containerd --config /tmp/cri.toml --log-level debug > "$logs/containerd.out" 2>&1 &
daemon=$!
cleanup() {
    ctr --address "$socket" --namespace k8s.io images list > "$logs/images.log" 2>&1 || true
    ctr --address "$socket" --namespace k8s.io tasks list > "$logs/tasks-at-exit.log" 2>&1 || true
    if test -n "${gateway_pid:-}"; then
        kill "$gateway_pid" 2>/dev/null || true
        wait "$gateway_pid" || true
    fi
    kill "$daemon" 2>/dev/null || true
    wait "$daemon" || true
}
trap cleanup EXIT
for attempt in $(seq 1 30); do
    if ctr --address "$socket" version > "$logs/runtime-version.log" 2>&1; then break; fi
    if ! kill -0 "$daemon" 2>/dev/null; then tail -40 "$logs/containerd.out"; exit 1; fi
    sleep 1
done
cat "$logs/runtime-version.log"
test "$(awk -v rev="$CRI_RUNTIME_REVISION" '$1 == "Revision:" && $2 == rev {n++} END {print n+0}' "$logs/runtime-version.log")" = 2
firecracker-cri --version | tee "$logs/cri-version.log"
grep -qx "firecracker-cri $CRI_RUNTIME_REVISION" "$logs/cri-version.log"
firecracker-cri --backend "$socket" --address "$cri_socket" > "$logs/cri-gateway.out" 2>&1 &
gateway_pid=$!
for attempt in $(seq 1 30); do
    if test -S "$cri_socket"; then break; fi
    if ! kill -0 "$gateway_pid" 2>/dev/null; then cat "$logs/cri-gateway.out"; exit 1; fi
    sleep 1
done
test -S "$cri_socket"
ctr --address "$socket" plugins list > "$logs/plugins.log"
cat > /tmp/test-images.yaml <<'YAML'
defaultTestContainerImage: registry.k8s.io/e2e-test-images/busybox:1.29-2@sha256:c318242786b139d18676b1c09a0ad7f15fc17f8f16a5b2e625cd0dc8c9703daf
YAML
args=(--runtime-endpoint "unix://$cri_socket" --image-endpoint "unix://$cri_socket"
    --runtime-handler firecracker --test-images-file /tmp/test-images.yaml
    --ginkgo.no-color --ginkgo.v --ginkgo.fail-on-empty
    --ginkgo.json-report "$logs/report.json")
if test -n "$CRI_FOCUS"; then args+=(--ginkgo.focus "$CRI_FOCUS"); fi
if test "${CRI_WEBSOCKETS:-0}" = 1; then
    args+=(--websocket-exec --websocket-attach --websocket-portforward)
fi
critest "${args[@]}"
for attempt in $(seq 1 30); do
    tasks=$(ctr --address "$socket" --namespace k8s.io tasks list --quiet)
    if test -z "$tasks" && ! pgrep -x firecracker >/dev/null; then break; fi
    sleep 1
done
test -z "$(ctr --address "$socket" --namespace k8s.io tasks list --quiet)"
if pgrep -x firecracker; then echo 'VMM process leaked after CRI tests' >&2; exit 1; fi
test -z "$(ip netns list)"
test -z "$(find /srv/firecracker_containerd_tests -name recovery.json -print)"
echo CRI_RUNTIME_CLEANUP_OK
echo CRI_TESTS_PASSED
