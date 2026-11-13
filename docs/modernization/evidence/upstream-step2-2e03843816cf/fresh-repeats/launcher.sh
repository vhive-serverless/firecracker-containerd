#!/usr/bin/env bash
# Authorized bounded diagnostic: unchanged test, five concurrent VMs, three runs.
set -euo pipefail
if [[ $# != 2 || $1 != /* || $2 != /* ]]; then
    echo "usage: $0 /absolute/exact-source /absolute/NEW-evidence" >&2
    exit 2
fi
source_dir=$(realpath -- "$1")
evidence_dir=$(realpath -m -- "$2")
revision=2e03843816cf95d184ad4ad429adf6ea2ccc1412
image=sha256:97f3745973e8856190db513d2d1def3a84a603eb3337bc4ccac655e554b27117
owner_label=org.firecracker.cni-three-diagnostic
[[ "$evidence_dir/" != "$source_dir/"* && ! -e "$evidence_dir" ]]
test "$(git -C "$source_dir" rev-parse HEAD)" = "$revision"
test -z "$(git -C "$source_dir" status --porcelain)"
test "$(docker image inspect --format '{{.Id}}' "$image")" = "$image"
mkdir -m 700 "$evidence_dir"
cp -- "${BASH_SOURCE[0]}" "$evidence_dir/launcher.sh"
sha256sum "$evidence_dir/launcher.sh" > "$evidence_dir/launcher.sha256"
export PATH=/users/nehalem/fc-ctrd-mod/toolchains/go1.26.11/bin:$PATH
cd -- "$source_dir"
python3 - "$evidence_dir" <<'PY'
import hashlib,json,pathlib,subprocess,sys
paths=['snapshotter/service_integ_test.go','snapshotter/Makefile','internal/integtest/runtime.go','tools/demo/fcnet.conflist','tools/docker/Dockerfile.integ-test','tools/docker/entrypoint.sh','tools/docker/config.toml','tools/docker/do_not_edit_for_firecracker-control.config.json','Makefile','go.mod','go.sum']
def git(*args): return subprocess.check_output(['git',*args])
records={}
for p in paths:
    old=git('show','7386ab5c5e8c3519ad1698c27f548803e714e304:'+p);new=git('show','HEAD:'+p)
    records[p]={'equal_to_accepted7386':old==new,'sha256':hashlib.sha256(new).hexdigest()}
    assert old==new,p
assert 'NumberOfVms = 5' in pathlib.Path('internal/integtest/runtime.go').read_text()
pathlib.Path(sys.argv[1],'source-inputs.json').write_text(json.dumps({'revision':git('rev-parse','HEAD').decode().strip(),'tree':git('rev-parse','HEAD^{tree}').decode().strip(),'default_concurrent_vms':5,'test_command':"go test -v -count=1 -timeout=300s -run '^TestLaunchMultipleContainersWithRemoteSnapshotter_Isolated$'",'inputs':records},indent=2)+'\n')
PY
# This reads old logs only; all diagnostic containers bind their own new logdir.
old_log_manifest() {
sudo -n python3 - "$source_dir/snapshotter/logs" <<'PY'
import hashlib,json,pathlib,sys
root=pathlib.Path(sys.argv[1]);out={}
for p in sorted(root.rglob('*')):
    if p.is_file() and not p.is_symlink(): out[str(p.relative_to(root))]=hashlib.sha256(p.read_bytes()).hexdigest()
print(json.dumps(out,sort_keys=True,indent=2))
PY
}
old_log_manifest > "$evidence_dir/original-snapshotter-logs-before.json"
docker image inspect "$image" > "$evidence_dir/image.json"
docker ps --no-trunc --format '{{json .}}' > "$evidence_dir/baseline-containers.jsonl"
printf 'run\ttest_exit\tcleanup_exit\n' > "$evidence_dir/status.tsv"
run_id=$(python3 -c 'import secrets;print(secrets.token_hex(6))')
pool= container= active=0
cleanup_owned() {
    local result=0
    if [[ -n "$container" ]] && docker inspect "$container" > /dev/null 2>&1; then
        test "$(docker inspect --format '{{index .Config.Labels "org.firecracker.cni-three-diagnostic"}}' "$container")" = "$run_id" || return 1
        docker rm -f "$container" || result=1
    fi
    if [[ -n "$pool" ]] && sudo -n dmsetup info "$pool" > /dev/null 2>&1; then
        if tools/thinpool.sh remove "$pool"; then :; else result=1; fi
    fi
    if [[ -n "$pool" ]]; then
        if sudo -n dmsetup info "$pool" > /dev/null 2>&1; then result=1; fi
        for backing in data metadata; do
            test ! -e "$source_dir/tmp/devmapper/$pool/$backing" || result=1
            test -z "$(sudo -n losetup --noheadings --output NAME --associated "$source_dir/tmp/devmapper/$pool/$backing")" || result=1
        done
    fi
    return "$result"
}
finish() {
    local result=$?
    trap - EXIT
    if [[ "$active" == 1 ]]; then
        if cleanup_owned > "$evidence_dir/abnormal-cleanup.log" 2>&1; then :; else result=1; fi
    fi
    exit "$result"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
for run in 1 2 3; do
    current="$evidence_dir/run-$run"
    mkdir -m 755 "$current" "$current/logs"
    pool="fc-cni-2e038-$run_id-$run"
    container="fc-cni-2e038-$run_id-$run"
    test ! -e "$source_dir/tmp/devmapper/$pool"
    if docker inspect "$container" > /dev/null 2>&1; then exit 1; fi
    if sudo -n dmsetup info "$pool" > /dev/null 2>&1; then exit 1; fi
    active=1
    export FICD_DM_VOLUME_GROUP=''
    tools/thinpool.sh create "$pool" > "$current/pool-create.log" 2>&1
    command=(docker run --rm --name "$container" --label "$owner_label=$run_id" --cidfile "$current/container-id"
        --privileged --ipc=host --network=bridge
        --volume /dev:/dev --volume /run/udev/control:/run/udev/control
        --volume "$current/logs:/var/log/firecracker-containerd-test"
        --volume "$source_dir:/src" --volume gocache:/go
        --env ENABLE_ISOLATED_TESTS=1 --env GOPROXY=direct --env GOSUMDB=off --env GO111MODULE=on
        --env NUMBER_OF_VMS= --env FICD_DM_VOLUME_GROUP= --env "FICD_DM_POOL=$pool"
        --workdir=/src/snapshotter --init "$image"
        "go test -v -count=1 -timeout=300s -run '^TestLaunchMultipleContainersWithRemoteSnapshotter_Isolated$'")
    printf '%q ' "${command[@]}" > "$current/command.sh"
    printf '\n' >> "$current/command.sh"
    if "${command[@]}" > "$current/test.log" 2>&1; then test_status=0; else test_status=$?; fi
    if cleanup_owned > "$current/cleanup.log" 2>&1; then cleanup_status=0; else cleanup_status=$?; fi
    printf '%s\t%s\t%s\n' "$run" "$test_status" "$cleanup_status" >> "$evidence_dir/status.tsv"
    # Fixed three observations: a failure never becomes retry-until-pass.
    active=0
    if [[ "$cleanup_status" != 0 ]]; then echo 'Owned cleanup failed; refusing more VM work' >&2; exit 1; fi
done
old_log_manifest > "$evidence_dir/original-snapshotter-logs-after.json"
cmp "$evidence_dir/original-snapshotter-logs-before.json" "$evidence_dir/original-snapshotter-logs-after.json"
test -z "$(git status --porcelain)"
docker ps -a --no-trunc --format '{{json .}}' > "$evidence_dir/containers-after.jsonl"
sudo -n dmsetup ls --noheadings > "$evidence_dir/mappings-after.txt"
sudo -n losetup --list --json --output NAME,BACK-FILE > "$evidence_dir/loops-after.json"
python3 - "$evidence_dir" "$run_id" <<'PY'
import hashlib,json,pathlib,re,sys
root=pathlib.Path(sys.argv[1]);run_id=sys.argv[2]
rows=[row.split('\t') for row in (root/'status.tsv').read_text().splitlines()[1:]]
assert [r[0] for r in rows]==['1','2','3']
results=[]
for run,code,cleanup in rows:
    text=(root/('run-'+run)/'test.log').read_text()
    results.append({'run':int(run),'test_exit':int(code),'cleanup_exit':int(cleanup),'passes':re.findall(r'^--- PASS: (.+)$',text,re.M),'failures':re.findall(r'^--- FAIL: (.+)$',text,re.M),'skips':re.findall(r'^--- SKIP: (.+)$',text,re.M),'nft_chain_eexist':'CHAIN_USER_ADD failed (File exists): chain CNI-FORWARD' in text})
assert all(r['cleanup_exit']==0 for r in results)
assert run_id not in (root/'containers-after.jsonl').read_text()
assert run_id not in (root/'mappings-after.txt').read_text()
assert run_id not in (root/'loops-after.json').read_text()
summary={'purpose':'Exactly three diagnostic observations, unchanged source/image/test/defaultfiveVMconcurrency; no production fix or full-stack pass claim','results':results,'independent_owned_cleanup_clean':True,'original_snapshotter_logs_unchanged':True}
(root/'summary.json').write_text(json.dumps(summary,indent=2)+'\n')
files={str(p.relative_to(root)):{'bytes':p.stat().st_size,'sha256':hashlib.sha256(p.read_bytes()).hexdigest()} for p in sorted(root.rglob('*')) if p.is_file()}
(root/'files.json').write_text(json.dumps(files,indent=2)+'\n')
print(json.dumps(summary,indent=2))
PY
echo THREE_FIXED_CNI_DIAGNOSTIC_OBSERVATIONS_COMPLETE
