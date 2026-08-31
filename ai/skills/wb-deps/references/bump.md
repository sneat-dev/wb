# Propagate release events

Supply every already-published root event in one campaign:

```sh
wb deps bump go --fleet --changed example.com/lib@v1.2.3 --dry-run

# Multiple release roots can be coalesced into one wave campaign:
wb deps bump go --fleet \
  --changed <module-a>@<version-a> \
  --changed <module-b>@<version-b> \
  --match '<owner>/*' --dry-run
```

Combining roots lets WB recalculate one dependency graph, accumulate all
ready updates for a consumer, and build that consumer once per wave instead
of once per upstream dependency.

Publish after reviewing the first-wave preview:

```sh
wb deps bump go --fleet \
  --changed <module-a>@<version-a> \
  --changed <module-b>@<version-b> \
  --match '<owner>/*' --validation=fast --parallel 4 --merge
```

`--validation=full` is the default and runs local lint, test, and build checks.
`--validation=fast` removes that duplicate full local pass while retaining the
repository's managed push hooks; it is accepted only with `--merge`, so WB must
observe passing checks for the exact PR head before merging. The legacy
`--no-verify` flag remains a separate explicit escape hatch and is not the fast
campaign mode.

WB opens independent eligible PRs in the ready wave concurrently up to
`--parallel`, then waits on their checks concurrently. It merges passing
providers, observes releases, recalculates ready consumers, and never merges a
checkless, failing, cancelled, conflicted, stale-head, or timed-out PR.
Dependency layers remain ordered even when PRs inside one layer are parallel.

`--refresh-after` defaults to `5m`. Before starting a downstream build from an
older event, WB checks for a newer semantic version and substitutes it. This
avoids spending CI on a version already superseded during a long provider
wait. Use `--refresh-after=0` only when the exact older event must be preserved.

After interruption or a failed check, fix the existing branch/PR and use the
same inputs with `--resume`; `--parallel` and `--validation` may be changed for
the next incomplete wave, while completed waves retain their recorded mode.
Restarting without resume risks duplicate work and unnecessary CI. Keep the
report directory stable when overriding it.
