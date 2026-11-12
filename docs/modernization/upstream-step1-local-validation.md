# Local validation of the migrated Step 1 candidate

The implementation in `6f4e0c5c94322b5f3bf2cf521cdc448480c281f7` passed local
checks with Go 1.26.11:

- All-package non-root short unit suite: `DISABLE_ROOT_TESTS=1 go test -short -count=1 -timeout=180s ./...`.
- Foundation race checks: `DISABLE_ROOT_TESTS=1 go test -race -count=1 -timeout=180s ./eventbridge ./internal/vm ./runtime`.
- Configured golangci-lint: zero issues.
- Static agent, runtime, custom containerd and volume-init binary builds with `CGO_ENABLED=0`.
- IOProxy regeneration with pinned protoc 3.21.12, protoc-gen-go 1.36.10 and ttrpc 1.2.8.
- SDK dd9db32 module origin and schema equality to the official Firecracker 1.18.0 Git object.

The parent workspace records exact commands, logs and hashes in
`migration-upstream/stage1-candidate-report.json`. These are local checks;
privileged integration and exact-candidate VM acceptance remain pending. The
candidate submodules are uninitialized, and full Make/Docker/kernel/rootfs
builds were not run. Final reconstructed logical commits must carry the
project-required DCO signoff; these disposable implementation commits are
source inputs for that reconstruction.
