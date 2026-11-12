# Stargz native restore development evidence

These are dirty development runs on checkout `/users/nehalem/fc-ctrd-mod/step4-stargz-restore-dev` at base HEAD `7588b070418513587558f57ee038000ae9ee6df0`; they are not clean committed acceptance. Test image was `localhost/firecracker-containerd-test:7588b0704185`, image ID `sha256:a88290e17fd3b5327bbadadf8249e998d320391511b9c53670eacddb73355987`.

## Initial partial matrix

`runner.log` preserves the run of `TestStargzNativeRestore_Isolated` with `-failfast -timeout=1200s`. It ran 584.49s and exited 2: disk passed in both jailer modes (127.30s); ext4-block unjailed passed (67.39s); ext4-block jailed failed after 389.71s when the restored `/etc/system-release` read returned `Input/output error`. The test source then had SHA-256 `f43d28553f2752fa5a0dcd8c24ca4effbc635e8db4e8b3617e6639ea253914b4`.

For the failing VM, the native restore handler and `/snapshot/load` completed at 06:19:12–13 UTC. Requests for GHCR blob `sha256:421252…` then timed out repeatedly and ultimately failed after six attempts with `fetchedPercent:0`. The worker copy of the raw first-failure logs is `evidence/pre-diag-matrix-f43d2855/containerd.out.raw.log`; the sanitized excerpt is `timeout-excerpt.redacted.log`.

## Network diagnosis

Host and Docker-container authenticated HEAD and Range probes to that exact blob returned 200 and 206. Guest logs showed default route via 198.18.42.1, resolver 128.110.156.4 and successful `ghcr.io` lookup. Host/container interface and PMTU probes showed 1500 bytes (`network-probes.log`); subsequent diagnostic runs also logged the restored guest interface MTU.

An unpinned-bridge focused run passed in 110.5s but recorded stale restored TCP sockets and a delayed reconnect. A separate opt-in `SnapshotService.Mounts` probe failed the guest-to-host bridge check; its pcap showed unanswered guest SYNs. This was not treated as evidence of a `Mounts` regression: the two runs had different automatically selected bridge gateway MACs, and a separate worker reproduction confirmed Linux bridge MAC changes when the source TAP is replaced.

The fixed-MAC focused run then passed the same ext4-block jailed profile in 32.48s, without calling `SnapshotService.Mounts`. It set gateway MAC `02:fc:42:ff:ff:01` before adding TAPs and verified the same address after source-TAP deletion and restored-TAP creation. The guest ARP cache mapped 198.18.42.1 to that MAC before and after restore. Fresh GHCR/CDN and guest-to-host TCP connections and the lower-file read succeeded. Packet summaries and test output are in `fixed-mac-focused/`; raw pcaps and raw worker logs remain at `/users/nehalem/fc-ctrd-mod/step4-stargz-restore-dev/evidence/fixed-mac-focused/`.

The final trimmed test source has SHA-256 `a74b348fc4378a5c5010d9221dd16a484c2ac380ab5d81967985bfeb25b1a4e7`; it was committed as `06b55e3b78cc6bedfe3f0671fcdc49df73046eb2`. It retains fixed bridge MAC assertions, pre/post guest ARP logging, and the guest-to-host TCP probe before the lower-file read; it removes the temporary Mounts and packet-capture diagnostics. The final file passed pinned Go compile-only and `make lint` (both modules, zero lint issues). The subsequent [clean committed acceptance](../clean-06b55e3/verification.json) passed the full suite.

## Cleanup

Both development pools (`fc-stargz-restore-7588b0704185` and `fc-sgz-netdiag-00312`) were removed. No test container remains, and private VM storage directories are empty.
