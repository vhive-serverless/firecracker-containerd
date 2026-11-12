# Step 3 socket path and SIGPIPE maintenance receipt

Validation was performed in a clean committed worker checkout at
`aacebc90f4fe7d87819bb91ad736816a2ee0300f` using Go 1.26.11. The checkout was
clean before and after these commands; all exited 0.

```sh
go test -mod=readonly -run '^TestRunIgnoresSIGPIPEAndStopsOnSIGTERM$' -count=1 -v ./snapshotter/app
go test -mod=readonly -run '^(TestGetVMInfoSocketPaths|TestBuildVMConfiguration)$' -count=1 -v ./runtime
make lint
```

The SIGPIPE subprocess test writes to a closed non-stdio pipe, verifies EPIPE
and SIGPIPE delivery, checks that demux remains available, and confirms SIGTERM
stops it. The socket tests cover relative and absolute API socket paths for both
noop and runc jailers. `make lint` includes DCO/short-subject checks, reported
zero golangci-lint issues, and reported zero issues in `cri/`.

This source carries the jailed socket path fix from
`93d594c306089397810e30c0d0c7ecf6343bd589` and SIGPIPE fix from
`e09cf274139bfff7b276d7be775f898b4c6b6d1c`. Step 3 remains WIP; these focused
checks are not full runtime, CRI, restore, or Kubernetes acceptance.

Worker log SHA-256 values:

| Log | SHA-256 |
| --- | --- |
| SIGPIPE regression | `fa1eadcc6653f6c44229a1855d50297bc1bdea8a08bf41e4ac1bc7a666fee9dc` |
| Socket/config regressions | `bc291892610a4a096bdec8446de5e08e9bd01b2c69348a65f7306a9e565dc499` |
| `make lint` | `ef2cbc27afbf3eab75bf6e8ea25a353ebd023546e402d9e4606e8bed172e0b73` |

Source-file SHA-256 values: `snapshotter/app/service.go`
`08aaf9a489d7d44ff5f92a13a55c63f30efab3b9cca2745227ee04ad415c3f95`,
`snapshotter/app/service_signal_test.go`
`05eec6d0ccc9fe8950667e2ae7cfb9e659a6833be447cee08a554b3e1d5abc09`,
`runtime/service.go`
`658efdd515eb0887ea0971d78de55895e2fc12cd08cebda4cda906c4e352fada`, and
`runtime/service_test.go`
`5b70bb1e9be23c584e628f5c6154c5f621e9b2db16b43ae5d9ef82775aa5e505`.
