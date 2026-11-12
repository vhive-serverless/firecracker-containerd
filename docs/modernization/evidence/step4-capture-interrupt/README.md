# Native capture interruption development evidence

This directory preserves development runs for
`runtime/native_checkpoint_interrupt_integ_test.go`. The clean committed run in
[clean-5186d40](clean-5186d40/verification.json) is the acceptance record: commit
`5186d40578d58b23c432e6c5af15789654d548c1` passed lint and both real-VM jailer
modes from a clean checkout. Attempt 5 below is the preceding
dirty-development validation. It passed both
`jailed=false` and `jailed=true`; the test observes a nonempty staged memory
image, closes a dedicated control ttrpc connection, checks task recovery and
staging cleanup, then captures successfully again.

The stage marker is phase-based: `CheckpointVM` pauses task processes and the
VMM, then freezes/saves exported volumes before `nativeVMFiles` copies the
snapshot memory file into `.fc-checkpoint-*/memory`. The test disconnects only
after that nonempty file exists. The follow-up `exec` verifies the source VM is
usable after thaw.

Attempts 1–3 are preserved as test-design failures, not runtime regressions:

- Attempts 1–2 used Firecracker REST state polling. Capture completed before
  the observer saw the transient paused state.
- Attempt 3 retained that observer and passed unjailed, but the jailer API
  socket was not reachable at the host path. It established that this observer
  was unsuitable for the two-mode test.

Attempt 4 passed both modes with the stage marker, before final cleanup-bound
and comment refinements. Attempt 5 reran the final file and passed both modes.
Each attempt directory retains its raw driver output; attempts 4–5 also retain
container logs, lint output, provenance, and SHA-256 manifests. The clean
committed run has its own machine-readable result and raw worker logs under
`clean-5186d40/`.

Attempt 5 used source base `907fc268ea85f60dfcdae2b1471b5ffb28811003`, with
only the new untracked test file in the worker checkout. It used the retained
runtime test image built from `72f4fc9173bef33cc1c981ad285758716f635e02`
(`sha256:0ed23833cf6043e6f2e0fa6ef3e590cfd9626c03e69d66303f9a693b079021b5`),
Go 1.26.11, and pinned ttrpc v1.2.8. The command was:

```sh
PATH=/users/nehalem/fc-ctrd-mod/toolchains/go1.26.11/bin:$PATH \
STACK_DIR=/users/nehalem/fc-ctrd-mod \
DOCKER_IMAGE_TAG=72f4fc9173be \
FIRECRACKER_CONTAINERD_TEST_IMAGE=localhost/firecracker-containerd-test \
FICD_DM_POOL=fc-step4-capture-interrupt-dev \
FICD_VM_STORAGE=/users/nehalem/fc-ctrd-mod/.ficd-test-vms/step4-capture-interrupt-dev \
EXTRAGOARGS='-v -count=1 -timeout=1200s' \
make -C runtime integ-test-TestNativeCheckpointInterrupt_Isolated
```

The dirty-development run used the same retained image. Its private pool and VM
storage directory were removed; an unrelated control-plane container remained
untouched. The clean committed validation is recorded separately and supersedes
this run as acceptance evidence.
