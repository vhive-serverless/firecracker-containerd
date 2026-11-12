# Reproduce the frozen result artifacts

Evaluation source is `0a268c5589c37832e1c3dc89a7b60ed2701e0b9e`.
Analysis does not modify or run the evaluator, analyzer, guest or runtime.
The worker evaluation checkout remained clean at that source. Both named
observation processes were absent and matrix/analysis logs were complete before
analysis or package installation. The parent also confirmed both observation
and queued CI sessions were terminal.

The observation archive is 3.6 MiB compressed. It retains original trial logs,
phase clocks, guest probes, host/VMM samples, provenance and all seven original
strict-summary files. `images/` is excluded: the two immutable input images have
612368384 combined apparent bytes and remain at the exact worker paths and
SHA256 values in `provenance.json`. No observation file was edited, zero-filled
or trimmed. `SHA256SUMS` validates this result package.

Create a separate Python 3.12 venv, install the exact locked binary packages,
and reproduce into a new directory:

```bash
python3 -m venv /absolute/new-analysis-venv
/absolute/new-analysis-venv/bin/python -m pip install --only-binary=:all: \
  -r requirements-lock.txt
mkdir /absolute/extracted-observations
tar -xzf raw-observations.tar.gz -C /absolute/extracted-observations
/absolute/new-analysis-venv/bin/python make_results.py \
  --summary /absolute/extracted-observations/memory-summary-0a268c5589c3 \
  --raw /absolute/extracted-observations/memory-matrix-0a268c5589c3 \
  --output /absolute/new-result-artifacts
/absolute/new-analysis-venv/bin/python write_report.py \
  --results /absolute/new-result-artifacts
```

`make_results.py` requires a new output directory. It verifies the strict label,
source/provenance, 21 trials, seven arms in each of three blocks, 12 predefined
paired contrasts, 168 raw-file hashes, trial/probe correctness, 42 cgroup
integrals, 21 idle-PSS integrals, 72 paired metric differences and DAX state.
It uses the same valid-bracket rule (maximum 1.5 s gap), with no extrapolation.

Actual worker commands used the isolated root
`/users/nehalem/fc-ctrd-mod/evidence/memory-results-0a268c5589c3`:

```bash
analysis_root=/users/nehalem/fc-ctrd-mod/evidence/memory-results-0a268c5589c3
"$analysis_root/venv/bin/python" "$analysis_root/make_results.py" \
  --summary /users/nehalem/fc-ctrd-mod/evidence/memory-summary-0a268c5589c3 \
  --raw /users/nehalem/fc-ctrd-mod/evidence/memory-matrix-0a268c5589c3 \
  --output "$analysis_root/artifacts-verified"
"$analysis_root/venv/bin/python" "$analysis_root/write_report.py" \
  --results "$analysis_root/artifacts-verified"
```

The worker lacked `ensurepip`; initial `python3 -m venv` reported that condition.
No system packages were changed. Pinned `pip==25.3` was bootstrapped from its
PyPI wheel into that venv, after checking the wheel against PyPI's SHA256, and
`matplotlib==3.10.7 numpy==2.3.4` were installed with `--only-binary=:all:`.
`requirements-lock.txt` records every resolved package; the bootstrap/install
logs are retained. No Go/Rust builds or local compilation occurred. Early
figure drafts were regenerated after visual layout review; underlying data and
statistics did not change.

SVGs use Matplotlib's bundled DejaVu font as vector paths; PNGs are 300 dpi.
Figure 1 has zero-based absolute axes. Figure 2 has explicit signed differences
and a zero reference. Figure 3 labels its logarithmic latency axis and retains
the 605 ms observation. Figure 4 labels its zoomed axes. Every block or VM point
is retained; bands and ranges describe observed variation, not uncertainty
intervals. No hypothesis test, confidence interval or independence assumption
for the two VMs within a trial is introduced.
