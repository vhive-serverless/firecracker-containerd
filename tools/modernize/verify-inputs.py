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
# `go list -m` omits Dir when a newly pinned version is not cached yet.
# Resolve the verified replacement explicitly before inspecting its schema.
sdk_module = json.loads(command("go", "mod", "download", "-json",
                                f"{expected_path}@{expected_version}"))
if sdk_module.get("Origin", {}).get("Hash") != selected["sdk"]["commit"]:
    raise SystemExit(f"SDK source commit differs from its pin: {sdk_module}")

for module, version in (
    ("github.com/containerd/containerd/v2", selected["containerd"]["version"]),
    ("github.com/opencontainers/runc", selected["runc"]["version"]),
):
    actual = command("go", "list", "-m", "-f", "{{.Version}}", module)
    if actual != version:
        raise SystemExit(f"{module}: expected {version}, found {actual}")
    print(f"{module}: {actual}")

vmm_schema = (root / "_submodules/firecracker/src/firecracker/swagger/firecracker.yaml").read_bytes()
sdk_schema = (Path(sdk_module["Dir"]) / "client/swagger.yaml").read_bytes()
if vmm_schema != sdk_schema:
    raise SystemExit("SDK API schema differs from the pinned VMM source")
print(f"SDK: {expected_path}@{expected_version}")
print(f"VMM/SDK schema sha256: {hashlib.sha256(vmm_schema).hexdigest()}")

print(command("go", "version"))
