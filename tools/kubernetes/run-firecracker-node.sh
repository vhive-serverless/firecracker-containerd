#!/usr/bin/env bash
# This runs only inside the private test container created by test-firecracker-node.sh.
set -euo pipefail
logs=/var/log/firecracker-containerd-test
socket=/run/firecracker-containerd/containerd.sock
cri_socket=/run/firecracker-containerd/cri.sock
mkdir -p "$logs" /etc/cni/net.d /etc/cni/runc.d
cat /proc/self/cgroup > "$logs/node-cgroup.log"
test "$(stat -fc %T /sys/fs/cgroup)" = cgroup2fs
# The outer driver supplies a private cgroup namespace rooted at its own Docker
# cgroup. Move the node processes into a leaf before enabling Pod controllers.
mkdir /sys/fs/cgroup/init.scope
mapfile -t node_pids < /sys/fs/cgroup/cgroup.procs
for pid in "${node_pids[@]}"; do
    if test -e "/proc/$pid"; then echo "$pid" > /sys/fs/cgroup/init.scope/cgroup.procs; fi
done
for controller in cpu cpuset io memory pids; do
    if grep -qw "$controller" /sys/fs/cgroup/cgroup.controllers; then
        echo "+$controller" > /sys/fs/cgroup/cgroup.subtree_control
    fi
done
grep -qw cpu /sys/fs/cgroup/cgroup.subtree_control
grep -qw memory /sys/fs/cgroup/cgroup.subtree_control

rm -f /etc/cni/net.d/10-fcnet.conflist
for runtime in firecracker runc; do
    directory=/etc/cni/net.d
    tap=', {"type":"tc-redirect-tap"}'
    if test "$runtime" = runc; then directory=/etc/cni/runc.d; tap=; fi
    # Both chains share their bridge and IPAM name. Only Firecracker redirects
    # the Pod interface into a TAP; ordinary system Pods retain their veth.
    cat > "$directory/10-fc-kube.conflist" <<JSON
{"cniVersion":"1.0.0","name":"fc-kube","plugins":[
 {"type":"bridge","bridge":"fc-kube0","isGateway":true,"ipMasq":true,"hairpinMode":true,
  "ipam":{"type":"host-local","subnet":"198.20.0.0/24","routes":[{"dst":"0.0.0.0/0"}]}},
 {"type":"portmap","capabilities":{"portMappings":true}}, {"type":"firewall"}$tap]}
JSON
done
cat > /tmp/kube-runtime.toml <<CONFIG
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
    default_runtime_name = "runc"
    [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.runc]
      runtime_type = "io.containerd.runc.v2"
      snapshotter = "devmapper"
      sandboxer = "podsandbox"
      cni_conf_dir = "/etc/cni/runc.d"
      [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.runc.options]
        SystemdCgroup = false
    [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.firecracker]
      runtime_type = "aws.firecracker"
      snapshotter = "devmapper"
      sandboxer = "podsandbox"
      cni_conf_dir = "/etc/cni/net.d"
  [plugins."io.containerd.cri.v1.runtime".cni]
    bin_dirs = ["/opt/cni/bin"]
    conf_dir = "/etc/cni/runc.d"
CONFIG
containerd --config /tmp/kube-runtime.toml --log-level debug > "$logs/containerd.out" 2>&1 &
daemon=$!
cleanup() {
    if test "$?" != 0 && test -f /etc/rancher/k3s/k3s.yaml; then
        timeout 15s k3s kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml get pods --all-namespaces -o json > "$logs/pods-at-failure.json" 2>&1 || true
        timeout 15s k3s kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml get events --all-namespaces > "$logs/events-at-failure.log" 2>&1 || true
        timeout 15s k3s kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml --namespace kube-system logs deployment/coredns --tail=40 > "$logs/dns-at-failure.log" 2>&1 || true
    fi
    for pid in "${k3s_pid:-}" "${gateway_pid:-}" "$daemon"; do
        if test -n "$pid"; then kill "$pid" 2>/dev/null || true; fi
    done
}
trap cleanup EXIT
for attempt in $(seq 1 60); do
    if ctr --address "$socket" version > "$logs/runtime-version.log" 2>&1; then break; fi
    kill -0 "$daemon"
    sleep 1
done
test "$(awk -v rev="$KUBE_RUNTIME_REVISION" '$1 == "Revision:" && $2 == rev {n++} END {print n+0}' "$logs/runtime-version.log")" = 2
firecracker-cri --version | tee "$logs/cri-version.log"
grep -qx "firecracker-cri $KUBE_RUNTIME_REVISION" "$logs/cri-version.log"
firecracker-cri --backend "$socket" --address "$cri_socket" > "$logs/cri-gateway.out" 2>&1 &
gateway_pid=$!
for attempt in $(seq 1 30); do
    if test -S "$cri_socket"; then break; fi
    kill -0 "$gateway_pid"
    sleep 1
done
test -S "$cri_socket"
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
k3s server --container-runtime-endpoint="unix://$cri_socket" \
    --flannel-backend=none --disable-network-policy \
    --disable=traefik,servicelb,local-storage,metrics-server,gateway-api-crd \
    --node-name=fcmod-firecracker --cluster-cidr=198.20.0.0/16 \
    --kube-proxy-arg=masquerade-all=true \
    --kubelet-arg=cgroup-driver=cgroupfs \
    --kubelet-arg=fail-swap-on=false > "$logs/k3s.out" 2>&1 &
k3s_pid=$!
for attempt in $(seq 1 180); do
    if k3s kubectl get node fcmod-firecracker > "$logs/node-status.log" 2>&1; then break; fi
    kill -0 "$k3s_pid"
    sleep 1
done
k3s kubectl wait --for=condition=Ready node/fcmod-firecracker --timeout=120s
k3s kubectl get nodes -o json > "$logs/nodes.json"
for attempt in $(seq 1 60); do
    if k3s kubectl --namespace kube-system get deployment/coredns >/dev/null 2>&1; then break; fi
    kill -0 "$k3s_pid"
    sleep 1
done
k3s kubectl --namespace kube-system set image deployment/coredns \
    coredns=docker.io/rancher/mirrored-coredns-coredns@sha256:7efd3c635b03efd68c4e8398fc45f0d993d0e9ab016f72c1cefb0fd6d01aa286
k3s kubectl --namespace kube-system rollout status deployment/coredns --timeout=180s
k3s kubectl get pods --all-namespaces -o json > "$logs/system-pods.json"
k3s kubectl apply -f /firecracker-smoke.yaml
k3s kubectl --namespace fcmod-smoke wait --for=condition=Ready pod/firecracker --timeout=180s
k3s kubectl --namespace fcmod-smoke logs firecracker -c main | tee "$logs/pod.log"
grep -qx FIRECRACKER_KUBERNETES_STARTED "$logs/pod.log"
k3s kubectl --namespace fcmod-smoke exec firecracker -c main -- /bin/sh -ec \
    'test "$(cat /shared/hello)" = FIRECRACKER_SHARED_VOLUME; test "$(cat /var/run/secrets/kubernetes.io/serviceaccount/namespace)" = fcmod-smoke; test -s /var/run/secrets/kubernetes.io/serviceaccount/token; echo EXEC_OK; nslookup -type=A kubernetes.default.svc.cluster.local.' \
    | tee "$logs/exec-dns.log"
grep -Eq '^Address([ 0-9]*)?:[[:space:]]*10\.43\.0\.1([[:space:]]|$)' "$logs/exec-dns.log"
k3s kubectl --namespace fcmod-smoke exec firecracker -c main -- cat /proc/mounts > "$logs/guest-mounts.log"
grep -q fuse.fcvolume "$logs/guest-mounts.log"
test "$(pgrep -cx firecracker)" = 1
pgrep -ax firecracker > "$logs/vmm-process.log"
k3s crictl --runtime-endpoint="unix://$cri_socket" pods -o json > "$logs/sandboxes.json"
k3s crictl --runtime-endpoint="unix://$cri_socket" ps -o json > "$logs/containers.json"
k3s crictl --runtime-endpoint="unix://$cri_socket" stats -o json > "$logs/stats.json"
k3s kubectl --namespace fcmod-smoke run client \
    --image=registry.k8s.io/e2e-test-images/busybox:1.29-2@sha256:c318242786b139d18676b1c09a0ad7f15fc17f8f16a5b2e625cd0dc8c9703daf \
    --restart=Never --command -- /bin/sh -ec \
    'wget -qO- http://firecracker:8080/hello; sleep 300'
k3s kubectl --namespace fcmod-smoke wait --for=condition=Ready pod/client --timeout=120s
k3s kubectl --namespace fcmod-smoke logs client | tee "$logs/network-client.log"
grep -qx FIRECRACKER_SHARED_VOLUME "$logs/network-client.log"
k3s kubectl --namespace fcmod-smoke get pods -o json > "$logs/workload-pods.json"
k3s kubectl delete namespace fcmod-smoke --wait=true --timeout=120s
k3s kubectl delete runtimeclass firecracker
for attempt in $(seq 1 60); do
    if ! pgrep -x firecracker >/dev/null; then break; fi
    sleep 1
done
if pgrep -x firecracker; then echo 'Firecracker VM leaked after Pod deletion' >&2; exit 1; fi
k3s crictl --runtime-endpoint="unix://$cri_socket" pods -o json > "$logs/sandboxes-after.json"
k3s crictl --runtime-endpoint="unix://$cri_socket" ps -o json > "$logs/containers-after.json"
echo K3S_FIRECRACKER_RUNTIMECLASS_OK
