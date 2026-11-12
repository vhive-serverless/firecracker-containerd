# Upstream Step 1 candidate

This candidate starts from archived original Step 1
`6856c5ce61667c1feb5a058459460440219ca3d1` (tree
`031b37dd8601228d810f95fac1a795bb42315a9c`). It selects official Firecracker
and jailer v1.18.0 release archives and the published vHive SDK
`dd9db322432989b96cff7a38a64703d60b6cabf9`. Existing containerd 2/task-v3,
runc, ownership/recovery and telemetry fixes remain in the Step 1 baseline.

The migration adds the acknowledged lifecycle journal, stable reconnecting task
client with retained Waits, framed FIFO transport, and reusable peer
Prepare/Describe/Resume and cursor validation. It does not add the Step 2
snapshot coordinator, sidecar or captured-path namespace, Step 3 CRI/native APIs,
or Step 4 storage/PMEM extensions. Existing ordinary VM pause/resume methods
remain part of the historical Step 1 API.

The implementation is selectively derived from committed source `f35bbf4`:
`c8ae068`/`8559ebe` for official release/runc builds;
`bf25b05`/`a5178dc` for journals and stable clients;
`1de30c6`/`8341d63`/`ca48704` for framed transport and reusable preparation;
`920a3f0` for the published SDK acknowledgement fix;
`4f43f8d`/`57b93fc` for worker workflows and uncached module verification.
Later source reconnect coverage `b526339` is retained as a generic transport
unit test, without its Step 2 snapshot consumer.

Historical receipts under `evidence/` preserve their original tested revisions.
The SDK dd9db32 receipt reports the SDK suite only. Neither it nor the historical
Step 1 acceptance validates this reconstructed runtime. Fresh committed worker
VM acceptance, jail modes, integration images and privileged filesystem/network
checks remain pending. Local compilation/unit evidence is recorded separately
in the parent workspace candidate report; no VM validation is claimed.
