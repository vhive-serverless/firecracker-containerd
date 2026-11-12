# Post-restore network diagnostic runs

These were development-only runs using opt-in packet capture. Raw pcaps and raw `containerd.out` remain on the worker; local packet listings contain Ethernet/IP/TCP headers and timestamps, not TLS payloads.

- `focused.log` / source hash `0071462c29e66a5237b8355f5d2d72a302d06762e2eb3777687c571af2f7688b`: unpinned bridge, no `SnapshotService.Mounts`, ext4-block jailed passed in 110.53s. The guest initially had stale established GHCR/CDN sockets, then reconnected and read the lower file. See `tap-packets.txt`, `uplink-packets.txt`, and raw pcap hashes in `pcaps.sha256`.
- `with-mounts/focused.log` / source hash `c9254cdb35520a956172f4710f163d91e5fe829c7bae5c117d297e2a0fc7dbf5`: `SnapshotService.Mounts` returned overlay mounts in 0.521s; subsequent guest public TCP and guest-to-host bridge checks failed. The TAP capture showed repeated guest SYNs to the host listener without replies. The bridge MAC was not recorded in that run. A later worker reproduction showed that Linux auto-selects a different bridge MAC when replacing a TAP, so this run does not establish that `Mounts` caused the failure.
- The fixed-MAC focused result is in `../fixed-mac-focused/`; it passed without `Mounts` in 32.48s and recorded a stable gateway MAC/ARP mapping.
