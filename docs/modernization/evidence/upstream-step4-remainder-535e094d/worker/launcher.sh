#!/usr/bin/env bash
# PREPARED ONLY: lead schedules this after reviewing the exact fixture fix.
# Usage: stage4-remainder.sh FIX_SHA /absolute/fix-checkout /absolute/NEW-evidence-dir
# Production/component commands stay in the original clean 9c3e checkout.
# Only the corrected snapshotter test compiles from FIX_SHA. No stack/image build.
set -Eeuo pipefail
umask 077

if [[ $# != 3 || ! $1 =~ ^[0-9a-f]{40}$ || $2 != /* || $3 != /* ]]; then
    echo "usage: $0 FIX_SHA /absolute/fix-checkout /absolute/NEW-evidence-dir" >&2
    exit 2
fi
fix_sha=$1
reviewed_fix=535e094d36ff2689d254bdefd2d0452ea37c9ef0
[[ $fix_sha == "$reviewed_fix" ]] || { echo 'Fixture SHA requires renewed review' >&2; exit 2; }
base_sha=9c3ea8e618a46d6d59e64a5812eff4f1bd382cdc
image_id=sha256:70b8de8f4d8250a4a7d2919607dae67c8949094268569fadc58ee321cd1c66e6
base_dir=$(cd -- "${BASE_REPO:-/users/nehalem/fc-ctrd-mod/upstream-migration/stage4-runtime}" && pwd -P)
fix_dir=$(cd -- "$2" && pwd -P)
evidence_parent=$(cd -- "$(dirname -- "$3")" && pwd -P)
evidence_leaf=$(basename -- "$3")
[[ $evidence_leaf != . && $evidence_leaf != .. && $fix_dir != "$base_dir" ]] || exit 2
evidence="$evidence_parent/$evidence_leaf"
for source_dir in "$base_dir" "$fix_dir"; do
    case "$evidence/" in "$source_dir/"*) echo 'Evidence must be outside checkouts' >&2; exit 2;; esac
done
mkdir -- "$evidence"
mkdir -- "$evidence/commands" "$evidence/runtime-logs" "$evidence/probes" "$evidence/vms"
# Preserve the actual scheduled launcher, not merely its filename.
launcher_source=$(realpath -- "${BASH_SOURCE[0]}")
cp -- "$launcher_source" "$evidence/launcher.sh"
sha256sum "$evidence/launcher.sh" > "$evidence/launcher.sha256"
status_file="$evidence/command-status.tsv"
printf 'step\texit_code\n' > "$status_file"
export STACK_DIR=${STACK_DIR:-/users/nehalem/fc-ctrd-mod}
export PATH="$STACK_DIR/toolchains/go1.26.11/bin:$PATH"
export GOTOOLCHAIN=local STATIC_AGENT=on STATIC_DOCKER_CREDENTIAL_MMDS=on
export GIT_OPTIONAL_LOCKS=0 GIT_TERMINAL_PROMPT=0
unset ENABLE_ISOLATED_TESTS VOLFS_MOUNT_TESTS FICD_VOLFS_VM_TEST
unset FICD_EVAL_ARM FICD_EVAL_PROVENANCE FC_TEST_BIN DISABLE_ROOT_TESTS
export FICD_DM_VOLUME_GROUP=''
run_id="${fix_sha:0:12}-$(python3 -I -c 'import secrets; print(secrets.token_hex(6))')"
owner_label=org.firecracker.stage4-remainder
pool="fc-rem-$run_id-stargz"
pool_dir="$base_dir/tmp/devmapper/$pool"
pool_attempted=0
sdk_attempted=0
cleanup_done=0
all_passed=0
active_step=
real_docker=$(command -v docker)
prior_dir=${PRIOR_EVIDENCE_DIR:-"$(dirname -- "$base_dir")/evidence"}
prior_log="$prior_dir/stage4-acceptance-9c3ea8e618a4.log"
prior_status="$prior_dir/stage4-command-status-9c3ea8e618a4.tsv"
export base_sha fix_sha base_dir fix_dir evidence image_id owner_label run_id real_docker

# Label every Docker run, including the unchanged SDK runner's --rm container.
# Never intercept Docker build, alter daemon state, or remove unrelated containers.
mkdir "$evidence/docker-wrapper"
cat > "$evidence/docker-wrapper/docker" <<'DOCKER_WRAPPER'
#!/usr/bin/env bash
set -euo pipefail
if [[ ${1:-} == run ]]; then
    shift
    exec "$real_docker" run --label "$owner_label=$run_id" "$@"
fi
exec "$real_docker" "$@"
DOCKER_WRAPPER
chmod 700 "$evidence/docker-wrapper/docker"
export PATH="$evidence/docker-wrapper:$PATH"

run_logged() {
    local step=$1 rc
    shift
    active_step=$step
    printf '%q ' "$@" > "$evidence/commands/$step.sh"
    printf '\n' >> "$evidence/commands/$step.sh"
    if "$@" > "$evidence/$step.log" 2>&1; then rc=0; else rc=$?; fi
    printf '%s\t%s\n' "$step" "$rc" >> "$status_file"
    active_step=
    return "$rc"
}

verify_sources() {
    python3 -I - <<'PY'
import hashlib,json,os,pathlib,subprocess
base,fix,bsha,fsha=(os.environ[k] for k in ['base_dir','fix_dir','base_sha','fix_sha'])
def git(repo,*args): return subprocess.check_output(['git','-C',repo,*args])
def rows(repo,ref):
    return dict(e.split(b'\t',1)[::-1] for e in git(repo,'ls-tree','-rz','--full-tree',ref).split(b'\0') if e)
for repo,sha in [(base,bsha),(fix,fsha)]:
    assert git(repo,'rev-parse','HEAD').decode().strip()==sha,'wrong source HEAD'
    assert git(repo,'rev-parse','--show-toplevel').decode().strip()==repo
    assert not git(repo,'status','--porcelain','--untracked-files=all').strip(),'dirty checkout'
    entries=rows(repo,sha)
    packages={pathlib.PurePosixPath(p.decode()).parent for p in entries if p.endswith(b'.go')}
    for p in git(repo,'ls-files','--others','--ignored','--exclude-standard','-z').split(b'\0'):
        if p.endswith(b'.go'):
            assert pathlib.PurePosixPath(p.decode()).parent not in packages,'ignored Go source injection'
assert git(fix,'rev-list','--parents','-n','1',fsha).decode().split()==[fsha,bsha],'fix is not the reviewed direct child'
old,new=rows(base,bsha),rows(fix,fsha)
changed={p for p in old.keys()|new.keys() if old.get(p)!=new.get(p)}
assert changed=={b'snapshotter/stargz_restore_integ_test.go'},changed
assert old[next(iter(changed))].split()[0]==new[next(iter(changed))].split()[0]==b'100644'
unchanged={p:m for p,m in old.items() if p not in changed}
assert all(new[p]==m for p,m in unchanged.items())
print(json.dumps({'runtime_source':bsha,'test_source':fsha,'sole_changed_path':next(iter(changed)).decode(),
 'all_other_mode_type_oid_entries_exact':len(unchanged),
 'production_entries_sha256':hashlib.sha256(b'\0'.join(p+b'\t'+m for p,m in sorted(unchanged.items()))).hexdigest()},indent=2))
PY
}

verify_prior_failure() {
    python3 -I - "$prior_log" "$prior_status" <<'PY'
import hashlib,json,pathlib,shutil,os,sys
expected=['aa7b314b5cf99fc614d7f384633c4f0188af0a156ed0d8fa93628bbd04034279',
          'ce6fc30befdbdbbe06afb14faf17c0d9fd92556a281a39ba703f2c920d2ce7df']
for source,digest in zip(map(pathlib.Path,sys.argv[1:]),expected):
    assert hashlib.sha256(source.read_bytes()).hexdigest()==digest,'original failure evidence changed'
    shutil.copyfile(source,pathlib.Path(os.environ['evidence'])/('original-'+source.name))
assert pathlib.Path(sys.argv[2]).read_text()=='step\texit_code\nbuild\t0\ninstall\t0\nacceptance\t2\n'
print(json.dumps({'original_run':'9c3ea8e618a46d6d59e64a5812eff4f1bd382cdc','result':'failed',
 'acceptance_exit':2,'retained_top_level_passes':55,'reason':'stargz fixture manifest1 versus mandatory manifest2',
 'thinpool_SDK_unique_checks':'not reached','original_failed_status_is_not_rewritten':True},indent=2))
PY
}

artifact_manifest() {
    python3 -I - <<'PY'
import hashlib,json,os,pathlib
root=pathlib.Path(os.environ['base_dir'])
assert (root/'bin/receipts/build/runtime-revision').read_text().strip()==os.environ['base_sha']
receipt=root/'bin/receipts/build/binaries.sha256'
checked={}
for line in receipt.read_text().splitlines():
    expected,relative=line.split(None,1)
    p=root/relative
    assert p.is_file() and hashlib.sha256(p.read_bytes()).hexdigest()==expected,'build binary receipt mismatch: '+relative
    checked[relative]=expected
files={}
for name in list(checked)+['default-vmlinux.bin','tools/image-builder/rootfs.img',
 'tools/image-builder/rootfs-stargz.img','tools/image-builder/rootfs-dax.img','tools/image-builder/rootfs-stargz-dax.img']:
    p=root/name
    assert p.is_file(),name
    files[name]={'bytes':p.stat().st_size,'sha256':hashlib.sha256(p.read_bytes()).hexdigest()}
print(json.dumps({'clean_runtime_source':os.environ['base_sha'],'build_receipt_verified':checked,
 'current_host_artifacts':files,'scope':'Current post-failed-test-stack host inputs; not an installed-rootfs or completed acceptance receipt.'},indent=2,sort_keys=True))
PY
}

require_passes() {
    python3 -I - "$@" <<'PY'
import pathlib,re,sys
text=pathlib.Path(sys.argv[1]).read_text()
for name in sys.argv[2:]:
    assert re.search(r'^--- PASS: '+re.escape(name)+r' \(',text,re.M),'missing PASS: '+name
PY
}

cleanup_pool() {
    local name=$1 directory=$2 rc=0 maps loops dev
    if sudo -n dmsetup info "$name" >/dev/null 2>&1; then
        env FICD_DM_VOLUME_GROUP='' "$base_dir/tools/thinpool.sh" remove "$name" || rc=1
    fi
    if maps=$(sudo -n dmsetup ls --noheadings); then
        maps=$(awk -v n="$name" '$1==n || index($1,n "-snap-")==1 {print $1}' <<< "$maps")
        [[ -z $maps ]] || rc=1
    else rc=1; maps=unknown; fi
    if [[ -z $maps ]]; then
        if loops=$(sudo -n losetup --list --json --output NAME,BACK-FILE); then
            if loops=$(python3 -I -c 'import json,sys; d=sys.argv[1]; owned={d+"/data",d+"/metadata"}; print("\n".join(x["name"] for x in json.load(sys.stdin).get("loopdevices",[]) if x.get("back-file","").removesuffix(" (deleted)") in owned))' "$directory" <<< "$loops"); then
                while IFS= read -r dev; do
                    [[ -z $dev ]] || sudo -n losetup --detach "$dev" || rc=1
                done <<< "$loops"
            else rc=1; fi
        else rc=1; fi
        if loops=$(sudo -n losetup --list --json --output NAME,BACK-FILE); then
            if python3 -I -c 'import json,sys; d=sys.argv[1]; owned={d+"/data",d+"/metadata"}; assert not [x for x in json.load(sys.stdin).get("loopdevices",[]) if x.get("back-file","").removesuffix(" (deleted)") in owned]' "$directory" <<< "$loops"; then
                rm -f -- "$directory/data" "$directory/metadata" || rc=1
            else rc=1; fi
        else rc=1; fi
    fi
    return "$rc"
}

cleanup_owned() {
    local rc=0 ids cid label pid path name
    if ids=$(docker ps -aq --no-trunc --filter "label=$owner_label=$run_id"); then
        while IFS= read -r cid; do
            [[ -n $cid ]] || continue
            if label=$(docker inspect --format "{{index .Config.Labels \"$owner_label\"}}" "$cid") && [[ $label == "$run_id" ]]; then
                docker logs "$cid" > "$evidence/container-$cid.log" 2>&1 || true
                docker inspect "$cid" > "$evidence/container-$cid.json" 2>&1 || rc=1
                docker rm --force "$cid" || rc=1
            else rc=1; fi
        done <<< "$ids"
        ids=$(docker ps -aq --no-trunc --filter "label=$owner_label=$run_id") || rc=1
        [[ -z $ids ]] || rc=1
    else rc=1; fi
    if mountpoint -q "$evidence/probes/fs"; then sudo -n umount "$evidence/probes/fs" || rc=1; fi
    if [[ -f $evidence/probes/enospc.ext4 ]]; then
        if ids=$(sudo -n losetup --output NAME --noheadings --associated "$evidence/probes/enospc.ext4"); then
            [[ -z $ids ]] || rc=1
        else rc=1; fi
    fi
    if (( pool_attempted )); then cleanup_pool "$pool" "$pool_dir" || rc=1; fi
    if [[ -s $evidence/thinpool-owner-pid ]]; then
        pid=$(cat "$evidence/thinpool-owner-pid")
        if [[ $pid =~ ^[0-9]+$ ]]; then
            for name in "fcmod-pool-audit-$pid-a" "fcmod-pool-audit-$pid-b"; do
                cleanup_pool "$name" "$base_dir/tmp/devmapper/$name" || rc=1
            done
        else rc=1; fi
    fi
    if (( sdk_attempted )) && sudo -n test -d /sys/fs/cgroup/firecracker/b; then
        if [[ -z $(sudo -n cat /sys/fs/cgroup/firecracker/b/cgroup.procs) ]]; then
            sudo -n rmdir /sys/fs/cgroup/firecracker/b || rc=1
        else rc=1; fi
    fi
    # Only remove empty mount targets created by this run, never existing files.
    if [[ -f $evidence/created-bind-targets ]]; then
        while IFS= read -r path; do
            case "$path" in "$fix_dir/tools/image-builder/rootfs-stargz.img"|"$fix_dir/tools/image-builder/rootfs-stargz-dax.img")
                if [[ -f $path && ! -L $path && ! -s $path ]]; then rm -- "$path" || rc=1; else rc=1; fi;;
                *) rc=1;;
            esac
        done < "$evidence/created-bind-targets"
        if (( rc==0 )); then rm -- "$evidence/created-bind-targets" || rc=1; fi
    fi
    if (( rc==0 )); then
        sudo -n rm -rf -- "$evidence/vms" || rc=1
        if (( rc==0 )); then cleanup_done=1; fi
    fi
    return "$rc"
}

finish() {
    local rc=$? cleanup_rc=0
    trap - EXIT INT TERM
    set +e
    if (( ! cleanup_done )); then
        cleanup_owned >> "$evidence/cleanup-on-exit.log" 2>&1
        cleanup_rc=$?
        printf 'cleanup-on-exit\t%s\n' "$cleanup_rc" >> "$status_file"
    fi
    (( cleanup_rc==0 )) || { (( rc!=0 )) || rc=1; }
    if (( rc==0 && ! all_passed )); then rc=1; fi
    python3 -I - "$rc" "$cleanup_rc" "$active_step" <<'PY'
import json,os,pathlib,sys
root=pathlib.Path(os.environ['evidence'])
rc,cleanup,active=sys.argv[1:]
rows=[line.split('\t') for line in (root/'command-status.tsv').read_text().splitlines()[1:]]
result={'runtime_source':os.environ['base_sha'],'test_fixture_source':os.environ['fix_sha'],
 'image_id':os.environ['image_id'],'result':'passed' if rc=='0' else 'failed','exit_code':int(rc),
 'cleanup_exit':int(cleanup),'active_step_on_exit':active,'phase_exit_codes':dict(rows),
 'original_failed_run':{'result':'failed','acceptance_exit':2,'retained_top_level_passes':55},
 'scope':'Targeted corrected stargz twelve leaves plus previously-unreached thinpool/SDK and unique followups; no full-stack rebuild/rerun.',
 'rootfs_scope':'Image ID is frozen. Current host test-rootfs hashes are recorded before/after; no equality to installed rootfs or pre-test build rootfs is claimed.'}
(root/'summary.json').write_text(json.dumps(result,indent=2)+'\n')
PY
    if (( $?!=0 )); then rc=1; fi
    if (( rc==0 && all_passed && cleanup_done )); then
        if printf '%s\n' STAGE4_REMAINDER_TARGETED_AND_UNIQUE_CHECKS_OK > "$evidence/completion-marker.txt"; then
            echo STAGE4_REMAINDER_TARGETED_AND_UNIQUE_CHECKS_OK
        else rc=1; fi
    fi
    exit "$rc"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cd "$base_dir"
run_logged source-before verify_sources
run_logged prior-failure verify_prior_failure
# Embed/copy of the original local failure summary is generated during preparation.
cat > "$evidence/original-summary.json" <<'ORIGINAL_FAILURE_SUMMARY'
{
  "runtime_revision": "9c3ea8e618a46d6d59e64a5812eff4f1bd382cdc",
  "runtime_tree": "9955efe6cdaf21ba197b2dcc03da891d85ae3ab1",
  "result": "failed",
  "phase_exit_codes": {
    "build": 0,
    "install": 0,
    "acceptance": 2
  },
  "top_level_pass_count": 55,
  "failures": [
    "TestStargzNativeRestore_Isolated (213.47s)",
    "TestStargzNativeRestore_Isolated/disk (33.74s)",
    "TestStargzNativeRestore_Isolated/disk/jailed=false (17.31s)",
    "TestStargzNativeRestore_Isolated/disk/jailed=true (16.43s)",
    "TestStargzNativeRestore_Isolated/ext4-block (38.43s)",
    "TestStargzNativeRestore_Isolated/ext4-block/jailed=false (20.95s)",
    "TestStargzNativeRestore_Isolated/ext4-block/jailed=true (17.49s)",
    "TestStargzNativeRestore_Isolated/pmem (36.04s)",
    "TestStargzNativeRestore_Isolated/pmem/jailed=false (18.68s)",
    "TestStargzNativeRestore_Isolated/pmem/jailed=true (17.36s)",
    "TestStargzNativeRestore_Isolated/reclaim (34.01s)",
    "TestStargzNativeRestore_Isolated/reclaim/jailed=false (16.75s)",
    "TestStargzNativeRestore_Isolated/reclaim/jailed=true (17.25s)",
    "TestStargzNativeRestore_Isolated/combined (35.58s)",
    "TestStargzNativeRestore_Isolated/combined/jailed=false (17.85s)",
    "TestStargzNativeRestore_Isolated/combined/jailed=true (17.73s)",
    "TestStargzNativeRestore_Isolated/ext4-reclaim (35.56s)",
    "TestStargzNativeRestore_Isolated/ext4-reclaim/jailed=false (17.41s)",
    "TestStargzNativeRestore_Isolated/ext4-reclaim/jailed=true (18.15s)"
  ],
  "failure_reason": "Stargz native restore fixture expects manifest1; actual mandatory migrated manifest2. All12 leaves stop before restore.",
  "unique_followups": "Not reached",
  "files": {
    "stage4-acceptance-9c3ea8e618a4.log": {
      "bytes": 699424,
      "sha256": "aa7b314b5cf99fc614d7f384633c4f0188af0a156ed0d8fa93628bbd04034279"
    },
    "stage4-build-9c3ea8e618a4.log": {
      "bytes": 275930,
      "sha256": "2d1acd598034836ed642f0875e45ecda5c66b68581d85cca94e12318b5f1997e"
    },
    "stage4-command-status-9c3ea8e618a4.tsv": {
      "bytes": 46,
      "sha256": "ce6fc30befdbdbbe06afb14faf17c0d9fd92556a281a39ba703f2c920d2ce7df"
    },
    "stage4-install-9c3ea8e618a4.log": {
      "bytes": 9978,
      "sha256": "98adf9110d22a34c9be6f6114042e1790af38a8f6779381f6e14ae577f180947"
    },
    "stage4-submodules-9c3ea8e618a4.txt": {
      "bytes": 198,
      "sha256": "3e8aec0aa6326c9582c15809dd86288fe1f2c395a328676ca52cd01a9014df8d"
    },
    "stage4-tested-source-9c3ea8e618a4.txt": {
      "bytes": 82,
      "sha256": "9acc21ae4016f99a5907a899173bc79312f068e991f3cd2d127ad541c1a1d063"
    }
  }
}
ORIGINAL_FAILURE_SUMMARY
run_logged original-summary-integrity python3 -I - "$evidence/original-summary.json" <<'PY'
import hashlib,pathlib,sys
assert hashlib.sha256(pathlib.Path(sys.argv[1]).read_bytes()).hexdigest()=='c89afecd45f8a20301920e00c3b560c5d658192b45bd0aed0a07c68dd47869dd'
PY
run_logged artifacts-before artifact_manifest
run_logged image-inspect docker image inspect "$image_id"
run_logged image-identity docker image inspect --format '{{.Id}}' "$image_id"
[[ $(cat "$evidence/image-identity.log") == "$image_id" ]]
run_logged image-artifacts docker run --rm --network=none --entrypoint=/bin/bash "$image_id" -ec \
    'sha256sum /usr/local/bin/firecracker /usr/local/bin/firecracker-containerd /usr/local/bin/containerd-shim-aws-firecracker /var/lib/firecracker-containerd/runtime/default-rootfs.img /var/lib/firecracker-containerd/runtime/default-vmlinux.bin; firecracker-containerd --version'
run_logged sudo-ready sudo -n true
run_logged filesystem findmnt --noheadings --output FSTYPE --target "$evidence"
! grep -Eq '^(tmpfs|ramfs)$' "$evidence/filesystem.log"

# No image/test-deps make target: those can rebuild or retag the existing stack.
# The test binary is compiled in the existing image against /src at FIX_SHA.
# Placeholders are ignored empty bind targets; actual bytes come from frozen9c3e
# host test artifacts via read-only nested mounts and are hashed before/after.
for name in rootfs-stargz.img rootfs-stargz-dax.img; do
    path="$fix_dir/tools/image-builder/$name"
    if [[ ! -e $path && ! -L $path ]]; then
        git -C "$fix_dir" check-ignore -q -- "tools/image-builder/$name"
        (set -o noclobber; : > "$path")
        printf '%s\n' "$path" >> "$evidence/created-bind-targets"
    else
        [[ -f $path && ! -L $path ]]
    fi
done
sudo -n dmsetup ls --noheadings > "$evidence/pool-preflight.txt"
[[ ! -e $pool_dir && ! -L $pool_dir ]]
! awk -v n="$pool" '$1==n || index($1,n "-snap-")==1 {found=1} END {exit !found}' "$evidence/pool-preflight.txt"
pool_attempted=1
run_logged pool-create env FICD_DM_VOLUME_GROUP='' "$base_dir/tools/thinpool.sh" create "$pool"
run_logged stargz-native docker run --rm --name "fc-rem-$run_id" \
    --privileged --ipc=host --network=bridge --init \
    --volume /dev:/dev --volume /run/udev/control:/run/udev/control \
    --volume "$evidence/runtime-logs:/var/log/firecracker-containerd-test" \
    --volume "$evidence/vms:/srv/firecracker_containerd_tests" \
    --volume "$fix_dir:/src:ro" --volume gocache:/go \
    --mount "type=bind,source=$base_dir/tools/image-builder/rootfs-stargz.img,target=/src/tools/image-builder/rootfs-stargz.img,readonly" \
    --mount "type=bind,source=$base_dir/tools/image-builder/rootfs-stargz-dax.img,target=/src/tools/image-builder/rootfs-stargz-dax.img,readonly" \
    --env ENABLE_ISOLATED_TESTS=1 --env GOPROXY=off --env GOSUMDB=off \
    --env FICD_DM_VOLUME_GROUP= --env "FICD_DM_POOL=$pool" --workdir /src/snapshotter \
    "$image_id" "go test -mod=readonly -v -count=1 -timeout=1200s -run '^TestStargzNativeRestore_Isolated$'"
run_logged stargz-twelve-pass-guard python3 -I - "$evidence/stargz-native.log" <<'PY'
import collections,pathlib,re,sys
t=pathlib.Path(sys.argv[1]).read_text()
expected={f'TestStargzNativeRestore_Isolated/{p}/jailed={j}' for p in ['disk','ext4-block','pmem','reclaim','combined','ext4-reclaim'] for j in ['false','true']}
passed=collections.Counter(re.findall(r'^\s*--- PASS: (TestStargzNativeRestore_Isolated/[^ /]+/jailed=(?:false|true)) \(',t,re.M))
assert set(passed)==expected and all(n==1 for n in passed.values()),passed
assert re.search(r'^--- PASS: TestStargzNativeRestore_Isolated \(',t,re.M)
assert not re.search(r'^\s*--- (?:FAIL|SKIP): TestStargzNativeRestore_Isolated',t,re.M)
print('All twelve corrected stargz native restore leaves passed without skips')
PY

# Exact unchanged commands from the original9c3e test-stack tail.
run_logged thinpool bash -Eeuo pipefail -c '
    for suffix in a b; do
        n="fcmod-pool-audit-$$-$suffix"
        test ! -e "$base_dir/tmp/devmapper/$n"
        if sudo -n dmsetup info "$n" >/dev/null 2>&1; then exit 2; fi
    done
    printf "%s\n" "$$" > "$evidence/thinpool-owner-pid"
    exec "$base_dir/tools/modernize/test-thinpool.sh"'
run_logged thinpool-pass-guard grep -qx THINPOOL_ISOLATION_AND_CLEANUP_OK "$evidence/thinpool.log"
sudo -n test ! -e /sys/fs/cgroup/firecracker/b
sdk_attempted=1
run_logged sdk env SDK_TEST_IMAGE="$image_id" "$base_dir/tools/modernize/test-sdk.sh"
run_logged sdk-pass-guard python3 -I - "$evidence/sdk.log" <<'PY'
import pathlib,re,sys
t=pathlib.Path(sys.argv[1]).read_text()
assert re.search(r'^PINNED_SDK_PRIVILEGED_SUITE_OK$',t,re.M)
assert len(re.findall(r'^--- PASS: ',t,re.M))==86
assert re.findall(r'^--- SKIP: (\S+) ',t,re.M)==['TestLogAndMetrics']
assert not re.search(r'^\s*--- FAIL:',t,re.M)
print('Published pinned SDK:86 top-level PASS; retained TestLogAndMetrics skip')
PY

# Previously-unreached unique checks, using original clean9c3e source/artifacts.
run_logged unit-race env DISABLE_ROOT_TESTS=1 go test -mod=readonly -short -race -count=1 -timeout=180s ./...
run_logged overlay-syntax sh -n tools/image-builder/files_debootstrap/sbin/overlay-init
run_logged stargz-syntax sh -n tools/image-builder/files_stargz/usr/local/bin/prepare-stargz-storage
run_logged evaluation-unit python3 -m unittest discover -s tools/memory-eval -p 'test_*.py' -v
run_logged evaluation-count-guard python3 -I - "$evidence/evaluation-unit.log" <<'PY'
import pathlib,re,sys
t=pathlib.Path(sys.argv[1]).read_text()
assert re.search(r'^Ran 36 tests in ',t,re.M) and re.search(r'^OK$',t,re.M)
PY
run_logged runtime-probe-build env CGO_ENABLED=0 go test -mod=readonly -c -o "$evidence/probes/runtime.test" ./runtime
run_logged volfs-probe-build go test -mod=readonly -race -c -o "$evidence/probes/volfs.test" ./internal/volfs
sha256sum "$evidence/probes/runtime.test" "$evidence/probes/volfs.test" > "$evidence/probe-binaries.sha256"
go version -m "$evidence/probes/runtime.test" > "$evidence/runtime-probe-buildinfo.txt"
go version -m "$evidence/probes/volfs.test" > "$evidence/volfs-probe-buildinfo.txt"
run_logged namespace-api sudo -n env FC_TEST_BIN="$base_dir/bin/firecracker" \
    "$evidence/probes/runtime.test" -test.v -test.count=1 -test.timeout=120s \
    -test.run='^TestRestoreNamespace(Isolation|ReleaseAPI)$'
run_logged namespace-pass-guard require_passes "$evidence/namespace-api.log" TestRestoreNamespaceIsolation TestRestoreNamespaceReleaseAPI
mkdir "$evidence/probes/fs"
truncate -s 32M "$evidence/probes/enospc.ext4"
run_logged enospc-format mkfs.ext4 -q -F "$evidence/probes/enospc.ext4"
run_logged enospc-mount sudo -n mount -o loop "$evidence/probes/enospc.ext4" "$evidence/probes/fs"
run_logged host-enospc sudo -n env FICD_OVERLAY_ENOSPC_DIR="$evidence/probes/fs" \
    "$evidence/probes/runtime.test" -test.v -test.count=1 -test.timeout=120s -test.run='^TestGuestOverlayHostENOSPC$'
run_logged enospc-pass-guard require_passes "$evidence/host-enospc.log" TestGuestOverlayHostENOSPC
run_logged enospc-unmount sudo -n umount "$evidence/probes/fs"
run_logged host-fuse sudo -n env VOLFS_MOUNT_TESTS=1 unshare --mount --propagation private \
    "$evidence/probes/volfs.test" -test.v -test.count=1 -test.timeout=120s \
    -test.run='^TestGuest(FilesystemLifecycleAndHostCoherence|FilesystemDirectoryCookiesAndReconnect|SingleFileAndReadOnlyMount|MountCloseDetachesBorrowedFile|KernelPermissionsAndOwnership)$'
run_logged fuse-pass-guard require_passes "$evidence/host-fuse.log" \
    TestGuestFilesystemLifecycleAndHostCoherence TestGuestFilesystemDirectoryCookiesAndReconnect \
    TestGuestSingleFileAndReadOnlyMount TestGuestMountCloseDetachesBorrowedFile TestGuestKernelPermissionsAndOwnership
run_logged artifacts-after artifact_manifest
run_logged artifact-identity-guard cmp "$evidence/artifacts-before.log" "$evidence/artifacts-after.log"
run_logged source-after verify_sources
run_logged cleanup cleanup_owned
run_logged final-image-identity docker image inspect --format '{{.Id}}' "$image_id"
[[ $(cat "$evidence/final-image-identity.log") == "$image_id" ]]
all_passed=1
# EXIT writes the terminal receipt successfully before emitting the sole marker.
