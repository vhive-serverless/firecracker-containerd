#!/usr/bin/env bash
# Prepared only. Lead schedules after the active Step 4 pipeline and cleanup.
# Compile supplemental tests against the frozen accepted image; never rebuild,
# install or replace runtime binaries/images. Own only this run's named resources.
set -Eeuo pipefail
umask 077

if [[ $# != 2 || "$1" != /* || "$2" != /* ]]; then
    echo "usage: $0 /absolute/supplement-checkout /absolute/NEW-evidence-directory" >&2
    exit 2
fi
supplement_dir=$(cd -- "$1" && pwd -P)
evidence_parent=$(cd -- "$(dirname -- "$2")" && pwd -P)
evidence_leaf=$(basename -- "$2")
if [[ "$evidence_leaf" == . || "$evidence_leaf" == .. ]]; then exit 2; fi
evidence_dir="$evidence_parent/$evidence_leaf"
case "$evidence_dir/" in "$supplement_dir/"*) echo 'Evidence must be outside the checkout' >&2; exit 2;; esac
mkdir -- "$evidence_dir"
mkdir -- "$evidence_dir/runtime-logs" "$evidence_dir/commands"
status_file="$evidence_dir/command-status.tsv"
printf 'step\texit_code\n' > "$status_file"
expected_revision=064c9884a0a34a52c1e648847a4bb5f336fa2dde
accepted_revision=332da2a7065baa0050037250bbf2ff316a1a8519
accepted_image=localhost/firecracker-containerd-test:332da2a7065b
accepted_image_id=sha256:17046a2aeb5c501161f0761094b96b3f5347602888759d56ac8cf9d6c40ab64d
run_id="064c9884-$(python3 -I -c 'import secrets; print(secrets.token_hex(6))')"
container_name="fc-transport-supp-$run_id"
owner_label=org.firecracker.transport-supplement
pool="fc-supp-$run_id"
pool_dir="$supplement_dir/tmp/devmapper/$pool"
container_attempted=0
pool_attempted=0
cleanup_done=0
acceptance_checked=0
active_step=

run_logged() {
    local step=$1 rc
    shift
    active_step=$step
    printf '%q ' "$@" > "$evidence_dir/commands/$step.sh"
    printf '\n' >> "$evidence_dir/commands/$step.sh"
    if "$@" > "$evidence_dir/$step.log" 2>&1; then rc=0; else rc=$?; fi
    printf '%s\t%s\n' "$step" "$rc" >> "$status_file"
    active_step=
    return "$rc"
}

verify_source() {
    python3 -I - "$supplement_dir" "$accepted_revision" "$expected_revision" <<'PY'
import hashlib,json,pathlib,subprocess,sys
repo,base,target=sys.argv[1:]
def git(*args): return subprocess.check_output(['git','-C',repo,*args])
assert git('rev-parse','HEAD').decode().strip()==target,'wrong supplemental revision'
assert git('rev-parse','--show-toplevel').decode().strip()==repo,'checkout root mismatch'
assert not git('status','--porcelain','--untracked-files=all').strip(),'checkout is dirty'
assert git('rev-list','--parents','-n','1','HEAD').decode().split()==[target,base],'unexpected parent'
def rows(ref): return [x for x in git('ls-tree','-rz','--full-tree',ref).split(b'\0') if x]
old,new=rows(base),rows(target)
assert all(x in new for x in old),'an accepted tracked entry changed'
expected={b'runtime/native_buffered_stdin_supplement_integ_test.go',b'runtime/snapshot_admission_supplement_test.go'}
added={x.split(b'\t',1)[1] for x in new if x not in old}
assert added==expected,'unexpected additions'
production=lambda rs:[x for x in rs if not x.split(b'\t',1)[1].endswith(b'_test.go')]
a,b=production(old),production(new)
assert a==b and len(a)==433,'production/build input equality failed'
digest=hashlib.sha256(b'\0'.join(a)+b'\0').hexdigest()
assert digest=='39bc1700b828d850e62790d1222c6a31c846f8c649f49f76782469eecb6a015d','production manifest mismatch'
# Ignored Go additions in a compiled package must not bypass git cleanliness.
package_dirs={pathlib.PurePosixPath(x.split(b'\t',1)[1].decode()).parent for x in new if x.endswith(b'.go')}
for path in git('ls-files','--others','--ignored','--exclude-standard','-z').split(b'\0'):
    if path and path.endswith(b'.go'):
        assert pathlib.PurePosixPath(path.decode()).parent not in package_dirs,'ignored Go source injection'
print(json.dumps({'base':base,'target':target,'production_entries':len(a),'production_manifest_sha256':digest,'all_accepted_entries_unchanged':True,'added_tests':sorted(x.decode() for x in added)},indent=2))
PY
}

verify_image() {
    test "$(cat "$evidence_dir/image-identity.log")" = "$accepted_image_id"
    printf 'verified_image_id=%s\n' "$accepted_image_id"
}

verify_pool_absent() {
    python3 -I - "$evidence_dir/pool-preflight.log" "$pool" "$pool_dir" <<'PY_POOL'
import pathlib,sys
listing,pool,directory=sys.argv[1:]
assert not pathlib.Path(directory).exists() and not pathlib.Path(directory).is_symlink(),'dedicated pool path already exists'
for line in pathlib.Path(listing).read_text().splitlines():
    fields=line.split()
    assert not fields or not (fields[0]==pool or fields[0].startswith(pool+'-snap-')),'dedicated mapping already exists'
print('Dedicated pool and private backing path are absent')
PY_POOL
}

verify_binaries() {
    python3 -I - "$evidence_dir/image-binaries.log" <<'PY'
import json,pathlib,sys
expected={
 '/usr/local/bin/firecracker-containerd':'47a157ebf66d55921ea81b937957ba6e91dbe63372f474cd33e166490f7a5c74',
 '/usr/local/bin/containerd-shim-aws-firecracker':'4c0c5e1dc13e130bdec45e9115f207a5105e9ed93ffe49ee8a4b1ac8058eafad',
 '/usr/local/bin/firecracker':'b33f46cd6d88834cf6a4f8d184f08f219809c636236bffd286d233a60602a2ba',
 '/var/lib/firecracker-containerd/runtime/default-rootfs.img':'b8455888c02d3eab83d9ea7ca1000d9978f46830eefbdb7b09d9d67166f56c72',
}
actual={}
for line in pathlib.Path(sys.argv[1]).read_text().splitlines():
    digest,path=line.split(None,1)
    assert path not in actual,'duplicate binary hash record'
    actual[path]=digest
assert actual==expected,'accepted image binary hashes differ from frozen332d receipt'
print(json.dumps({'frozen_runtime_revision':'332da2a7065baa0050037250bbf2ff316a1a8519','verified':actual},indent=2))
PY
}

verify_vm_guards() {
    python3 -I - "$evidence_dir/buffered-stdin-vm.log" <<'PY'
import pathlib,re,sys
log=pathlib.Path(sys.argv[1]).read_text()
for mode in ['false','true']:
    assert re.search(r'^\s*--- PASS: TestNativeBufferedStdinSnapshot_Isolated/jailed='+mode+r' \(',log,re.M),'missing jailed PASS:'+mode
assert re.search(r'^--- PASS: TestNativeBufferedStdinSnapshot_Isolated \(',log,re.M),'missing top-level PASS'
expected=len('accepted-before-cut-held-guest-reader-6d6cedf3\n')
witnesses=[int(x) for x in re.findall(r'buffered_stdin_precapture guest_receiver_accepted=([1-9][0-9]*) application_held=true',log)]
assert witnesses==[expected,expected],'missing/mismatched positive acceptance witnesses'
finals=[tuple(map(int,x)) for x in re.findall(r'buffered_stdin_snapshot sentinel_bytes=([1-9][0-9]*) captured_stdin_offset=([1-9][0-9]*) source_once=true clone_once=true captured_counter=0 stdout_stderr_exact=true eof_no_tail=true',log)]
assert finals==[(expected,expected),(expected,expected)],'missing/mismatched final sentinel assertions'
assert not re.search(r'^\s*--- (FAIL|SKIP): TestNativeBufferedStdinSnapshot_Isolated',log,re.M),'failed/skipped supplement'
print('Both jail leaves, positive receiver witnesses and final sentinel/EOF assertions passed')
PY
}

cleanup_owned() {
    local rc=0 cid label ids maps loop_rows dev loops_clear=0
    if (( container_attempted )); then
        if ids=$(docker ps -aq --no-trunc --filter "label=$owner_label=$run_id"); then
            printf '%s\n' "$ids" > "$evidence_dir/owned-containers-before-cleanup.txt"
            while IFS= read -r cid; do
                [[ -n "$cid" ]] || continue
                if [[ ! "$cid" =~ ^[0-9a-f]{64}$ ]]; then rc=1; continue; fi
                if label=$(docker inspect --format "{{index .Config.Labels \"$owner_label\"}}" "$cid") && [[ "$label" == "$run_id" ]]; then
                    docker logs "$cid" > "$evidence_dir/container-$cid.log" 2>&1 || rc=1
                    docker inspect "$cid" > "$evidence_dir/container-$cid.json" 2>&1 || rc=1
                    docker rm --force "$cid" >> "$evidence_dir/container-cleanup.log" 2>&1 || rc=1
                else
                    echo "Cannot establish ownership of container $cid" >> "$evidence_dir/container-cleanup.log"
                    rc=1
                fi
            done <<< "$ids"
        else rc=1; fi
        if ids=$(docker ps -aq --no-trunc --filter "label=$owner_label=$run_id"); then
            printf '%s\n' "$ids" > "$evidence_dir/owned-containers-after-cleanup.txt"
            [[ -z "$ids" ]] || rc=1
        else rc=1; fi
    fi
    if (( pool_attempted )); then
        if sudo -n dmsetup info "$pool" >/dev/null 2>&1; then
            env FICD_DM_VOLUME_GROUP='' "$supplement_dir/tools/thinpool.sh" remove "$pool" >> "$evidence_dir/pool-cleanup.log" 2>&1 || rc=1
        fi
        if maps=$(sudo -n dmsetup ls --noheadings); then
            maps=$(awk -v pool="$pool" '$1 == pool || index($1,pool "-snap-") == 1 {print $1}' <<< "$maps")
            printf '%s\n' "$maps" > "$evidence_dir/owned-mappings-after-cleanup.txt"
            if [[ -n "$maps" ]]; then
                rc=1
            else
                # Also handle a partial pool-create failure before a DM mapping
                # existed. Detach only loops backed by our fresh private paths.
                if loop_rows=$(sudo -n losetup --list --json --output NAME,BACK-FILE); then
                    if loop_rows=$(python3 -I -c 'import json,sys; root=sys.argv[1]; data=json.loads(sys.stdin.read()); owned={root+"/data",root+"/metadata"}; print("\n".join(x["name"] for x in data.get("loopdevices",[]) if x.get("back-file","").removesuffix(" (deleted)") in owned))' "$pool_dir" <<< "$loop_rows"); then
                        while IFS= read -r dev; do
                            [[ -n "$dev" ]] || continue
                            sudo -n losetup --detach "$dev" >> "$evidence_dir/pool-cleanup.log" 2>&1 || rc=1
                        done <<< "$loop_rows"
                    else rc=1; fi
                else rc=1; fi
                if loop_rows=$(sudo -n losetup --list --json --output NAME,BACK-FILE); then
                    printf '%s\n' "$loop_rows" > "$evidence_dir/loops-after-cleanup.json"
                    if python3 -I -c 'import json,sys; root=sys.argv[1]; owned={root+"/data",root+"/metadata"}; assert not [x for x in json.load(open(sys.argv[2])).get("loopdevices",[]) if x.get("back-file","").removesuffix(" (deleted)") in owned],"owned loop remains"' "$pool_dir" "$evidence_dir/loops-after-cleanup.json"; then loops_clear=1; else rc=1; fi
                else rc=1; fi
                if (( loops_clear )); then
                    for dev in "$pool_dir/data" "$pool_dir/metadata"; do
                        if [[ -L "$dev" ]]; then rc=1; else rm -f -- "$dev" || rc=1; fi
                    done
                fi
            fi
        else rc=1; fi
    fi
    printf 'cleanup_exit_code\t%s\n' "$rc" > "$evidence_dir/cleanup-status.tsv" || rc=1
    printf 'cleanup\t%s\n' "$rc" >> "$status_file" || rc=1
    cleanup_done=1
    return "$rc"
}

finish() {
    local original_rc=$? cleanup_rc=0 final_rc
    trap - EXIT INT TERM
    if [[ -n "$active_step" ]]; then
        printf '%s\t%s\n' "$active_step" "$original_rc" >> "$status_file"
    fi
    if (( ! cleanup_done )); then cleanup_owned || cleanup_rc=$?; fi
    final_rc=$original_rc
    if (( final_rc == 0 && cleanup_rc != 0 )); then final_rc=$cleanup_rc; fi
    if (( final_rc == 0 && ! acceptance_checked )); then final_rc=1; fi
    printf '%s\n' "$final_rc" > "$evidence_dir/exit-status"
    if (( final_rc == 0 )); then
        printf 'SUPPLEMENTAL_SNAPSHOT_TRANSPORT_ACCEPTANCE_OK\n' > "$evidence_dir/success-marker"
        cat "$evidence_dir/success-marker"
    else
        printf 'Supplement failed with exit %s; evidence preserved at %s\n' "$final_rc" "$evidence_dir" >&2
    fi
    exit "$final_rc"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cp -- "${BASH_SOURCE[0]}" "$evidence_dir/launcher.sh"
sha256sum "$evidence_dir/launcher.sh" > "$evidence_dir/launcher.sha256"
cd "$supplement_dir"
run_logged source-proof verify_source
cp "$evidence_dir/source-proof.log" "$evidence_dir/production-equality.json"
run_logged docker-info docker info --format '{{.ServerVersion}}'
run_logged image-identity docker image inspect --format '{{.Id}}' "$accepted_image"
run_logged image-proof verify_image
run_logged image-inspect docker image inspect "$accepted_image"
run_logged owner-preflight docker ps -aq --no-trunc --filter "label=$owner_label=$run_id"
test ! -s "$evidence_dir/owner-preflight.log"
container_attempted=1
run_logged image-binaries docker run --rm --pull=never --network=none \
    --name "$container_name-hash" --label "$owner_label=$run_id" \
    --cidfile "$evidence_dir/hash-container-id" --entrypoint=sha256sum "$accepted_image" \
    /usr/local/bin/firecracker-containerd /usr/local/bin/containerd-shim-aws-firecracker \
    /usr/local/bin/firecracker /var/lib/firecracker-containerd/runtime/default-rootfs.img
run_logged binary-proof verify_binaries
# Do not repeat the already-passing local race50. The VM command compiles only
# test source, using the frozen image's installed runtime/guest binaries.
printf 'supplement_revision=%s\naccepted_runtime_revision=%s\naccepted_image=%s\naccepted_image_id=%s\nowner_label=%s=%s\npool=%s\n' \
    "$expected_revision" "$accepted_revision" "$accepted_image" "$accepted_image_id" "$owner_label" "$run_id" "$pool" > "$evidence_dir/inputs"
sha256sum runtime/snapshot_admission_supplement_test.go runtime/native_buffered_stdin_supplement_integ_test.go > "$evidence_dir/test-sources.sha256"
run_logged sudo-preflight sudo -n true
run_logged pool-preflight sudo -n dmsetup ls --noheadings
run_logged pool-proof verify_pool_absent
pool_attempted=1
run_logged pool-create env FICD_DM_VOLUME_GROUP='' "$supplement_dir/tools/thinpool.sh" create "$pool"
run_logged buffered-stdin-vm docker run --pull=never \
    --name "$container_name" --label "$owner_label=$run_id" --cidfile "$evidence_dir/test-container-id" \
    --privileged --ipc=host --network=none \
    --volume /dev:/dev --volume /run/udev/control:/run/udev/control \
    --volume "$evidence_dir/runtime-logs:/var/log/firecracker-containerd-test" \
    --volume "$supplement_dir:/src" --volume gocache:/go \
    --env ENABLE_ISOLATED_TESTS=1 --env FICD_DM_VOLUME_GROUP= --env "FICD_DM_POOL=$pool" \
    --env GOPROXY=direct --env GOSUMDB=off --env GOWORK=off \
    --workdir=/src/runtime --init "$accepted_image" \
    'go test -mod=readonly -v -count=1 -timeout=1200s -run "^TestNativeBufferedStdinSnapshot_Isolated$"'
run_logged vm-guards verify_vm_guards
acceptance_checked=1
run_logged checkout-clean git diff --exit-code
run_logged checkout-status git status --porcelain --untracked-files=all
test ! -s "$evidence_dir/checkout-status.log"
cleanup_owned
# The EXIT trap emits the only terminal success marker after successful cleanup.
