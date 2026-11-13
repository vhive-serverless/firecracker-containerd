#!/usr/bin/env bash
# Exercise a released one-node k3s cluster in its own Docker namespaces and pool.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
cd "$repo_dir"
revision=$(git rev-parse HEAD)
dirty=false
if test -n "$(git status --porcelain)"; then dirty=true; fi
if test "$dirty" = true && test -z "${RUNTIMECLASS_BASE_IMAGE:-}"; then
    echo 'Commit the source or select an explicit development test image.' >&2
    exit 1
fi
image=${RUNTIMECLASS_BASE_IMAGE:-"localhost/firecracker-containerd-test:${revision:0:12}"}
runtime_revision=${RUNTIMECLASS_RUNTIME_REVISION:-$revision}
profile=${KUBE_MEMORY_PROFILE:-disk}
case "$profile" in disk|pmem|reclaim|combined) ;; *) echo 'Unknown KUBE_MEMORY_PROFILE' >&2; exit 2 ;; esac
k3s="$stack_dir/toolchains/k3s-v1.37.2/k3s"
k3s_sha=265b3ee6a3198ef14033c63ef46cbb32370d83795199fb6d1b01f21575eddbda
if ! test -f "$k3s" || ! echo "$k3s_sha  $k3s" | sha256sum --check --status; then
    mkdir -p "$(dirname "$k3s")"
    curl --fail --location --retry 3 \
        'https://github.com/k3s-io/k3s/releases/download/v1.37.2%2Bk3s1/k3s' -o "$k3s.partial"
    echo "$k3s_sha  $k3s.partial" | sha256sum --check
    mv "$k3s.partial" "$k3s"
    chmod +x "$k3s"
fi
echo "$k3s_sha  $k3s" | sha256sum --check
mkdir -p "$stack_dir/evidence"
logs=$(mktemp -d "$stack_dir/evidence/k3s-firecracker-${revision:0:12}.XXXXXXXX")
suffix=${logs##*.}
name="fcmod-k3s-$suffix"
storage=''
export FICD_DM_VOLUME_GROUP=
export FICD_DM_POOL="fc-kube-$suffix"
container_owned=0
pool_owned=0
cleanup() {
    if test "$container_owned" = 1; then
        docker logs "$name" > "$logs/node-console.log" 2>&1 || true
        docker rm --force "$name" >/dev/null
        container_owned=0
    fi
    if test "$pool_owned" = 1; then
        tools/thinpool.sh remove "$FICD_DM_POOL"
        pool_owned=0
    fi
    # This unique directory was created by this invocation, outside source.
    if test -n "$storage"; then sudo -n rm -rf -- "$storage"; fi
}
trap cleanup EXIT
mkdir -p "$stack_dir/.ficd-test-vms"
storage=$(mktemp -d "$stack_dir/.ficd-test-vms/k3s-$suffix.XXXXXXXX")
root_mount=()
root_digest=''
if test "$profile" = pmem || test "$profile" = combined; then
    root_image="$repo_dir/tools/image-builder/rootfs-dax.img"
    test -f "$root_image"
    root_digest=$(sha256sum "$root_image" | awk '{print $1}')
    root_mount=(--volume "$root_image:/fc-root-dax.img:ro")
fi
python3 - "$repo_dir" "$logs/runtime-config.json" "$profile" "$root_digest" <<'PY'
import json
from pathlib import Path
import sys
repo, output, profile, digest = sys.argv[1:]
config = json.loads((Path(repo) / "tools/docker/do_not_edit_for_firecracker-control.config.json").read_text())
config.update(guest_root_mode="disk", guest_root_size_mib=512,
              guest_reclamation={"Enabled": profile in ("reclaim", "combined")})
if digest:
    config.update(root_drive="/fc-root-dax.img", root_drive_use_pmem=True,
                  root_drive_sha256=digest)
Path(output).write_text(json.dumps(config, indent=2) + "\n")
PY
printf 'K3S_FIRECRACKER_LOGS=%s\n' "$logs"
{
    printf 'script_revision=%s\nruntime_revision=%s\nsource_dirty=%s\nmemory_profile=%s\nvm_storage=%s\n' "$revision" "$runtime_revision" "$dirty" "$profile" "$storage"
    docker image inspect "$image" --format 'image={{.Id}}'
    "$k3s" --version
    sha256sum "$k3s" tools/kubernetes/{test-firecracker-node.sh,run-firecracker-node.sh,firecracker-smoke.yaml}
    sha256sum "$logs/runtime-config.json"
    findmnt -T "$storage" -o TARGET,SOURCE,FSTYPE
} > "$logs/inputs.log"
tools/thinpool.sh create "$FICD_DM_POOL"
pool_owned=1
docker create --init --name "$name" --privileged --cgroupns=private --network bridge --ipc=host \
    --volume /dev:/dev --volume /run/udev/control:/run/udev/control \
    --volume "$logs:/var/log/firecracker-containerd-test" \
    --volume "$storage:/srv/firecracker_containerd_tests" \
    --volume "$logs/runtime-config.json:/etc/containerd/firecracker-runtime.json:ro" \
    "${root_mount[@]}" \
    --volume "$k3s:/usr/local/bin/k3s:ro" \
    --volume "$repo_dir/tools/kubernetes/run-firecracker-node.sh:/run-firecracker-node:ro" \
    --volume "$repo_dir/tools/kubernetes/firecracker-smoke.yaml:/firecracker-smoke.yaml:ro" \
    --env FICD_DM_POOL --env "KUBE_RUNTIME_REVISION=$runtime_revision" --env "KUBE_MEMORY_PROFILE=$profile" \
    --entrypoint /bin/bash "$image" /run-firecracker-node > "$logs/container-id"
container_owned=1
set +e
docker start --attach "$name" 2>&1 | tee "$logs/run.log"
attach_code=${PIPESTATUS[0]}
set -e
test_code=$(docker inspect --format '{{.State.ExitCode}}' "$name")
printf 'attach_exit=%s\ncontainer_exit=%s\n' "$attach_code" "$test_code" > "$logs/exit-status"
cleanup
test "$attach_code" = 0
test "$test_code" = 0
grep -qx K3S_FIRECRACKER_RUNTIMECLASS_OK "$logs/run.log"
python3 - "$logs" <<'PY'
import json
from pathlib import Path
import sys

logs = Path(sys.argv[1])
def read(name):
    return json.loads((logs / name).read_text())

nodes = read("nodes.json")["items"]
assert len(nodes) == 1
assert nodes[0]["status"]["nodeInfo"]["kubeletVersion"] == "v1.37.2+k3s1"
assert any(c["type"] == "Ready" and c["status"] == "True" for c in nodes[0]["status"]["conditions"])
pods = read("workload-pods.json")["items"]
pod = next(p for p in pods if p["metadata"]["name"] == "firecracker")
assert pod["spec"]["runtimeClassName"] == "firecracker"
assert len(pod["status"]["containerStatuses"]) == 2
assert all(c["ready"] and c["restartCount"] == 0 for c in pod["status"]["containerStatuses"])
sandboxes = read("sandboxes.json")["items"]
sandbox = next(s for s in sandboxes if s["metadata"]["name"] == "firecracker")
assert sandbox["runtimeHandler"] == "firecracker"
assert sandbox["metadata"]["uid"] == pod["metadata"]["uid"]
assert "--id " + sandbox["id"] in (logs / "vmm-process.log").read_text()
assert len((logs / "vmm-process.log").read_text().splitlines()) == 1
containers = [c for c in read("containers.json")["containers"] if c["podSandboxId"] == sandbox["id"]]
assert {c["metadata"]["name"] for c in containers} == {"main", "sidecar"}
stats = {s["attributes"]["id"]: s for s in read("stats.json")["stats"]}
for container in containers:
    stat = stats[container["id"]]
    assert int(stat["cpu"]["usageCoreNanoSeconds"]["value"]) > 0
    assert int(stat["memory"]["workingSetBytes"]["value"]) > 0
    assert int(stat["memory"]["rssBytes"]["value"]) > 0
assert all(s["runtimeHandler"] != "firecracker" for s in read("sandboxes-after.json")["items"])
assert not any(c["labels"].get("io.kubernetes.pod.namespace") == "fcmod-smoke" for c in read("containers-after.json")["containers"])
dns = [p for p in read("system-pods.json")["items"] if p["metadata"]["labels"].get("k8s-app") == "kube-dns" and not p["metadata"].get("deletionTimestamp")]
assert len(dns) == 1
assert dns[0]["status"]["containerStatuses"][0]["ready"]
assert dns[0]["spec"]["containers"][0]["image"] == "docker.io/rancher/mirrored-coredns-coredns@sha256:7efd3c635b03efd68c4e8398fc45f0d993d0e9ab016f72c1cefb0fd6d01aa286"
config = read("runtime-config.json")
assert config["guest_root_mode"] == "disk"
size, blocks, inode = map(int, (logs / "guest-upper-stat.log").read_text().split())
assert size == config["guest_root_size_mib"] * 1024 * 1024
assert blocks * 512 >= size and inode > 0
assert (logs / "guest-upper-host-magic.log").read_text().strip() in ("ef53", "58465342", "9123683e")
assert not (logs / "guest-upper-after.log").read_text().strip()
enabled = config["guest_reclamation"]["Enabled"]
assert (logs / "guest-reclamation.log").read_text().strip() == ("Y" if enabled else "N")
if config.get("root_drive_use_pmem"):
    mapped = [line.split() for line in (logs / "vmm-maps.log").read_text().splitlines()
              if config["root_drive_sha256"] + ".img" in line]
    assert mapped and all("s" in fields[1] and "w" not in fields[1] for fields in mapped)
print("RuntimeClass Pod UID, one-VM mapping, both live container stats and cleanup verified.")
PY
echo K3S_FIRECRACKER_WORKER_TEST_OK
