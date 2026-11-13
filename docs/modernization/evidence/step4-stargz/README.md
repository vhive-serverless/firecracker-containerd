# Guest stargz storage validation

The [clean final receipt](clean-06b55e3/verification.json) records full acceptance
at `06b55e3b78cc6bedfe3f0671fcdc49df73046eb2`: build/install/test exit zero,
47 runtime, seven snapshotter and 79 SDK top-level passes, with three named
skips. Both new stargz matrices passed. Cleanup removed private pools, VM files
and test containers while preserving the unrelated control plane. The full
[test output](clean-06b55e3/test.log) and exact inputs are retained.
`checksums.json` records all included evidence files.

The sections below preserve component and diagnostic history; their failed
runs are not the final acceptance result.

## Storage component

The clean committed worker checkout at
`7588b070418513587558f57ee038000ae9ee6df0` passed
`TestStargzGuestDiskStorage_Isolated`: disk, reclamation, pmem and combined
profiles, each with an unjailed VM and a UID 300001 jailed VM. The
[storage receipt](storage-component-7588b07/storage-test.log) records raw ext4
store/upper/work backing, private allocation, copy-up/rename/delete, exhaustion
and recovery, writeback and cleanup. The test uses owned static TAP devices;
this does not establish nonroot-jailer compatibility with the default CNI fixture.

The overall focused runner subsequently failed during source task creation in
the native restore test. CRI annotations incorrectly made its guest-only bind
sources subject to host-volume preparation. `4584206` removes those annotations
for this direct runtime fixture. The storage pass alone is not restore acceptance.

## Shared demux correction

VM shutdown exposed an inherited lifecycle defect: the host demux subscribed
to SIGPIPE as a shutdown signal. A broken guest connection could therefore stop
the entire snapshotter service. `e09cf27` removes that subscription and adds a
subprocess regression that writes to a closed pipe, observes EPIPE/SIGPIPE,
checks that gRPC remains available, then verifies normal SIGTERM shutdown.

The [negative control](step1-sigpipe/baseline-repro.txt) failed on the previous
source. The committed Step 1 correction passed ten repetitions and lint.
Forward merges at Step 2 `7c8d2ce` and Step 3 `aacebc9` passed the regression,
socket/configuration tests and lint from clean worker checkouts. These are
bounded checks, not new full vHive or CRI acceptance. Step 4 includes the fix
through `7588b07`, where the complete storage matrix passed without losing demux.

All runs used the designated worker `nehalem@amd218.utah.cloudlab.us` and Go
1.26.11. Kubernetes Pod checkpoint/restore remains deferred to the 1.38
interface work; observed CRI conformance failures remain separately WIP.

## Restore follow-up

The corrected development fixture passed disk with both jailers and ext4-block
without the jailer, but ext4-block with the jailer failed while reading
`/etc/system-release` after restore. The guest stargz client exhausted six
registry header-timeout attempts and returned an I/O error. Fresh host and
Docker-container probes reached the same blob. This initially left the cause unresolved. Subsequent packet captures and the
bridge-MAC experiment below identified a changing gateway identity in the
fixture; the failed run remains part of the evidence.

A separate network-only container on the worker's actual host kernel
`6.8.0-147-generic` reproduced the fixture hazard: an automatically addressed
bridge changed from the source TAP's MAC to the replacement TAP's MAC. Setting
an explicit bridge MAC before attaching either TAP preserved it across the same
operations. See [raw output](bridge-mac-reproduction.log). The test used a
private network namespace, two TAPs with deterministic distinct MACs, and no VMs.
This establishes the fixture behavior; restore acceptance still requires the
corrected VM matrix.

The fixed-MAC focused restore run passed in 32.48 seconds without a snapshotter
refresh or connection-reset workaround. Its guest ARP entry and packet capture
matched the fixed gateway MAC, fresh connections worked, and the lower-file
read succeeded without registry timeout logs. `06b55e3` retains the fixed MAC,
assertions across TAP replacement, ARP logging and the original read checks;
temporary packet capture and refresh experiments are removed. The final clean full-suite run passed both matrices without these temporary
diagnostics.
