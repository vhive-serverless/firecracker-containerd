#!/usr/bin/env bash
# Start and exercise the Step 1 stack using its pinned build inputs.
# Run against a dedicated service, thin pool, state directory and containerd socket.
set -euo pipefail
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
src=${RUNTIME_SRC:-"$stack_dir/runtime"}
work=${WORK_DIR:-"$stack_dir/modernize-run"}
unit=${RUN_NAME:-fcmod-modernize}
[[ "$unit" =~ ^fcmod-[a-z0-9-]+$ ]] || { echo 'RUN_NAME must start with fcmod- and contain lowercase letters, digits or hyphens.' >&2; exit 2; }
pool=$unit
socket=/run/$unit/containerd.sock
install_dir=${INSTALL_DIR:-}
if [ -n "$install_dir" ]; then
    daemon="$install_dir/bin/firecracker-containerd"
    ctr="$install_dir/bin/firecracker-ctr"
    vmm="$install_dir/bin/firecracker"
    kernel="$install_dir/share/runtime/default-vmlinux.bin"
    rootfs="$install_dir/share/runtime/default-rootfs.img"
    runc="$install_dir/bin/runc"
    jail_config="$install_dir/etc/firecracker-runc-config.json"
    runtime_path="$install_dir/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
else
    daemon="$src/firecracker-control/cmd/containerd/firecracker-containerd"
    ctr="$src/firecracker-control/cmd/containerd/firecracker-ctr"
    vmm="$src/bin/firecracker"
    kernel="$src/default-vmlinux.bin"
    rootfs="$src/tools/image-builder/rootfs.img"
    runc="$src/_submodules/runc/runc"
    jail_config="$src/runtime/firecracker-runc-config.json.example"
    runtime_path="$src/runtime:$src/_submodules/runc:$src/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
fi
mkdir -p "$work" "$stack_dir/evidence"
if sudo -n systemctl is-active --quiet "$unit"; then
    echo "Modernized service already running; inspect it before rerunning." >&2
    exit 1
fi
git -C "$src" rev-parse HEAD
if [ "${ALLOW_DIRTY:-0}" != 1 ]; then
    test -z "$(git -C "$src" status --porcelain)"
fi
for binary in bin/firecracker agent/agent runtime/containerd-shim-aws-firecracker \
    firecracker-control/cmd/containerd/firecracker-containerd \
    firecracker-control/cmd/containerd/firecracker-ctr _submodules/runc/runc; do
    test -x "$src/$binary"
done
test -f "$src/tools/image-builder/rootfs.img"
test -f "$src/default-vmlinux.bin"
findmnt -no SOURCE,FSTYPE,TARGET -T "$work"
if ! sudo -n dmsetup info "$pool" >/dev/null 2>&1; then
    FICD_DM_VOLUME_GROUP='' "$src/tools/thinpool.sh" create "$pool"
fi
cat > "$work/config.toml" <<EOF
version = 4
root = "$work/root"
state = "/run/$unit"
disabled_plugins = ["io.containerd.grpc.v1.cri", "io.containerd.cri.v1.images", "io.containerd.cri.v1.runtime"]
[plugins."io.containerd.server.v1.grpc"]
  address = "$socket"
[plugins."io.containerd.server.v1.ttrpc"]
  address = "$socket.ttrpc"
[plugins."io.containerd.snapshotter.v1.devmapper"]
  pool_name = "$pool"
  base_image_size = "1GB"
  root_path = "$work/devmapper"
[[plugins."io.containerd.transfer.v1.local".unpack_config]]
  platform = "linux/amd64"
  snapshotter = "devmapper"
[debug]
  level = "debug"
EOF
python3 - "$work" "$vmm" "$kernel" "$rootfs" "$runc" "$jail_config" <<'PY'
import json, os, sys
work, vmm, kernel, rootfs, runc, jail_config = sys.argv[1:]
os.makedirs(work + '/shim', exist_ok=True)
with open(work + '/runtime.json', 'w') as f:
    json.dump({
        'cpu_template': '', 'log_levels': ['debug'],
        'firecracker_binary_path': vmm,
        'shim_base_dir': work + '/shim',
        'kernel_image_path': kernel,
        'kernel_args': 'ro console=ttyS0 reboot=k panic=1 pci=off nomodules systemd.unified_cgroup_hierarchy=1 systemd.journald.forward_to_console systemd.log_color=false systemd.unit=firecracker.target init=/sbin/overlay-init',
        'root_drive': rootfs,
        'jailer': {'runc_binary_path': runc, 'runc_config_path': jail_config},
    }, f, indent=2)
PY
# Verify effective v4 server configuration before opening any sockets.
"$daemon" --config "$work/config.toml" config dump | python3 -c 'import sys,tomllib; c=tomllib.loads(sys.stdin.read()); p=c["plugins"]; assert p["io.containerd.server.v1.grpc"]["address"] == sys.argv[1]; assert p["io.containerd.server.v1.ttrpc"]["address"] == sys.argv[1]+".ttrpc"' "$socket"
sudo -n systemd-run --unit="$unit" --service-type=simple --property=Delegate=yes \
    --property=KillMode=process \
    --property="StandardOutput=append:$stack_dir/evidence/$unit-runtime.log" \
    --property="StandardError=append:$stack_dir/evidence/$unit-runtime.log" \
    --setenv="PATH=$runtime_path" \
    --setenv="FIRECRACKER_CONTAINERD_RUNTIME_CONFIG_PATH=$work/runtime.json" \
    "$daemon" --config "$work/config.toml"
for attempt in $(seq 1 30); do
    if sudo -n "$ctr" --address "$socket" version; then break; fi
    sleep 1
done
sudo -n "$ctr" --address "$socket" plugins ls
sudo -n "$ctr" --address "$socket" images pull --snapshotter devmapper docker.io/library/alpine:3.23.0
sudo -n env PATH="$runtime_path" "$ctr" --address "$socket" run \
    --runtime aws.firecracker --snapshotter devmapper --rm \
    docker.io/library/alpine:3.23.0 fcmod-modernize-smoke \
    /bin/sh -ec 'uname -a; cat /proc/mounts; echo modernize-write > /modernize-write; test "$(cat /modernize-write)" = modernize-write; echo MODERNIZED_CONTAINER_OK'
