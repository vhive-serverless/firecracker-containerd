# Derived data dictionary

All values come from the frozen strict committed summary and original samples.
One trial is a two-VM pair; block is the replication unit. Three blocks are
observed per arm. A missing field is not treated as zero.

- `trials_compact.csv`: one row per trial. `idle_*` uses the 20-second common
  idle window. `full_*` uses the full setup-to-completion window. Memory is
  MiB (2^20 bytes), memory-time GiB·s (2^30 byte-seconds), CPU seconds, latency
  milliseconds per VM (two recorded RPC times averaged), and faults raw counts.
  `physical_*_cumulative_MiB` is the cumulative physical-device counter at the
  window end, since fresh cgroup creation; it is not a full-window delta.
  `idle_end_cgroup_*` is an endpoint, not the window mean. `shmem` is a subset
  of file accounting and must not be added to file/anon/kernel as a new category.
  Image and thin-pool `allocated` fields are per-file st_blocks, not unique
  physical extents or memory charges; they must not be added to cgroup memory.
- `arm_statistics.csv`: mean, minimum, maximum and sample SD across blocks.
  SD is descriptive; it is not a confidence interval or standard error.
- `paired_block_differences.csv`: target minus baseline within the same block
  for the analyzer's four predefined contrasts. Units follow the named metric.
  Pmem comparisons hold ext4 format and reclamation state fixed.
- `guests.csv`: one row per VM (42 rows), including both blocks and VM IDs.
  Instrumented retouch/touch timings are milliseconds. Guest memory endpoints
  are MiB; probe faults are counts. The nominal pmem metadata column is an
  assumption, not measured attribution. Reporting counts/traffic may precede
  final flush consumption and are explicitly flagged. They do not measure
  unique freed bytes, prove inactivity when zero, or certify final failures.
- `idle_curves.csv`: original observations linearly interpolated on a 0.25 s
  display grid within valid local brackets, seconds 5–25 after the later idle
  start. The original sampler interval is 0.5 s; the display grid adds no new
  independent observations. Cgroup and mapped-PSS domains stay separate.
- `integral_verification.csv`: independent recomputation of the 42 cgroup
  full/idle byte-second integrals compared with the strict summary. The script
  separately asserts equality for all 21 idle VMM-PSS integrals.
- `missing_metrics.csv`: explicit unavailable full-trial PSS and physical-I/O
  delta reasons. No boot-interval PSS extrapolation is performed.
- `verification.json`: structural, provenance, numeric and correctness checks.
- `raw-observations.tar.gz`: original observations and strict summary, lossless
  except that large image binaries are deliberately excluded and referenced
  by exact path, apparent/allocation size and SHA256 in `provenance.json`.

Interpretation is scoped to one worker, this fixed workload and these three
blocks. Host-wide meminfo includes unrelated activity. Cgroup charging does not
capture all global shared backing, and PSS is not a complete physical-memory
census. These views and disk/device layers are never summed into a total.
