#!/usr/bin/env bash
# Build inputs must already have been built in the original committed checkout.
# Run against a dedicated service, thin pool, state directory and containerd socket.
set -euo pipefail
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
src="$stack_dir/baseline"
work="$stack_dir/baseline-run"
unit=fcmod-baseline
pool=fcmod-baseline
socket=/run/fcmod-baseline/containerd.sock
mkdir -p "$work" "$stack_dir/evidence"
if sudo -n systemctl is-active --quiet "$unit"; then
    echo "Baseline service already running; inspect it before rerunning." >&2
    exit 1
fi
test "$(git -C "$src" rev-parse HEAD)" = be68640a5d2237f5b427c37c1f5809ec154126c5
git -C "$src" diff --exit-code
for binary in bin/firecracker agent/agent runtime/containerd-shim-aws-firecracker \
    firecracker-control/cmd/containerd/firecracker-containerd \
    firecracker-control/cmd/containerd/firecracker-ctr _submodules/runc/runc; do
    test -x "$src/$binary"
done
test -f "$src/tools/image-builder/rootfs.img"
test -f "$src/default-vmlinux.bin"
findmnt -no SOURCE,FSTYPE,TARGET -T "$work"
FICD_DM_VOLUME_GROUP='' "$src/tools/thinpool.sh" create "$pool"
cat > "$work/config.toml" <<EOF
version = 2
root = "$work/root"
state = "/run/fcmod-baseline"
disabled_plugins = ["io.containerd.grpc.v1.cri"]
[grpc]
  address = "$socket"
[plugins."io.containerd.snapshotter.v1.devmapper"]
  pool_name = "$pool"
  base_image_size = "1GB"
  root_path = "$work/devmapper"
[debug]
  level = "debug"
EOF
python3 - "$src" "$work" <<'PY'
import json, os, sys
src, work = sys.argv[1:]
os.makedirs(work + '/shim', exist_ok=True)
with open(work + '/runtime.json', 'w') as f:
    json.dump({
        'cpu_template': '', 'log_levels': ['debug'],
        'firecracker_binary_path': src + '/bin/firecracker',
        'shim_base_dir': work + '/shim',
        'kernel_image_path': src + '/default-vmlinux.bin',
        'kernel_args': 'ro console=ttyS0 noapic reboot=k panic=1 pci=off nomodules systemd.unified_cgroup_hierarchy=0 systemd.journald.forward_to_console systemd.log_color=false systemd.unit=firecracker.target init=/sbin/overlay-init',
        'root_drive': src + '/tools/image-builder/rootfs.img',
        'jailer': {'runc_binary_path': src + '/_submodules/runc/runc',
                   'runc_config_path': src + '/runtime/firecracker-runc-config.json.example'},
    }, f, indent=2)
PY
runtime_path="$src/runtime:$src/_submodules/runc:$src/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
sudo -n systemd-run --unit="$unit" --service-type=simple --property=Delegate=yes \
    --property="StandardOutput=append:$stack_dir/evidence/baseline-runtime.log" \
    --property="StandardError=append:$stack_dir/evidence/baseline-runtime.log" \
    --setenv="PATH=$runtime_path" \
    --setenv="FIRECRACKER_CONTAINERD_RUNTIME_CONFIG_PATH=$work/runtime.json" \
    "$src/firecracker-control/cmd/containerd/firecracker-containerd" --config "$work/config.toml"
ctr="$src/firecracker-control/cmd/containerd/firecracker-ctr"
for attempt in $(seq 1 30); do
    if sudo -n "$ctr" --address "$socket" version; then break; fi
    sleep 1
done
sudo -n "$ctr" --address "$socket" plugins ls
sudo -n "$ctr" --address "$socket" images pull --snapshotter devmapper docker.io/library/alpine:3.23.0
sudo -n env PATH="$runtime_path" "$ctr" --address "$socket" run \
    --runtime aws.firecracker --snapshotter devmapper --rm \
    docker.io/library/alpine:3.23.0 fcmod-baseline-smoke \
    /bin/sh -ec 'uname -a; cat /proc/mounts; echo baseline-write > /baseline-write; test "$(cat /baseline-write)" = baseline-write; echo BASELINE_CONTAINER_OK'
