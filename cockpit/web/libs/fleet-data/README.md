# fleet-data

The Cockpit's data layer: schema 2 types and client, the filter matcher and vocabulary, the task
state, the memoised view model, the copy-command templates and the shared fixtures. Pages are
built only from this library's exports; fixtures come from `@cockpit/fleet-data/testing`.
Contract: `spec/features/cockpit-views/README.md` (REQ:field-tables for every field). Pure
TypeScript except `FleetClient` and `FleetStore`, which are Angular services.

## Entry points

The shell and the first paint of Home import only what they render; everything else is behind an
entry point that a page imports lazily (REQ:initial-script-fits-the-budget: the bundler assigns code
to chunks by file, so a lazy name must not live in a file the first page imports).

| Entry | What is in it | Who imports it |
|---|---|---|
| `@cockpit/fleet-data` | the types, `FleetClient`, `FleetStore`, `FleetModel`/`FleetModels` with the Home sections above the fold (`needsYou`, `readyToLand`, `inFlight`, `resume`, `machines`, `tasks`, `taskNamed`, `worktreePullRequests`, `homeBadge`, `homeBadgeLabel`, `runningAgentCount`), the task state, `webAddress`, the link builders and `ListQuery` (`vocabulary.ts`), `parseQuery`, the helpers of `fleet-view.ts` (freshness `formatAge`, labels, `emptyDocument`, the small entity filters), the control types, and the few commands Home and the shell copy (`PLACEHOLDERS`, `pullRequestLand`, `selfUpdate`, ...) | the shell, Home, everything |
| `@cockpit/fleet-data/home-details` | Home below the fold as functions over a model: `buildCleanup`, `buildHealth`, `buildThroughput`, `remoteErrorText`, `remoteFix` | Home after its first paint (`import()`), and the list entry (the cleanup sets) |
| `@cockpit/fleet-data/panel` | the entity panels: `buildWorktreePanel(model, id)`, `buildTaskPanel`, `buildRepositoryPanel`, `buildAgentPanel`, `buildMachinePanel`, `buildPullRequestPanel` and their types | pages that open a panel |
| `@cockpit/fleet-data/task-reason` | `taskReason(model, name)`, the plain-words sentence for a task's state, and `seenElsewhereOnly` | the Tasks panel, Home's rows and the palette |
| `@cockpit/fleet-data/lazy-client` | the reads that only some pages make, as functions of the fetch (`inject(FETCH)`): `readBranches(fetcher, repositoryId)`, `readMachineMetrics(fetcher, machineId, signal?)`, `readReadme(fetcher, repositoryId)` | pages that open a repository, the metrics poller and the README section, with `import()` |
| `@cockpit/fleet-data/commands` | every Copy-command template (and what the main entry has) | pages and the New task form |
| `@cockpit/fleet-data/list` | `buildTaskRows(model)` and the other row builders, `applyListQuery`, the matching half of the matcher (`matchesTerms`, `globMatch`, `Subject`, `MatchEnv`, `StepCounter`), the full filter vocabulary (`VOCABULARY`, `chipOf`, `SEL_KEYS`, `defaultDirection`), `parseListQuery`/`emptyListQuery`/`termLink` and the count-cell links, `buildRepositories(model)` and `mergeRepositories`, the page helpers (`worktreeLabel`, `codeIndexText`, `readmeFailureText`, ...) | list pages and the palette |
| `@cockpit/fleet-data/testing` | fixtures | specs |

The views are free functions over a model, not methods of it (`model.worktreeView(id)` is
`buildWorktreePanel(model, id)`, `model.taskRows` is `buildTaskRows(model)`, `model.repositories` is
`buildRepositories(model)`, `model.cleanup`, `model.health` and `model.throughput` are
`buildCleanup`, `buildHealth` and `buildThroughput`). Each memoises on the model through
`model.memo`, so they still run once per document.

## Reading the daemon

| Export | What it is |
|---|---|
| `FleetClient` | `readFleet(etag?, expected = SCHEMA_VERSION)` (200 or 304; a body of another schema throws `FleetSchemaError` with the message `update wb on this machine` (daemon older) or `reload` (page older)), `readSession()`, the three lazy reads are not methods of the client but functions in `@cockpit/fleet-data/lazy-client`: `readBranches(fetcher, repositoryId)` (lazy route, `{branches, reason?}`), `readMachineMetrics(fetcher, machineId, signal?)` (`{machine, route, fetched_at?, samples, reason?}`; `signal` cancels the request, which the metrics poller does when its last page leaves; the request timeout still applies) and `readReadme(fetcher, id)` |
| `FETCH` | injection token for `fetch`; tests replace it |
| `FleetStore` | polls, keeps the last document, exposes `document`, `model` (the memoised view model of the current document), `schemaMismatch`, `error`, `session`, `now`, `warmingUp`, `progress` |
| `EXPECTED_SCHEMA`, `MODEL_OPTIONS`, `POLL_INTERVALS` | injection tokens (expected schema, view model options such as a clock or derivation counter, poll intervals) |
| types | `FleetDocument`, `Machine`, `Repository`, `Worktree`, `PullRequest`, `Agent`, `Branch`, `Throughput`, `MachineMetrics`, `MetricsSample`, `BranchesResponse`, `Session`, ... and `isRunning(agent)` (a session `live` or a run `running`) |

A poll that returns 304, or a 200 whose body is byte-identical, keeps the same document object, so
nothing derived from it is recomputed. The client is strict per entry: an entry missing a required
field or of the wrong type is dropped (not thrown), `FleetRead.dropped` and
`store.droppedEntries()` count them for a diagnostic line, and unknown enum values are tolerated. An optional field of the wrong type, or an `activity` or `mergeable` outside its set, is removed (it reads as "not reported"; the entry stays), and a malformed `throughput` block is dropped (`cleanOptionalFields`, `cleanThroughput`).
A document missing its document-level facts, or a body that is not JSON, is a `FleetFormatError`.
`Session.machine_routes` (OWNER-ONLY: `[{machine_id, ssh: {host, user?, wb_path?}}]`; anonymous
readers get none) is threaded by the store into the model and the copy commands, but only for the `owner` principal (`ownerRoutes`): a response that carried them for another principal is not believed. A machine entry's `publish_error` (this machine's own failed periodic publish; four codes, a value outside them is removed) and `agents_truncated` (another machine's agents were cut; `agentsTruncated(document, machine)` reads the document's flag for this machine and the entry's for another) are typed per machine. A metrics sample may omit any measurement (the first has no `cpu_percent`): it reads "not reported", the load "unknown", never a zero or a guess.

## The view model

`store.model()` returns a `FleetModel` for the current document. Every getter is computed once per
model; `FleetModels.forDocument(document, now?, routes?)` caches by document identity plus a 60 s
clock bucket (`CLOCK_BUCKET_MS`) plus the session routes, so a poll that changes nothing derives
nothing, while expiries, staleness and ages are re-derived each minute. `model.now` is the store's
clock when the model was made. An optional `onDerive` hook counts derivations. A derivation that
throws is contained: it shows its empty value, is not retried for the model, is reported once
through `onError`, and is listed in `model.failedDerivations`.

| Getter | Result |
|---|---|
| `buildRepositories(model)` (`/list`) | `MergedRepository[]`: one per lower-cased `owner/name` across machines (`id` = entry id of the preferred checkout, `key`, `slug`, `checkouts` with route, age and `stale`, summed counts, worst `codeIndex`, newest activity, `errors`, `webUrl`, which is a checked `webAddress` or absent) |
| `tasks` | `TaskView[]`: worktrees, pull requests, agents, `state` + `stateInfo`, `stateSource` (`local`, or `remote` when no entry of the task is on this machine: the state is then that machine's report, `reportedBy`, and Home offers no action for it), repositories, machines, `lastActivityAt`, `openPullRequests`, `unobservedPullRequests` |
| `needsYou` | `NeedsYou`: `items` (one per task, its worst kind, in the order of the kinds then newest activity), `shown` (at most 5), `more`, `moreLink`, `withoutTask` (one row for blocked agents with no task). A signal, not a debt counter: a task at risk, and "Agent finished", is listed only when its last activity is within `NEEDS_YOU_WINDOW_DAYS` (14; no recorded activity is not recent; `model.isRecent(task)`); an older at-risk task still gets its failed-checks, blocked or PR-needs-you row, and its worktrees are counted by Cleanup's `lookCount`. Row `url`s are checked `webAddress`es. |
| `readyToLand` | `{ready, notReady}`: ready tasks with `stateSource`/`reportedBy` and their pull requests (`machine`, `remote`, and for a `local` task `landCommand`, checks passed/total, oldest `checkedAt`; a `remote` task has no land command), and the muted tasks that wait on checks only |
| `inFlight` | running agents on every machine (`remote`, `controllable`, `activity`, `startedAt`) |
| `resume` | the last 5 tasks by activity |
| `buildCleanup(model)` (`/home-details`) | `{safeCount, lookCount, safeIds, lookIds, reviewLink, bars (age term links), unknownAge}` (indicative); `lookIds` includes the at-risk worktrees of tasks older than the Needs you window |
| `machines` | `MachineView[]`: `live`/`cached`/`stale`, age, `outdated`, running agents, uptime |
| `buildHealth(model)` (`/home-details`) | `FleetHealth`: `ok`, stale machines, older WB, remote errors (each with its fix command, or a reason when there is nothing to run), `publishErrors` (this machine's `publish_error`, with its guidance and the command to copy: `wb remote publish --dry-run`, `wb remote status`, `wb remote publish`, or the reason for the hub that is too old), `exportDropped` ("N entries left out of <machine>'s export"), scan errors |
| `buildThroughput(model)` (`/home-details`) | `ThroughputSeries`: `perDay` (every day of the window, zero-filled: `finished`, `dropped`, `landed`), `totalFinished`, `totalDropped`, `maxPerDay`, `hasLanded` (draw the landed series only then), `slowest` (five, slowest first), `medianSeconds`, `p90Seconds`, `capped`; non-linking; `undefined` without the block |
| `buildTaskRows(model)`, `buildRepositoryRows`, `buildWorktreeRows`, `buildAgentRows`, `buildMachineRows` (`/list`) | `ListRow<T>[]` per page (below) |
| `worktreePullRequests` | `ReadonlyMap<worktreeId, PullRequest[]>`: the ONE worktree-to-pull-request join. A pull request that names a worktree of the document joins that worktree only; one that names none (or a missing one) joins the worktrees with its repository entry and branch. The `pr` chip, the worktree rows, the worktree, task and agent panels all read it; a page must not re-derive it || `repositoryName(id)`, `taskOfPullRequest(pr)`, `tasksOfAgent(agent)`, `taskNamed(name)` (from `taskMap`), `worktreeById`, `agentById`, `pullRequestById`, `machineById` | lookups |
| `homeBadge`, `homeBadgeLabel`, `runningAgentCount` | the Home badge (tasks needing the operator; `homeBadgeLabel` is the number, or `99+` above `BADGE_CAP`, also `badgeLabel(n)`) and the Agents tab badge |
| `buildWorktreePanel(model, id)`, `buildTaskPanel(model, name)`, `buildRepositoryPanel(model, key)`, `buildAgentPanel`, `buildMachinePanel`, `buildPullRequestPanel` (`/panel`) | per-entity panels (below); `undefined` for an unknown id |

Also exported: `machineLoad(metrics, now)` (`free` below 70 % CPU and 80 % memory, `busy` otherwise,
`not-reported` without a sample or with one older than 5 minutes, `stale: true`). Commands that change something (`pullRequestCreate`, `pullRequestLand`, `agentStop`, `sessionSend`) refuse any target with `machine` or `ssh` (`onThisMachine`), `agentTitle`, `parseVersion`, `compareVersions`, `versionKey`,
`remoteErrorText`, `remoteFix` (the command for a `remote_error` code: none, with the reason, for `remote_warming_up`, `export_too_large`, `self_export` and a code this library does not know), `NEEDS_YOU_VISIBLE`, `RESUME_COUNT`. A field the daemon omitted
gives the "not reported" outcome, never a guess.

## Entity panels

Each `build*Panel(model, id)` (`@cockpit/fleet-data/panel`) returns `{summary, related, commands, raw}`: the summary facts, the related entities, the
Copy-command list (`PanelCommand {title, command: CopyCommand}`; already quoted, each with `needsEdit`
and, for another machine without an SSH route, the label "run on <machine>") and the raw entries
exactly as the read model sent them (the `related` pull requests and `summary.url` carry only a checked `webAddress`, else no `url`). A dispatched run has the agent verbs; any other session has no
command and `summary.controllable` is false; a machine has no command. `buildTaskPanel` carries the commands that change something (committing and opening a pull request, and `wb pr land` for each open pull request, titled with the repository and number) only for a task decided on this machine; for a task only another machine reports (`stateSource` `remote`) it withholds them and keeps the reading ones, so a page filters nothing. `taskReason(model, name)` says the task's state in one sentence from the library's own predicates ("At risk: 1 commit only on this machine in specscore-go (worktree idle)", "Checks failed: wb#131 has 1 failing check (build-linux)", "Ready to land: 2 pull requests green and mergeable"; at most three parts and "+n more"), so the panel, Home's rows and the palette cannot disagree with the badge. Types: `WorktreePanel`,
`TaskPanel`, `RepositoryPanel`, `AgentPanel`, `MachinePanel`, `PullRequestPanel`. `model.targetOf(entry)`
gives the `CommandTarget` of any entry (here, through the session's SSH route, or "run on").

## Task state

`taskState(inputs, now)` is the pure decision table of REQ:task-state: `at-risk`, `checks-failed`,
`blocked`, `ready`, `not-ready`, `working`, `landed`, `idle`, `not-reported` (`TASK_STATES` has the
ids, labels and ranks, worst first). Only observed pull requests (with `checked_at`) take part in
row 2. A task is `ready` only when every open pull request is observed and ready; an unobserved one
makes it `not-ready` ("pull request not yet checked"; `TaskView.unobservedPullRequests` and
`ReadyToLandRow.unobservedPullRequests` count them). `landed` needs no open pull request and no worktree
with unpushed work. Trust rule: a task that has any entry of this machine reads only its local pull requests and worktrees for `landed`, needs a local open pull request among the ready ones for `ready` (a remote one that is not ready still blocks), and takes remote entries only to worsen it or add `working` (`hasLocalEntry`, `stateSourceOf`). A failed run blocks for strictly under 24 h from `finished_at`, else `started_at`;
with neither it does not block. `notReadyReasons(pr)` returns only reasons of `NOT_READY_REASONS`
(draft, checks failed, checks pending, review, checks not reported, not mergeable, behind, unstable, merge
state not reported, pull request not yet checked) and is empty exactly when the pull request is ready.
Home's "waiting on checks" list needs a non-empty reason list that is only "checks pending".

## The matcher and lists

`parseQuery(text)` and `matchesTerms(terms, subject, env)`: whitespace-separated terms (AND),
`"quoted values"`, `*`/`?` globs over the whole value (`globMatch`, steps at most about pattern
length times value length, countable with a `StepCounter`), substring otherwise, `-` excludes,
`field:value` restricts to a declared field (an undeclared one is plain text), a QUOTED field value
(`repo:"sneat-co/sneat-go"`) is an exact whole-value match with literal `*` and `?`, quotes protect a
leading `-` and a `field:` prefix, `?` is one code point, case-insensitive,
at most 256 characters and 16 terms (`MAX_QUERY_LENGTH`, `MAX_TERMS`), no regular expressions.

A page applies a query to its rows with
`applyListQuery(page, rows, query, now) -> {rows, total}`; `query` is a `ListQuery`
(`q`, `sort`, `dir`, `machines`, `chips`, `sel`) from `parseListQuery(page, params)` and back with
`listQueryParams(query)`. A chip not listed for a page, and a sort column not listed, are ignored.
Defaults: Tasks, Worktrees and Repositories by activity newest first; Agents running first then
newest; Machines this machine first then by name. `findMergedRepository(rows, entryId)` resolves a
Repositories `sel`.

## Filter vocabulary (REQ:filter-vocabulary)

`VOCABULARY[page]` (`@cockpit/fleet-data/list`) holds, per page, the data below, with `chips` as `{id, label, hint}` so a list renders its chips from the page alone (`chipOf(page, id)` also answers the dynamic `runtime-<name>` chip of agents); `declaredFields(page)` (main entry) the `field:` names. The ids, `state:` values, fields and sorts that link validation needs are `PAGE_RULES` in the main entry, which `VOCABULARY` is built on.

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
`stateLink`, `ageLink`, `machineLink`, `selectionLink` (and `sortLink`, in `/list`), and the detail addresses `taskDetailLink`,
`repositoryDetailLink(host, name, id?)` (a name that is not `owner/name` opens by entry `id`: `/repositories/<id>`), `agentDetailLink`, `machineDetailLink`, `worktreeDetailLink`. An address whose id is a path segment also carries `commands` (router commands with the raw id) and `linkTarget(link)` gives what `[routerLink]` is bound to, so the id is encoded once.
`linkProblems(link)` lists what is wrong with an address (none when valid). Count cells that link (`/list`)
(REQ:every-number-is-a-link) use these helpers, each returning a `LinkResult`
(`{ok: true, link}` or `{ok: false, reason}`): `taskWorktreesLink`, `repositoryWorktreesLink`,
`repositoryAgentsLink`, `repositoryPullRequestsLink`, `machineRepositoriesLink`, `machineWorktreesLink`,
`machineAgentsLink`, `agentsBadgeLink`; `termLink` and `fieldTerm` are the general forms and always
write the value quoted (`field:"value"`), so a link opens exactly the rows it counted. They never throw.
Unlinkable: a name that holds a double quote, or one that would push the filter past 256 characters;
the page then shows the number as plain text with the `reason` as its `title`. Branch counts and the
throughput numbers never link. Machine ids in `machine=` are escaped (`encodeListItem`), so a comma in
an id cannot split the list.

## Copy command (REQ:copy-the-command)

Pure templates (`@cockpit/fleet-data/commands`; the ones Home and the shell copy and `PLACEHOLDERS` are also in the main entry) returning `CopyCommand` (`{ok: true, text, label?, needsEdit}` or `{ok: false, reason}`).
A placeholder (`<<<edit:message>>>`, `<<<edit:model>>>`, `<<<edit:file>>>`, `<<<edit:profile>>>`, `<<<edit:task>>>`,
`<<<edit:brief>>>`, `<<<edit:hub-url>>>`; exported as `PLACEHOLDERS`, which the UI marks) is written bare, never quoted, and
`needsEdit` is true. It is a shell syntax error in every position (`<<<` is a here-string, `>>>` a redirection with no
target; the shorter `<<edit:x>>` parses when another word follows), checked by a test that runs `bash -n`, `zsh -n` and
`dash -n` over every template:
`worktreeList`, `pullRequestCreate`, `worktreeCleanup`, `pullRequestLand`, `worktreeCreate`,
`branchList`, `fleetStatus`, `branchCleanup`, `agentStatus`, `agentLogs`, `agentStop`,
`sessionSend`, `agentDispatch` (`options.brief` is the `--task` text, the task name goes to
`--new-worktree`), `newTaskCommands({task, brief, repositories, base?, model, profile?, target?})` (`{create?, dispatch[]}`: with a brief the dispatches alone, without one `create` alone, because `dispatch --new-worktree` creates the worktree itself), `pickRepositories(names, text, now)`,
`remotePublish`, `selfUpdate`, `daemonStart`, `remoteEnroll`, `cockpitExport`. Every interpolated
value is POSIX single-quoted, flags are `--flag=value`, a value that starts with `-` or has a
control or invisible character (U+061C, U+200B to U+200F, U+2028/2029, bidirectional controls, U+FEFF) is
refused with a reason (a brief may hold line breaks and tabs), a machine without an SSH route is
labelled "run on <machine>", and one with an `SshRoute` (`user` optional) gets
`ssh [<user>@]<host> <wb_path> ...`; use `commandTarget(entry, machineRoutes)` / `sshRouteOf`.
Health lines say where the command runs: "run here" for the ssh form and `wb remote enroll`,
"run on <machine>" otherwise. A unit test
parses every template against `ai/capabilities.json`.

## Fixtures (`@cockpit/fleet-data/testing`)

`fleetDocument`, `machine`, `repository`, `worktree`, `pullRequest`, `agent`, `run`;
`performanceFixture()` (500 repository entries, 600 worktrees, 4,000 branches served per repository,
3 machines, code_index with statistics, metrics in each route; seeded, deterministic; most worktrees are old, so
~300 tasks are at risk and the Home badge is about 10, and a third of the worktrees have an `agent/`, `codex/` or
`fix/` branch, the rest the branch of their task);
`repeatRows` (for the 5,000-row benchmark); `registryAction`, `FakeOperations` and
`fakeControlFetch({registry?, operations?}, next)` (a fake action registry and operations route
through the `FETCH` token). The registry and operation types are in `control.types` and are
provisional until the `cockpit-actions` clients exist.
