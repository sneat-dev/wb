# fleet-data

The Cockpit's data layer: schema 2 types and client, the filter matcher and vocabulary, the task
state, the memoised view model, the copy-command templates and the shared fixtures. Pages are
built only from this library's exports (`@cockpit/fleet-data`); fixtures come from
`@cockpit/fleet-data/testing`. Contract: `spec/features/cockpit-views/README.md` (REQ:field-tables
for every field). Pure TypeScript except `FleetClient` and `FleetStore`, which are Angular
services.

## Reading the daemon

| Export | What it is |
|---|---|
| `FleetClient` | `readFleet(etag?, expected = SCHEMA_VERSION)` (200 or 304; a body of another schema throws `FleetSchemaError` with the message `update wb on this machine` (daemon older) or `reload` (page older)), `readSession()`, `readReadme(id)`, `readBranches(repositoryId)` (lazy route, `{branches, reason?}`), `readMachineMetrics(machineId)` (`{machine, route, fetched_at?, samples, reason?}`) |
| `FETCH` | injection token for `fetch`; tests replace it |
| `FleetStore` | polls, keeps the last document, exposes `document`, `model` (the memoised view model of the current document), `schemaMismatch`, `error`, `session`, `now`, `warmingUp`, `progress` |
| `EXPECTED_SCHEMA`, `MODEL_OPTIONS`, `POLL_INTERVALS` | injection tokens (expected schema, view model options such as a clock or derivation counter, poll intervals) |
| types | `FleetDocument`, `Machine`, `Repository`, `Worktree`, `PullRequest`, `Agent`, `Branch`, `Throughput`, `MachineMetrics`, `MetricsSample`, `BranchesResponse`, `Session`, ... and `isRunning(agent)` (a session `live` or a run `running`) |

A poll that returns 304, or a 200 whose body is byte-identical, keeps the same document object, so
nothing derived from it is recomputed.

## The view model

`store.model()` returns a `FleetModel` for the current document. Every getter is computed once per
document (`FleetModels.forDocument(document)` caches by identity) and an optional `onDerive` hook
counts the derivations.

| Getter | Result |
|---|---|
| `repositories` | `MergedRepository[]`: one per lower-cased `owner/name` across machines (`id` = entry id of the preferred checkout, `key`, `slug`, `checkouts` with route, age and `stale`, summed counts, worst `codeIndex`, newest activity, `errors`, `webUrl`) |
| `tasks` | `TaskView[]`: worktrees, pull requests, agents, `state` + `stateInfo`, repositories, machines, `lastActivityAt`, `openPullRequests`, `unobservedPullRequests` |
| `needsYou` | `NeedsYou`: `items` (one per task, its worst kind, in the order of the kinds then newest activity), `shown` (at most 5), `more`, `moreLink`, `withoutTask` (one row for blocked agents with no task) |
| `readyToLand` | `{ready, notReady}`: ready tasks with their pull requests (`landCommand`, checks passed/total, oldest `checkedAt`), and the muted tasks that wait on checks only |
| `inFlight` | running agents on every machine (`remote`, `controllable`, `activity`, `startedAt`) |
| `resume` | the last 5 tasks by activity |
| `cleanup` | `{safeCount, lookCount, safeIds, lookIds, reviewLink, bars (age term links), unknownAge}` (indicative) |
| `machines` | `MachineView[]`: `live`/`cached`/`stale`, age, `outdated`, running agents, uptime |
| `health` | `FleetHealth`: `ok`, stale machines, older WB, remote errors (each with its fix command), scan errors |
| `throughput` | per-day series and the slowest five (non-linking); `undefined` without the block |
| `taskRows`, `repositoryRows`, `worktreeRows`, `agentRows`, `machineRows` | `ListRow<T>[]` per page (below) |
| `repositoryName(id)`, `taskOfPullRequest(pr)`, `tasksOfAgent(agent)`, `taskNamed(name)` | lookups |

Also exported: `machineLoad(metrics)` (`free` below 70 % CPU and 80 % memory, `busy` otherwise,
`not-reported` without a sample), `agentTitle`, `parseVersion`, `compareVersions`, `versionKey`,
`remoteErrorText`, `remoteFix`, `NEEDS_YOU_VISIBLE`, `RESUME_COUNT`. A field the daemon omitted
gives the "not reported" outcome, never a guess.

## Task state

`taskState(inputs, now)` is the pure decision table of REQ:task-state: `at-risk`, `checks-failed`,
`blocked`, `ready`, `not-ready`, `working`, `landed`, `idle`, `not-reported` (`TASK_STATES` has the
ids, labels and ranks, worst first). Only observed pull requests (with `checked_at`) take part in
rows 2, 4 and 5. `notReadyReasons(pr)` says why a pull request is not ready (draft, checks failed,
checks pending, not mergeable, behind, review).

## The matcher and lists

`parseQuery(text)` and `matchesTerms(terms, subject, env)`: whitespace-separated terms (AND),
`"quoted values"`, `*`/`?` globs over the whole value (`globMatch`, steps at most about pattern
length times value length, countable with a `StepCounter`), substring otherwise, `-` excludes,
`field:value` restricts to a declared field (an undeclared one is plain text), case-insensitive,
at most 256 characters and 16 terms (`MAX_QUERY_LENGTH`, `MAX_TERMS`), no regular expressions.

A page applies a query to its rows with
`applyListQuery(page, rows, query, now) -> {rows, total}`; `query` is a `ListQuery`
(`q`, `sort`, `dir`, `machines`, `chips`, `sel`) from `parseListQuery(page, params)` and back with
`listQueryParams(query)`. A chip not listed for a page, and a sort column not listed, are ignored.
Defaults: Tasks, Worktrees and Repositories by activity newest first; Agents running first then
newest; Machines this machine first then by name. `findMergedRepository(rows, entryId)` resolves a
Repositories `sel`.

## Filter vocabulary (REQ:filter-vocabulary)

`VOCABULARY[page]` holds, per page, the data below; `declaredFields(page)` the `field:` names.

| Page | Bare term searches | Chips | `state:` values | Fields | `sel` | Sort columns |
|---|---|---|---|---|---|---|
| tasks | task, repository | `needs-you` (= the tasks Home lists), `ready`, `working`, `agent`, `pr`, `multirepo`, `idle30` | the task state ids | `task`, `repo`, `machine`, `age` | task name | `task`, `state`, `worktrees`, `activity` |
| repositories | repository | `worktrees`, `agents`, `prs`, `index`, `errors` | `fresh`, `stale`, `diverged`, `pending`, `failed`, `never` | `repo`, `machine`, `age` | repository entry id | `repository`, `activity`, `worktrees`, `branches` |
| worktrees | task, repository, branch | `active`, `orphaned`, `unpushed`, `gone`, `pr`, `idle30`, `safe`, `look` | `active`, `idle`, `orphaned`, `unknown` | `task`, `repo`, `branch`, `machine`, `age` | worktree id | `worktree`, `state`, `machine`, `activity` |
| agents | runtime, model, task, repository | `running`, `blocked`, `runtime-<name>` (`[a-z0-9-]+`) | `working`, `blocked`, `idle`, `done`, `unknown`, `live`, `parked`, `running`, `completed`, `failed`, `timeout`, `abandoned` | `runtime`, `task`, `repo`, `machine` | agent id | `label`, `activity`, `machine`, `started` |
| machines | machine | `stale`, `outdated` | `live`, `cached`, `stale` | `machine` | machine id | `machine`, `state`, `version` |

`state` is matched whole; `age:` terms are exactly `age:<1d`, `age:1-7d`, `age:8-30d`,
`age:31-90d`, `age:>90d` (`AGE_TERMS`); there is no `day:` term. A repository value is its
`owner/name` without the host.

## Links

A link is built only by these functions, which refuse anything outside the vocabulary; each returns
an `AppLink {path, query}` and `hrefOf(link)` the percent-encoded string: `listLink`, `chipLink`,
`stateLink`, `ageLink`, `sortLink`, `machineLink`, `selectionLink`, `termLink` (a `field:value`
count-cell link, the value quoted when it has spaces), and the detail addresses `taskDetailLink`,
`repositoryDetailLink`, `agentDetailLink`, `machineDetailLink`, `worktreeDetailLink`.
`linkProblems(link)` lists what is wrong with an address (none when valid). Count cells that link
(REQ:every-number-is-a-link): the Tasks worktree count (`termLink('worktrees','task',name)`), the
Repositories worktree, agent and pull request counts (`termLink` with `repo`), the Machines counts
(`machineLink`). Branch counts and the throughput numbers do not link.

## Copy command (REQ:copy-the-command)

Pure templates returning `CopyCommand` (`{ok: true, text, label?}` or `{ok: false, reason}`):
`worktreeList`, `pullRequestCreate`, `worktreeCleanup`, `pullRequestLand`, `worktreeCreate`,
`branchList`, `fleetStatus`, `branchCleanup`, `agentStatus`, `agentLogs`, `agentStop`,
`sessionSend`, `agentDispatch`, `newTaskCommands(form)`, `pickRepositories(names, text, now)`,
`remotePublish`, `selfUpdate`, `daemonStart`, `remoteEnroll`, `cockpitExport`. Every interpolated
value is POSIX single-quoted, flags are `--flag=value`, a value that starts with `-` or has a
control character is refused with a reason, a machine without an SSH route is labelled
"run on <machine>", and one with an `SshRoute` gets `ssh <user>@<host> <wb_path> ...`. A unit test
parses every template against `ai/capabilities.json`.

## Fixtures (`@cockpit/fleet-data/testing`)

`fleetDocument`, `machine`, `repository`, `worktree`, `pullRequest`, `agent`, `run`;
`performanceFixture()` (500 repository entries, 600 worktrees, 4,000 branches served per repository,
3 machines, code_index with statistics, metrics in each route; seeded, deterministic);
`repeatRows` (for the 5,000-row benchmark); `registryAction`, `FakeOperations` and
`fakeControlFetch({registry?, operations?}, next)` (a fake action registry and operations route
through the `FETCH` token). The registry and operation types are in `control.types` and are
provisional until the `cockpit-actions` clients exist.
