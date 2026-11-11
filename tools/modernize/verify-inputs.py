#!/usr/bin/env python3
"""Check the immutable source/module inputs used by a milestone build."""
import hashlib
import json
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parents[2]
lock = json.loads((root / "docs/modernization/versions.json").read_text())
selected = lock["selected_inputs"]


def command(*args, cwd=root):
    return subprocess.check_output(args, cwd=cwd, text=True).strip()


for path, key in (("firecracker", "firecracker"), ("runc", "runc"),
                  ("stargz-snapshotter", "stargz_snapshotter")):
    dependency = root / "_submodules" / path
    expected = selected[key]["commit"]
    actual = command("git", "rev-parse", "HEAD", cwd=dependency)
    if actual != expected:
        raise SystemExit(f"{dependency}: expected {expected}, found {actual}")
    command("git", "diff", "--exit-code", cwd=dependency)
    command("git", "diff", "--cached", "--exit-code", cwd=dependency)
    print(f"{path}: {actual}")

sdk = json.loads(command("go", "list", "-m", "-json",
                         "github.com/firecracker-microvm/firecracker-go-sdk"))
replacement = sdk.get("Replace", {})
expected_path, expected_version = selected["sdk"]["module_replacement"].split()
if (replacement.get("Path"), replacement.get("Version")) != (expected_path, expected_version):
    raise SystemExit(f"unexpected SDK module: {sdk}")

for module, version in (
    ("github.com/containerd/containerd/v2", selected["containerd"]["version"]),
    ("github.com/opencontainers/runc", selected["runc"]["version"]),
):
    actual = command("go", "list", "-m", "-f", "{{.Version}}", module)
    if actual != version:
        raise SystemExit(f"{module}: expected {version}, found {actual}")
    print(f"{module}: {actual}")

vmm_schema = (root / "_submodules/firecracker/src/firecracker/swagger/firecracker.yaml").read_bytes()
sdk_schema = (Path(replacement["Dir"]) / "client/swagger.yaml").read_bytes()
if vmm_schema != sdk_schema:
    raise SystemExit("SDK API schema differs from the pinned VMM source")
print(f"SDK: {expected_path}@{expected_version}")
print(f"VMM/SDK schema sha256: {hashlib.sha256(vmm_schema).hexdigest()}")

volume_fs = selected["guest_volume_fs"]
volume_module = json.loads(command("go", "mod", "download", "-json",
                                   f'{volume_fs["module"]}@{volume_fs["version"]}'))
for actual_key, expected_key in (("Version", "version"), ("Sum", "module_sum"),
                                 ("GoModSum", "go_mod_sum")):
    if volume_module.get(actual_key) != volume_fs[expected_key]:
        raise SystemExit(f"guest volume filesystem input differs from its pin: {volume_module}")
if volume_module.get("Origin", {}).get("Hash") != volume_fs["commit"]:
    raise SystemExit(f"guest volume filesystem source commit differs from its pin: {volume_module}")
resolved_volume = command("go", "list", "-mod=readonly", "-m", "-f", "{{.Version}}", volume_fs["module"])
if resolved_volume != volume_fs["version"]:
    raise SystemExit(f"guest volume filesystem resolved to unexpected version {resolved_volume}")
print(f'Guest volume filesystem: {volume_fs["module"]}@{resolved_volume} ({volume_fs["commit"]})')

cri = selected["cri_integration"]
for directory, versions in (
    (root, {"k8s.io/cri-api": cri["backend_cri_api"]}),
    (root / "cri", {
        "k8s.io/cri-api": cri["frontend_cri_api"],
        "k8s.io/cri-streaming": cri["frontend_cri_streaming"],
        "github.com/containerd/containerd/v2": selected["containerd"]["version"],
    }),
):
    for module, version in versions.items():
        actual = command("go", "list", "-mod=readonly", "-m", "-f", "{{.Version}}", module,
                         cwd=directory)
        if actual != version:
            raise SystemExit(f"{directory}: {module}: expected {version}, found {actual}")
        print(f"{directory.name}: {module}@{actual}")

parent = json.loads(command("go", "list", "-mod=readonly", "-m", "-json",
                            "github.com/firecracker-microvm/firecracker-containerd", cwd=root / "cri"))
local = parent.get("Replace", {})
if local.get("Path") != ".." or Path(local.get("Dir", "")).resolve() != root:
    raise SystemExit(f"CRI frontend must use parent source in this same checkout: {parent}")
child_sdk = json.loads(command("go", "list", "-mod=readonly", "-m", "-json",
                               "github.com/firecracker-microvm/firecracker-go-sdk", cwd=root / "cri"))
child_replacement = child_sdk.get("Replace", {})
if (child_replacement.get("Path"), child_replacement.get("Version")) != (expected_path, expected_version):
    raise SystemExit(f"CRI frontend SDK differs from pinned parent SDK: {child_sdk}")
print("CRI frontend: co-versioned parent source and exact pinned SDK verified")
print(command("go", "version"))
