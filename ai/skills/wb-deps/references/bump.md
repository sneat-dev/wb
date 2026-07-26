# Propagate release events

Supply every already-published root event in one campaign:

```sh
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
  --match '<owner>/*' --parallel 2 --merge
```

WB opens all eligible PRs before waiting on their checks. It merges passing
providers, observes releases, recalculates ready consumers, and never merges
a checkless, failing, cancelled, conflicted, or timed-out PR.

`--refresh-after` defaults to `5m`. Before starting a downstream build from an
older event, WB checks for a newer semantic version and substitutes it. This
avoids spending CI on a version already superseded during a long provider
wait. Use `--refresh-after=0` only when the exact older event must be preserved.

After interruption or a failed check, fix the existing branch/PR and use the
same inputs with `--resume`. Restarting without resume risks duplicate work and
unnecessary CI. Keep the report directory stable when overriding it.
