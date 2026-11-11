# Guest memory probe

`fc-memory-probe` observes an existing file and a private anonymous mapping. It
never requests reclaim, drops caches, writes the selected file, or enables DAMON.
It uses the parent Go module and builds with `CGO_ENABLED=0`. `files_ephemeral`
installs the same binary into the standard, debug, stargz and DAX image inputs;
SquashFS remains the default guest image. Image-level verification is separate.

```sh
make memory-probe
./tools/memory-probe/fc-memory-probe -file /usr/local/bin/agent -stat-only
./tools/memory-probe/fc-memory-probe -file /usr/local/bin/agent \
    -anon-mib 32 -idle 30s
```

Stat-only mode emits one JSON object from `statx` on the opened file descriptor.
`statx_dax_supported` reports whether `STATX_ATTR_DAX` is present in
`stx_attributes_mask`; `statx_dax` reports its value in `stx_attributes`.
An unsupported attribute cannot establish whether a file uses DAX. The output
also includes the returned statx mask, raw attribute bits, mode, size, inode,
owner, block count/size, filesystem device, mount ID and modification timestamp.
The returned mask identifies which requested statx fields the kernel supplied.

Normal mode requires a nonempty regular file. It maps a read-only private view,
SHA-256 hashes every selected byte, fills a writable anonymous mapping with a
deterministic canary, idles, hashes the file again and checks every canary byte.
Success requires both hashes to match and the entire canary to remain intact.
Keep the selected file immutable during a run: external truncation of a mapped
file can cause SIGBUS. The probe does not freeze writers or lock pages in RAM.

The default file selection is the whole file. `-file-bytes N` selects a prefix.
The selected file length is at most 256 MiB, anonymous allocation is 1–256 MiB
(`-anon-mib`, default 32), and `-idle` is 0–1 hour (default 5 seconds). Invalid
arguments, zero-length mappings, non-regular files, oversized selections and
verification failures produce a JSON error record and a nonzero process exit.
Stat-only mode does not allocate either mapping and can inspect an empty file.

Normal output is JSONL with phases `stat`, `file_touch`, `anon_fill`, `idle_start`,
`idle_end`, `file_retouch`, `anon_verify` and `complete`. Every phase includes:

- UTC timestamp and monotonic elapsed/phase nanoseconds. Phase intervals include
  the previous sample's instrumentation overhead; they are not isolated syscall
  benchmark durations.
- Process rusage totals and interval deltas: user/system CPU microseconds and
  minor/major page faults.
- `/proc/meminfo` values with their original units, and readable parameters from
  `damon_reclaim` and `damon_lru_sort`, including available reclamation counters.
  Missing modules produce an empty map; sampling errors appear in `sample_errors`.

These are process and guest observations. They do not measure host RSS, KVM
backing allocation, actual reclaim causality or guest DAX by themselves.
Run `-stat-only` against the actual file in each guest to establish DAX support
and value before comparing control arms. Retain the probe's binary SHA-256 with
each run so all arms can be checked for identical bytes.
