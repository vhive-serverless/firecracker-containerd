# Fixed bridge MAC focused development run

- Checkout HEAD: `7588b070418513587558f57ee038000ae9ee6df0`.
- Test source SHA-256: `bb49a19ce44293dc76724b22905433c20c035ed0e02eb03ebeb9a560faca61e2`.
- Image: `localhost/firecracker-containerd-test:7588b0704185`, image ID `sha256:a88290e17fd3b5327bbadadf8249e998d320391511b9c53670eacddb73355987`.
- Command: `go test -v -count=1 -failfast -timeout=600s -run '^TestStargzNativeRestore_Isolated/ext4-block/jailed=true$'`, via `run-isolated-vm-test.sh` with `STARGZ_RESTORE_CAPTURE_NETWORK=1`, private pool `fc-sgz-netdiag-00312`, and private storage `/users/nehalem/fc-ctrd-mod/.ficd-test-vms/step4-stargz-restore-netdiag`.
- Result: PASS in 32.48s. No `SnapshotService.Mounts` refresh was called.
- The test logged and asserted gateway MAC `02:fc:42:ff:ff:01` before checkpoint, after source TAP deletion, and after restored TAP creation. The guest ARP cache showed gateway 198.18.42.1 mapping to the same MAC before and after restore. Post-restore fresh TCP to GHCR/CDN and the bridge host probe succeeded; lower file read succeeded.
- Raw TAP/uplink pcaps and raw `containerd.out` are retained on the worker at `/users/nehalem/fc-ctrd-mod/step4-stargz-restore-dev/evidence/fixed-mac-focused/`. Local packet listings omit TLS payload content; SHA-256 hashes are in `pcaps.sha256`.
- Private test container exited and its pool was removed after evidence capture.
