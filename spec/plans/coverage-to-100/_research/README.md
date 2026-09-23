# Coverage-to-100 research evidence

Evidence behind [`spec/plans/coverage-to-100/README.md`](../README.md), copied
from the coordinator session's scratchpad (`wb-coverage/`, 2026-09-23) that
produced [`REPORT.md`](REPORT.md).

- `REPORT.md` — the full research report; see its own "Evidence and
  reproduction" section for the PR/issue/CI-run citations.
- `pkgs_all.txt`, `files_all.txt`, `categories.txt` — per-package and
  per-file uncovered-statement classification.
- `zero_funcs.txt` — the 95 exported functions at 0% local coverage.
- `zero_cross.txt` — the 17 of those 95 that have a caller outside their own
  package (so none of the 17 is dead code by this evidence).
- `failed_runs.txt` — CI runs that failed on the coverage floor alone.
- `analyze.py`, `classify.py`, `classify2.py`, `errsrc.py` — the scripts that
  produced the classification files above.
- `seams/`, `noassert/` — small standalone Go research scripts, kept as
  `main.go.txt` reference text (not `.go`, so this module's build/vet/
  coverage tooling never sees them) used to probe replaceable-seam counts
  and assertion-free test rates.

Not copied here (large raw working files from the research session, not
needed to reproduce the findings above): `local.cov`, `local.json`,
`nightly/`, `src295/`, `ss/`, `hist/`.

## Open Questions

None at this time.
