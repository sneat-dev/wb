# Answer "can I reuse this package here"

`wb deps peers` judges a published npm package's peer requirements against one
checkout, without installing anything:

```sh
wb deps peers @sneat/core --against ../renewon
wb deps peers @sneat/core@0.31.0 --against ../renewon
wb deps peers @sneat/core --against ../renewon --format json
```

WB reads the published package's own `peerDependencies` and
`peerDependenciesMeta`, then reads what the target checkout actually resolves
for each of them — the version the governing `pnpm-lock.yaml` or
`package-lock.json` installs, not the caret range a manifest declares. A range
cannot be judged against another range; only an installed version answers the
question.

## Reading the table

One row per published peer, each with the resolved version, the lockfile or
manifest field that produced it, and one verdict:

| verdict | meaning |
|---|---|
| `satisfied` | the target's resolved version is admitted by the peer range |
| `unsatisfied` | the target has it, at a version the range rejects |
| `missing` | the target does not have it at all |
| `optional_missing` | the publisher marked it optional; the target omits it |
| `unevaluated` | WB will not guess this specifier shape, and says so |

`unevaluated` is never a pass. WB evaluates the specifier subset a Sneat
manifest actually uses: exact pins, carets, tildes, comparison operators, the
space-separated conjunction every Angular and Ionic peer uses
(`>=22.0.0 <23.0.0`), and `||` unions of those. A hyphen range or a
`workspace:`/`catalog:` protocol is reported with its reason rather than
silently counted as compatible. Treat those rows as unanswered, not as answered
"yes".

A package declaring no peers at all is reported as requiring nothing of its
host — the most reusable answer there is, not an empty screen.

## Exit codes

`0` when nothing blocks reuse, `1` when any required peer is `unsatisfied` or
`missing`. An `optional_missing` or `unevaluated` row does not fail the run.
Nothing is installed and nothing is written, so the command is safe to run
against a checkout someone else is working in.
