---
format: https://specscore.md/feature-specification
status: Approved
---
# Feature: Cockpit views

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit-views?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit-views?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit-views?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit-views?op=request-change) |
**Status:** Approved
**Source Ideas:** wb-cockpit
**Approval:** Approved by delegation: the founder said "work autonomously" on 2026-10-01 and asked for the Cockpit to be reviewed and redesigned for a user who juggles hundreds of repositories, worktrees, agents, machines and tasks.

## Summary

Cockpit views redesigns the [Cockpit](../cockpit/README.md) application for an
operator who juggles hundreds of repositories and worktrees across several
machines, tasks and agents. It leads with what is running and what needs
attention, makes every list searchable, sortable and fast, adds Tasks pages,
agent and machine detail pages and live machine metrics, and changes the fleet
read model (schema version 2) so the page is small enough to load in an
instant.

## Problem

Measured on the founder's Mac (wb 0.174.0, 2026-10-01): 438 repository rows,
529 worktrees (455 distinct tasks), 3,834 branches, 16 pull requests, 1 agent,
3 machines. The first Cockpit slice cannot be used at that size.

- **Speed.** `GET /api/v1/cockpit/fleet` is 1.57 MB sent uncompressed, 1.24 MB
  of it the `branches` collection that only the repository page uses. Static
  assets carry no `Content-Encoding`. Every row of every list is a DOM
  element, rows wrap to two lines, and the initial script is 472 kB with every
  page in it.
- **Finding things.** Filters are two dropdowns, one with 438 options. There
  is no text search, no wildcard, no sort and no global search. The host
  prefix repeats on every repository name. The Repositories page has one row
  per machine and repository, so a repository shows up to three times.
- **Knowing what needs me.** The Dashboard is five counters and two plain
  tables. It does not say that 21 worktrees are orphaned, that most are idle
  for 25 to 75 days, or that two of three machines are stale by 25 days or
  more. Columns that are empty for every row are still shown. An agent is an
  identifier, not a description of what it is doing. There is no task view
  although one task spans several repositories and worktrees, and machines
  have no page and no load information. The page heading repeats the selected
  tab, and the "Code" text link looks like a column of the same word.

## Journey

1. **Start.** I run `wb cockpit`. **Observable good result:** the Dashboard is
   on screen already signed in, and its first row says which agents are
   running and on which machines, and its second row lists what needs me, each
   item with a count, in under one second on a fleet of 500 repositories.
2. **Middle.** I press `/`, type `sneat-* -archive task:fix` and read the
   result count. I click the "37" beside an attention item, sort the table by
   last activity, and open a task that spans three repositories.
   **Observable good result:** every filter, sort and quick filter is in the
   address, so the back button and a pasted link return exactly that view; the
   list never holds more than 60 row elements however long it is.
3. **End.** I open a machine and see its CPU, memory and disk over the last
   hour, and an agent and see its task, worktrees and pull requests.
   **Observable good result:** a machine that reports no metrics says so
   plainly instead of showing zeros, and any stale or cached data is marked
   with its age.

## Behavior

Principles that every requirement below serves: attention first; one keystroke
to anything; dense but calm; every number is a link to the list that produced
it; every filter and sort lives in the address; never lie about freshness;
fast by budget, not by hope.

### Shell

#### REQ: top-bar

The application MUST show a top bar with the brand; the tabs Dashboard, Tasks,
Repositories, Worktrees, Agents and Machines, each with its count as a small
badge; a global search box; a snapshot freshness chip; and a session chip
(`anonymous` or `owner`). The Agents badge shows the running agent count and is
highlighted when that count is above zero. The freshness chip reads
"updated N s ago" with the age of the snapshot, turns amber when the snapshot
is older than two refresh intervals, and while the daemon is warming up shows
how many repositories have been scanned.

#### REQ: no-visible-page-heading

The application MUST NOT show a visible heading that repeats the selected tab.
The document `<title>` names the page. The active tab carries
`aria-current="page"` and every page has a visually hidden `h1`.

#### REQ: global-search

Pressing `/` or Cmd/Ctrl+K MUST open one search input over repositories,
tasks, worktrees, the branches of loaded repositories, agents and machines.
Results are grouped by kind with at most 8 per kind, can be moved through with
the arrow keys, and Enter opens the highlighted result. The search uses the
matcher of REQ:list-filter-and-matcher.

#### REQ: keyboard-shortcuts

The application MUST provide `g d`, `g t`, `g r`, `g w`, `g a` and `g m` to
switch to Dashboard, Tasks, Repositories, Worktrees, Agents and Machines;
`/` focuses the page filter when a list is shown and opens the global search
otherwise; `Esc` clears the focused filter or closes the search; `?` shows the
shortcut sheet. A shortcut MUST NOT fire while the focus is in an input,
textarea or editable element.

### Lists and matcher

#### REQ: list-filter-and-matcher

Every list page has a filter box, which replaces the repository dropdown.
Machine remains a compact multi-select chip group. The filter and the global
search use one pure matcher with this grammar:

- the input is split on whitespace into terms; a row matches only when all
  terms match (AND);
- a term containing `*` or `?` is a glob matched against the whole field
  value, so `sneat-*/*-go` matches `sneat-co/bots-go`; `*` matches any run of
  characters and `?` exactly one;
- a term without wildcards is a substring match;
- a term prefixed with `-` excludes rows that match the rest of the term;
- a term `field:value` restricts the match to one field where the page
  declares that field — `machine`, `repo`, `task`, `branch`, `state`,
  `runtime` — and a term whose field name the page does not declare is plain
  text;
- matching is case-insensitive;
- there are no regular expressions, and globs are matched by an algorithm that
  is linear in the input, so no input can make it slow.

#### REQ: list-quick-filters-sort-and-url-state

Every list page has the quick-filter chips its section below names; a chip is
a toggle, one click. A click on a column header sorts by that column, a second
click reverses the direction. The filter text, sort column, direction, machine
selection and active chips MUST be held in the page address, as
`?q=…&sort=<column>&dir=asc|desc&machine=…&chips=…`, so that the back button
and a pasted link restore the same view. A list page shows its result count as
"37 of 438".

#### REQ: one-line-virtual-rows

Rows are one line, with an ellipsis and the full value in the `title`
attribute, under a sticky header. A list MUST render rows by virtual
scrolling.

#### REQ: empty-columns-hidden

A column whose value is empty, or the same default, for every visible row MUST
be hidden automatically, for example Lifecycle when it is empty for all rows
and Source when every row is `local`.

#### REQ: names-and-times-rendering

A repository name renders as the owner and a slash in muted type followed by
the name in strong type; the host is shown only when the fleet contains more
than one host. A time renders as a relative age such as "3 d ago" with the
absolute timestamp in the `title`, and a time older than 30 days renders
muted.

#### REQ: empty-and-no-match-states

A list with no rows says what was filtered and offers "clear filters"; a list
with no entities at all says that nothing has been observed yet.

### Dashboard

#### REQ: dashboard-now-row

The Dashboard's first row, "Now", MUST show the running agents as cards, each
with runtime, model, task, repository, machine and how long it has run; when
none is running, one muted line says so. Beside it a machine strip shows one
tile per machine with its freshness, its WB version and, for a machine that
reports metrics, a CPU and a memory mini-bar.

#### REQ: dashboard-needs-attention

The Dashboard's second row, "Needs attention", MUST list counted, clickable
items, each linking to the list filtered to exactly the entities counted, and
MUST show only items whose count is above zero. When every count is zero it
shows one line saying nothing needs the operator. The items are:

- orphaned worktrees (`owner_state` is `orphaned`);
- worktrees with unpushed commits (`ahead` above zero) and worktrees whose
  upstream is gone;
- worktrees idle longer than 30 days;
- open pull requests;
- repositories whose code index is stale, behind or failed;
- stale machines, whose state is older than 24 hours;
- scan diagnostics and repository errors.

The row leaves a visible slot for the work-loss risk section of the
[work-loss-risk](../work-loss-risk/README.md) Feature, which is not built here.

#### REQ: dashboard-charts

The Dashboard's third row MUST show four charts drawn with Chart.js:

- **Activity:** worktrees by last-activity day over the last 30 days, stacked
  by machine; clicking a bar opens Worktrees filtered to that day;
- **Worktree age:** bars for today, 1 to 7 days, 8 to 30 days, 31 to 90 days
  and older; clicking a bar opens the filtered list;
- **Worktrees by repository:** horizontal bars for the top 10 repositories;
- **Code index:** a doughnut of code-index states.

Each chart has a text alternative, a visually hidden table of the same
numbers, and uses the theme's colours in both light and dark.

#### REQ: dashboard-panels

The Dashboard's fourth row MUST show a Repositories panel with a one-click
segmented switch of Recent, Most worktrees and Most branches (top 10 each),
whose choice is kept in `localStorage` and in the address, and beside it a
Recent tasks panel (top 10 by last activity).

### Tasks

#### REQ: tasks-list

A task is the set of worktrees that share a task name; no task storage is
introduced. The Tasks page MUST show one row per task with these columns:
Task, Repositories (the first two and "+n"), Worktrees (count), Machines,
Agents (running), Pull requests, State, Last activity. State is the worst
state among the task's worktrees in the order orphaned, unpushed, active,
idle. The quick filters are Active, With agent, With PR, Idle > 30 d and
Multi-repo. The declared match fields are `task`, `repo`, `machine` and
`state`.

#### REQ: task-detail

`/tasks/:task` MUST show, for the task named by the URL-encoded `:task`, a
summary header, the task's worktrees table, its branches, its pull requests and
its agents.

### Repositories

#### REQ: repositories-list

The Repositories page MUST show one row per repository identity (host and
name), merged across machines, with these columns: Repository; Machines (a
chip per machine, each linking to that machine's checkout); Worktrees;
Branches (local / remote); Agents; PRs; Code index (the worst state across
machines); Last activity; and an actions cell. The actions cell holds two icon
buttons, each with an `aria-label` and a tooltip: browse code (a code icon,
shown only when a code browser is configured, per
[cockpit](../cockpit/README.md)#req:code-browser-link) and open on host (an
external-link icon, to `remote_url_web`). There is no text "Code" link. The
quick filters are With worktrees, With agents, With PRs and Index not fresh.
The declared match fields are `repo`, `machine` and `state`.

#### REQ: repository-detail

`/repositories/:host/:owner/:name` MUST show a merged header; one section per
machine with that checkout's facts, worktrees and branches; the code-index
panel; and the README for an owner, as [cockpit](../cockpit/README.md) defines.
Repository ids that worked before this Feature keep working. Branches load
lazily from `GET /api/v1/cockpit/branches` when the page opens, and while they
load the section shows skeleton rows.

### Worktrees

#### REQ: worktrees-list

The Worktrees page MUST show these columns in order: Worktree (its WB worktree
name, or for a worktree outside WB management its directory name, linking to
the worktree page); Repository; Branch (hidden when equal to the worktree name
for every visible row); Task (linking to the task page); Machine; State (the
owner state plus sync badges `↑n` for unpushed commits, `↓n` for commits
behind, and "gone" for a vanished upstream); PR; Code index; Last activity.
The quick filters are Active, Orphaned, Unpushed, Upstream gone, With PR and
Idle > 30 d. The default sort is last activity, newest first. The declared
match fields are `repo`, `task`, `branch`, `machine`, `state`.

### Agents

#### REQ: agents-list

The Agents page MUST show each agent as a human label — runtime, model and
what it works on (its task or repository) — with a state badge, its machine,
how long it has run, and its session or run id as secondary text with a copy
button. The quick filters are Running and one per runtime. The declared match
fields are `runtime`, `machine`, `task`, `repo`, `state`.

#### REQ: agent-detail

`/agents/:id` MUST show the agent's identity, state, machine (a link), repository, the
worktrees and branches it works on, its task (a link) and the pull requests of
those worktrees.

### Machines

#### REQ: machines-list

The Machines page MUST NOT have a separate section heading plus a "Machine"
column: the first column header reads "Machines" and is the section title, in
larger type. Each machine name links to its page. The columns are Machines;
State (live or cached with its age, stale marked); WB version (marked when
older than the newest in the fleet); Repositories; Worktrees; Agents; CPU;
Memory. CPU and Memory are empty for a machine that reports no metrics.

#### REQ: machine-detail

`/machines/:id` MUST show a summary (OS, architecture, CPU count, WB version,
state age and, when metrics are reported, uptime); live metrics with charts
over the last hour — CPU percent, load, memory used against total, and free
disk on the projects root; counts that link to the filtered lists; the
machine's running agents; and its most recent worktrees. A machine that
reports no metrics MUST say "metrics are reported only by the machine this
Cockpit runs on" instead of showing zeros.

### Read-model contract v2

These requirements change the fleet document and add two routes. The closed
list of anonymous-readable fields is defined by
[cockpit](../cockpit/README.md)#req:anonymous-local-reads-metadata-only, which
is updated for every field below; none of them carries file content, paths,
environment, command lines or process lists.

#### REQ: schema-version-2

The fleet document MUST carry `schema_version` 2. The application accepts only
schema version 2; for any other version it shows "Cockpit and daemon versions
differ, reload" and renders no data.

#### REQ: compressed-responses

The fleet document, the new JSON routes and the static assets MUST be served
gzip-compressed when the client sends `Accept-Encoding: gzip`. The ETag
behaviour is preserved: a request with a matching `If-None-Match` still
receives `304`, and the ETag differs per content encoding or the response
carries a correct `Vary: Accept-Encoding`.

#### REQ: lazy-branches-route

The `branches` collection MUST NOT be part of the fleet document. The route
`GET /api/v1/cockpit/branches?repository=<id>` returns the branches of that
repository checkout, with the same access class as the fleet document:
metadata, readable by `anonymous-local`, and carrying `branch.read`. The
document keeps the per-repository branch counts. A request for an unknown
repository id is answered with status 404 and no data.

#### REQ: repository-activity-fields

A repository entry MUST carry `last_activity_at`, the newest branch activity
time, and `remote_url_web`, the `https://<host>/<owner>/<name>` address built
only from the repository's forge host and `owner/name`. No other new data
class is introduced; the origin URL itself is never in the read model.

#### REQ: worktree-name-and-sync-fields

A worktree entry MUST carry `name`, the worktree name and never a filesystem
path, and the sync facts of its branch: `ahead`, `behind` and `upstream_gone`,
each omitted when unknown. The documented vocabulary of `owner_state` is
`active`, `idle`, `orphaned`, `unknown`, and the application's types accept
all four.

#### REQ: agent-fields

An agent entry MUST carry `worktrees` (worktree ids), `task`, and `started_at`
when known.

#### REQ: machine-fields

A machine entry MUST carry `os`, `arch` and `cpu_count`. The local machine's
entry also carries `metrics`, its latest sample: `cpu_percent`, `load1`,
`memory_used_bytes`, `memory_total_bytes`, `disk_free_bytes`,
`disk_total_bytes` and `sampled_at`. A remote machine's entry carries no
`metrics`; remote machines publishing metrics through remote state is a
follow-up.

#### REQ: derived-collections-memoised

The application MUST compute the merged repositories, the tasks and the
attention counts once per snapshot, memoised on the identity of the fleet
document, not on every render.

### Machine metrics

#### REQ: metrics-sampler

The daemon MUST sample the local machine's CPU percent, one-minute load,
memory used and total, and free and total disk of the projects root every 10
seconds into an in-memory ring buffer of 360 samples. Sampling is off the
request path, reads through an injectable source so unit tests need no real
machine, and compiles on Windows, where it may report that metrics are
unsupported. Samples are not persisted across a daemon restart.

#### REQ: machine-metrics-route

`GET /api/v1/cockpit/machine-metrics?machine=<id>` MUST return the in-memory
sample history for that machine, oldest first, with the same access class as
the fleet document. For a machine that has no sampler, or on a platform where
sampling is unsupported, it returns an empty history and says why, with status
200; for an unknown machine id it returns status 404. A sample carries only
the numeric fields of REQ:machine-fields and its time.

### Performance budgets

These budgets are tested on a fixture of 500 repositories, 600 worktrees,
4,000 branches and 3 machines.

#### REQ: fleet-document-size

The fleet document MUST be at most 150 kB over the wire with gzip.

#### REQ: initial-script-size

The initial JavaScript MUST be at most 350 kB raw. Chart.js and the detail
pages are lazy chunks, loaded only by the routes that use them.

#### REQ: bounded-row-elements

A list MUST NOT have more than 60 row elements in the DOM, whatever the number
of rows.

#### REQ: fast-filtering

Filtering 5,000 rows with the matcher and the view functions MUST complete in
under 30 ms, and typing in a filter MUST NOT be debounced beyond one animation
frame.

#### REQ: no-recompute-when-unchanged

Polling uses the ETag; a poll that returns `304`, or a snapshot whose document is
unchanged, MUST cause no recomputation of derived collections and no
re-render.

### Look and accessibility

#### REQ: look-dependencies

The application keeps PrimeNG and adds Chart.js (MIT licence) at an exact
pinned version, registered tree-shaken so only the controllers and elements
used are bundled. No other new runtime dependency is added without a reason
recorded in the plan.

#### REQ: look-typography-and-state-colour

The application MUST use the system UI font with tabular numerals in tables,
13 px table text and 12 px secondary text, and one accent colour. State
colours are green for live or fresh, amber for stale or behind, red for
orphaned or failed, grey for idle or cached. State MUST NOT be conveyed by
colour alone: an icon or text accompanies it. Light and dark are both
first-class.

#### REQ: look-layout

Cards have subtle borders, an 8 px radius and a consistent 8 px spacing grid.
Skeleton rows are shown while the daemon is warming up, and data arriving
MUST NOT shift the layout.

#### REQ: responsive-to-360

The application MUST work down to a viewport 360 px wide: the tabs collapse to
a scrollable strip, tables drop low-priority columns, and charts stack.

#### REQ: strict-csp-unchanged

The strict content security policy of
[cockpit](../cockpit/README.md)#req:strict-content-security-policy stays
unchanged: no inline script and a style nonce. Chart.js draws to canvas only.

### Test coverage

#### REQ: views-new-code-fully-covered

All code this Feature adds MUST reach 100% test coverage, by the same gates as
[cockpit](../cockpit/README.md)#req:new-code-is-fully-covered: Go through
`wb coverage --changed`, and `cockpit/web` through thresholds of 100 for
statements, branches, functions and lines plus a rendering test for every
component. The end-to-end tests of this Feature run against stubbed
responses; the one real-daemon journey runs on Linux CI only.

## Dependencies

- [cockpit](../cockpit/README.md)
- [work-loss-risk](../work-loss-risk/README.md)
- [code-index-freshness](../code-index-freshness/README.md)

## Not Doing

- Mutating actions (the [cockpit-actions](../cockpit-actions/README.md)
  Feature) and work-loss risk assessment (the
  [work-loss-risk](../work-loss-risk/README.md) Feature); the Dashboard
  attention row leaves a slot for the latter.
- Pull request state from the remote. State stays what local bindings record;
  see Open Questions.
- Metrics from remote machines, and persistence of metrics across daemon
  restarts.
- Task storage: a task stays derived from worktrees.
- The PrimeUI licence key; see Open Questions.

## Acceptance Criteria

### AC: top-bar-shows-tabs-badges-and-freshness

**Requirements:** cockpit-views#req:top-bar

Scenario: Counts, running agents and an old snapshot
Given a fleet with 3 machines, 529 worktrees and 1 running agent, and a snapshot taken longer than two refresh intervals ago
When the application is opened
Then the tabs Dashboard, Tasks, Repositories, Worktrees, Agents and Machines are shown each with its count badge, the Agents badge reads 1 and is highlighted, the freshness chip reads "updated" with the snapshot's age in amber, and the session chip names the principal

### AC: warming-up-shows-progress

**Requirements:** cockpit-views#req:top-bar, cockpit-views#req:look-layout

Scenario: Before the first pass completes
Given a document marked as warming up with 120 of 438 repositories scanned
When the application is opened
Then the freshness chip shows the scanned count, skeleton rows are shown, and when the complete document arrives no element moves

### AC: no-heading-repeats-the-tab

**Requirements:** cockpit-views#req:no-visible-page-heading

Scenario: Any page
Given the application on the Worktrees page
When the page is inspected
Then no visible heading reads "Worktrees", the active tab has `aria-current="page"`, one visually hidden `h1` names the page, and the document title names the page

### AC: global-search-groups-results

**Requirements:** cockpit-views#req:global-search

Scenario: Search across kinds
Given a fleet with more than 8 repositories whose name contains `go`, and a task, a worktree and an agent that also match
When the operator presses Cmd+K, types `go`, presses the down arrow twice and Enter
Then results are grouped by kind with at most 8 per kind, the highlighted result moves with the arrows, and Enter opens the page of the highlighted result

### AC: shortcuts-navigate-and-respect-typing

**Requirements:** cockpit-views#req:keyboard-shortcuts

Scenario: Shortcuts and an input
Given the Dashboard on screen
When the operator presses `g` then `w`, then `/` on a list, types `g w` into the filter, and presses `?`
Then Worktrees opens, the filter box has the focus, the typed `g w` stays as text and no tab switches, and the shortcut sheet appears

### AC: matcher-grammar

**Requirements:** cockpit-views#req:list-filter-and-matcher

Scenario: Terms, glob, exclusion and field
Given rows `sneat-co/bots-go` on machine `mac`, `sneat-co/sneat-go` on machine `vm`, `sneat-dev/wb` on `mac` and `Strongo/Dalgo` on `vm`
When the matcher is applied with `sneat-*/*-go`, with `WB`, with `sneat -wb`, with `machine:vm go`, with `colour:red` on a page that does not declare `colour`, and with `a.*`
Then the results are the two `-go` repositories, `sneat-dev/wb`, the two sneat-co rows, `sneat-co/sneat-go` and `Strongo/Dalgo`, no row (the text `colour:red` matches nothing), and no row (the dot and star are a glob with a literal dot, never a regular expression)

### AC: matcher-is-linear-time

**Requirements:** cockpit-views#req:list-filter-and-matcher

Scenario: Hostile glob
Given the term `*a*a*a*a*a*a*a*a*a*a*b` and a value of 50,000 `a` characters
When the matcher is applied
Then it returns no match in under 30 ms

### AC: filter-state-lives-in-the-address

**Requirements:** cockpit-views#req:list-quick-filters-sort-and-url-state

Scenario: Back button and a pasted link
Given the Worktrees page
When the operator types `fix`, toggles the Unpushed chip, selects machine `mac`, clicks the Last activity header twice, then presses back, and separately opens the pasted address
Then the address carries `q`, `chips`, `machine`, `sort` and `dir`, back removes the last change, the result count reads "n of N", and the pasted address shows the same filtered, sorted rows

### AC: rows-are-one-line-and-virtual

**Requirements:** cockpit-views#req:one-line-virtual-rows

Scenario: Long values in a long list
Given 529 worktrees, one with a 200-character task name
When the Worktrees page is opened and scrolled
Then each row is one line with an ellipsis and the full value in its `title`, the header stays in view, and rows appear and disappear as the list scrolls

### AC: uniform-columns-are-hidden

**Requirements:** cockpit-views#req:empty-columns-hidden

Scenario: Lifecycle empty, Source all local
Given 529 worktrees with an empty lifecycle and route `local`, and a second fleet in which one worktree has a lifecycle and one is `cached`
When the Worktrees page is opened for each
Then the first shows neither the Lifecycle nor the Source column and the second shows both

### AC: repository-and-time-rendering

**Requirements:** cockpit-views#req:names-and-times-rendering

Scenario: One host, two hosts, old and new
Given a fleet whose repositories are all on `github.com`, and one activity 3 days old and one 45 days old
When the Repositories page is opened, and again with a repository on a second host added
Then names show a muted `owner/` before a strong name with no host the first time and the host the second, the 3-day value reads "3 d ago" in normal type, the 45-day value is muted, and both carry the absolute timestamp in `title`

### AC: empty-states-offer-clear

**Requirements:** cockpit-views#req:empty-and-no-match-states

Scenario: No match, then no data
Given a Repositories page with filter `zzz` active and, separately, an empty fleet
When each is shown
Then the first names the filter and offers "clear filters", which restores the full list, and the second says nothing has been observed yet

### AC: dashboard-now-shows-agents-and-machines

**Requirements:** cockpit-views#req:dashboard-now-row

Scenario: One running agent, then none
Given one running agent and three machines, one of which reports metrics
When the Dashboard is opened, and again after the agent stops
Then a card shows the agent's runtime, model, task, repository, machine and running time, each machine has a tile with freshness and version, only the reporting machine has CPU and memory bars, and afterwards one muted line replaces the cards

### AC: attention-lists-nonzero-items

**Requirements:** cockpit-views#req:dashboard-needs-attention

Scenario: Mixed counts, then all zero
Given 21 orphaned worktrees, 7 with unpushed commits, no idle worktree, 16 open pull requests and 2 stale machines
When the Dashboard is opened, the orphaned item is clicked, and later the data has every count zero
Then exactly the non-zero items are shown with their counts, the click opens Worktrees filtered to those 21 worktrees, and with all zero one "nothing needs you" line replaces the list

### AC: attention-covers-every-item-kind

**Requirements:** cockpit-views#req:dashboard-needs-attention

Scenario: One of each
Given a worktree with `ahead` 2, one with `upstream_gone`, one idle for 31 days, an open pull request, a repository with a failed code index, a machine whose state is 25 hours old, and a repository error
When the Dashboard is opened
Then the attention row shows an item for each of the seven kinds, each linking to a list that shows exactly those entities

### AC: charts-are-lazy-linked-and-accessible

**Requirements:** cockpit-views#req:dashboard-charts

Scenario: Four charts
Given worktrees with activity over the last 30 days on two machines
When the Dashboard is opened and a bar of the Activity chart is clicked, and the same in dark mode
Then four charts are drawn, Worktrees opens filtered to that day, each chart has a visually hidden table with the same numbers, and the chart colours come from the active theme

### AC: dashboard-panels-switch-and-remember

**Requirements:** cockpit-views#req:dashboard-panels

Scenario: Segmented switch
Given more than 10 repositories
When the operator selects Most branches and reloads the page
Then the panel lists the 10 repositories with the most branches, the address and `localStorage` hold the choice, Most branches is still selected, and the Recent tasks panel lists 10 tasks by last activity

### AC: tasks-list-aggregates-worktrees

**Requirements:** cockpit-views#req:tasks-list

Scenario: One task over three repositories
Given worktrees named for task `fix-ci` in three repositories on two machines, one orphaned and two active, and a running agent and a pull request on it
When the Tasks page is opened and the Multi-repo and With agent chips are toggled
Then one row names `fix-ci` with two repositories and "+1", 3 worktrees, 2 machines, 1 agent, 1 pull request, state orphaned and its newest activity, and it stays in the list with both chips on

### AC: task-detail-shows-its-entities

**Requirements:** cockpit-views#req:task-detail

Scenario: A task name that needs encoding
Given a task named `fix/ci 100%`
When the Tasks page links to it and the link is followed
Then the address is `/tasks/fix%2Fci%20100%25`, and the page shows the summary header, the worktrees table, the branches, the pull requests and the agents of that task

### AC: repositories-merge-across-machines

**Requirements:** cockpit-views#req:repositories-list

Scenario: One repository on three machines
Given `sneat-co/sneat-go` checked out on three machines with different code-index states and a code browser configured
When the Repositories page is opened
Then one row shows the repository with three machine chips each linking to that machine's checkout, summed counts, the worst code-index state, the newest activity, and two icon buttons with `aria-label` and tooltip for browsing code and opening on the host, and there is no text "Code" link

### AC: repository-actions-follow-configuration

**Requirements:** cockpit-views#req:repositories-list

Scenario: No code browser
Given a daemon with no code browser configured
When the Repositories page is opened
Then the browse-code button is absent and the open-on-host button links to `remote_url_web`

### AC: repositories-quick-filters

**Requirements:** cockpit-views#req:repositories-list

Scenario: Four chips
Given repositories with and without worktrees, agents, pull requests and a fresh index
When each of With worktrees, With agents, With PRs and Index not fresh is toggled in turn
Then each leaves exactly the repositories that satisfy it

### AC: repository-detail-loads-branches-lazily

**Requirements:** cockpit-views#req:repository-detail

Scenario: Opening a repository
Given the fleet document carries no branches and a repository present on two machines
When `/repositories/github.com/sneat-co/sneat-go` is opened
Then the page shows a merged header and one section per machine, requests `/api/v1/cockpit/branches` for that repository only when it opens, shows skeleton rows until it answers, and then lists the branches; the older repository id still opens the page

### AC: worktrees-columns-and-badges

**Requirements:** cockpit-views#req:worktrees-list

Scenario: Managed and unmanaged worktrees
Given a WB worktree named `fix-ci` with branch `fix-ci`, `ahead` 2 and `behind` 1, an unmanaged worktree in directory `scratch`, and one with `upstream_gone`
When the Worktrees page is opened
Then the first column holds each worktree's name linking to its page, the Branch column is hidden while every branch equals its worktree name and shown otherwise, the State column shows `↑2` and `↓1` and "gone" beside the owner state, the Task cell links to the task page, and the default order is last activity, newest first

### AC: worktrees-quick-filters

**Requirements:** cockpit-views#req:worktrees-list

Scenario: Six chips
Given worktrees that are active, orphaned, unpushed, upstream-gone, with a pull request and idle for 31 days
When each of Active, Orphaned, Unpushed, Upstream gone, With PR and Idle > 30 d is toggled in turn
Then each leaves exactly the worktrees that satisfy it

### AC: agents-list-describes-the-work

**Requirements:** cockpit-views#req:agents-list

Scenario: A claude agent on a task
Given a running `claude` agent with model `opus` on task `fix-ci` in `sneat-dev/wb` on machine `mac` with session id `wbs-3da4ea95`
When the Agents page is opened and the copy button is pressed
Then the row reads the runtime, model and task or repository as its label, shows a state badge, the machine and the running time, the session id appears as secondary text, the clipboard holds the full id, and the Running and per-runtime chips filter the list

### AC: agent-detail-links-its-work

**Requirements:** cockpit-views#req:agent-detail

Scenario: Following the links
Given an agent working on two worktrees with pull requests
When `/agents/<id>` is opened
Then the page shows identity, state, a link to its machine, the repository, both worktrees and their branches, a link to its task, and the pull requests of those worktrees

### AC: machines-table-title-and-links

**Requirements:** cockpit-views#req:machines-list

Scenario: Three machines
Given three machines, one live and two cached more than 24 hours ago, and one running an older WB version than another
When the Machines page is opened
Then the first column header reads "Machines" in larger type with no separate section heading, each name links to its page, the cached machines show their age and are marked stale, the older version is marked, and CPU and Memory are empty for the machines without metrics

### AC: machine-detail-metrics-charts

**Requirements:** cockpit-views#req:machine-detail

Scenario: A reporting machine
Given the local machine with 360 samples
When `/machines/<id>` is opened
Then the page shows OS, architecture, CPU count, WB version, state age and uptime, four charts over the last hour (CPU percent, load, memory used against total, disk free), counts linking to the filtered lists, the machine's running agents and its most recent worktrees

### AC: machine-without-metrics-says-so

**Requirements:** cockpit-views#req:machine-detail, cockpit-views#req:machine-metrics-route

Scenario: A remote machine
Given a cached machine with no `metrics` and an empty history from the metrics route
When its page is opened
Then it says "metrics are reported only by the machine this Cockpit runs on" and shows no chart and no zero values

### AC: client-accepts-only-schema-2

**Requirements:** cockpit-views#req:schema-version-2

Scenario: Version mismatch
Given a daemon answering `schema_version` 1 and another answering 2
When the application loads from each
Then the first shows "Cockpit and daemon versions differ, reload" and no data, and the second renders normally, and the daemon's fleet document carries `schema_version` 2

### AC: responses-are-gzip-with-etag

**Requirements:** cockpit-views#req:compressed-responses

Scenario: Compressed and conditional
Given a daemon serving the fleet document, the branches and metrics routes and a static asset
When each is requested with `Accept-Encoding: gzip`, and the fleet document is then requested again with the ETag it returned and the same encoding, and once with no `Accept-Encoding`
Then every response carries `Content-Encoding: gzip`, the repeated request is answered `304`, and the request without the header receives an identity body whose ETag differs or whose response says `Vary: Accept-Encoding`

### AC: branches-leave-the-document

**Requirements:** cockpit-views#req:lazy-branches-route

Scenario: Document, route and unknown id
Given a repository with 12 branches
When the fleet document is requested, then `/api/v1/cockpit/branches?repository=<its id>` without a session, then the same with an unknown id
Then the document has no `branches` collection and reports the repository's branch counts, the route returns the 12 branches with status 200, and the unknown id gets status 404 with no data

### AC: repository-entries-carry-activity-and-web-url

**Requirements:** cockpit-views#req:repository-activity-fields

Scenario: Newest branch activity
Given a repository on `github.com` named `sneat-dev/wb` whose newest branch activity is at a known time and whose origin URL carries a credential
When the fleet document is requested
Then its entry has `last_activity_at` equal to that time and `remote_url_web` equal to `https://github.com/sneat-dev/wb`, and no origin URL or credential appears anywhere in the document

### AC: worktree-entries-carry-name-and-sync

**Requirements:** cockpit-views#req:worktree-name-and-sync-fields

Scenario: Known and unknown sync facts
Given a worktree whose branch is 2 ahead and 1 behind, one whose upstream is gone, and one with no upstream information, and one with `owner_state` `unknown`
When the fleet document is requested
Then the first has `ahead` 2 and `behind` 1, the second `upstream_gone` true, the third has none of the three fields, every worktree has a `name` that contains no path separator, and the `unknown` state is accepted by the application's types

### AC: agent-entries-carry-worktrees-task-start

**Requirements:** cockpit-views#req:agent-fields

Scenario: Agent with and without a known start
Given an agent working on two worktrees on task `fix-ci` that started at a known time, and another whose start is unknown
When the fleet document is requested
Then the first has `worktrees` listing both ids, `task` and `started_at`, and the second has no `started_at`

### AC: machine-entries-carry-hardware-and-metrics

**Requirements:** cockpit-views#req:machine-fields

Scenario: Local and remote machine
Given the local machine and a machine known from a remote snapshot
When the fleet document is requested
Then both entries have `os`, `arch` and `cpu_count`, only the local entry has `metrics` with `cpu_percent`, `load1`, `memory_used_bytes`, `memory_total_bytes`, `disk_free_bytes`, `disk_total_bytes` and `sampled_at`, and no process list, path or environment value appears

### AC: derived-collections-computed-once

**Requirements:** cockpit-views#req:derived-collections-memoised

Scenario: Many renders, one snapshot
Given one fleet document and a counter on the derivation functions
When the Repositories, Tasks and Dashboard pages are rendered repeatedly and a filter is typed
Then merged repositories, tasks and attention counts were each computed once for that document, and a new document computes them once more

### AC: sampler-fills-a-ring-buffer

**Requirements:** cockpit-views#req:metrics-sampler

Scenario: 400 samples, then no source
Given a sampler with a fake source and a fake clock, and a build for Windows
When 400 ten-second ticks elapse, and separately the source reports unsupported
Then the buffer holds exactly the newest 360 samples oldest first, each tick spaced 10 seconds apart, no sampling ran on a request, the unsupported platform reports that metrics are unsupported without an error, and `GOOS=windows go build ./...` succeeds

### AC: metrics-route-returns-history

**Requirements:** cockpit-views#req:machine-metrics-route

Scenario: Local, unsupported and unknown machine
Given a sampler holding 5 samples for the local machine id
When `/api/v1/cockpit/machine-metrics?machine=<local id>` is requested without a session, then for a machine with no sampler, then for an unknown id
Then the first returns the 5 samples oldest first with status 200 and only numeric fields and times, the second returns an empty history with a reason and status 200, and the third returns 404

### AC: fleet-document-fits-the-budget

**Requirements:** cockpit-views#req:fleet-document-size

Scenario: Fixture of 500, 600, 4,000, 3
Given the fixture of 500 repositories, 600 worktrees, 4,000 branches and 3 machines
When the fleet document is requested with gzip
Then the response body is at most 150 kB

### AC: initial-script-fits-the-budget

**Requirements:** cockpit-views#req:initial-script-size

Scenario: Production build
Given the production build of `cockpit/web`
When the build's bundle budget check runs and the Dashboard, then a detail page, are opened
Then the initial JavaScript is at most 350 kB raw, the build fails when it is larger, and Chart.js and the detail-page code load as separate chunks only on the routes that use them

### AC: list-never-exceeds-60-row-elements

**Requirements:** cockpit-views#req:bounded-row-elements

Scenario: Largest lists
Given the fixture
When the Repositories and Worktrees pages are rendered and scrolled to the end
Then at no moment are there more than 60 row elements in the DOM

### AC: filtering-5000-rows-is-fast

**Requirements:** cockpit-views#req:fast-filtering

Scenario: Benchmark
Given 5,000 rows
When the matcher and view functions filter them with a multi-term glob query, and a key is typed in a filter box
Then filtering completes in under 30 ms, and the list updates on the next animation frame with no debounce timer

### AC: unchanged-snapshot-does-nothing

**Requirements:** cockpit-views#req:no-recompute-when-unchanged

Scenario: Poll returns 304
Given the application showing a snapshot with counters on the derivation functions and on rendering
When the next poll returns `304`, and a later poll returns an identical document
Then neither poll recomputes a derived collection or re-renders a page

### AC: chart-library-is-pinned-and-tree-shaken

**Requirements:** cockpit-views#req:look-dependencies

Scenario: Dependency manifest
Given `cockpit/web/package.json` and the production build
When the dependency check runs
Then Chart.js is listed at an exact version with no range, only the controllers and elements the charts use are imported, and no other new runtime dependency is present without a recorded reason

### AC: state-is-never-colour-only

**Requirements:** cockpit-views#req:look-typography-and-state-colour

Scenario: Every state in both themes
Given rows in states live, cached, fresh, stale, behind, orphaned, failed and idle
When the pages are rendered in light and in dark
Then each state shows an icon or text as well as its colour, tables use tabular numerals at 13 px, secondary text is 12 px, and the colours follow the green, amber, red and grey assignment

### AC: no-layout-shift-on-arrival

**Requirements:** cockpit-views#req:look-layout

Scenario: Skeleton to data
Given a page showing skeleton rows and cards
When the data arrives
Then no element changes position or size, as measured by a layout-shift observer

### AC: usable-at-360-wide

**Requirements:** cockpit-views#req:responsive-to-360

Scenario: Phone width
Given a viewport 360 px wide
When the Dashboard and each list page are opened
Then there is no horizontal scrolling of the page, the tabs form a scrollable strip, low-priority columns are dropped, and the charts are stacked

### AC: csp-and-canvas-only

**Requirements:** cockpit-views#req:strict-csp-unchanged

Scenario: Charts under the policy
Given the Dashboard with charts
When it is loaded and the browser console is read
Then the response policy has no `unsafe-inline` for scripts and styles use a nonce, no policy violation is reported, and the charts are canvas elements

### AC: views-coverage-gates-hold

**Requirements:** cockpit-views#req:views-new-code-fully-covered

Scenario: The gates on a views pull request
Given a pull request that changes Go code and `cockpit/web`
When `wb coverage --changed`, the `cockpit/web` test run with its thresholds, the component-spec check and the stubbed end-to-end run execute
Then every added Go statement is covered, thresholds of 100 hold for statements, branches, functions and lines, every new component has a rendering test, and the end-to-end tests pass against stubbed responses

## Open Questions

- Pull request state is `unknown` for all 16 pull requests because only local
  bindings are read (finding F14 of the 2026-10-01 review). Reading it from the
  remote is out of scope here; the pull request column and the open pull
  request attention item show what local bindings record, and the "unknown"
  state is shown as it is, not hidden. Who owns the follow-up, and whether it
  needs a network read in the daemon, is undecided.
- The "Invalid PrimeUI License" watermark (finding F15) is a pending founder
  decision about the PrimeUI licence and is not part of this Feature.

---
*This document follows the https://specscore.md/feature-specification*
