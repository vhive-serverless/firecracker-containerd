#!/usr/bin/env bash
# Precompile only on the designated worker; the experiment never invokes Go.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
stack_dir=${STACK_DIR:-"$HOME/fc-ctrd-mod"}
export PATH="$stack_dir/toolchains/go1.26.11/bin:$PATH"
cd "$repo_dir"
revision=$(git rev-parse HEAD)
dirty=false
if test -n "$(git status --porcelain)"; then
    test "${ALLOW_DIRTY:-0}" = 1 || { echo 'clean committed source required' >&2; exit 1; }
    dirty=true
fi
python3 tools/modernize/verify-inputs.py
source_hash=$(python3 tools/memory-eval/provenance.py)
mkdir -p bin
go test -mod=readonly -c -o bin/memory-eval.test \
    -ldflags "-X github.com/firecracker-microvm/firecracker-containerd/runtime.evaluationBuildRevision=$revision -X github.com/firecracker-microvm/firecracker-containerd/runtime.evaluationBuildSourceDigest=$source_hash -X github.com/firecracker-microvm/firecracker-containerd/runtime.evaluationBuildDirty=$dirty" ./runtime
python3 - <<'PY'
import importlib.util,json,pathlib,subprocess
repo=pathlib.Path.cwd()
spec=importlib.util.spec_from_file_location('provenance',repo/'tools/memory-eval/provenance.py')
module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
binary=repo/'bin/memory-eval.test'
receipt={'build_info':module.binary_info(binary),'binary_sha256':module.binary_digest(binary),
         'go_version':subprocess.check_output(['go','version'],text=True).strip()}
(repo/'bin/memory-eval-build.json').write_text(json.dumps(receipt,indent=2)+'\n')
print(json.dumps(receipt))
PY
