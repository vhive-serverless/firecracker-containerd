# Step 1 socket path and SIGPIPE maintenance receipt

These checks cover focused maintenance after the last full Step 1 acceptance at
`06bd2058ba7938ad31eec9e17ac4d2e4c33a958f`. They do not replace or extend that
full VM/lifecycle acceptance.

## Socket paths

Commit `93d594c306089397810e30c0d0c7ecf6343bd589` fixes jailed VM socket path
reporting. On clean source `89556d1876f308eb892986c1fa1203256773f713`, the
following passed with Go 1.26.11 on the designated worker:

```sh
go test -mod=readonly -run '^(TestGetVMInfoSocketPaths|TestBuildVMConfiguration)$' -count=1 -v ./runtime
make lint
```

Both commands exited 0. This source contains the socket fix; the later SIGPIPE
commit did not change runtime socket handling.

## SIGPIPE handling

Commit `e09cf274139bfff7b276d7be775f898b4c6b6d1c` removes SIGPIPE from the
demux service's stop signals and unregisters its signal channel on return. On
that clean source, the subprocess test passed ten repetitions and lint passed
with Go 1.26.11:

```sh
go test -mod=readonly -run '^TestRunIgnoresSIGPIPEAndStopsOnSIGTERM$' -count=10 -v ./snapshotter/app
make lint
```

Both commands exited 0. The test generates EPIPE by writing to a closed
non-stdio pipe, observes SIGPIPE, verifies the service still answers gRPC, and
then checks that SIGTERM stops it cleanly. On the unfixed source at
`89556d1876f308eb892986c1fa1203256773f713`, the same regression failed because
`Run` exited after SIGPIPE.

The committed regression source SHA-256 is
`05eec6d0ccc9fe8950667e2ae7cfb9e659a6833be447cee08a554b3e1d5abc09`. These
focused checks and lint do not establish a new full runtime or VM acceptance;
the full acceptance remains the one recorded at `06bd205`.
