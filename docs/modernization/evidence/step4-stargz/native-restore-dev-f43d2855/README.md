# Stargz native restore development run

This is a dirty development validation of the restore test after removing CRI annotations that incorrectly triggered CRI volume export for guest-only bind sources. It is not clean committed acceptance.

- Checkout: `/users/nehalem/fc-ctrd-mod/step4-stargz-restore-dev`, HEAD `7588b070418513587558f57ee038000ae9ee6df0`.
- Test file SHA-256: `f43d28553f2752fa5a0dcd8c24ca4effbc635e8db4e8b3617e6639ea253914b4`.
- Runtime test image: `localhost/firecracker-containerd-test:7588b0704185`, image ID `sha256:a88290e17fd3b5327bbadadf8249e998d320391511b9c53670eacddb73355987`.
- Command: `FICD_DM_POOL=fc-stargz-restore-7588b0704185 make -C snapshotter integ-test-TestStargzNativeRestore_Isolated EXTRAGOARGS='-v -count=1 -failfast -timeout=1200s'` with `STATIC_AGENT=on`, `STATIC_DOCKER_CREDENTIAL_MMDS=on`, private VM storage `/users/nehalem/fc-ctrd-mod/.ficd-test-vms/step4-stargz-restore-dev`, and `DOCKER_IMAGE_TAG=7588b0704185`.
- Test duration: 584.49s. Go test/make exit: 2. Disk profile passed both jailer modes (127.30s total); ext4-block unjailed passed (67.39s); ext4-block jailed failed after 389.71s. Failure: restored `cat /etc/system-release` returned `Input/output error` (test assertion at then-current line 365).
- `containerd.out` on the worker shows native restore handler and `/snapshot/load` at 06:19:12–13 UTC; GHCR blob `sha256:421252…` requests for `sgzR-1-1` then timed out through 06:25 and failed after six attempts with `fetchedPercent:0`. Redacted event excerpt is `timeout-excerpt.redacted.log`.
- Host and Docker-container blob HEAD/Range probes were healthy (200/206); guest route, resolver, DNS lookup, forwarding counters, MTUs, and PMTU are recorded in `network-probes.log`.
- The focused unjailed disk case passed previously but emitted GHCR timeout retries. No skip or reset workaround was used.
- Cleanup: test container exited and is absent; only pool `fc-stargz-restore-7588b0704185` was removed; private VM storage directory is empty. Raw `containerd.out` remains on the worker at `/users/nehalem/fc-ctrd-mod/step4-stargz-restore-dev/snapshotter/logs/containerd.out` and is not copied here to avoid archiving signed blob URLs.
