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

## Deriving the roots instead of typing them

A coordinated release of a dozen packages under one scope is a dozen chances
to typo a version or omit a provider — and an omitted provider is not an
error, just a consumer that stays stale. `--latest --scope` reads the modules
the selected repositories declare, keeps the ones a scope glob matches, and
asks the registry for each one's published latest version:

```sh
wb deps bump npm --fleet --latest --scope '@sneat/*' --dry-run
```

`--scope` is a `path.Match` glob against a module path or package name, exactly
as in `wb deps drift --scope`: `*` never crosses a `/`, so `@sneat/*` matches
`@sneat/core`, and `github.com/dal-go/*` matches `github.com/dal-go/dalgo` but
not a nested `github.com/dal-go/dalgo/x`. `--latest` requires at least one
scope, and a scope that matches nothing — or whose modules have published
nothing — is refused rather than run as an empty campaign.

The report's **Derived scopes** table lists every matched module, including the
ones with no readable published version, so a scope's coverage is auditable
rather than assumed. `--changed` composes with `--latest` under the engine's own
rule — the newest version observed for a dependency wins — so a provider release
still in flight can be named explicitly alongside a scope sweep. Repositories
removed by `--exclude` seed nothing either.

Publish after reviewing the first-wave preview:

```sh
wb deps bump go --fleet \
  --changed <module-a>@<version-a> \
  --changed <module-b>@<version-b> \
  --match '<owner>/*' --validation=fast --parallel 4 --merge
```

`--validation=full` is the default and runs local lint, test, and build checks.
`--validation=fast` removes that duplicate full local pass while retaining the
repository's managed push hooks. With `--pr`, WB observes passing checks for
the exact PR head and reports each PR as validated/awaiting merge; a later
merger refreshes the target and lands it. With `--merge`, WB additionally
requires the server-enforced freshness fence before merging. The legacy
`--no-verify` flag remains a separate explicit escape hatch and is not the fast
campaign mode.

WB opens independent eligible PRs in the ready wave concurrently up to
`--parallel`, then waits on their checks concurrently. It merges passing
providers, observes releases, recalculates ready consumers, and never merges a
checkless, failing, cancelled, conflicted, stale-head, or timed-out PR.
Dependency layers remain ordered even when PRs inside one layer are parallel.
When `--parallel` is left at its default, read-only pools — per-wave graph
discovery fetches and registry release observations — widen to a floor of 4
workers; an explicit `--parallel` bounds every pool in both directions.

`--fetch-cache` (opt-in) memoizes DISCOVERY fetches within one invocation:
repositories this run never pushed to, opened a PR for, or merged are fetched
once per 15 minutes instead of once per wave, while the wave engine still
re-fetches before every mutation so branch bases stay fresh. A repository the
run ever touched is re-fetched on every later discovery — WB merges
server-side, so its default-branch commits appear with no local push and only
a real fetch can observe them. The cache is process-local; a fresh invocation
(including `--resume`) always fetches. Do NOT use it when anything other than
this run may land on `main` mid-campaign (a teammate, a sibling campaign,
dependabot): memoized reads of untouched repositories can be up to 15 minutes
stale. Expect roughly a minute saved per wave on a ~450-repository fleet at
the default read-only pool of 4.

## Choosing which repositories the campaign touches

Two flags narrow a campaign, and they mean different things:

```sh
# The repository is removed from the campaign entirely: no graph entry, no
# wave, no worktree, no PR. For an archived or irrelevant repository.
wb deps bump npm --fleet --changed @sneat/core@0.31.0 --exclude 'sneat-co/legacy-*'

# The repository IS bumped, verified, pushed, gets a PR and a CI wait — and is
# then left OPEN, even under --merge. For a repository whose merge is a human
# decision, such as a gated deploy repository.
wb deps bump npm --fleet --changed @sneat/core@0.31.0 --merge --hold sneat-co/sneat-go --hold sneat-co/sneat-apps
```

Both accept `path.Match` globs (`*` never crosses a `/`), and an exact
`owner/name` always matches itself. Excluded slugs are listed in the report, so
"needed nothing" is never confused with "never looked at".

A release that needs a human merge cannot be waited for, so a wave containing a
held repository stops the campaign with status `awaiting_hold_release` and
names the pull requests the remaining waves are waiting on. That is a stopping
point, not a failure: merge the held PRs, then `--resume`.

`--refresh-after` defaults to `5m`. Before starting a downstream build from an
older event, WB checks for a newer semantic version and substitutes it. This
avoids spending CI on a version already superseded during a long provider
wait. Use `--refresh-after=0` only when the exact older event must be preserved.

After interruption or a failed check, fix the existing branch/PR and use the
same inputs with `--resume`; `--parallel` and `--validation` may be changed for
the next incomplete wave, while completed waves retain their recorded mode.
A resume that omits `--parallel` restores the original run's value and its
explicit/default authority, so an explicitly serial `--parallel 1` campaign
stays fully serial across resumes.
Restarting without resume risks duplicate work and unnecessary CI. Keep the
report directory stable when overriding it.
