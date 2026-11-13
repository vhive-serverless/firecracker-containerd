# Step 2 socket path and SIGPIPE maintenance receipt

Validation was performed in a clean committed worker checkout at
`7c8d2ce1fd562323b447f08a64b0d0f5e557247c` using Go 1.26.11. The checkout was
clean before and after these commands; all exited 0.

```sh
go test -mod=readonly -run '^TestRunIgnoresSIGPIPEAndStopsOnSIGTERM$' -count=1 -v ./snapshotter/app
go test -mod=readonly -run '^(TestGetVMInfoSocketPaths|TestBuildVMConfiguration)$' -count=1 -v ./runtime
make lint
```

The SIGPIPE subprocess test writes to a closed non-stdio pipe, verifies EPIPE
and SIGPIPE delivery, checks that demux remains available, and confirms SIGTERM
stops it. The socket tests cover relative and absolute API socket paths for both
noop and runc jailers. Lint includes DCO/short-subject checks and reported zero
golangci-lint issues.

This source carries the jailed socket path fix from
`93d594c306089397810e30c0d0c7ecf6343bd589` and SIGPIPE fix from
`e09cf274139bfff7b276d7be775f898b4c6b6d1c`. The full Step 2 runtime and vHive
acceptance remains tied to `4e0033b81a17d9a32c2861263c2792ebcfe37ebd`; these
focused checks are not a rerun of that acceptance.

Worker log SHA-256 values:

| Log | SHA-256 |
| --- | --- |
| SIGPIPE regression | `a7110962374fc42795cf180c87be2caf3da230aa5f37e1593491a63d78c779e7` |
| Socket/config regressions | `bc291892610a4a096bdec8446de5e08e9bd01b2c69348a65f7306a9e565dc499` |
| `make lint` | `0cde665f34f7ac840fb439690877b513c7048e71ad86fbbad58e8e6f3ec3e95f` |

Test-source SHA-256 values: `snapshotter/app/service.go`
`08aaf9a489d7d44ff5f92a13a55c63f30efab3b9cca2745227ee04ad415c3f95`,
`snapshotter/app/service_signal_test.go`
`05eec6d0ccc9fe8950667e2ae7cfb9e659a6833be447cee08a554b3e1d5abc09`,
`runtime/service.go`
`cc107ebe5cd297f108872785c0e8f68a055b5c99eab3087a3dfaf3cc480b3e8e`, and
`runtime/service_test.go`
`5b70bb1e9be23c584e628f5c6154c5f621e9b2db16b43ae5d9ef82775aa5e505`.
