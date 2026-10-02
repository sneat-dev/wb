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

Cockpit views redesigns the [Cockpit](../cockpit/README.md) application for a
dispatcher: a developer who starts agents on several machines, unblocks them,
reviews and lands their work, and cleans up as a by-product, across hundreds of
repositories and worktrees. The task is the primary object. Home answers "what is
blocked on me, what is ready to land, what is in flight"; every list is
searchable and fast; the underlying data is exposed in three levels; and the
views are where the actions of [cockpit-actions](../cockpit-actions/README.md)
live, because the Cockpit is the control panel of the fleet and not only a
screen to read. The fleet read model changes (schema version 2) so that the
page is small, and gains pull request state and checks, agent activity, agents
and load from other machines, and sealed-work throughput.

## Problem

Measured on the founder's Mac (wb 0.174.0, 2026-10-01): 438 repository rows,
529 worktrees (455 distinct tasks), 3,834 branches, 16 pull requests, 1 agent,
3 machines. The first Cockpit slice cannot be used at that size, and it answers
the wrong question.

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
  tables. It does not say that a pull request's checks failed, that an agent is
  blocked, or which finished work is ready to land. Pull request state is
  `unknown` for all 16 pull requests. An agent is an identifier, not a
  description of what it is doing, and agents and load on other machines are
  invisible. There is no task view although one task spans several repositories
  and worktrees, and machines have no page and no load information.

## Journey

The Cockpit is organised by seven jobs to be done. Every page element serves one
of them, each requirement names the job it serves, and anything that serves none
is not in this Feature.

- **J1. What is blocked on me?** Home "Needs you".
- **J2. Review and land finished work.** Home "Ready to land", Tasks.
- **J3. Start new work.** "New task" in the top bar.
- **J4. Is any work at risk?** Home "Needs you", fed by
  [work-loss-risk](../work-loss-risk/README.md).
- **J5. Find X in seconds.** The palette and wildcard filters.
- **J6. Clean up.** Home "Cleanup" and its flow.
- **J7. Is the fleet healthy?** Home "Fleet health", Machines.

Understanding one repository is a detail page, not a job. Navigation is Home,
Tasks, then the inventory pages Repositories, Worktrees, Agents and Machines,
which are secondary.

The journey of one working morning:

1. **Start (J1, J2).** I open the Cockpit. **Observable good result:** Home shows
   at most five things blocked on me, each named, with one primary action, then
   the pull requests that are green and ready to land, grouped by task, then the
   agents in flight on every machine, in under one second on a fleet of 500
   repositories.
2. **Middle (J3, J5).** I press `/`, type `sneat-* -archive` and read the result
   count, select a task and read its worktrees, pull requests and agents in the
   side panel without leaving the list, and open "New task" to get the exact
   command that starts it. **Observable good result:** the selection is in the
   address (`?sel=`), the back button and a pasted link return exactly that view,
   and the list never holds more than 80 row elements however long it is.
3. **Land (J2, J4).** I choose "Land task" on a ready task. **Observable good
   result:** a preview opens without a page navigation, nothing runs until I
   confirm it, and the task's row reflects the result within one refetch; a
   reader without an owner session sees the same action disabled with one
   explanation and can copy the equivalent `wb` command instead.
4. **End (J6, J7).** I read the one-line Cleanup summary and the Fleet health
   line, which is shown only when something is not OK. **Observable good result:**
   data from another machine is marked with its age and never presented as
   current, and a machine that reports no metrics says so plainly.

## Behavior

Principles that every requirement below serves: what needs me first; one
keystroke to anything; dense but calm; every number is a link to the list that
produced it; every filter, sort and selection lives in the address; never lie
about freshness; fast by budget, not by hope. There is no column chooser, no
saved or pinned filter, no settings page, no multi-selection and no bulk action,
and nothing here acknowledges or snoozes an item.

### Shell

#### REQ: top-bar

Serves J1 to J7. The application MUST show a top bar with the brand; the tabs
Home, Tasks, Repositories, Worktrees, Agents and Machines; the palette entry; a
"New task" button; the snapshot freshness chip; and the session chip (`anonymous` or
`owner`). A tab badge is shown only for a signal: Home shows the number of tasks in
"Needs you" (REQ:home-needs-you), written `99+` above 99 with the whole number in its tooltip, and Agents the number of running agents (REQ:field-tables
defines "running"), highlighted when above zero. No tab shows a badge for a static count.
The freshness chip reads "updated N s ago" with the time since the daemon last found the
document current (the `X-Wb-Cockpit-Checked-At` response header of the fleet read, which a `304`
carries too, REQ:compressed-responses; not `snapshot_at`, which does not move while the fleet is
quiet), turns amber when that is older than two refresh intervals (the document's
`refresh_interval_seconds`), and while the daemon is warming up shows how many repositories
have been scanned.

#### REQ: home-route

Serves J1. The first page is named Home and is shown at `/`; `/dashboard`
keeps working as an alias that shows Home. The document `<title>` of the page is
"Home".

#### REQ: no-visible-page-heading

The application MUST NOT show a visible heading that repeats the selected tab.
The document `<title>` names the page. The active tab carries
`aria-current="page"` and every page has a visually hidden `h1`.

#### REQ: command-palette

Serves J5. Pressing `/` where no list filter applies, or Cmd/Ctrl+K anywhere, MUST open
one palette input over repositories, tasks, worktrees, the branches of worktrees listed in
the fleet document, agents and machines; branches loaded lazily for a repository page are
not searched. Results are grouped by kind with at most 8 per kind, can be moved through
with the arrow keys, and Enter opens the highlighted result. The palette uses the matcher
of REQ:list-filter-and-matcher. Actions in the palette are specified by `cockpit-actions`
(its Task 8), not here.

#### REQ: keyboard-shortcuts

The application MUST provide `g h`, `g t`, `g r`, `g w`, `g a` and `g m` to switch
to Home, Tasks, Repositories, Worktrees, Agents and Machines; `/` focuses the page
filter when a list is shown and opens the palette otherwise; `Esc` clears the
focused filter, closes the palette or closes the side panel; `?` shows the
shortcut sheet. A shortcut MUST NOT fire while the focus is in an input, textarea
or editable element.

#### REQ: schema-version-2

The fleet document MUST carry `schema_version` 2. The application accepts only
schema version 2 and renders no data for any other version. When the daemon's
version is older it says "update wb on this machine"; when the page's version
is older it says "reload".

### Lists and matcher

#### REQ: list-filter-and-matcher

Serves J4. Every list page has a filter box, which replaces the repository
dropdown. Machine remains a compact multi-select chip group. The filter and the
global search use one pure matcher with this grammar:

- the input is split on whitespace into terms, except that a value in double
  quotes may contain spaces (`task:"fix ci"`); a row matches only when all
  terms match (AND);
- a term containing `*` or `?` is a glob matched against the whole field
  value, so `sneat-*/*-go` matches `sneat-co/bots-go`; `*` matches any run of
  characters and `?` exactly one;
- a term without wildcards is a substring match;
- a term prefixed with `-` excludes rows that match the rest of the term, and
  that includes a field term, so `-machine:vm` excludes rows on machine `vm`;
- a term `field:value` restricts the match to one field where the page
  declares that field (the fields of REQ:filter-vocabulary) and a term whose
  field name the page does not declare is plain text; a quoted field value
  (`repo:"sneat-co/sneat-go"`) is an exact, whole-value, case-insensitive match
  in which `*` and `?` are ordinary characters, so it does not match
  `sneat-co/sneat-go-backend`, while an unquoted value stays a substring or a glob;
  every link that carries a field term writes the value quoted, so that a count
  cell opens exactly the rows it counted; a value that holds a double quote, or
  one whose term would pass the 256-character cap, cannot be written as a link;
- double quotes protect what they enclose: a quoted leading `-` is text, not an
  exclusion, and a quoted `:` does not make a field term;
- a bare term is matched against the fields the page declares as searched by
  default (REQ:filter-vocabulary); a repository's value is its `owner/name`
  without the host;
- matching is case-insensitive, and `?` matches exactly one code point;
- the filter text is capped at 256 characters and 16 terms, and what exceeds
  either cap is ignored;
- there are no regular expressions, and globs are matched by an algorithm whose
  step count is at most a constant times the pattern length times the value
  length, so no input can make it slow.

#### REQ: filter-vocabulary

The fleet-data library owns one filter vocabulary, and chart clicks, Home links and
quick-filter chips MUST use only that vocabulary. It is this table.

| Page | Bare term searches | Chip ids | `state:` values | Other fields | `sel` key | Sort column ids |
|---|---|---|---|---|---|---|
| Tasks | task, repository | `needs-you`, `ready`, `working`, `agent`, `pr`, `multirepo`, `idle30`, `older` | the task state ids `at-risk`, `checks-failed`, `blocked`, `ready`, `not-ready`, `working`, `landed`, `idle`, `not-reported` | `task`, `repo`, `machine`, `age` | the task name | `task`, `state`, `worktrees`, `activity` |
| Repositories | repository | `worktrees`, `agents`, `prs`, `index`, `errors` | `fresh`, `stale`, `diverged`, `pending`, `failed`, `never` (code index) | `repo`, `machine`, `age` | the repository entry id | `repository`, `activity`, `worktrees`, `branches` |
| Worktrees | task, repository, branch | `active`, `orphaned`, `unpushed`, `gone`, `pr`, `idle30`, `safe`, `look` | `active`, `idle`, `orphaned`, `unknown` | `task`, `repo`, `branch`, `machine`, `age` | the worktree entry id | `worktree`, `state`, `machine`, `activity` |
| Agents | runtime, model, task, repository | `running`, `blocked`, `runtime-<name>` where `<name>` matches `[a-z0-9-]+` | `working`, `blocked`, `idle`, `done`, `unknown`, `live`, `parked`, `running`, `completed`, `failed`, `timeout`, `abandoned` | `runtime`, `task`, `repo`, `machine` | the agent entry id | `label`, `activity`, `machine`, `started` |
| Machines | machine name | `stale`, `outdated` | `live`, `cached`, `stale` | `machine` | the machine entry id | `machine`, `state`, `version` |

`needs-you` on Tasks is the tasks that REQ:home-needs-you lists (the same set, not a
second definition: an at-risk task older than the Needs you window is not in it). Every chip
carries, in the vocabulary itself, an id, a label (the words on the chip) and a one-sentence
hint (its tooltip), so a list renders its chips from the page's vocabulary alone. `age:` terms apply to last activity and are exactly `age:<1d`,
`age:1-7d`, `age:8-30d`, `age:31-90d` and `age:>90d`; there is no `day:` term. The chip
`idle30` is `age:>30d`. The Tasks chip `older` is not a filter but an inclusion: Tasks leaves out the
tasks in state `at-risk` whose last activity is outside the Needs you window (14 days; no recorded
activity is not recent), as Home does, because older at-risk work is a cleanup matter; with `older` on,
or with a `state:at-risk` term in the filter, they are listed too, and the result count still says "n of
all". The palette never hides such a task from a search that matches it: it ranks it after the others of its kind, and an empty query suggests none.
The Worktrees chips `safe` and `look` are the two cleanup counts of
REQ:home-cleanup, `stale` on Machines is a state older than 24 hours and `outdated` a WB
older than the newest in the fleet. The Repositories sort ids `activity`, `worktrees` and
`branches` are the presets of REQ:repositories-list. A chip whose id a page does not list,
a `sel` that names no entry and a sort column id a page does not list are ignored.

#### REQ: list-quick-filters-sort-and-url-state

Every list page has the quick-filter chips of REQ:filter-vocabulary; a chip is a toggle,
one click. A click on a column header sorts by that column, a second click reverses the
direction. The filter text, sort column, direction, machine selection, active chips and
the selected row MUST be held in the page address, as
`?q=…&sort=<column>&dir=asc|desc&machine=…&chips=…&sel=<key>`, where the sort column ids and
the `sel` key of each page are those of REQ:filter-vocabulary, so that the back button and a
pasted link restore the same view. A list page shows its result count as "37 of 438". The
default sorts are: Tasks by state precedence (the order of REQ:task-state, worst first), then
last activity newest first; Worktrees and Repositories by last activity, newest first; Agents
with running agents first, then newest first; Machines with the local machine first, then
by name. Choosing a chip, a machine or a sort (a header, or `s`) replaces the history entry instead of
adding one, while opening and closing the side panel are entries of their own and moving between
rows with `j` and `k` adds none; a change of selection alone does not filter or sort again. Under an
agents list whose machine cut its agents (`agents_truncated`) every count that includes them reads
"at least n": the Agents badge ("n+"), Home's In flight and the list's own count.

#### REQ: one-line-virtual-rows

Rows are one line, with an ellipsis and the full value in the `title`
attribute, under a sticky header. A list MUST render rows by virtual
scrolling.

#### REQ: default-columns-are-few

Every list shows at most 7 columns by default. That is a default and not a limit: a
page may declare more than seven columns, each with a `priority`, and the columns past
the seventh are shown while the list is wide enough for the minimum width of every one
of them, and are hidden lowest priority first as the list narrows (a panel beside it, a
tablet, a phone); a column that declares no priority is held to seven. A trailing
actions or chrome cell is not a column: it is not counted, its header is not drawn (it
stays as the name assistive technology reads), and it hides by priority like the rest.
A column whose value is empty, or the same default, for every visible row MUST be hidden
automatically, for example Lifecycle when it is empty for all rows and Source when every
row is `local`.

#### REQ: names-and-times-rendering

A repository name renders as the owner and a slash in muted type followed by
the name in strong type; the host is shown only when the fleet contains more
than one host. A time renders as a relative age such as "3 d ago" with the
absolute timestamp in the `title`, and a time older than 30 days renders
muted.

#### REQ: empty-and-no-match-states

A list with no rows says what was filtered and offers "clear filters"; a list
with no entities at all says that nothing has been observed yet.

#### REQ: every-number-is-a-link

The count cells below MUST be links to the list that produced them, using only the
vocabulary of REQ:filter-vocabulary, and no other number is required to link:

- Home: the "+n more" of "Needs you" (Tasks, chip `needs-you`); the cleanup counts (Worktrees,
  chips `safe` and `look`) and the age bars of the cleanup chart (Worktrees with the `age:`
  term); a stale or outdated machine in "Fleet health" (Machines, chip `stale` or
  `outdated`); the Agents tab badge (Agents, chip `running`).
- Tasks: the worktree count (Worktrees with `task:`).
- Repositories: the worktree count (Worktrees with `repo:`), the agent count (Agents with
  `repo:`) and the pull request count (Tasks, chip `pr`, with `repo:`).
- Machines: the repository, worktree and agent counts (each list with `machine:`).

These are declared non-linking, each with its reason: the branch counts on Repositories
(there is no branches list page), the throughput charts and their numbers (they come from
sealed terminal records that have no entries in the fleet document, so no list can reproduce
them), and the checks passed over total of a pull request (it is a fact of one pull request,
whose link is its `url`). A count whose name cannot be written as a link (it holds a double
quote or is too long for the filter) is shown as plain text with the reason in its `title`.
The links carry quoted field terms (REQ:list-filter-and-matcher), so each opens exactly the
rows it counted. Hover cards are not required.

### Progressive exposure

Three levels apply everywhere: a signal (a count, a badge or a row), a list with
a side panel, and the raw data.

#### REQ: side-panel

Selecting a row, by click or Enter, MUST open a side panel beside the list; the
list stays, the address gains `?sel=<id>`, and Esc closes the panel and removes
`sel`. The panel shows the entity's summary, its related entities, its action
slot (REQ:action-slots), its "Copy command" entries (REQ:copy-the-command), and a
collapsed "Raw data" block that, when opened, shows the entry's fields exactly as
the read model sent them and nothing the caller could not already read. There are
no hover cards, row-expansion chevrons or other intermediate levels.

#### REQ: detail-routes-share-the-panel

The detail routes — `/repositories/:host/:owner/:name`, `/tasks/detail?task=<name>`
with the task name URL-encoded in the query, `/agents/:id`, `/machines/:id` and the
worktree page that [cockpit](../cockpit/README.md) defines — MUST remain, as the
full-page version of the same panel content for deep links and the phone. One
component renders both the panel and the page.

#### REQ: row-keyboard-and-copy

On a list, `j` and `k` MUST move the focused row down and up, Enter selects it
(opening the side panel) and Esc closes the panel. A copy button MUST be shown on
a task name, a branch name and a session id; pressing it puts the full value in
the clipboard.

### Task state

#### REQ: task-state

Serves J1, J2, J4. A task is the set of worktrees that share a task name; no task
storage is introduced. Its computed value is called its `state` everywhere (a worktree has a
`lifecycle` field of its own; the two are different things). The state is a pure function
of the fleet document in the fleet-data library, evaluated top to bottom, the first row that
matches winning, which makes the order worst first. A pull request, agent or worktree "of
the task" is one whose task name equals the task's. A pull request is open when its `state`
is `open` or `draft`. Rows 2, 4 and 5 consider only pull requests that carry `checked_at`; a
task with an open pull request that is unobserved is never `ready`, and the side panel says how
many pull requests are unobserved. The fields are those of REQ:field-tables.

| # | State id | Label | The task has |
|---|---|---|---|
| 1 | `at-risk` | at risk | a worktree on this machine whose `owner_state` is not `active` and which has `ahead` above zero or `has_upstream` false (the interim rule, below) |
| 2 | `checks-failed` | checks failed | an open pull request with `checks_failed` above zero |
| 3 | `blocked` | blocked | an agent whose `activity` is `blocked`, or whose most recent dispatched run ended `failed` or `timeout` strictly less than 24 hours ago (counted from `finished_at`, else `started_at`; a run with neither is not reported as blocking) with no later run or session for the task |
| 4 | `ready` | ready to land | at least one open pull request, and every open pull request (observed or not) carries `checked_at`, has `state` `open` (not a draft), `checks_green` true and `mergeable` equal to `clean` or `has_hooks` |
| 5 | `not-ready` | not ready | an open pull request that rows 2 and 4 did not take (an unobserved one included); the side panel says why, only from: draft, checks failed, checks pending, review, checks not reported, not mergeable, behind, unstable, merge state not reported, pull request not yet checked |
| 6 | `working` | working | an agent whose `activity` is `working`, a dispatched run in state `running`, or a worktree whose `owner_state` is `active` |
| 7 | `landed` | landed | no open pull request and no worktree of the task with unpushed work (`ahead` above zero, or `has_upstream` false), and at least one pull request with `state` `merged`, or, where the worktree's `lifecycle` is populated, every worktree with `lifecycle` `merged` |
| 8 | `idle` | idle | none of the above, and at least one of: a worktree `owner_state`, a pull request `state` or an agent `activity` reported |
| 9 | `not-reported` | state not reported | none of the above because no worktree `owner_state`, no pull request `state` and no agent `activity` is reported |

Row 1 is the interim rule. When the work-loss-risk read model lands it replaces row 1; this
Feature does not type its input. The second arm of row 7 applies only to worktrees whose
`lifecycle` the daemon populates (REQ:field-tables); if no worktree carries it, only the
first arm applies. A pull request whose `state` is not reported is never counted as open,
ready or landed.

**Trust rule.** Tasks are joined by name across machines, so an entry of another machine
(`route` `cached` or `live-remote`) MUST NOT decide the good states of a task that has an entry of this
machine (a worktree, pull request or agent with `route` `local`). For such a task: row 4 is `ready`
only when at least one of its open pull requests is local and every open pull request, local or
remote, is ready (a remote open pull request that is not ready still blocks, a remote ready one alone
cannot make the task ready); row 7 (both arms and the unpushed-work veto) reads only local pull
requests and local worktrees; remote entries may still worsen the state (rows 2, 3 and 5) or add
`working` (row 6), but never lift it: a failed run of this machine (row 3) is cleared only by a later agent of this
machine, not by one that another machine reported. A task with no local entry is computed from its remote entries, and its state is
that machine's report: the view model carries `stateSource` (`local` or `remote`) and `reportedBy`
(the machines) so the page can say "as reported by <machine>", and every pull request in a Home row
says which machine it came from. Home lists such a task, and offers no land or push action for it
(REQ:home-needs-you, REQ:home-ready-to-land).

#### REQ: tasks-list

Serves J1, J2, J5. The Tasks page MUST show one row per task with these columns: Task,
Repositories (the first two and "+n"), Worktrees (count), Machines, Agent, Pull requests
(each with its checks passed over total), State (the badge of REQ:task-state, an icon with
its label), Last activity. The quick filters are those of REQ:filter-vocabulary; selecting a
row opens the side panel with the task's worktrees, pull requests and agents.

#### REQ: task-detail

Serves J2. `/tasks/detail?task=<name>` is the full-page version of the task panel
(REQ:detail-routes-share-the-panel): a summary header with the state badge, the
task's worktrees table, its branches, its pull requests with checks and its
agents, ending with the "Raw data" block.

### Home

#### REQ: home-needs-you

Serves J1 and J4. Home's first decision section, "Needs you" (the first section of all when the document has no `throughput` block, and directly below the Throughput charts when it has, REQ:home-charts), MUST show one row per task, at most 5
rows, each task for its worst kind and with exactly one primary action, then "+n more" that
opens Tasks with chip `needs-you` (the same set); when there are none it shows one line saying
nothing needs the operator. The Home badge of REQ:top-bar is the number of such tasks. "Needs
you" is a signal, not a debt counter: a task whose state is `at-risk`, and a task of the kind
Agent finished, is listed (and counted) only when its last activity, the newest `last_activity_at`
of its worktrees, is within the last 14 days; no recorded activity is not recent. An at-risk task
older than that is not a row and is not in the badge: its worktrees are counted by the Cleanup
line's "need a look" (REQ:home-cleanup). Such a task that also has failed checks, a blocked agent
or a pull request that needs the operator still has that row, which is not age-limited, like
Agent blocked and Run failed. A pull request link of a row is the pull request's `url` only when
it is an `https` address with a plain host (the check the daemon applies, applied again here);
otherwise the row names the pull request and has no link. Rows
of a task whose state is only another machine's report (REQ:task-state, `stateSource` `remote`) say "as reported by <machine>"; a land or push action is offered only for a task whose `stateSource` is `local` (a remote row has only its "Open" links). Agent finished and Agent blocked are read from the agent's `activity` only when it is reported.
Rows
are ordered by the rank of the kind (the order below, which follows REQ:task-state), then by
last activity, newest first. Items disappear when their state changes; there is no acknowledge
or snooze. After the task rows, blocked agents that have no task are one row, "n blocked agents
with no task", whose action opens Agents with chip `blocked`. The kinds, in rank order:

1. **Work at risk** (task state `at-risk`): the task, the reason in words and the worktree(s)
   concerned; the action is "Push" when the registry offers `branch.push`, and otherwise "Copy
   command", one command per worktree concerned (REQ:copy-the-command).
2. **PR checks failed** (state `checks-failed`): the task, `owner/name#number` and
   `failed_check`; the action is "Open failure", a link to the pull request's `url`.
3. **Agent blocked** or **Run failed or timed out** (state `blocked`): the task, the machine and
   for a run its state and exit code, never the run's free text; the action is "Open agent"
   (an agent whose `activity` is not reported is never shown as blocked).
4. **PR needs you** (state `not-ready`, checks green): a pull request with `checks_green` true
   whose `mergeable` is `dirty`, `behind` or `blocked` and so needs a review or a merge
   resolution; the action is "Open pull request", a link to its `url`.
5. **Agent finished** (an agent whose `activity` is `done` or `idle` for a task with no open
   pull request, not landed, and a worktree with `ahead` above zero or `has_upstream` false):
   the task and the agent; the action is "Open task", selecting it on Tasks.

#### REQ: home-ready-to-land

Serves J2. The second section, "Ready to land", MUST list the tasks in state `ready`, one row
per task: the task, the number of repositories, the pull request numbers, the checks passed
over total and the age of the pull request observation (`checked_at`, the oldest among them).
A task whose `stateSource` is `remote` (REQ:task-state) is listed with "as reported by <machine>" and with no action at all, and the pull requests of a task decided here show the machine each one came from. The action is a list of per-pull-request action slots (REQ:action-slots), one for each of the
task's local pull requests (a pull request another machine reported has no land command: its row says "reported by <machine>"), offering the registry's landing action, and otherwise "Copy command" with
`wb pr land '<owner/repository>#<number>'` for each. Tasks in state `not-ready` that wait only
on checks are shown below them muted, with how long ago their checks were read and no action.

#### REQ: home-in-flight

Serves J1. The third section, "In flight", MUST list the running agents on every
machine that publishes them: runtime and model, task, machine, how long it has
run, and its `activity` or "state not reported". An agent from another machine is
shown with the age of its snapshot and with no action. "Stop" and "Log" are shown
only for dispatched runs on this machine, through the action slot, and otherwise as
"Copy command" entries. Machine chips at the right show live or cached with age and,
where metrics exist, a load indicator that answers "can this machine take another
agent": `free` when `cpu_percent` is below 70 and memory used is below 80 percent
of the total, `busy` otherwise, and `unknown` (never `free`) when the sample has no
`cpu_percent` or no memory value, or when the sample is older than 5 minutes (an old reading says nothing of the
machine now; its age is shown beside the word `unknown`), with the sample's route and its age. The route is said
in three words everywhere: `local`, `live` (the wire value `live-remote`) and `cached`.

#### REQ: home-resume

Serves J2. The fourth section, "Resume", MUST list the last 5 tasks by last activity, each with
its state badge and an "Open" action that selects it on Tasks.

#### REQ: home-cleanup

Serves J6. The fifth section, "Cleanup", MUST be one line, "N safe to remove; M need a look",
with a "Review & clean" action. N counts the worktrees of tasks in state `landed` whose `ahead`
is present and equal to 0 and whose `owner_state` is not `active` (chip `safe`); M counts the
other worktrees whose `owner_state` is `orphaned` or `unknown`, or that are idle for more than
30 days, or that are at risk in a task that "Needs you" no longer lists because its last
activity is older than 14 days (REQ:home-needs-you) (chip `look`). The counts are indicative: the authoritative safe set is computed by the
cleanup operation that `cockpit-actions` specifies, and the line says so. "Review & clean"
opens Worktrees with the chip `safe`. The line expands to the Worktree age chart and the
summary: bars for today (`age:<1d`), 1 to 7 days, 8 to 30 days, 31 to 90 days and older
(`age:>90d`), each linking to Worktrees with that `age:` term, with a text alternative and the
theme's colours in light and dark.

#### REQ: home-fleet-health

Serves J7. The sixth section, "Fleet health", MUST be shown only when something is not OK, as
one line per problem: a stale machine (state older than 24 hours), a machine running an older
WB than the newest in the fleet, a scan error, a machine's `remote_error`
(REQ:remote-error-is-visible), this machine's `publish_error` (a typed code, with its fixing
guidance: `collect_failed`, "the scan or the GitHub login failed: run `gh auth status` and `wb remote
publish --dry-run`"; `store_unavailable`, "the remote store cannot be opened: check `wb remote
status`"; `publish_failed`, "the store refused or could not be reached: run `wb remote publish` and
read its error"; `optional_fields_dropped`, "the hub is older than this wb and does not take agents,
metrics or hardware: update the hub"), or a machine whose `export_dropped` is above zero ("N entries left out of <machine>'s export", with the export command to try). Each has a "Copy fix command": `wb remote publish` labelled "run
on <machine>" for a stale machine, `wb self-update` labelled "run on <machine>" for an older WB,
`wb fleet status --filter=<owner/repository>` for a scan error, and the command of
REQ:remote-error-is-visible for a remote error.

#### REQ: home-charts

Serves J2 and J6. Home MUST show, as its very first section and above "Needs you", the section
"Throughput" with two charts drawn with Chart.js from the document's `throughput` block (REQ:throughput-block):
"Finished per day" over the last 30 days (stacked bars of `finished` and `dropped`) and
"Time to finish" (the slowest five finished tasks named, with the `median_seconds` and
`p90_seconds` in the caption). They are non-linking
(REQ:every-number-is-a-link). Each chart
has a text alternative, a visually hidden table of the same numbers, and uses the theme's colours
in light and dark. The section's heading and a slot that keeps the charts' height at the current
breakpoint are part of the first page; the charts and Chart.js are a lazy chunk requested as soon
as the section is created, so they arrive right after the first paint and nothing below them moves
(REQ:initial-script-size and REQ:look-layout hold). On a phone the section stays on
top, not behind "more", and is dense: each chart's legend or caption is on its title's line, the
cards are close together and tightly padded, and the plots are short, so that at 375 x 812 px the
"Needs you" heading is at or above y 400 and both charts are readable. While it is not yet known
whether there is a block (no document yet, or the daemon's first scan is still running) the section
is its heading and an empty slot of the height of the charts, so the common case causes no shift
when the first complete document arrives. When a complete document has no block there are no
charts and Throughput is not on top: the slot is removed, the top of Home is "Needs you", and the
section is the one calm line "No charts: the daemon reports no throughput" at the bottom of Home,
below "Fleet health" (shown only after the first scan, like the sections around it). It is one
component placed by that condition. The sections after Throughput keep their order: Needs you, Ready to land, In flight,
Resume, Cleanup, Fleet health.

#### REQ: home-phone

Serves J1 and J2. At a viewport 360 px wide, in hosted mode as well, Home MUST show
sections 1 to 3 as cards and sections 4 to 6 behind "more"; the Throughput charts, when there are
any, stay on top, compact (REQ:home-charts). Every
other page MUST merely not break at that width (REQ:responsive-to-360).

### Repositories

#### REQ: repository-identity

A repository's identity is its lower-cased `owner/name`; the host is a tie-breaker only when both
rows carry one. The daemon emits `name` as `owner/name` and `host` as a separate field for every
entry, local and cached. A snapshot's repository name is split into `host` and `name` when it has
three or more `/` segments and the first contains a dot; otherwise `host` is omitted and the name
is taken as it is. A local row and a cached row that share the identity therefore merge into one
row.

#### REQ: repositories-list

Serves J5. The Repositories page MUST show one row per repository identity, with
these columns: Repository; Machines (a chip per machine, each linking to that
machine's checkout and, for cached data, showing its age and a stale mark);
Worktrees; Branches (local / remote); Agents; PRs; Code index (the worst state
across machines); Last activity; and an actions cell. The page header has a
one-click segmented switch, Recent, Most worktrees and Most branches, which are
sort presets (`sort=activity`, `sort=worktrees`, `sort=branches`) held in the
address. The actions cell holds two icon buttons, each with an `aria-label` and a
tooltip: browse code (a code icon, shown only when a code browser is configured,
per [cockpit](../cockpit/README.md)#req:code-browser-link) and open on host (an
external-link icon, to `remote_url_web`, shown only when that field is present),
with `rel="noopener noreferrer"` on every external link. There is no text "Code"
link. The chip `index` means the code index is `stale`, `diverged` or `failed`; the
chip `errors` leaves repositories with a scan error. The other quick filters are
those of REQ:filter-vocabulary. That is eight columns and an actions cell, which the page
declares with a priority each (REQ:default-columns-are-few): a list wide enough for all of
them shows all of them, and a narrower one (a panel beside it, a tablet) drops the quietest
first, Agents then PRs, whose counts and links are then the panel's, and then more.

#### REQ: repository-detail

Serves J5. `/repositories/:host/:owner/:name` is the full-page version of the
repository panel: a merged header; one section per machine with that checkout's
facts, worktrees and branches; the code-index panel; the README for an owner, as
[cockpit](../cockpit/README.md) defines, read in parts so a long one never holds the page still (the first 64 KiB
is drawn at once, and a "Show the rest" button draws the remainder one part at a time with the page free in between);
and the "Raw data" block. Repository ids
that worked before this Feature keep working. Branches load lazily from
`GET /api/v1/cockpit/branches` when the page or panel opens, and while they load
the section shows skeleton rows. The first machine's section is open, and reads its
checkout's branches, when the page or panel opens; another machine's section reads its own
when it is opened.

### Worktrees

#### REQ: worktrees-list

Serves J5 and J6. The Worktrees page MUST show these columns in order: Worktree,
the identity cell, showing the task in strong type and the repository
`owner/name` in muted type, with a small link on the task part to the task page;
Branch (shown when any visible row's branch differs from its task);
Machine (the name alone for this machine; for a cached or remote machine the name
and one unbreakable chip with the age, "stale" and the transport, as `vm · 19 m · ssh`,
the details in its `title`); State (the owner state plus sync badges `↑n` for unpushed commits, `↓n`
for commits behind, and "gone" for a vanished upstream); PR; Code index; Last
activity. A click on a row selects it and opens its panel; a small button at the
row end, shown on the hovered or focused row and always on a touch screen, and the
key `o` open the worktree page that [cockpit](../cockpit/README.md) defines. There
is no separate Task, Source or Lifecycle column (the route is in the machine chip,
the lifecycle in the panel), and a column that is empty or uniform for every
visible row, such as Machine on a fleet of one machine, is hidden. When the list is
narrower than its columns need (a panel open beside it, a narrow window), columns are hidden
by priority, never squeezed all alike: Code index and Branch first, then PR, then Machine;
Worktree, State and Last activity always stay, and what is hidden is still in the panel. The
`idle` owner state is plain muted text, so the states that need a look stand out. The sync badges and the chips
`unpushed` and `gone` and their counts concern this machine only and say so. The
quick filters are those of REQ:filter-vocabulary. The PR cell, the chip `pr`, the
worktree's side panel and the pull requests listed on its page all read one worktree-to-pull-request
join: a pull request that names a worktree of the snapshot belongs to that worktree and to no
other, and one that names none (or one that is not in the snapshot) belongs to the worktrees
that have its repository entry and its branch.

### Agents

#### REQ: agents-list

Serves J1. The Agents page MUST show each agent as a human label — runtime, model
and what it works on (its task or repository) — with an activity badge (`working`,
`blocked`, `idle`, `done`, or "state not reported" when the field is absent), its
machine and how long it has run. Its session or run id is not in the row (an
identifier is not what an agent is doing): it is in the panel, with a copy button.
For a session with no link to work the row reads "<runtime> <model>" and, muted,
"session, started <age>". An agent from another machine is shown with the age of
its snapshot and no actions. For a session that cannot be controlled the side
panel says so plainly. The quick filters are those of REQ:filter-vocabulary.

#### REQ: agent-detail

Serves J1. `/agents/:id` is the full-page version of the agent panel: identity,
activity, machine (a link), repository, the worktrees and branches it works on,
its task (a link), the pull requests of those worktrees, and the "Raw data"
block.

### Machines

#### REQ: machines-list

Serves J7. The Machines page MUST NOT have a separate section heading plus a
"Machine" column: the first column header reads "Machines" and is the section
title, in larger type. It has a filter box and the chip `stale`. Each machine name
links to its page. The columns are Machines; State (live or cached with its age,
stale marked, a remote error as a quiet warning with its text); WB version (marked
when older than the newest in the fleet); Repositories; Worktrees; Agents; Load; CPU; Memory. Load is the free, busy or
unknown verdict, CPU and Memory are bars of the latest sample (its route and age are
the cell's tooltip); they are columns past the default seven, each with a
declared priority, so they are the first to go when the list is narrow or a panel is
open (REQ:default-columns-are-few). A machine that reports no metrics reads unknown,
with no bar and no zero. The chip `outdated` leaves the machines
running a WB older than the newest in the fleet.

#### REQ: machine-detail

Serves J7. `/machines/:id` is the full-page version of the machine panel: a
summary (OS, architecture, CPU count, WB version, state age and, when reported,
uptime derived from `boot_time`); metrics charts over the last hour — CPU percent,
load, memory used against total, and free disk on the projects root — marked with
the route of the data (`local`, `live-remote` or `cached`) and its age; counts that
link to the filtered lists; the machine's running agents; its most recent
worktrees; and the "Raw data" block. A machine that reports no metrics MUST say
"metrics are not reported for this machine" instead of showing zeros, and one with
only a cached latest sample shows that sample with its age and no history chart.

#### REQ: machine-metrics-polling

Serves J7. The application MUST read a machine's samples from
`GET /api/v1/cockpit/machine-metrics?machine=<id>` and poll it every 10 seconds only
while Home or a Machines page is visible, for the machines shown.

### Control surface

The Cockpit is a control panel for the fleet, not only a screen to read. The
action registry, authorization, preview and execution are specified by
[cockpit-actions](../cockpit-actions/README.md) and
[work-loss-risk](../work-loss-risk/README.md); this section specifies where and
how actions appear in the views. Nothing in this Feature executes or changes
anything: every control here opens a preview, copies text, or reads, and every
execution path is a `cockpit-actions` route. There is no generic multi-selection
or bulk bar, because the approved `cockpit-actions` Feature excludes them.

#### REQ: action-slots

Serves J1, J2, J4, J6. Every side panel, every detail page header and every Home row MUST have
an action area that renders exactly what the registry returns for that entity from
`GET /api/v1/cockpit/actions?target=<type>:<id>`: each action's title, enabled or disabled, and
for a disabled one the registry's reason. There is one slot per pull request or worktree (the
registry's target types), not per task: where a task row shows actions, it shows the list of its
pull requests' or worktrees' slots. The application does not hardcode which actions exist, and
how a direct button and an overflow menu are chosen is defined entirely by
[cockpit-actions](../cockpit-actions/README.md)#req:common-actions-are-direct. When the registry
route is absent or returns no action for the entity, the action area is not rendered at all: no
disabled placeholder and no gap in the layout. An entity kind with no registry target type
(task, agent, machine) has no action area of its own, and an entity on another machine has
none. A slot shows a live button only where its page gives it a handler that runs the action; with no handler
(Home, whose rows only point at work) the slot shows the matching Copy entry instead, never a button that does
nothing.

#### REQ: copy-the-command

Serves J1, J2, J3, J6. Independently of the registry, and available to `anonymous-local` readers
because it executes nothing, each entity's side panel and detail header MUST offer "Copy
command" entries with the exact equivalent WB CLI invocation, only from this list. Every
template exists in the command manifest (`ai/capabilities.json`) with the flags it needs, and a
unit test parses every template against that manifest:

- worktree and task: `wb worktree list '<task>'`; `wb pr create '<task>' --commit-all
  --message='<message>'`; `wb worktree cleanup '<task>'`, the dry-run plan, never with `--apply`;
- pull request: `wb pr land '<owner/repository>#<number>'`;
- repository: `wb worktree create '<task>' '<owner/repository>' --model='<model>'
  --original-prompt-file='<file>'` (that verb requires both flags); `wb branch list
  --repo='<owner/repository>'`; `wb fleet status --filter='<owner/repository>'`;
- branch: `wb branch list --repo='<owner/repository>' --branch='<branch>'`; `wb branch cleanup
  --repo='<owner/repository>' --branch='<branch>'`, a dry-run plan that never carries `--apply`;
- a dispatched run: `wb agent status '<agent-id>'`, `wb agent logs '<agent-id>'` and `wb agent
  stop '<agent-id>'`; a recorded successor session: `wb session send '<wb-session-id>'
  --message='<message>'`; no entry for any other session.

Only the commands of this machine change anything: a command that creates, changes or stops something (`wb pr
create`, `wb pr land`, `wb worktree create`, `wb agent stop`, `wb session send`, the dispatch forms) is offered
for an entity of this machine only, because the SSH form of it would act on a checkout the operator did not choose;
for an entity of another machine the panel offers the read-only entries (`list`, `status`, `logs`) and says in
words to run the changing command in a terminal on that machine. The command builders enforce this: each one that changes
something takes the entity's target (where it runs) as a required argument and refuses another machine's, so no page, Home's
"In flight" and "Needs you" included, can build one without saying whose entity it is for. A pull request another machine reported has no
land command. A blocked session's next step names `wb session send` with the message left to edit, and a session
that is not blocked, or has no recorded id, has no send entry. A button that copies reads "Copy" (a command with a
part to edit: "Copy template"), and its accessible name begins with that word, then the verb and what it is for
("Copy wb pr land: Land acme/cli#7"). A command written for the SSH route whose value the remote shell would split
again is quoted twice, and the entry says so in a hint under it. A long command wraps inside its entry, and scrolls
there past about six lines, and never widens the page.

Every interpolated value is POSIX single-quoted (an embedded `'` is written `'\''`), and flags
are written `--flag=value`. A placeholder is written `<<<edit:name>>>` (`<<<edit:message>>>`, `<<<edit:model>>>`,
`<<<edit:file>>>`, `<<<edit:profile>>>`, `<<<edit:task>>>`, `<<<edit:brief>>>`,
`<<<edit:hub-url>>>`), bare and never quoted, for example `--message=<<<edit:message>>>`. It is a
shell syntax error wherever it stands (first word, after `--flag=`, between two words, last, in
bash, zsh and POSIX sh): `<<<` opens a here-string and `>>>` is a redirection with no target. The
shorter `<<edit:name>>` is not enough, because `>>` takes the next word as its file and the line
parses. Pasting an entry unedited therefore fails to parse and never runs, and the entry is
flagged as needing an edit for the interface to say so and to mark exactly these tokens; the
`'<...>'` forms in the list above stand for a quoted value or, until the operator supplies it, a
bare placeholder. A value that contains a control character (including U+061C, U+200B to U+200F, U+2028, U+2029,
the bidirectional controls and U+FEFF), or that starts with `-`, is never interpolated: the
entry is refused and says why (a brief may hold line breaks and tabs, nothing else of that). For an entity on a machine that
has an SSH route (REQ:remote-ssh-fetch) the copied text is `ssh <user>@<host> <wb_path>
<command>` (just the host when the configuration has no user), built from the same configuration with each token shell-quoted as one argument; for
an entity on any other machine the command is labelled "run on <machine>". The SSH routes come
from the session response's owner-only field `machine_routes` (a list of `machine_id` and `ssh`
with `host`, an optional `user` and `wb_path`, which is `wb` when the configuration names no path;
one element for each id a machine with an `ssh` section has in the fleet document, whether or not
the daemon reads that machine and whatever `cockpit.remote_ssh` says, because nothing is run with
them), which the daemon emits only to an owner session
([cockpit](../cockpit/README.md)#req:anonymous-local-reads-metadata-only never includes it); an
anonymous reader has no route, so every entity on another machine is labelled "run on <machine>". Machines and the
code-index refresh have no entry because the manifest has no command for them that the read
model's identifiers can fill, and no entry gets a worktree into its location, because the
manifest has no verb that prints or opens a worktree's location from a task name
(`wb worktree info` takes a path); paths stay out of the read model. The copied text contains
only identifiers already in the read model and never a filesystem path of the fleet (a worktree's,
a repository's). The one exception is the `<wb_path>` of the SSH form: it is the owner's own
configured value from `session_move.targets.<machine>.ssh`, it is not in the read model, and it is
sent only in an owner session's `machine_routes`. An owner session that reaches the daemon through
a tunnel or a proxy is still an owner session and receives the routes; the anonymous principal
never does, forwarded or not.

#### REQ: owner-gating-is-visible

Serves J1 and J2. For a session with no action capability (an `anonymous-local` reader) the
application MUST NOT show an action it cannot run, not even disabled, and MUST NOT ask the daemon's
action registry for actions; wherever an action would be it MUST show the "Copy command" that the read
model can write for it (REQ:copy-the-command), so the reader still sees what could be done and
nothing is a dead button. It MUST offer one affordance in the session chip reading "Sign in as
owner: run `wb cockpit`" and MUST NOT add a separate message to each button. For a session that has
an action capability, the registry's actions MUST show in action slots, a refused one disabled
with the registry's own reason, wherever a handler runs them (REQ:action-slots).

#### REQ: new-task-form

Serves J3. The "New task" button MUST open a form, the route `/tasks/new`, with a repository picker that uses the
wildcard matcher and offers only names that match `[A-Za-z0-9._-]+/[A-Za-z0-9._-]+`, a task name,
a brief (the text of the task prompt, several lines allowed), an optional base branch, a model and an optional agent profile, and MUST produce the exact commands to copy, with the quoting
and refusal rules of REQ:copy-the-command. `wb agent dispatch --new-worktree` creates the worktree itself, and
`wb worktree create` never reuses one that exists, so the two are alternatives, never a sequence: with a brief the
form gives the dispatch command of each repository and nothing before it (the model is not asked for); without a
brief it gives the creation command alone. The creation command is `wb worktree create '<task>' '<owner/repository>'...
--model='<model>' --original-prompt-file='<file>'` (both flags are required by that verb, so the
model is required in the form, `unknown` being the verb's explicit value, and the prompt file is a
placeholder for the operator) with `--base='<branch>'` when given, and the dispatch form `wb agent
dispatch --repo='<owner/repository>' --task='<brief>' --profile='<profile>'
--new-worktree='<task>'` (`--task` is the text of the task prompt, not the task name, which is the
worktree) with `--base='<branch>'` when given; the model is not passed to dispatch,
which has no such flag, and the profile is the form's field, a placeholder (`<<<edit:profile>>>`) while it is empty, because profiles are named in `wb.yaml`,
not in the read model. The form runs nothing. Running it from the Cockpit, Stop, Log and Reply on a
run, and the cleanup flow are follow-ups to be specified in `cockpit-actions` (see Open
Questions).

#### REQ: intent-to-done-budgets

Serves J5. The palette MUST be visible with no network request and no route navigation before it
appears, and an action slot MUST open its preview without a route navigation (the preview itself
is `cockpit-actions`'). Both are measured with a fake registry. The palette's code is a lazy chunk
that the shell fetches when the browser is idle after the first render (an idle callback with a timeout), and it
is not part of the code needed to render the first page (REQ:initial-script-size); once it has arrived, opening
the palette makes no request, and a palette opened before then waits for that one fetch. Operation feedback and action
results in the palette are specified by `cockpit-actions` (its Task 8).

### Read-model contract v2

These requirements change the fleet document and add routes. The closed list of
anonymous-readable fields is defined by
[cockpit](../cockpit/README.md)#req:anonymous-local-reads-metadata-only, which is
updated for every field below; none of them carries file content, paths,
environment, command lines or process lists.

#### REQ: field-tables

Every entry kind has the fields below; every REQ and AC of this Feature that names a field cites
this REQ. Types are JSON types; "opt." means the field may be absent; the last column says
whether it is present for entries of this machine (local), for entries of another machine
(cached or live-remote), or both. An unknown or out-of-set value from another machine is dropped,
not rendered. Existing field names are not renamed. Times are RFC 3339 strings. A `live-remote`
entry is the exporting machine's own local entry, validated and re-mapped
(REQ:remote-entries-replace-cached), so it carries the fields marked "local only" as that machine
observed them; "local only" excludes `cached` entries, whose published snapshot does not hold them.
What a `live-remote` entry says (`ahead`, `behind`, `has_upstream`, `checks_green`, `mergeable`, a
pull request `state` of `merged`, a worktree `lifecycle`) is that machine's report, not an
observation of this daemon. `route` is the marker of whose word a field is, and it cannot be forged:
no entry with a route other than `local` ever carries an id of one of this machine's entries or this
machine's `machine_id`, whatever ids the other machine sent. A client therefore decides the state of a
task that has an entry of this machine from its `local` entries alone, and shows any other entry's
facts as reported by its machine.
A link of another machine's entry (`cached` or `live-remote`) is never taken as that machine sent it:
a pull request's `url` is rebuilt by this daemon from the host of its repository (or, for a
repository published with no host, the host of the address sent), the repository's `owner/name` and
the number, as `https://<host>/<owner>/<name>/pull/<number>`, and is kept only when that host is the
host of a repository of this machine and the link rebuilt is exactly the sanitised address that was
sent (so a forge whose pull request addresses have another shape has no link, never a wrong one, and
a sender cannot choose a path); a live-remote repository's `remote_url_web` is kept only when its
host is the host of a repository of this machine; otherwise the entry has no link.

**Every entry** (machines, repositories, worktrees, pull requests, agents): `id` string; `machine`
string, the machine's name; `machine_id` string, the id of its machine entry; `route` string,
`local`, `cached` or `live-remote`; `observed_at` time, opt. (the snapshot's time for a cached or
live-remote entry; for an entry of this machine, the time of the pass that produced the published
document, see `snapshot_at`). Both.

**Document**: `schema_version` int (2); `snapshot_at` time (when the published document was
taken: it moves when the document's content does, not on every pass, REQ:compressed-responses); `warming_up` bool;
`repositories_total` int; `repositories_scanned` int; `diagnostics` int; `error` string opt.
(a code); `code_index_provider` string opt.; `refresh_interval_seconds` int; `throughput`
object opt. (below); `agents_truncated` bool opt.; `pull_requests_throttled` bool opt.; the collections `machines`, `repositories`,
`worktrees`, `pull_requests` and `agents`. There is no `branches` collection and no `metrics`.

| Machine field | Type | Opt. | Source | Local/cached |
|---|---|---|---|---|
| `wb_version` | string | opt. | daemon / snapshot | both |
| `repository_count`, `worktree_count` | int | no | counted | both |
| `os`, `arch` | string | opt. | daemon / snapshot | local, and cached when published |
| `cpu_count` | int | opt. | daemon / snapshot | as above |
| `boot_time` | time | opt. | daemon / snapshot | as above |
| `transport` | string `http`\|`ssh` | opt. | the live-remote exporter | live-remote only |
| `remote_error` | string, a code of REQ:remote-error-is-visible | opt. | the live-remote exporter | live-remote and cached |
| `export_dropped` | int | opt. | the number of that machine's entries its export left out (REQ:cockpit-export-verb) plus those this daemon cut at its caps (REQ:remote-entries-replace-cached; for a published snapshot, REQ:remote-envelope-is-untrusted); set only by the reader, refused in an export | live-remote and cached |
| `publish_error` | string, `collect_failed`\|`store_unavailable`\|`publish_failed`\|`optional_fields_dropped` | opt. | the periodic publisher's last diagnostic; absent when healthy and when publishing is off | local only |
| `agents_truncated` | bool | opt. | that machine's agents were cut, by it, by its publisher or by this daemon's cap; carried by that machine's entry only | live-remote and cached |

| Repository field | Type | Opt. | Source | Local/cached |
|---|---|---|---|---|
| `host` | string | opt. | origin host; split from a snapshot name (REQ:repository-identity) | both |
| `name` | string, `owner/name` | no | repository slug | both |
| `default_branch` | string | opt. | Git | both |
| `worktree_count` | int | no | counted | both |
| `local_branch_count`, `remote_branch_count`, `open_pull_request_count`, `active_agent_count` | int | opt. | counted | local only |
| `last_activity_at` | time | opt. | newest local-branch activity | local only |
| `remote_url_web` | string | opt. | host and `owner/name` (REQ:repository-activity-fields) | local only |
| `error` | string, a code | opt. | scan | local |
| `code_index` | list | opt. | receipts and provider | local only |

| Worktree field | Type | Opt. | Source | Local/cached |
|---|---|---|---|---|
| `repository` | string, an entry id | no | mapping | both |
| `task` | string | no | claim / record | both |
| `name` | string, equal to `task` | no | mapping | both |
| `stream` | string | opt. | task parent | both |
| `branch` | string | no | record | both |
| `lifecycle` | string, `working`\|`review`\|`merged`\|`superseded` | opt. | the snapshot; locally only if the claim or record already holds it | cached; local if available |
| `owner_state` | string, `active`\|`idle`\|`orphaned`\|`unknown` | opt. | REQ:owner-state-vocabulary | both |
| `last_activity_at` | time | opt. | heartbeat or last commit | both |
| `ahead`, `behind` | int | opt. | branch sync | local only |
| `upstream_gone`, `has_upstream` | bool | opt. | branch sync | local only |
| `code_index` | list | opt. | receipts | local only |

| Pull request field | Type | Opt. | Source | Local/cached |
|---|---|---|---|---|
| `repository`, `worktree` | string, entry ids | opt. | mapping | both |
| `branch` | string | opt. | binding | both |
| `number` | int | no | binding | both |
| `state` | string, `open`\|`merged`\|`closed`\|`draft` | opt. | the watcher; a snapshot's value only if in the set | both |
| `url` | string | opt. | emitted only when `https` with a host of ASCII letters, digits, dots and hyphens that is not an IP literal or `localhost`, at most 2,048 characters, no port and no user information (a GitHub Enterprise address with a port therefore has no link, by design) | both |
| `mergeable` | string, a closed enum (`clean`, `blocked`, `dirty`, `behind`, `unstable`, `has_hooks`, `draft`, `unknown`) | opt. | the watcher | local only |
| `checks_total`, `checks_passed`, `checks_failed`, `checks_skipped`, `checks_pending` | int | opt. | the watcher | local only |
| `checks_green` | bool | opt. | the watcher's verdict | local only |
| `failed_check` | string, at most 100 characters, control and bidirectional characters removed | opt. | the first failing check | local only |
| `checked_at` | time | opt. | the watcher | local only |

| Agent field | Type | Opt. | Source | Local/cached |
|---|---|---|---|---|
| `kind` | string `session`\|`run` | no | mapping | both |
| `session_id`, `run_id` | string | opt. | record | both |
| `runtime`, `model` | string | opt. | record | both |
| `state` | string: a session `live`\|`parked`; a run `running`\|`completed`\|`failed`\|`timeout`\|`abandoned` | no | record | both |
| `activity` | string `working`\|`blocked`\|`idle`\|`done`\|`unknown` | opt. | herdr; the snapshot's value only if in the set | local, and cached when published |
| `repository`, `task` | string | opt. | run record; for a session, the declared owner process of its worktrees | both |
| `worktrees` | list of entry ids | opt. | as above | both |
| `started_at` | time | opt. | record | both |
| `finished_at` | time | opt. | a finished run's record | both |
| `exit_code` | int | opt. | a finished run's record | both |

A client's notion "running" is derived, not a field: a session `live` or a run `running`. The
agents of one machine are capped at 200 at read and at publish, and every string is length-capped.

**Throughput** (`throughput`): `window_days` int (30); `per_day` list of `date` string
(`YYYY-MM-DD`), `finished` int, `dropped` int and `landed` int opt. (above zero only);
`slowest` list of at most 5 of `task` string, `duration_seconds` int and `landed_at` time (the
time the task was sealed); `median_seconds` int opt.; `p90_seconds` int opt.; `capped` bool opt.
Local only.

**Metrics payload** (`machine-metrics`): `machine` string, the machine id (not its name); `route`
string, `local`, `live-remote`, `cached` or `none`; `fetched_at` time, opt. (`live-remote` only);
`samples` list, at most 360, oldest first; `reason` string, opt., one of `no_source`, `unsupported`
`unavailable` or `stale` (a published sample older than 24 hours). A sample has `sampled_at` time (not in the future, strictly later than the sample
before it) and these measurements, each optional (a part that could not be read is absent, never
zero): `cpu_percent` number (0 to 100), `load1` number (0 or more), `memory_used_bytes` and
`memory_total_bytes` ints, `disk_free_bytes` and `disk_total_bytes` ints (each pair present
together, used not above total, free not above total). `cpu_percent` is absent on the first
sample, when the counters did not advance or went backwards, and where sampling is unsupported.
Memory used is one rule on every platform: the total less the memory that is available without
swapping, which is `MemTotal - MemAvailable` on Linux and the total less the free and inactive
pages on macOS (gopsutil's `Available`), so a healthy machine with a large file cache does not
read as busy. The route sanitizes every answer, whatever its source, before serving it.

#### REQ: compressed-responses

The fleet document, the new JSON routes and the static assets MUST be served
gzip-compressed when the client sends `Accept-Encoding: gzip`. The ETag is
strong and specific to the encoding (`"<hash>-gzip"` for the gzip body),
`If-None-Match` accepts either form and a match is answered `304`, and every
such response carries `Vary: Origin, Accept-Encoding`. The gzip bytes of the
fleet document are computed once when the snapshot is stored, never per request.
A snapshot is stored only when it says something the stored one does not. A pass, or a side read
(the other machines' snapshots, the agents' activity, the pull-request records, the throughput
scan, an export of another machine), that finds the fleet as it was publishes nothing: the body,
its `snapshot_at`, the `observed_at` of this machine's entries and the ETag stay as they are, so a
client that polls a quiet fleet is answered `304` and not a full body. What "the same" ignores is
only when the document was taken (`snapshot_at` and the `observed_at` of this machine's own
entries); every other time is content (a pull request's `checked_at`, another machine's
`observed_at`). The freshness a client needs is not in the body: every answer of the fleet route,
a `304` included, carries the response header `X-Wb-Cockpit-Checked-At`, the RFC 3339 time at
which the daemon last assembled the document and found the published one current (or published a
changed one).
Static assets are compressed at build time; content-hashed assets carry
`Cache-Control: public, max-age=31536000, immutable`; the index document, which
has its nonce substituted per response, stays `no-cache` and is compressed per
response.

#### REQ: hosted-origin-conditional-requests

The two new GET routes are deliberately readable by the configured hosted
origin, as the fleet document is. For that origin the responses MUST expose
`ETag` and `X-Wb-Cockpit-Checked-At` and the preflight MUST allow `If-None-Match`, so that conditional
requests and `304` work from the hosted page and it can read how fresh what it holds is.

#### REQ: lazy-branches-route

The `branches` collection MUST NOT be part of the fleet document. The route
`GET /api/v1/cockpit/branches?repository=<id>` returns the branches of that
repository checkout, with the same access class as the fleet document:
metadata, readable by `anonymous-local`, and carrying `branch.read`. It is
served from the last snapshot and never runs Git on the request path. The
document keeps the per-repository branch counts. A known repository id whose
entry is cached from another machine is answered with status 200, an empty list
and a `reason`; an unknown id is answered with status 404 and no data.

#### REQ: repository-activity-fields

A repository entry carries `last_activity_at`, the newest local-branch activity time, and
`remote_url_web`, the `https://<host>/<owner>/<name>` address built from the repository's forge
host and `owner/name`; both only on an entry of this machine and omitted on a cached entry.
`remote_url_web` is emitted only when the host matches a hostname pattern and every path segment
matches `[A-Za-z0-9._-]+` and is not `.` or `..`; otherwise it is omitted. No other new data class
is introduced; the origin URL itself is never in the read model.

#### REQ: owner-state-vocabulary

The daemon normalises `owner_state` to `active`, `idle`, `orphaned` or `unknown`, or omits it, on
every route, and drops any value from another machine's snapshot outside that set. For a worktree
of this machine it is the owner-process-liveness derivation that the published remote snapshot
already uses (`worktreeclaims.WorktreeOwnerState`): `active` when an owner process is recorded and
alive, `orphaned` when one is recorded and gone, and otherwise `unknown` when no owner process is
recorded; `idle` is never derived locally and is kept only for a snapshot that publishes it. The
heartbeat is not used. The liveness is read once per snapshot and cached for it; the contract task
reports the measured cost. Today `internal/cockpit/fleet/mapping.go` derives only `active` or
`idle` from the heartbeat and the published snapshot never carries `idle`, so the mapper changes.
`unknown` is part of the at-risk condition of REQ:task-state ("not `active`") and of the Cockpit
"look" count.

#### REQ: worktree-name-and-sync-fields

A worktree entry carries `name`, which is its task, never a filesystem path, and the sync facts of
its branch: `ahead`, `behind`, `upstream_gone` and `has_upstream` (false when the branch tracks no
upstream), each omitted when unknown. The sync facts are present only for worktrees of the machine
the Cockpit runs on and omitted for cached ones. Its `lifecycle` is populated for a local worktree
only if the claim or record already holds it; if not, the field stays absent and the second arm of
row 7 of REQ:task-state is not used, which the contract task reports.

#### REQ: pull-request-fields

A pull request entry carries, in addition to `number`, `repository`, `worktree`, `branch` and
`url`, the fields of REQ:field-tables: `state`, `mergeable` (a closed enum, otherwise omitted),
`checks_total`, `checks_passed`, `checks_failed` (failed and cancelled), `checks_skipped` (counted
separately from passed), `checks_pending`, `checks_green` (the verdict of
`prsnapshot.Snapshot.Green`, which is not re-derived from the counts because it includes the
required-check policy), `failed_check` (the name of the first failing check, at most 100
characters, with control and bidirectional characters removed, and rendered only as text) and
`checked_at`. `url` is emitted only when it is `https` with a host of ASCII letters, digits, dots
and hyphens, no port and no user information. They come from the daemon's snapshotter, which runs
the existing watcher (`internal/prwatch`, over `internal/prsnapshot.Observe`) beside each refresh for
the pull requests that `worktrees.ListRegisteredPullRequestBindings` returns, with the credentials
WB already uses. Each pull request has its own cadence: one never observed is observed on the next pass (those of a
worktree this machine has before the others); one whose checks are pending, or which has no check
yet, after 90 seconds, stretched to `waiting x 3600 / (budget x 0.7)` seconds when that is longer, so
that the pending ones use at most 70% of the budget and the settled ones keep their share; one whose
mergeability is `unknown` likewise, until it has been unknown 5 observations in a row, after which it
is settled; one whose verdict is known (green, failed, blocked on a missing required check, draft)
after 10 minutes; one whose read failed after a backoff doubling from 2 to 30 minutes; one closed
without merging after 60 minutes, in case it is reopened. These cadences are the least time between
two observations, not a timer of their own: a pull request is observed by the first refresh at or
after the time it is due, so each cadence is rounded up to the next refresh
(`cockpit.refresh_interval`): with a refresh every 30 seconds a pending pull request is observed
every 90 seconds, and with one every 60 seconds every 120. A pass starts on a refresh, once the
worktrees are known, and observes the pull requests that are due, at most `cockpit.pull_request_limit`
of them (default 10, 1 to 200), the longest due first; each observation is bounded to 30 seconds, at
most 4 run at once, a pass whose context ends records nothing for what it did not observe and gives
its budget back, and a pass never delays the publication of the local snapshot. The observation is the
lean one (`prsnapshot.ObserveLean`: it does not read the annotations that explain a red head). It
reads the pull request once and, only if it is open, its check runs, its Actions workflow runs, its
commit statuses and the target's branch policy and active rules: 6 GitHub reads for an open pull
request (also when a required check is missing, which reuses those reads) and 1 for a merged or
closed one; both numbers are tested. The reads are conditional (ETag), and a 304 answer is not charged
to the rate limit. At most `cockpit.pull_request_hourly_budget` observations are made in any rolling
hour (default 120, 10 to 400): with the defaults that is at most 120 x 6 = 720 reads an hour in the
worst case (a seventh of the 5,000 an authenticated token has), the ceiling of 400 at most 2,400, and
about 360 for 10 settled pull requests (6 observations an hour each). When the budget cuts a pass
short, passes stop until the window frees, the document keeps the last values with their `checked_at`
and carries `pull_requests_throttled` so the application says how old the state is. A merged pull
request leaves the watch set after one confirmed observation; an observation that fails leaves the previous values and `checked_at` in place, so the
age shows. No request reads GitHub. When no observation has ever succeeded for a pull request, the
fields other than `number`, `repository` and `url` are omitted and the application says the state
is not reported. A pull request of another machine carries only what its snapshot published
(`number`, `url`, `state` if in the set).

#### REQ: agent-activity

An agent entry carries `activity` (a closed enum: `working`, `blocked`, `idle`, `done` or
`unknown`) when the daemon can read it: when herdr is available, the snapshotter lists herdr's
agents once per refresh (`herdr.Client.AgentList`, whose `Agent.Status` has exactly those five
values) and joins each to a registered session by the harness session id
(`Agent.HarnessSessionID`). With no herdr or no match the field is omitted and the application says
"state not reported". It is present only for agents of this machine. Screen text and logs are
content: they are owner-only and belong to `cockpit-actions`, not to this Feature. The daemon
reads herdr as a process of its own, with the environment it was started with: a daemon started by
launchd or systemd has no `HERDR_SOCKET_PATH` and so reaches only herdr's default server, and the
agents of any other herdr server have no `activity` (see Open Questions).

#### REQ: agent-fields

An agent entry carries `worktrees` (worktree ids), `task`, `repository` and `started_at`,
populated for a dispatched run from its run record (`agents.Result`: `Repository`, `Worktree`,
`Branch`, `StartedAt`, `State`, `FinishedAt`, `ExitCode`); a finished run also carries
`finished_at` and `exit_code`, never its free-text failure, and its `state` is `running`,
`completed`, `failed`, `timeout` or `abandoned`. A registered session has `state` `live` or
`parked`, and only `started_at` is populated, plus its worktrees, task and repository when a
worktree's declared owner process (the process id its Work Log journal records) is the process of
that live session (a claim records no session, so it links nothing) and the two records agree on
everything else both carry: the declared runtime and harness session id, and a process start time
(the owner's process cannot have started after the session registered), which is observed only on
Linux. Where neither record carries more than the process id, or the platform cannot observe a
start time, the process id and the owner's liveness are all there is to link by, which is a
limitation: a reused process id on such a machine can link a session to a worktree it does not
hold. Otherwise they are absent and are never guessed. A session whose worktrees name more than one task or repository carries none of
that one; at most 10 worktrees are listed. The agents of another
machine are capped at 200 per machine when read from a snapshot and when published.

#### REQ: machine-fields

A machine entry carries `os`, `arch`, `cpu_count` and `boot_time` (RFC 3339) for the local
machine, and for another machine when its published snapshot carries them; they are omitted
otherwise. The fleet document carries no `metrics`; the latest sample and the history come only
from REQ:machine-metrics-route. Uptime is derived from `boot_time`.

#### REQ: refresh-interval-field

The fleet document MUST carry `refresh_interval_seconds`, the daemon's snapshot
refresh interval, which the freshness chip uses.

#### REQ: derived-collections-memoised

The application MUST compute the merged repositories, the tasks with their
state, the "Needs you" items, the ready-to-land list and the cleanup counts once per
snapshot, memoised on the identity of the fleet document, not on every render.

#### REQ: periodic-remote-publish

The daemon MAY publish this machine's snapshot to the remote store after a successful local scan,
by the same publish path as `wb remote publish` (the same snapshot builder and the same provider,
`gitrepo` or `hub`; there is no second publisher). It is opt-in: it runs only when
`remote.publish.interval` is set (a duration, minimum 5 minutes: a shorter value is raised to 5
minutes, a negative one is a configuration error); with no value, or no remote store, nothing is
published, exactly as before, and a machine that only ever published by hand does not start
publishing because it was upgraded. A scan is successful when it listed the repositories and was not
cancelled (a repository that failed carries its error code in the snapshot, as in `wb remote
publish`). The publish runs off the request path and off the scan, in a goroutine of its own, at
most once at a time, under a bound of 5 minutes for the scan and the publish together, and never
more often than the interval. It is skipped when the snapshot says what the last published one said
(the same digest, apart from the publish time, the metrics sample, the agents' `activity` and a
worktree's last-activity time within 15 minutes), so a git store gains no commit for an idle machine
or for a heartbeat, except that an unchanged snapshot is published anyway once `max(6 hours, the
interval)` has passed, so an idle machine is not taken for a stale one (`wb remote machines` marks a
snapshot stale after 24 hours). A change of the agents alone (an agent started or ended, a state
changed; an `activity` flap is not a change) is held back until `max(interval, 15 minutes)` has
passed since the last publish, so agents add at most one commit per `max(interval, 15 minutes)`
(at most 96 a day), where any other change publishes at once.

The scan itself is gated: the publisher does not scan at all (the scan reads the git status of every
repository and duplicates the fleet snapshotter's) while the snapshotter's change token, made of
each clone's change fingerprint and the worktree and pull-request facts it reads on every pass (the
last-activity time at the same 15-minute granularity as the digest), and the digest of the agents
when they are published, are those of the last publish (or of the last scan that found nothing
changed) and the keepalive has not passed. For those fields the token moves exactly when the digest
would. A repository whose fingerprint cannot be computed opens the gate. A change that moves no
fingerprint and none of those facts (a new untracked file, or an unstaged edit of a tracked file,
which the snapshotter does not read) reaches the store with the next one that does, or with the
keepalive. A failed attempt never gates the next.

A scan that does run reads with Git only what changed. What it read of a clone (its status and
tracking) is kept for as long as the clone's change fingerprint stays the same and for less than the
keepalive, and taken again by the next scan, so a scan made because one repository changed reads
that one with Git and not the fleet; a read that failed, and a clone whose fingerprint cannot be
computed, are never kept. And when the snapshotter's change token is the one the last scan was made
for, and the oldest read that scan kept is younger than the keepalive (so nothing is ever published
that is older than one keepalive, not two), the attempt takes that scan again and runs none:
only the agents moved, which the scan does not read, so an agents-only change, and each attempt made
while one is held back, costs no scan. What is kept is bounded by the same rule as the gate: a
change no fingerprint sees reaches the store with the next change of that clone, or with the
keepalive, whose scan reads every repository. The worktree inventory is not kept: it reads state
that no fingerprint covers (heartbeats, manifests, owners) and is made anew by every scan.

A failed publish is a typed diagnostic (`collect_failed`, `store_unavailable`, `publish_failed` or
`optional_fields_dropped`, a code and never the text of an error), logged when it changes, shown as
`publish_error` on this machine's own entry (REQ:home-fleet-health) and absent when healthy or when
publishing is off. One rule governs it: a failure's code stays until the step that failed has worked
again, and no longer. A failure sets its code. `collect_failed` is cleared by the next scan that
works, whatever that attempt then does (publish, skip or hold back), and resets the failure count.
`store_unavailable` and `publish_failed` are cleared by the next publish that reaches the store,
and so that there is one, the attempt made while either stands is never gated, skipped or held back:
it publishes, even a snapshot that says what the last published one said. An attempt made while
`collect_failed` stands is never gated either. A publish that carried the optional fields clears
every code and resets the failure count; a publish that had to leave them out sets
`optional_fields_dropped`, which stays until a publish that carried them succeeds (a failure's code
takes its place while the failure stands, and it returns when a scan that works clears
`collect_failed`); a gated, skipped or held-back attempt never changes a code other than
`collect_failed`. A failed publish is retried after the interval, then after twice, four times and so on up to one
hour while it keeps failing; it never ends the daemon and never delays the local snapshot. After an
older hub's refusal has been remembered for 24 hours (REQ:remote-snapshot-agents-and-metrics) the
next attempt is forced to publish the full payload, bypassing the gate and the digest skip once, so
an upgraded hub is noticed within 24 hours. The scan reads two repositories at a time, where the
snapshotter reads up to eight (the CPU count, at most eight) and `wb remote publish` eight.
[remote-state](../remote-state/README.md)#req:remote-publish-periodic specifies the behaviour;
until the founder settles which remote store is the fleet's shared one (Open Questions), each
machine publishes to whatever it has configured. It stays as the fallback for machines without a
live route (REQ:remote-exporter-transports), whose live data replaces the published entries only
while fresh.

#### REQ: remote-snapshot-agents-and-metrics

The published remote snapshot MAY carry optional `agents` (at most 200 entries, each with
`kind`, `state`, runtime, model, task, repository name, `activity` when known, `started_at` and the
`session_id` or `run_id`) and `metrics` (the latest sample of REQ:machine-metrics-route), and its
machine entry MAY carry `os`, `arch`, `cpu_count` and `boot_time`. Agents are published only with
`remote.publish.agents: true` and metrics only with `remote.publish.metrics: true`; both default to
off, as does the interval of REQ:periodic-remote-publish. All are optional and `schema_version`
does not change: an older reader decodes the YAML snapshot without strict field checking and
ignores them. The hub provider's HTTP snapshot model (`api/githubapp/machinesnapshot.Snapshot`) is
decoded with unknown fields refused, so it MUST accept the same optional fields in the same task; a
publisher refused with status 400 by an older hub retries once without the optional fields (the
hardware, the agents and the sample) and records the diagnostic `optional_fields_dropped`; the retry
is made for `wb remote publish` as well, and the refusal is remembered for 24 hours for the life of
that provider (a daemon restart forgets it), during which the optional fields are left out at once. The fleet document shows another machine's agents as
`cached` with the age of the snapshot, with no actions, at most 200 for each machine, and sets
`agents_truncated` on that machine's entry (never on this machine's own document, and hidden with the
machine when a live read replaces it) when more valid agents were published or the snapshot says so.
Agent strings are validated, not repaired, by one set of rules shared by the publisher, the hub model
and this reader and equal to the export decoder's: the `session_id`, `run_id` and `runtime` match the
token pattern, the `model` the model pattern (so not a path), the `kind`, `state` (by kind) and
`activity` are closed lists, the `repository` is `owner/name`, and the `task` is a task name (letters,
digits, dots, underscores, dashes and slashes, not starting with a separator). An agent whose `kind`,
`state` or identifier fails is dropped, any other failing field is blanked, and an invalid agent
beyond the cap is not a cut. The publisher applies the rules before sending, so a hub's 400 is never
caused by its own values; it also publishes an explicit truncated marker (a boolean) when it cut
valid agents, blanks a `boot_time`, `started_at` or sample time in the future, and the hub model
refuses the same. The machine's published sample is the
`cached` source of REQ:machine-metrics-route, served through the same source seam as the live
remote one and after it, and only while the sample is at most 24 hours old: an older sample is
answered as `none` with the reason `stale`, never drawn as a current bar (the page shows the age of
a younger one).

What leaves the machine, by mode (nothing else is in the part this REQ adds, and none of it is a
path, a command line, an environment value, a prompt, a process list or a check's text):

| Mode | Added to the snapshot |
|---|---|
| Interval unset | nothing is published by the daemon |
| Interval set, `agents` and `metrics` false | `os`, `arch`, `cpu_count`, `boot_time` of the machine entry |
| `wb remote publish` (by hand, whatever the config) | the same `os`, `arch`, `cpu_count`, `boot_time`, which this command did not publish before; never agents or metrics. Its help says so, and the first real publish after the upgrade prints one line saying so |
| `agents: true` | and `agents`: for each of at most 200 local sessions and runs, `kind` (`session` or `run`), `state`, `runtime`, `model`, `activity` when herdr reports one, `task`, the repository's name as `owner/name`, `started_at`, and the `session_id` or `run_id`, every string cleaned of control characters and cut at 200 characters |
| `metrics: true` | and `metrics`: one sample, `cpu_percent`, `load1`, `memory_used_bytes`, `memory_total_bytes`, `disk_free_bytes`, `disk_total_bytes` and `sampled_at`, each measurement only when known |

The rest of the snapshot is what `wb remote publish` publishes today and is unchanged: the hub
provider publishes only its allowlist (no path, no projects root, no commit subject), and the git
provider's file is the owner's own private store, which carries the paths of its checkouts as it
always has, redacted by `remote.publish.unpushed` (`subjects` or `counts`) as before.

#### REQ: throughput-block

The fleet document carries an optional `throughput` block that reports what this
machine's sealed work records prove, and never guesses a merge: `window_days` (30),
`per_day` (a list of `date`, `finished`, `dropped` and, when above zero, `landed`),
`slowest` (at most five entries of `task`, `duration_seconds` and `landed_at`, the time
the task was sealed), `median_seconds` and `p90_seconds` (the median and 90th
percentile, nearest rank, of the finished durations in the window, absent when no task
finished) and `capped` (true only when a bound below cut the scan). A collector reads
this machine's sealed terminal records (`worktreeclaims.TerminalRecord`, the files
`<wb home>/worklogs/<task>/runs/<run>/terminals/<claim id>.json` of every WB home the
projects root resolves to, read-only) and counts each by its `worktree_disposition`, at
its `sealed_at`; the duration is `sealed_at` minus the claim's `recorded_at` (the claim
record carries no `created_at`: its `recorded_at` is the time the claim was made, kept
unchanged in the terminal). The Work Log retirement archives themselves
(`worktreeretire.Manifest` and `Receipt`) carry no timestamps and are not the source.

| `worktree_disposition` | Counts as | Why |
|---|---|---|
| `landed` | finished, and `landed` | cleanup proved the work is on its target, or `wb worktree log finalize --apply` declared it (work-log#req:terminal-disposition-vocabulary) |
| `removed` | finished | cleanup removed a worktree with no landing to record; before cleanup sealed landings, most merged work |
| `retired` | finished | retired after archiving |
| `recycled` | finished | the worktree was reused after the work |
| `discarded` | dropped | work thrown away |
| `superseded` | dropped | replaced by other work |
| `not_landed` | dropped | finalized as a failure |
| `orphaned` | dropped | its worktree was gone |
| `handoff` | neither | the work continues elsewhere |
| any other value | neither | not counted |

The rules of the block:

- The window is today and the 29 UTC days before it; `date` is a UTC `YYYY-MM-DD`;
  `per_day` lists only the days with a sealing, oldest first, and `slowest` the
  longest durations first (ties: later sealing, then name). Both are lists, never null.
- A task counts once on each day it was sealed, in the better category: `finished` wins
  over `dropped`, so a task sealed `removed` and `discarded` on one day is one finished
  task. `landed` is how many of that day's finished tasks were sealed `landed` (a subset
  of `finished`), absent when none. Cleanup seals a proved landing as `landed`
  (work-log#req:terminal-disposition-vocabulary), so the count is the landings of each day
  from the release that does so; records sealed before it are not rewritten and count as
  finished only. A task finished in several repositories counts once a day, appears once among
  the slowest and in the percentiles, with its longest finished duration.
- `slowest`, `median_seconds` and `p90_seconds` use finished tasks only, in the window.
- A record is usable only when its disposition is in the table (not `handoff`), it has a
  `sealed_at` and a `recorded_at` both after 1999, a `sealed_at` not before its
  `recorded_at`, a duration of at most ten years, and a task name (else its effort id) that
  is not empty after control and bidirectional characters are removed (the name is cut at
  200 characters). A sealing later than 60 seconds ahead of the clock is not counted.
- When no terminal record is usable the block is omitted and the charts of
  REQ:home-charts are not shown; no value is invented. A usable record outside the
  window keeps the block, with empty lists.
- The collector runs on the snapshotter's cadence but not on every refresh: it
  lists the records at most every ten minutes (or on the next refresh while a cap left
  records unread), keeps what it learns by each record's identity (path, size and
  modification time) and reads again only a record that is new or changed; when the
  UTC day changes it recomputes the window from what it holds without listing. It
  never runs on a request.
- It reads newest first: the candidates are sorted by file modification time, newest
  first, and a file older than the window plus one day is not read at all (a record's
  file is written when it is sealed, so its time is its seal time or later), so one pass
  covers the window. The bounds are safety only: at most 5,000 records read in a scan and
  20,000 known; either sets `capped` and is logged.
- Listing is cached per directory: a task's `runs` directory and each run's `terminals`
  directory are listed again only when their modification time moved or is within two
  seconds of the listing (a record is immutable, and sealing one, or starting a run,
  moves its directory's time), so an unchanged task costs two stats.
- The block is local only: a machine's throughput is read from that machine, so
  `NewEnvelope` never exports it and the strict decoder refuses an envelope that
  carries one.
- The collector is read-only: it never writes, creates or locks anything under a
  WB home, and does not follow a symbolic link.

### Machine metrics

#### REQ: metrics-sampler

The daemon MUST sample the local machine's CPU percent, one-minute load, memory
used and total, and free and total disk of the projects root every 10 seconds
into an in-memory ring buffer of 360 samples. Sampling is off the request path,
reads through an injectable source so unit tests need no real machine, and
compiles on Windows, where it may report that metrics are unsupported. A part of a
reading that fails is left out of that sample and the rest is kept; after three
consecutive readings that give nothing the route answers `none` with the reason
`unavailable` until one succeeds. A panic or a hung read in a source never ends or
blocks the daemon (stopping waits at most 2 seconds for a read in flight). Samples
are not persisted across a daemon restart.

#### REQ: machine-metrics-route

`GET /api/v1/cockpit/machine-metrics?machine=<id>` MUST return, for a machine id in
the fleet document, `{machine, route, fetched_at?, samples, reason?}` where `machine` is the machine id with the same
access class as the fleet document (metadata; capability `machine.read`). `route` is
`local` for this machine's in-memory history, oldest first, so that its last element
is the latest sample; `live-remote` for the history fetched in the background over HTTP or SSH from
another machine (REQ:remote-exporter-transports), with `fetched_at` (when this daemon received it); `cached`
for the single latest sample carried in that machine's published snapshot, with its
`sampled_at`; and `none` with an empty list and a `reason` for a machine with no
source or a platform where sampling is unsupported, with status 200. A sample has
`cpu_percent`, `load1`, `memory_used_bytes`, `memory_total_bytes`, `disk_free_bytes`,
`disk_total_bytes` and `sampled_at`, and nothing else (every measurement may be absent, never guessed or zero-filled). The fallback order for another
machine is live remote, then cached, then none, and the response says which it is. An
unknown machine id is answered with status 404. The route runs no request-time fetch.

#### REQ: cockpit-export-verb

Serves J7. Every machine MUST have a read-only CLI verb, `wb cockpit export --format json`,
that never opens a browser, never mints a login code and never starts a daemon. It finds
this machine's running daemon from the daemon record, and reads that daemon's fleet
document (with the request header `X-Wb-Cockpit-Export`, so that the read is not taken for a
person looking, REQ:remote-exporter-transports) and machine-metrics (the latest sample and the history) over the daemon's
loopback transport as the `anonymous-local` principal, the same read any local browser
tab makes. It asks the daemon for this machine's part of the document alone, with the fleet route's
`scope` query parameter: `scope=own` is the document with this machine's own entries only (exactly
the `fleet` of its export, prepared once for each published document) and `scope=machine` the same
with no entry but this machine's own machine entry, which is all the `--metrics-only` export reads
of the fleet. A machine whose daemon shows many other machines therefore never fails its own export
on the size of entries it does not export, and a metrics-only export, which another machine may run
every 30 seconds, does not fetch and decode the fleet. A scoped read is never demand for the other
machines, whoever makes it; the response header `X-Wb-Cockpit-Export-Dropped` carries the four
counts of what the daemon left out of it (repositories, worktrees, pull requests, agents), numbers
only; any other value of `scope`, and a daemon that does not know the parameter, answer the whole
document, which the verb reads as before, within the same bound. It prints the export envelope `{schema_version, machine, exported_at, fleet,
metrics}` to stdout, bounded at 8 MiB, containing only the anonymous-readable metadata
set of [cockpit](../cockpit/README.md)#req:anonymous-local-reads-metadata-only. The flag
`--metrics-only` omits `fleet`. When no daemon is running, or the daemon refuses an
anonymous read (`cockpit.anonymous_metadata: false`), or the export fails for any other
reason (an unreadable or unsupported daemon record, a daemon that answers an error or a body
this binary does not understand, an envelope that fails its own validation), it prints
`{schema_version, error}` with `error` `daemon_not_running`, `export_refused` or
`export_failed` and exits with the findings code 1, and starts nothing; the text is fixed and
carries no path, no error text of a dependency and no response body. `daemon_not_running` is a
connection that could not be made; a daemon that accepts the connection and does not answer within
the verb's 10 seconds, a read that is cancelled and a request that cannot be made are
`export_failed`. The recorded address is
dialled exactly, and only `127.0.0.1`, `::1` and `localhost` are accepted, with a port that is a
port number: a record that names anything else is `export_failed`, exit code 1, never a crash (exit
code 2 would be read by a remote daemon as a wb that has no such verb). The verb writes
nothing, and records no heartbeat or invoked-command marker. A daemon is "running" when its
recorded process is alive and, where the platform can observe it, started when the record says
(a recycled process id is `daemon_not_running`); on macOS liveness is asked of launchd with
`launchctl print` (read-only), so a daemon started by hand in the foreground, outside launchd, is
reported as `daemon_not_running` there: a stated limitation. The envelope is built by one function that the
hub route of REQ:hub-export-route also calls. That function leaves out an entry of this machine
that the envelope's own rules would refuse and counts it in the optional envelope field `dropped`
(int, absent when zero), so one odd entry cannot take a machine's export down: an entry with a
field that breaks its rule (a name over the length cap, a time in the future), one that breaks a
rule between entries (a repeated id, a web address not built from its host and name, a pull request
address off its repository's host, a null kinds list), and every worktree, pull request and agent
of a repository that was left out; a pull request whose worktree was left out stays without the
reference. The exporter says how many entries of each kind it left out, as numbers and never as
names: the daemon in its log when the numbers change, the verb on stderr. Only a machine entry that
is itself invalid still fails the export as `export_failed`. A daemon whose first pass has not ended
holds a partial fleet, which must never replace what a reader holds: the full export is then the
fourth typed error `warming_up` (the metrics-only export, which has no fleet, is made). A daemon
that could not list its repositories and holds none (`error` `repositories_unreadable` and
`repositories_total` 0, [cockpit](../cockpit/README.md)#req:no-fleet-scan-on-the-request-path) cannot
say what the machine has: its empty fleet is not exported in the machine's place, the full export
is `export_failed` on both transports (a reader shows it as `bad_payload` and keeps what it holds
while that is fresh), and the metrics-only export is made. Its capability row, command-coverage entry,
Agent Skill coverage and flag-matrix line are added with it.

#### REQ: remote-exporter-transports

Serves J1, J7. The local daemon MUST read another machine's export envelope in the
background, never on a request path, through one interface, `RemoteExporter`
(`Export(ctx, target, metricsOnly) (Envelope, error)`), with two implementations tried in
this order for each machine:

1. **HTTP (default)**, REQ:remote-http-fetch, used when the machine has an HTTP route
   configured, or is the configured hub.
2. **SSH fallback**, REQ:remote-ssh-fetch, used when no HTTP route is configured for the
   machine or when the HTTP attempt fails with a transport or auth error.

The HTTP failures that trigger the fallback are: a connection error, a timeout, status
401 or 403, status 404 (the remote wb is older and has no such route), status 429, any 5xx
status, and a redirect (never followed). A well-formed envelope that fails validation
(REQ:remote-envelope-is-untrusted) is `bad_payload` on either transport and never triggers
a fallback. Three answers of a hub are understood by their exact status and typed body and are not
fallback-class, because no other transport would be answered differently: 503 `warming_up` (while a
live view of that machine is still fresh it is not a failure: nothing is replaced, no `remote_error`
is set and the machine is asked again one interval on, with no backoff; once nothing fresh is held
it is the failure `remote_warming_up`, REQ:remote-entries-replace-cached), 403 `export_refused` (shown as `export_refused`) and 503 `export_failed`
(shown as `bad_payload`). After a fallback (HTTP failed and SSH answered) the daemon keeps using SSH
alone for that machine, for its fleet and its metrics-only exports, for a cool-down of
5 minutes from that export's start and then tries HTTP again; while on SSH the machine entry carries the HTTP
failure in `remote_error`, and a later HTTP success clears it. The cool-down is for an SSH that
answers: an export that fails within it ends it, so the next attempt asks HTTP first again, and
when HTTP and SSH both fail the entry carries the code of the last transport tried. An HTTP failure
that is not fallback-class (`bad_payload`, `export_refused`) does not try SSH: the machine was
reached and answered, and what it answered would be the same over SSH, where the export is built by
the same function.

The cadence is the scheduler's and follows demand, so that a daemon
nobody is looking at does not open a connection to every machine every minute. Who is demand is
decided once, by Cockpit's own classification of a request (its Host check, its Origin
classification and its session), and for both demands below, the fleet read and the metrics request:

| Reader | Demand for HTTP | Demand for SSH |
|---|---|---|
| nobody | no (keepalive) | no (keepalive) |
| the export verb's marked read (`X-Wb-Cockpit-Export`), whatever session it carries | no | no |
| the hosted page, a foreign origin, any request not on a loopback `Host` | no | no |
| an anonymous reader on this machine (loopback `Host`, canonical or no `Origin`) | yes | no (keepalive) |
| an owner session (the same, with a live session) | yes | yes |

A reader on this machine is a request on a loopback `Host` whose `Origin` is the canonical one or
absent; the hosted page reads the same metadata routes and is never demand, and neither is a
request through a proxy, which has no anonymous reading at all. An SSH export is a login with the
user's own key, which an agent that asks for approval of each use turns into a prompt, or a silent
signature, per login: so only an owner session's read is demand for SSH, and an anonymous reader,
which any page or process on the machine can be, raises the HTTP transport alone. For such a
reader a machine that has no HTTP route, or whose HTTP route is failing, stays on SSH's idle
keepalive, max(15 minutes, the refresh interval), exactly as with nobody looking; its live entries stay
for that keepalive, with their age and, where HTTP failed, with the HTTP failure as `remote_error`,
and HTTP is retried on its own backoff, a request and never a login. A cool-down on SSH (below)
holds only for an export SSH is open to. A metrics-only export uses SSH only while an owner
requested that machine's metrics within the last 60 seconds: it is never a keepalive.

A read of the fleet
document by a reader on this machine is recorded (it fetches nothing itself and takes no lock). While such a reader read
the fleet document within the last 5 minutes, a machine's fleet export runs once per snapshot
refresh interval, and never more often than every 30 seconds whatever `cockpit.refresh_interval`
says. With no such reader it runs as an idle keepalive every 15 minutes (or every refresh interval,
when that is longer). A read that finds an export due (the first reader after a quiet time) wakes
the background loop, which starts that one export at once, without blocking the request: the reader
is served what is held and gets the fresh entries one request later. The read the export verb makes
(REQ:cockpit-export-verb) is another machine's daemon, not a person, and is not demand: it carries
the request header `X-Wb-Cockpit-Export`, without which two machines that read each other would
keep each other in demand for ever. While a reader on this machine has requested
that machine's metrics within the last 60 seconds, a metrics-only export runs every 30 seconds.
Every export, of either kind, is a login to the machine, so no two exports of a machine start within
30 seconds of each other, whichever kind each is and whatever came of the first: a wake-up by a
read, a metrics-only export next to a fleet export, or a refresh interval that is not a multiple of
30 seconds cannot bring two logins closer than that.
When every transport fails the delay doubles up to 5 minutes; with no reader a failing machine is
retried no sooner than its keepalive. A refused SSH login (`auth_failed`, a refused host key
included) stays refused until a person repairs it, and every attempt is a line in the remote's
authentication log: it bars SSH for that machine, for both kinds of export, for a delay that doubles
up to 1 hour, and an export that SSH answers lifts the bar. The bar is SSH's alone: a machine that
also has an HTTP route is still asked over HTTP on the 5 minute backoff, and SSH is tried again at
the first attempt after its bar has passed. At the default 60 second interval the connections an
hour to one machine are, by reader:

| Reader | HTTP requests an hour (machine with an HTTP route that answers) | SSH logins an hour (machine read over SSH) |
|---|---|---|
| nobody, the hosted page, the export verb | 4 | 4 |
| an anonymous reader on this machine reading the fleet document | 60 | 4 |
| the same, with a Machines page polling the metrics | 120 | 4 |
| an owner session reading the fleet document | 60 | 60 |
| the same, with a Machines page polling the metrics | 120 | 120 |
| any reader, a machine whose SSH login is refused | (its HTTP route is asked every 5 minutes) | 1 |

A machine with both routes is read over HTTP while that answers and has no SSH login at all,
whoever reads. A slow or failing remote
never delays the local snapshot. Both transports yield the same strictly validated envelope
and the same merge. With neither transport configured for a machine, no request is made and
no process is started. `cockpit.remote_http: false` and `cockpit.remote_ssh: false` turn the
transports off; both are on by default for configured machines. No network-facing Cockpit
route is added for other machines, and the Cockpit routes keep refusing every non-loopback
host.

#### REQ: hub-export-route

A wb server that mounts a daemon-hosted hub MUST serve its own machine's export envelope at
`GET /v0/workbench/machines/export`, and with `?metrics_only=1` the metrics-only envelope of
REQ:cockpit-export-verb, built from its own daemon's state in process. The route is never served
by the hosted multi-identity service, only by a daemon-hosted hub, and exposes only this machine's
own export, never another machine's. It is authenticated only by a machine bearer credential
carrying the existing scope `machine_snapshot:read` whose identity equals the identity of the host
owner: a request with no bearer, a peer credential (`peer:session` alone), a credential without
that scope, a credential of another identity, or only a session cookie is refused with status 401
(403 for another identity), and no anonymous principal is accepted. It is not under
`/api/v1/cockpit/`, so the Cockpit `Host` guard and the rule that forwarded requests are never
anonymous ([cockpit](../cockpit/README.md)#req:forwarded-requests-are-never-anonymous) are
untouched. No new scope is added. A wb server that does not mount a daemon-hosted hub has no such
route and is reached over SSH.

A machine's choice not to export is the same on every transport (ruling of the plan's coordinator,
2026-10-01, the conservative default, recorded for the founder in Open Questions): with
`cockpit.anonymous_metadata: false` the route answers status 403 with the typed body
`{"error":"export_refused"}` to the owner's credential too, for both shapes, exactly as the CLI verb
prints `export_refused`. The route's other typed reasons are status 503 `{"error":"warming_up"}`
until the daemon's first pass has ended (the metrics-only shape is served meanwhile) and status 503
`{"error":"export_failed"}` when the envelope would not pass its own rules or its size bound. The
envelope is built and validated once for each version of the daemon's published document and of its
metrics history, and served by the shared writer with gzip and a strong ETag, so a request copies
bytes. Its two halves are prepared apart: the fleet half is validated and encoded once for each
published document, and a new metrics sample (every 10 seconds) costs the encoding of the metrics
and the compression of the body, never the fleet's validation again; a reader learns whether the
metrics moved from the sampler's version, without copying the history. Every answer of the route, the envelope and a 304 included, carries `Cache-Control: no-store`. Each credential may make a burst of 5 requests and then one a second; over that the answer is
status 429.

#### REQ: remote-http-fetch

The HTTP exporter's address and credential come only from local configuration, never from a
snapshot, a request or the remote's output. A machine has an HTTP route when its entry in
`session_move.targets.<machine>` (`internal/sessionmove/config.go`) has the optional `http` section
beside `ssh`, with the keys `url` and `token_file`. The `remote` section names no machine for the
hub it points at, and entries are placed only by a configured key, so the machine that is the
configured hub (`remote.provider: hub`) is the target whose `http.url` has the same origin as
`remote.url`: for it `token_file` may be omitted and the hub client's `remote.token_file` is reused.
A target with no `token_file` whose `url` is not that hub has no HTTP route. The URL is validated by
the rule of `remotestate.ValidateHubURL` (an `https` URL, or `http` only for a loopback host, with no
user information, query or fragment and no path); the rule lives in the leaf package
`internal/hubaddress`, which both `remotestate` and `sessionmove` call, because `sessionmove` cannot
import `remotestate` (the import would be a cycle). The token file must be absolute and private
(a regular file that only its owner can read; the mode is not checked on Windows) and is read on
every export, so a rotated credential needs no restart; a credential that is missing or unusable
sends no request and is shown as `http_auth_failed`; a path that is not a regular file (a FIFO, or a
link to one) is refused without blocking. The address rule admits `localhost` however it is capitalised (the host is compared, and used, in
lower case, so `http://Localhost:8766` is a loopback address and is never proxied) and
no `?`, `#` or path (there is no base path), its error text never echoes the address (which could
hold a password), and the host is used in lower case. Proxies: a plain `http` request (loopback only,
by the rule) and any request to a loopback host is never proxied, whatever the environment names and
however the host is spelled, because a proxy would receive such a request whole, bearer included; an
`https` request follows the environment's proxy, through which it passes as a CONNECT tunnel that
does not see the bearer. The hub client (`internal/remotestate/hub`), which sends the same
credential, follows the same policy. TLS certificates are verified against the system roots, and TLS
1.2 is the oldest version spoken. A loopback `http://` address (the local end of an SSH tunnel, for
example) sends the bearer to whatever process listens on that local port: prefer `https`, or the SSH
transport, for a machine reached that way. A target whose `http.url` is this daemon's own listener is
refused when the daemon starts, with a diagnostic that names the machine. The section is not a `Courier`, so `default_courier` and session
delivery are unchanged. That target map is where a machine's addresses already live, and keeping
one list avoids a second place to be wrong; a separate `cockpit` section would repeat the machine
names. The client calls `GET /v0/workbench/machines/export` with `Authorization: Bearer`, follows
no redirect, caps the response at 8 MiB, uses a 3 second connect timeout and a 10 second total
timeout, and sends the bearer only to the configured host. How often it is called is the
demand-driven cadence of REQ:remote-exporter-transports. A machine with no `remote.provider: hub`
match, no `http` section or no readable token file has no HTTP route. The credential is installed by
the existing `wb remote enroll --url=<<<edit:hub-url>>> --token-stdin` (it verifies a one-time machine
credential, stores it privately and updates the hub-owned `remote` settings); for a per-machine
`http` section the operator places the token file by the same enrolment against that machine's hub
URL.

#### REQ: remote-ssh-fetch

Serves J1, J7. For every machine in `session_move.targets` that has an `ssh` section
(`sessionmove.SSHConfig`: `host`, `user`, `wb_path`, validated by `SSHConfig.Validate`,
loaded as `agents.LoadRemoteTargets` loads them), the SSH exporter MUST run `ssh` through
`internal/remotessh` (`Resolve`, `BuildWith`, a `Runner`, `NewLimitedBuffer` and
`SanitizeDiagnostic`) executing `<wb_path> cockpit export
--format json` (with `--metrics-only` for the metrics call), or `wb` when `wb_path` is empty.
The argument vector is built from that configuration only: it never contains text from a
published snapshot, from a request, from the remote's output or from the environment, and
nothing is written to the command's standard input. It is exactly `-T -o BatchMode=yes -o
ConnectTimeout=5 -o ForwardAgent=no -o ForwardX11=no -o ClearAllForwardings=yes -o ControlMaster=no
-o RemoteCommand=none -o PermitLocalCommand=no -o LogLevel=ERROR [-l <user>] --
<host> <wb_path> cockpit export --format json [--metrics-only]`: no terminal, no prompt, no agent,
X11 or port forwarding, the user as a fixed `-l` pair and the host after `--` so that neither can
be read as an option. The last four options neutralise directives of the user's ssh configuration
that would change what an unattended call does: the call never becomes a connection-sharing master
(with `ControlMaster auto` it would, and killing it at its timeout would drop the owner's own
session that shares it; `ControlPath` is left alone, so a master that already exists is reused), a
`RemoteCommand` configured for the host does not replace the remote words, a `LocalCommand` is not
run on this machine, and `ssh` writes errors only, not banners. Host key checking is left to the
user's ssh configuration and is never switched off. The child process is given an allow-listed
environment, not the daemon's: `HOME`, `USER`, `LOGNAME` and `SSH_AUTH_SOCK` as they are, a fixed
`PATH` (`/usr/bin:/bin:/usr/sbin:/sbin`, and the directory of the resolved `ssh` only when that
directory is root's and not writable by its group or by everyone) and `LANG=C`;
everything else is dropped, `DISPLAY`, `SSH_ASKPASS`, `GIT_*`, `WB_*` and any token included. On
Windows, where `ssh.exe` cannot start without them, `SystemRoot` and `USERPROFILE` are passed too
and the `PATH` is `%SystemRoot%\System32\OpenSSH`, `%SystemRoot%\System32` and the directory of
the resolved `ssh`. The child also gets a session of its own (`setsid`), so neither `ssh` nor a
helper it starts (the inner `ssh` of a `ProxyJump`, an askpass program) has a terminal to prompt
on, even when the daemon was started in one. The route is held to `SSHConfig.Validate` again by the exporter before anything is
started (a host, user or `wb_path` that starts with `-` or holds a space, a control character or
a shell character has no SSH route), and that rule keeps every remote word to characters no shell
interprets. The local `ssh` is the system's own, `/usr/bin/ssh`, when that exists; otherwise the
one on the `PATH`, followed through every symbolic link to the file itself and taken only when
that file and its directory are owned by root or by the daemon's user, neither is writable by its
group or by everyone, and the file is not under the user's home directory
(`remotessh.ResolveTrusted`; Windows has no such owner or mode, and only the home rule applies there). It is
resolved once and kept; a search that fails is made again at the next export, and so is one after
a call that could not be started. The call has a connect timeout of 5 seconds (a parameter of
`remotessh.BuildWith`; `remotessh.Build` keeps its 10 seconds for the other callers), a total
timeout of 15 seconds, at which the daemon's runner (`remotessh.GroupRunner`) kills the whole
process group of `ssh` (a helper that left the group and holds the output pipe delays the return by
at most 2 more seconds; on Windows only `ssh.exe` is killed and a `ProxyCommand` child may
survive), and the export returns only once the process has been waited for; stdout
capped at 8 MiB and read only through `fleet.DecodeEnvelope`; and stderr, of which the last
`remotessh.MaxDiagnosticBytes` are held (so a long banner cannot push the line that says why out
of it) to tell a refused login from an unreachable host and for the daemon's log, and which goes
nowhere else: it is never forwarded to any reader of the fleet document, the metrics, the branches,
the session or the export envelope. The log line of a failed export is the machine's configured
key, the code, and after it what stderr held, rendered by `remotessh.SanitizeDiagnostic` as one
line of printable characters of at most `remotessh.MaxDiagnosticBytes`, with a leading `...` when
earlier output was dropped (or a fixed cause for one case, below). It is written once each time a
machine's failure code changes, however the remote words its stderr each time. That text may name
a host or a login, which the fleet document may not; the log may hold it because the log is the
owner's alone: it is a file on the daemon's machine, and the dashboard's `GET /api/v1/log` serves
its tail to an owner session only
([cockpit](../cockpit/README.md)#req:daemon-log-is-owner-only). A daemon that is
stopping while `ssh` runs records and logs nothing of that call. A remote login shell that prints
to stdout (a profile that echoes, a banner script) puts text before the envelope: the export is
then `bad_payload`, and the log line says "output before the envelope" (those fixed words, not the output). A failure
is told by the exit status, not by text: a call that did not end with a status (no local `ssh`,
`ssh` that cannot be executed or was killed by a signal) is `ssh_unavailable`; status 255, which
is `ssh`'s own, is `auth_failed` when stderr holds one of four fixed OpenSSH phrases compared
without regard to case (`Permission denied (`, `Host key verification failed`, `Too many
authentication failures`, `No more authentication methods`) and `ssh_unavailable` otherwise;
status 127 or 126 (the remote shell found no `wb` it can run) is `wb_missing`; status 2 (wb's
usage code: no `cockpit export`, or not one of its flags) is `wb_too_old`, and so is an answer
that names an export `schema_version` other than this binary's; status 1 with the typed reason
of REQ:cockpit-export-verb on stdout (exactly that object: at most 256 bytes, its two fields and no
other, this binary's schema version, nothing after it; anything else is `bad_payload`) is `daemon_not_running`, `export_refused`, `bad_payload`
(for `export_failed`) or the warming rule of REQ:remote-exporter-transports (for `warming_up`);
the total timeout is `timeout`; any other status or reason is `bad_payload`. `ssh` passes the
remote command's status on, so a remote command that itself exits 255, 127, 126 or 2 picks the
code: the remote login already has that machine's full authority, and the choice is among the codes
of one closed set. How often `ssh` runs is the scheduler's rule
(REQ:remote-exporter-transports): `ssh` is started on demand only for an owner session's read, and
otherwise once per idle keepalive, max(15 minutes, the refresh interval), so that no page and no
process on this machine that is not the owner can raise the rate of logins.

What the user's SSH configuration can still do. The options above neutralise the directives that
would change what the call is, not the ones that decide how the host is reached, which stay the
user's: `ProxyCommand`, `ProxyJump`, `Match exec` and `KnownHostsCommand` run local commands, as
they do for the user's own `ssh`, here with the reduced environment and no terminal. An
`IdentityAgent`, or an agent that asks for approval of each use, may show its own prompt on every
unattended login, the 15 minute keepalive included (4 an hour with no owner looking, and one per
refresh interval only while an owner session reads), and a FIDO key that wants a touch blocks until
the 15 second timeout, which is then shown as `timeout`. A connection-sharing master that already
exists at the configured `ControlPath` is reused, so an export may travel over the owner's open
session. A host for which any of this is unwanted is given no `ssh` section, or
`cockpit.remote_ssh: false` is set. A daemon run by
launchd or systemd may have no SSH agent socket: the daemon does not look for one, and the login
then fails as `auth_failed`. The SSH login already carries full shell authority on the remote, so this adds no
privilege; the daemon never forwards request-supplied text to the remote command and the
Cockpit never exposes remote stderr. With no `ssh` section, or with `cockpit.remote_ssh: false`,
no process is started and the local `ssh` is not even looked up.

#### REQ: remote-envelope-is-untrusted

The remote envelope, from either transport, is untrusted and MUST be decoded strictly: an unknown
field is rejected; the size is bounded as above; every string is length-capped (256 bytes, 2048 for
URLs); the collections are capped at the document's own limits and the samples at 360; a number that
is not finite or is negative where it is a count, size or percentage, and a time in the future, are
refused. Every entry in it is placed on the machine named by the configured target key, whatever
machine the response names, and no machine name in the response is used for placement; it is never
applied to the local machine. A refused payload renders nothing and sets `remote_error`
`bad_payload`. The remote's own cached entries for third machines are dropped, so only that
machine's own entries are merged: the exporter never emits them, an envelope that carries one is
refused whole by the single-machine rule below, and the merger keeps only the entries of the
envelope's one machine entry whatever reaches it. Every envelope is validated again by the
scheduler, whichever transport produced it. The same validation applies to the SSH transport. The decoder (`fleet.DecodeEnvelope`) scans the
shape of the bytes before it decodes them (arrays over their caps, nesting beyond 10 levels and
more than a million tokens are refused without allocating for them), refuses every string field
that has no declared rule (each closed vocabulary and each identifier pattern is checked, and a
field added later is refused until it has one), and refuses an export that is not a single machine's
own: any route but `local`, an id that is empty, malformed or repeated, an entry whose `machine_id`
is not the one machine entry's, a null collection, a repository's web address that is not built from
its host and name, and a pull request address off its repository's host. The ids in an envelope are
the exporter's own, so a merger (REQ:remote-entries-replace-cached) re-derives every id under the
configured machine key and never uses one as received. Its refusal names the rule and the field's
path and never a value, a time or an error text of the remote.

A published snapshot (the `cached` route) is another machine's data as well, and the mapping that
reads it is held to the same bounds, by the same code where the rule is the same, with one
difference: it repairs where the decoder refuses, because a snapshot is read as a whole and one odd
value must not hide a machine. A publish time before 2000 makes the snapshot unusable and one in the
future is taken as now, so a clock that is ahead can never make a snapshot look fresh for ever; a
`last_activity_at` is never later than its snapshot's time and one before 2000 is dropped; a
`wb_version` that does not match the version pattern is dropped; a pull request whose number is
not within 1 to 10,000,000 is dropped; at most 2000 repositories, 2000 worktrees, 500 pull
requests and 200 agents of one machine are kept, the same ones on every read, and what is cut is
counted in that machine's `export_dropped` (the agents in `agents_truncated`); and at most 200
published machines are kept, the newest publications, with one diagnostic when more exist. The
text a machine says about itself, in an export or in a snapshot (a repository, task, stream or
branch name), is that machine's own text: it is bounded and stripped of control and format
characters, and the application renders it as text, never as markup.

#### REQ: remote-entries-replace-cached

The accepted entries are merged into the local fleet document as that machine's entries
with `route` `live-remote`, `observed_at` equal to the remote snapshot's time (the fleet's
`snapshot_at`, never later than when this daemon received it), and the
machine entry's `transport` (`http` or `ssh`) set to the transport that produced them. While
the export is younger than two refresh intervals, measured from when this daemon received it so
that a remote's clock cannot keep stale data live, they replace that machine's cached
(published-store) entries; otherwise the cached entries are shown with their age and the
`remote_error` explains why. A machine nobody is looking at is read once per idle keepalive
(REQ:remote-exporter-transports), so its live entries stay for that keepalive longer, with their
age shown as every entry's is, as long as no export of it failed or found it warming since they
were received: an idle machine's live view may be up to the keepalive and two intervals old, and
the first export that brings nothing puts it back on the two-interval bound. That machine's cached entries are the published snapshots under the
configured machine name by this machine's own login, and by no other: a machine another login
published under the same name is a machine of its own, is never hidden behind the configured
machine's live entries, never lends it its id and is never given its SSH route. The daemon knows
its login from its periodic publisher, which resolves it for its first publish, or from this
machine's own publication in the store (the one snapshot under this machine's name and projects
root; two logins that both claim to be this machine tell it nothing). While the login is not known
no published entry is taken for a configured machine's, so that machine may be shown twice, live and
published, until it is. The machine entry is
named by the configured key and keeps one id: the id of its published entry when exactly one
exists, otherwise an id derived from the login and the key; every other id is derived from the
configured key and never used as received. An agent's `activity`, `task`, `started_at`,
`finished_at` and `exit_code` are carried under the envelope's rules, and its `worktrees` are the
re-derived ids of that machine's carried worktrees: an id that is not one of them is dropped. A configured machine with a failure and no published
entry is shown as a bare machine entry carrying `remote_error`. A target configured under this
machine's own name is never read, and an export that is this machine's own (its envelope names this
machine, or its one machine entry has this machine's id: a tunnel, a proxy or a mistaken address
that leads back here), whatever address it came from and whichever shape, is refused whole as
`self_export`: nothing of it is placed and its metrics are not kept.

A fleet that says it is warming up (over either transport) is never taken: the previous live view
stays while it is fresh, with no error and no backoff. Warming is bounded: when no fresh live view
of that machine is held (it was never read, or its view is older than two refresh intervals) the
attempt is the failure `remote_warming_up`, shown on the machine's published entries or, with none,
on a bare machine entry, and followed by the same backoff as any failure. At most 2,000 repositories, 2,000
worktrees, 500 pull requests and 200 agents of one remote machine are kept; what is cut is added to
the machine entry's `export_dropped`, and cut agents set its `agents_truncated`. An export whose
entries are the ones already shown publishes nothing. The entries are mapped, and the document is
encoded and compressed, outside the snapshotter's exclusive lock, and a request for a machine's
metrics takes no exclusive lock. A document that would be over 32 MiB with the live machines' entries is
published without them: the size is estimated before anything is encoded (the size of everything
else in the last published document plus the encoded size of the fresh live views), so the document
is encoded once, and each machine left out is shown by its published entries or, with none, by a
bare machine entry, carrying `remote_error` `export_too_large` either way, never dropped silently,
with one diagnostic and one log line. The document's `throughput` block is local only and is never
merged from another machine. A panic while a remote's envelope is validated or mapped is `bad_payload` for that machine,
logged once, and never ends the daemon. Agents, pull request state, sync facts and `owner_state` of
that machine therefore become visible live, and such entries have no actions
(REQ:action-slots). A machine with no live route keeps its published-store entries as
before.

#### REQ: remote-error-is-visible

A machine entry carries `remote_error` when its last attempt failed, one of
`http_unavailable` (connection error, timeout, 404, 429, 5xx or a redirect), `http_auth_failed`
(401 or 403), `ssh_unavailable` (no local ssh, or the host unreachable), `auth_failed` (the SSH
login), `timeout`, `wb_missing`, `wb_too_old` (the remote wb has no `cockpit export`),
`daemon_not_running`, `export_refused` (the remote daemon refuses anonymous reads),
`bad_payload`, `remote_warming_up` (the remote daemon's first pass has not ended and no fresh live
view is held), `export_too_large` (this daemon left the machine's entries, live or published, out of a document
that would be over its size bound: the live entries first, each such machine then shown by its
published entries, and the published entries too when the document is still over the bound, each
published machine then shown by its machine entry alone; it is set while that holds and is not a failed attempt),
`self_export` (the export read is this machine's own) or `clock_skew` (the export carries a time
more than 60 seconds ahead of this daemon's clock: the clocks of two machines differ, a difference
of up to 60 seconds is accepted, and a larger one is named as what it is and never as
`bad_payload`; it is fixed by setting the clock that is wrong, and has no command to copy); it is cleared by the next successful full
export on the preferred transport, never by a metrics-only one. A remote export that prints
`export_failed` (its own daemon answered badly or could not be read) is shown as `bad_payload`. The `http_*`
codes name the HTTP transport and the others the SSH transport, except `export_refused`,
`bad_payload`, `remote_warming_up`, `self_export` and `clock_skew`, which either transport reports, and
`export_too_large`, which names none (REQ:remote-exporter-transports), so Fleet health shows which
failed. Home "Fleet health" shows the code with the fixing command to copy, labelled "run on
<machine>": for `http_auth_failed` or a missing HTTP credential, `wb remote enroll --url
<<<edit:hub-url>>> --token-stdin`; `wb daemon start` for `daemon_not_running`; `wb self-update` for
`wb_too_old` and `http_unavailable` caused by 404; and for the others the `ssh <user>@<host>
<wb_path> cockpit export --format json` command to try, except `self_export`,
`export_too_large` and `remote_warming_up`, which have no command to copy (the first is fixed in
this machine's configuration of the target's address, the second is this daemon's own bound, the
third passes when the remote's first pass ends). The stderr or response body behind
it is not shown.

### Performance budgets

These budgets are tested on a fixture of 500 repositories, 600 worktrees,
4,000 branches and 3 machines, with realistic names and `code_index` entries that
carry statistics, and a realistic activity: most worktrees are old (about 2 in 100 were
touched in the last two weeks), so about 300 tasks are at risk and only a handful need the
operator, and most worktrees have the branch of their task while about a third have an
`agent/`, `codex/` or `fix/` branch.

#### REQ: fleet-document-size

The fleet document MUST be at most 150 kB over the wire with gzip.

#### REQ: initial-script-size

The JavaScript needed to render the first page, Home, MUST be at most 350 kB raw: the scripts the entry
document loads (with the chunks they import statically) and the chunks Home's route loads before Home
renders. `cockpit/web/tools/finish-build.mjs` checks it over JavaScript files only, from the build's
metafile, and fails the build when it is larger, and also when a script the document names is missing
or is not a file directly under `dist`, or when the metafile is absent (it fails closed). It reports the
initial static graph and the first page separately. Chart.js, the detail pages and every page other than
Home are lazy chunks, loaded only by the routes that use them.

#### REQ: bounded-row-elements

At a test viewport 1080 px high a list MUST NOT have more than 60 row elements
in the DOM, the visible rows plus a fixed overscan, whatever the number of rows,
and on any viewport not more than 80.

#### REQ: fast-filtering

Filtering 5,000 rows with the matcher and the view functions MUST take a median
of under 30 ms over repeated runs, with a CI multiplier of 5, and the
matcher's step count MUST stay within the bound of REQ:list-filter-and-matcher.
Typing in a filter MUST NOT be debounced beyond one animation frame.

#### REQ: no-recompute-when-unchanged

Polling uses the ETag. A poll that returns `304`, or a snapshot whose document is
unchanged, MUST cause no recomputation of derived collections and no re-render
of list rows; clock-bound text, such as relative ages, may update. Polling follows the page's visibility: a hidden
page schedules no poll, and when it becomes visible again the session is read again (a changed principal or
capability set is picked up) and the fleet is polled at once; an answer 401 to a poll reads the session again.

### Look and accessibility

#### REQ: look-dependencies

The application adds Chart.js (MIT licence) at an exact
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
Skeleton rows and cards have fixed heights and are shown while the daemon is
warming up, and data arriving MUST NOT shift the layout: the cumulative layout
shift is under 0.01.

#### REQ: responsive-to-360

The application MUST work down to a viewport 360 px wide: the tabs collapse to a
scrollable strip, tables drop low-priority columns, and charts stack. Home's phone
layout is REQ:home-phone.

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
- [cockpit-actions](../cockpit-actions/README.md)
- [remote-state](../remote-state/README.md)
- [code-index-freshness](../code-index-freshness/README.md)

## Not Doing

- The action registry, authorization, preview and execution: they are specified
  by [cockpit-actions](../cockpit-actions/README.md), and the work-loss risk
  assessment by [work-loss-risk](../work-loss-risk/README.md). This Feature
  specifies where and how actions appear, and executes nothing itself.
- Generic multi-selection and a bulk bar, which `cockpit-actions` excludes.
- Hover cards, row-expansion chevrons and a raw-data level as a separate step.
- Tab badges for static counts, an Activity chart, a Worktrees-by-repository chart
  and a code-index doughnut.
- Acknowledging or snoozing an item; a column chooser, saved or pinned filters and
  a settings page.
- Reading screen text or logs of an agent (content, owner-only, in
  `cockpit-actions`).
- A separate pull request page.
- Operation feedback (the top-bar indicator and the updating row state) and action results in
  the palette: they move to `cockpit-actions` (its Task 8). This Feature keeps the action slots
  and copy-the-command.
- A push channel; the application refetches and polls.
- A network-facing Cockpit route that serves one machine's data to another (the hub route
  is authenticated by a machine credential, served only by a daemon-hosted hub, and lives outside
  `/api/v1/cockpit/`), and metrics from a machine with no live route beyond its last published
  sample; persistence of metrics across daemon restarts, and task storage.

## Acceptance Criteria

### AC: top-bar-shows-tabs-badges-and-freshness

**Requirements:** cockpit-views#req:top-bar, cockpit-views#req:refresh-interval-field, cockpit-views#req:field-tables

Scenario: Signals only, and an old snapshot
Given a fleet with 3 machines, 529 worktrees, 1 running agent (a session `live`) and 3 tasks in "Needs you", a document whose `refresh_interval_seconds` is 30, and a snapshot taken 70 seconds ago
When the application is opened
Then the tabs Home, Tasks, Repositories, Worktrees, Agents and Machines are shown, the Home badge reads 3 and the Agents badge reads 1 highlighted, no other tab has a badge, the palette entry, the "New task" button, the freshness chip reading "updated 70 s ago" in amber and the session chip naming the principal are in the top bar, and there is no operation indicator

### AC: home-route-and-alias

**Requirements:** cockpit-views#req:home-route

Scenario: The first page and its alias
Given the application
When `/` and then `/dashboard` are opened
Then both show Home, the Home tab is current, the document title is "Home", and no tab is named Dashboard

### AC: every-tab-lists-its-collection

**Requirements:** cockpit-views#req:top-bar, cockpit-views#req:home-route, cockpit-views#req:tasks-list

Scenario: Six pages, some cached
Given a read model with entries in every collection, some of them cached
When each of Home, Tasks, Repositories, Worktrees, Agents and Machines is opened and the machine filter is applied on a list page
Then each page shows its entries, every cached row shows its route and age (a merged row through its per-machine chip), and the filter leaves only that machine's rows

### AC: warming-up-shows-progress

**Requirements:** cockpit-views#req:top-bar, cockpit-views#req:look-layout

Scenario: Before the first pass completes
Given a document marked as warming up with 120 of 438 repositories scanned
When the application is opened
Then the freshness chip shows the scanned count, fixed-height skeleton rows are shown, and when the complete document arrives the cumulative layout shift stays under 0.01

### AC: no-heading-repeats-the-tab

**Requirements:** cockpit-views#req:no-visible-page-heading

Scenario: Any page
Given the application on the Worktrees page
When the page is inspected
Then no visible heading reads "Worktrees", the active tab has `aria-current="page"`, one visually hidden `h1` names the page, and the document title names the page

### AC: palette-groups-results

**Requirements:** cockpit-views#req:command-palette

Scenario: Search across kinds
Given a fleet with more than 8 repositories whose name contains `go`, and a task, a worktree branch and an agent that also match, and a branch that exists only in a lazily loaded repository page
When the operator presses Cmd+K, types `go`, presses the down arrow twice and Enter
Then results are grouped by kind with at most 8 per kind, the lazily loaded branch is not among them, the highlighted result moves with the arrows, and Enter opens the page of the highlighted result

### AC: shortcuts-navigate-and-respect-typing

**Requirements:** cockpit-views#req:keyboard-shortcuts

Scenario: Shortcuts and an input
Given Home on screen
When the operator presses `g` then `w`, then `/` on a list, types `g w` into the filter, presses `?`, and later presses Esc with the side panel open
Then Worktrees opens, the filter box has the focus, the typed `g w` stays as text and no tab switches, the shortcut sheet appears, and Esc closes the side panel

### AC: client-accepts-only-schema-2

**Requirements:** cockpit-views#req:schema-version-2

Scenario: Version mismatch in both directions
Given a page that expects schema version 2 and a daemon answering `schema_version` 1, one answering 3, and one answering 2
When the application loads from each
Then the first shows "update wb on this machine" and no data, the second shows "reload" and no data, the third renders normally, and the daemon's fleet document carries `schema_version` 2

### AC: matcher-grammar

**Requirements:** cockpit-views#req:list-filter-and-matcher

Scenario: Terms, glob, exclusion, field and quotes
Given rows `sneat-co/bots-go` on machine `mac`, `sneat-co/sneat-go` on machine `vm`, `sneat-dev/wb` on `mac` and `Strongo/Dalgo` on `vm`, with a task `fix ci` on `sneat-dev/wb`
When the matcher is applied with `sneat-*/*-go`, with `WB`, with `sneat -wb`, with `machine:vm go`, with `-machine:vm`, with `task:"fix ci"`, with `colour:red` on a page that does not declare `colour`, and with `a.*`
Then the results are the two `-go` repositories, `sneat-dev/wb`, the two sneat-co rows, `sneat-co/sneat-go` and `Strongo/Dalgo`, the two rows on `mac`, `sneat-dev/wb`, no row (the text `colour:red` matches nothing), and no row (the dot and star are a glob with a literal dot, never a regular expression); and given also `sneat-co/sneat-go-backend`, `repo:"sneat-co/sneat-go"` matches `sneat-co/sneat-go` and not `sneat-co/sneat-go-backend`, `repo:sneat-co/sneat-go` matches both, and `repo:"sneat-co/*"` matches only a row whose repository is literally `sneat-co/*`

### AC: matcher-limits-and-bare-fields

**Requirements:** cockpit-views#req:list-filter-and-matcher, cockpit-views#req:filter-vocabulary

Scenario: Caps and default fields
Given the Worktrees page with a worktree of task `fix-ci` in `sneat-dev/wb` on branch `topic`, whose host is `github.com`, and a filter of 20 terms, and one of 300 characters
When the filter `topic` and then `github` are applied, and then the two long filters
Then the first matches on the branch, the second matches nothing because the repository value has no host, only the first 16 terms of the first long filter and only the first 256 characters of the second are applied

### AC: matcher-is-linear-time

**Requirements:** cockpit-views#req:list-filter-and-matcher

Scenario: Hostile glob
Given the term `*a*a*a*a*a*a*a*a*a*a*b` and a value of 50,000 `a` characters
When the matcher is applied with a step counter
Then it returns no match and the steps counted are at most a constant times the pattern length times the value length

### AC: filter-vocabulary-is-the-only-link-target

**Requirements:** cockpit-views#req:filter-vocabulary, cockpit-views#req:every-number-is-a-link

Scenario: Every generated link parses
Given Home, its "Needs you", "Ready to land", "Cleanup" and "Fleet health" rows, the Repositories sort presets and every page's chips and count cells
When every link target is collected
Then each is an address on one of the pages whose chips, `state:` values, `age:` terms, sort column ids and `sel` keys are all in the vocabulary table, the `age:` terms are exactly `age:<1d`, `age:1-7d`, `age:8-30d`, `age:31-90d` and `age:>90d`, and no `day:` term exists, and every chip of the vocabulary carries a non-empty label and hint

### AC: filter-state-lives-in-the-address

**Requirements:** cockpit-views#req:list-quick-filters-sort-and-url-state

Scenario: Back button, a pasted link, the selection and a bad value
Given the Worktrees page
When the operator types `fix`, toggles the Unpushed chip, selects machine `mac`, clicks the Last activity header twice, selects a row, then presses back, and separately opens the pasted address, and then an address with `sort=bogus`, `chips=nonsense` and a `sel` that names no entry
Then the address carries `q`, `chips`, `machine`, `sort`, `dir` and `sel`, back removes the last change, the result count reads "n of N", the pasted address shows the same rows with the same side panel open, and the bad address ignores the three bad values and shows the default view

### AC: default-sorts

**Requirements:** cockpit-views#req:list-quick-filters-sort-and-url-state

Scenario: Each page's first order
Given a fleet with several tasks, repositories, worktrees, agents (some running) and machines including the local one
When each list page is opened with no sort in the address
Then Tasks are in state order (worst first) with the newest activity first within a state and without the at-risk tasks idle for over 14 days (the `older` chip includes them), Repositories and Worktrees are in last-activity order newest first, Agents list running agents first and then newest first, and Machines list the local machine first and then by name

### AC: rows-are-one-line-and-virtual

**Requirements:** cockpit-views#req:one-line-virtual-rows

Scenario: Long values in a long list
Given 529 worktrees, one with a 200-character task name
When the Worktrees page is opened and scrolled
Then each row is one line with an ellipsis and the full value in its `title`, the header stays in view, and rows appear and disappear as the list scrolls

### AC: columns-are-few-and-uniform-ones-hidden

**Requirements:** cockpit-views#req:default-columns-are-few

Scenario: At most seven, Branch uniform, PR empty, one machine
Given 529 worktrees on one machine whose branch equals its task and which have no pull request, and a second fleet on two machines in which one worktree has a different branch and one has a pull request
When every list page is opened, and the Worktrees page for each fleet
Then no list shows more than 7 columns unless its page declares a priority for each beyond the seventh, the first fleet shows neither the Branch, the Machine nor the PR column and the second shows all three

Scenario: More than seven by priority, and the actions cell
Given the Repositories page, which declares eight columns and an actions cell, each with a priority, and a fleet with more than one machine
When it is opened in a window wide enough for the minimum width of every column, and again with a panel beside the list and in a tablet-width window
Then the wide list shows all eight columns and the actions cell, whose header is read by assistive technology and not drawn, and each narrower list shows fewer columns, the lowest priority (Agents, then PRs) first, and never more than the width fits

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

### AC: every-number-is-a-link

**Requirements:** cockpit-views#req:every-number-is-a-link

Scenario: The enumerated count cells and the declared exceptions
Given Home, Tasks, Repositories and Machines with data, and a branch count on Repositories
When every count cell in the enumerated list of the requirement is inspected, and the branch counts, the throughput numbers and a pull request's checks over total are inspected
Then each enumerated cell is a link to the list that produced it with only vocabulary terms, and each declared exception is not a link and carries its reason in its `title`

### AC: side-panel-opens-and-closes

**Requirements:** cockpit-views#req:side-panel, cockpit-views#req:row-keyboard-and-copy

Scenario: Select, move, close
Given the Tasks list with several rows and the focus on the first
When the operator presses `j` twice, `k` once, Enter, and then Esc
Then the focus is on the second row, the side panel opens for it with the list still visible and `sel` in the address, and Esc closes the panel and removes `sel`

### AC: side-panel-shows-summary-actions-commands-and-raw-data

**Requirements:** cockpit-views#req:side-panel

Scenario: A worktree and a hostile-looking entry
Given a worktree whose entry the read model sent with its fields
When its row is selected and the "Raw data" block is opened
Then the panel shows the summary, the related entities, the action area, the "Copy command" entries and the collapsed "Raw data" block, which starts collapsed, shows the fields exactly as sent and shows no field outside the permitted metadata, and no hover card, chevron or other intermediate level exists anywhere

### AC: detail-routes-render-the-same-panel

**Requirements:** cockpit-views#req:detail-routes-share-the-panel

Scenario: Panel and page from one component
Given a repository, a task, an agent, a machine and a worktree
When each is opened as a side panel and then by its detail route
Then the content of the page equals the content of the panel (one component renders both), each route ends with the "Raw data" block, and the repository's older id still opens its page

### AC: copy-buttons-copy-the-full-value

**Requirements:** cockpit-views#req:row-keyboard-and-copy

Scenario: Task, branch and session id
Given a task name, a branch name and a session id that are each longer than the visible cell
When each copy button is pressed
Then the clipboard holds the full value, not the shortened text

### AC: task-state-at-risk

**Requirements:** cockpit-views#req:task-state, cockpit-views#req:field-tables

Scenario: The interim rule
Given local worktrees of four tasks: one with `owner_state` `orphaned` and `ahead` 2, one with `owner_state` `idle`, `ahead` 0 and `has_upstream` false, one with `owner_state` `active` and `ahead` 3, and one with `owner_state` `orphaned`, `ahead` 0 and `has_upstream` true, and a worktree cached from another machine with the same facts as the first
When the task state is computed
Then the first two tasks are `at-risk`, the third and fourth are not, and the cached worktree never makes its task `at-risk` because the sync facts are local only

### AC: task-state-checks-failed

**Requirements:** cockpit-views#req:task-state, cockpit-views#req:field-tables

Scenario: A failed check beats everything below it, and an unobserved one is ignored
Given a task with an observed open pull request whose `checks_failed` is 1, also a blocked agent and a ready pull request, and a task whose open pull request has `checks_failed` 1 but no `checked_at`
When the task state is computed
Then the first is `checks-failed`, not `blocked` or `ready`, and the second has no reported pull request input

### AC: task-state-blocked

**Requirements:** cockpit-views#req:task-state, cockpit-views#req:field-tables

Scenario: Blocked agent, failed run, a later run, and 24 hours
Given a task with an agent whose `activity` is `blocked`, a task whose latest dispatched run ended `timeout` 2 hours ago, a task whose latest run ended `failed` 2 hours ago but has a later session, a task whose latest run ended `failed` 25 hours ago or exactly 24 hours ago, and a task whose latest run ended `failed` with no `finished_at` and no `started_at`
When the task state is computed
Then the first two are `blocked` and the others are not

### AC: task-state-ready-to-land

**Requirements:** cockpit-views#req:task-state, cockpit-views#req:field-tables

Scenario: Every observed open pull request green and mergeable
Given a task with two observed open pull requests, both `state` `open`, `checks_green` true and `mergeable` `clean`, a task where one of its two has `mergeable` `blocked`, and a task where one is a `draft`
When the task state is computed
Then the first is `ready` and the other two are `not-ready`

### AC: task-state-not-ready

**Requirements:** cockpit-views#req:task-state, cockpit-views#req:field-tables

Scenario: Why a task is not ready
Given a task with a draft pull request, a task whose pull request has `checks_pending` 2, a task whose green pull request has `mergeable` `dirty`, a task whose green pull request has `mergeable` `behind`, and a task whose pull request has no failed and no pending check but `checks_green` false
When the task state is computed and the side panel is opened
Then all five are `not-ready` and the panel says draft, checks pending, not mergeable, behind and review respectively; a green pull request whose `mergeable` is absent says "merge state not reported", one whose `checks_green` is absent says "checks not reported" (never "review"), and a task with no reasons at all is never listed as waiting on checks

### AC: task-state-working

**Requirements:** cockpit-views#req:task-state

Scenario: Three signs of work
Given a task with an agent whose `activity` is `working`, a task with a dispatched run in state `running`, and a task with a worktree whose `owner_state` is `active`
When the task state is computed
Then all three are `working`

### AC: task-state-landed

**Requirements:** cockpit-views#req:task-state, cockpit-views#req:field-tables

Scenario: Merged, populated lifecycle, and an open one
Given a task whose only pull request has `state` `merged`, a task with no pull request whose every worktree has `lifecycle` `merged`, a task with no pull request whose worktrees carry no `lifecycle`, and a task with a merged pull request and an open one
When the task state is computed
Then the first two are `landed`, the third is not (the second arm needs a populated `lifecycle`), and the fourth is not

### AC: task-state-idle

**Requirements:** cockpit-views#req:task-state, cockpit-views#req:field-tables

Scenario: Reported but quiet
Given a task whose worktree `owner_state` is `idle` and which has no agent, no pull request and nothing else reported
When the task state is computed
Then it is `idle`

### AC: task-state-not-reported

**Requirements:** cockpit-views#req:task-state, cockpit-views#req:field-tables

Scenario: Nothing reported
Given a task, cached from an older publisher, whose worktrees carry no `owner_state`, with no pull request `state` and no agent `activity`
When the task state is computed
Then it is `not-reported`, the badge reads "state not reported", and a pull request whose `state` is absent is not counted as open

### AC: task-state-is-worst-first

**Requirements:** cockpit-views#req:task-state, cockpit-views#req:field-tables

Scenario: One task per state
Given nine tasks, one in each task state
When the Tasks page is sorted by state
Then they are in the order at risk, checks failed, blocked, ready to land, not ready, working, landed, idle, state not reported

### AC: task-state-ignores-unobserved-pull-requests

**Requirements:** cockpit-views#req:task-state, cockpit-views#req:field-tables

Scenario: Observed and unobserved pull requests
Given a task with one observed open pull request that is ready and one open pull request (`state` `open`) with no `checked_at`, and a task whose only pull request has no `state` and no `checked_at` and whose worktree `owner_state` is `idle`
When the task state is computed and the side panel is opened
Then the first is `not-ready` and its panel says "pull request not yet checked" and counts one pull request as not yet observed, and the second is `idle` with no reported pull request input

### AC: tasks-list-aggregates-worktrees

**Requirements:** cockpit-views#req:tasks-list, cockpit-views#req:field-tables

Scenario: One task over three repositories
Given worktrees named for task `fix-ci` in three repositories on two machines, a running agent and a pull request with 3 of 4 checks passed
When the Tasks page is opened and the Multi-repo and With agent chips are toggled
Then one row names `fix-ci` with two repositories and "+1", 3 worktrees, 2 machines, its agent, the pull request with "3/4", its state badge with icon and label and its newest activity, it stays in the list with both chips on, and selecting it opens the side panel with the worktrees, pull requests and agents

### AC: task-detail-shows-its-entities

**Requirements:** cockpit-views#req:task-detail

Scenario: A task name that needs encoding, and an asset-like name
Given tasks named `fix/ci 100%` and `release.js`
When the Tasks page links to each and the links are followed
Then the addresses are `/tasks/detail?task=fix%2Fci%20100%25` and `/tasks/detail?task=release.js`, and each page shows the summary header with the state badge, the worktrees table, the branches, the pull requests with checks and the agents of that task

### AC: needs-you-pr-checks-failed

**Requirements:** cockpit-views#req:home-needs-you, cockpit-views#req:field-tables

Scenario: A failed check
Given a task `fix-ci` whose observed pull request `sneat-dev/wb#12` has `failed_check` `go-ci / test`
When Home is opened
Then a "Needs you" row shows `fix-ci`, `sneat-dev/wb#12` and `go-ci / test` with exactly one action, "Open failure", a link to the pull request's `url` with `rel="noopener noreferrer"`

### AC: needs-you-agent-blocked

**Requirements:** cockpit-views#req:home-needs-you, cockpit-views#req:field-tables

Scenario: Reported and unreported agents
Given an agent whose `activity` is `blocked` on task `fix-ci` on machine `mac`, and another agent whose `activity` is absent
When Home is opened
Then one row shows `fix-ci` and `mac` with the single action "Open agent" linking to that agent's page, and the agent whose state is not reported produces no row

### AC: needs-you-run-failed

**Requirements:** cockpit-views#req:home-needs-you, cockpit-views#req:field-tables

Scenario: Failed and timed-out runs
Given a dispatched run for task `a` finished `failed` with exit code 2 whose record has a free-text failure, a run for task `b` finished `timeout`, and a run for task `c` finished `failed` with a later session on `c`
When Home is opened
Then rows for `a` and `b` show the state, and for `a` the exit code, never the free text, each with the single action "Open agent", and `c` has no row

### AC: needs-you-work-at-risk

**Requirements:** cockpit-views#req:home-needs-you, cockpit-views#req:copy-the-command, cockpit-views#req:field-tables

Scenario: Two worktrees, with and without the push action
Given a task in state `at-risk` with two worktrees on this machine for which a fake registry offers `branch.push`, and one for which it offers nothing
When Home is opened
Then the first row names both worktrees and the reason in words and offers "Copy template" for the push of each worktree, with the repository named in its label, the second names its worktree and offers "Copy command" with one command per worktree, `wb pr create '<task>' --commit-all --message='<message>'`, and each row has exactly one primary action

### AC: needs-you-pr-needs-you

**Requirements:** cockpit-views#req:home-needs-you, cockpit-views#req:field-tables

Scenario: Green checks but a merge to resolve
Given a task whose observed pull request has `checks_green` true and `mergeable` `dirty`, and one whose `mergeable` is `behind`
When Home is opened
Then each task has a row of the kind "PR needs you" with the single action "Open pull request" linking to its `url`

### AC: needs-you-agent-finished

**Requirements:** cockpit-views#req:home-needs-you, cockpit-views#req:field-tables

Scenario: A finished agent with unpushed work
Given a task with an agent whose `activity` is `done`, no open pull request, and a local worktree with `ahead` 2, and a task whose agent is `done` with no unpushed work and a merged pull request
When Home is opened
Then the first task has a row of the kind "Agent finished" with the single action "Open task", and the second has none

### AC: needs-you-is-capped-and-empty-line

**Requirements:** cockpit-views#req:home-needs-you, cockpit-views#req:field-tables

Scenario: One row per task, order, the cap, blocked agents without a task, then none
Given 7 tasks in "Needs you" (one with two kinds of need), including a task `old` last active a week ago and a task `new` last active an hour ago in the same kind, and 3 blocked agents with no task
When Home is opened, "+2 more" is activated, and later every state has changed
Then 5 task rows are shown, one per task and for its worst kind, ordered by kind rank and then by last activity newest first (`new` before `old`), the Home badge reads 7, after them one row says "3 blocked agents with no task" and opens Agents with chip `blocked`, "+2 more" opens Tasks with chip `needs-you` showing exactly the 7 tasks, and afterwards one line says nothing needs the operator and no row remains, with no acknowledge or snooze control anywhere

### AC: needs-you-lists-recent-work-at-risk-only

**Requirements:** cockpit-views#req:home-needs-you, cockpit-views#req:field-tables

Scenario: A debt of old work is not a signal
Given tasks at risk last active 0, 14, 15 and 200 days ago and with no recorded activity, one of them at risk and 90 days old with failed checks, one with a blocked agent, one with a pull request that needs a merge resolution, a task whose agent finished with work not pushed 14 and 15 days ago, and the performance fixture of 600 worktrees of which about 300 tasks are at risk and nearly all of them old
When Home is opened
Then only the at-risk tasks last active 0 and 14 days ago have the "Work at risk" row, the old one with failed checks, the one with the blocked agent and the one with the pull request that needs the operator keep their own rows, the task that finished 14 days ago has an "Agent finished" row and the one that finished 15 days ago has none, the badge counts the rows and, on the fixture, reads a handful and not about 300

### AC: cleanup-counts-older-at-risk-work

**Requirements:** cockpit-views#req:home-cleanup, cockpit-views#req:home-needs-you

Scenario: The old at-risk work moves to the Cleanup line
Given an at-risk task last active 3 days ago, one 20 days ago and one 40 days ago, none idle for 30 days, and an owner that is not orphaned
When Home is opened and the chip `look` is toggled on Worktrees
Then the Cleanup line counts the worktrees of the tasks last active 20 and 40 days ago and not the one of the task last active 3 days ago (which has its "Needs you" row), and the chip `look` shows exactly those worktrees

### AC: needs-you-chip-is-the-home-set

**Requirements:** cockpit-views#req:home-needs-you, cockpit-views#req:filter-vocabulary

Scenario: The chip and Home list the same tasks
Given tasks at risk last active 2 and 30 days ago and a calm task
When Tasks is opened with the chip `needs-you`
Then it shows exactly the tasks Home lists under "Needs you" (the one last active 2 days ago), in the same order, and its hint says so

### AC: home-badge-is-capped

**Requirements:** cockpit-views#req:top-bar, cockpit-views#req:home-needs-you

Scenario: 294 tasks need the operator
Given 99 tasks that need the operator, then 100, then 294
When the application is opened for each
Then the Home badge reads `99`, `99+` and `99+`, the model exposes the number and the label, and the last two badges carry the whole number in their tooltip

### AC: worktree-pr-join-is-one

**Requirements:** cockpit-views#req:worktrees-list, cockpit-views#req:side-panel

Scenario: A pull request found by branch
Given worktrees `w1` (branch `agent/a`) and `w2`, a pull request that names `w2`, and one that names no worktree but has the repository entry and branch of `w1`
When Worktrees is opened with the chip `pr` and the panel of each worktree is opened
Then the chip leaves exactly `w1` and `w2`, the PR cell of each row names its pull request, and the panel of each lists the same pull request

### AC: web-addresses-are-checked

**Requirements:** cockpit-views#req:home-needs-you, cockpit-views#req:worktrees-list, cockpit-views#req:side-panel

Scenario: A hostile address from another machine
Given pull requests whose `url` is `javascript:alert(1)`, `http://plain.example/1`, `https://user@github.com/a`, `https://github.com:8443/a` and `https://github.com/a b`, and a repository whose `remote_url_web` is `javascript:alert(1)`
When Home, a pull request panel, a worktree panel and a repository page are shown
Then none of them binds such an address to a link, each names the pull request or repository in plain text, and the Raw data block still shows the entry as received

### AC: ready-to-land-groups-by-task

**Requirements:** cockpit-views#req:home-ready-to-land, cockpit-views#req:field-tables

Scenario: Ready, not ready and the slots
Given a task `fix-ci` in state `ready` with pull requests `sneat-dev/wb#12` and `sneat-co/sneat-go#7`, each 5 of 5 checks passed and read 4 and 9 minutes ago, a task in `not-ready` waiting only on checks read 4 minutes ago, and a fake registry offering the landing action for pull requests
When Home is opened, and again with no registry and no owner session
Then a row shows `fix-ci`, 2 repositories, both numbers, "10/10" and the age 9 min of the oldest observation, with one landing Copy entry per local pull request, the not-ready task is shown muted below with "checked 4 min ago" and no action, and without a registry each pull request offers "Copy command" `wb pr land 'sneat-dev/wb#12'`

### AC: in-flight-lists-agents-on-every-machine

**Requirements:** cockpit-views#req:home-in-flight

Scenario: Local and cached agents, runs and sessions
Given a running dispatched run on this machine with activity `working`, a registered session with no activity, and an agent cached from machine `vm` 20 minutes ago
When Home is opened
Then each is listed with runtime and model, task, machine and how long it has run, the session shows "state not reported", the cached agent shows its snapshot age and no action, and "Stop" and "Log" appear only on the dispatched run, as registry actions or "Copy command" entries `wb agent stop <agent-id>` and `wb agent logs <agent-id>`

### AC: in-flight-machine-load-indicator

**Requirements:** cockpit-views#req:home-in-flight

Scenario: Free and busy
Given machine `mac` with `cpu_percent` 40 and 50 percent memory used (route `local`), machine `vm` with `cpu_percent` 85 (route `live-remote`), and machine `old` with a cached sample 30 minutes old
When Home is opened
Then the chips show `mac` free, `vm` busy and `old` with its sample's age and the route `cached`, each with its route label; and a machine whose latest sample lacks `cpu_percent` (the first after a daemon start) shows load `unknown`, never `free`

### AC: resume-lists-five-recent-tasks

**Requirements:** cockpit-views#req:home-resume

Scenario: Last five
Given 8 tasks with distinct last activity
When Home is opened and "Open" is pressed on one
Then 5 tasks are listed newest first, each with its state badge, and "Open" selects that task on Tasks

### AC: cleanup-line-counts-and-chart

**Requirements:** cockpit-views#req:home-cleanup, cockpit-views#req:field-tables

Scenario: Safe, look, and the age chart
Given 12 worktrees of landed tasks with `ahead` 0 and owner state `unknown`, one worktree of a landed task with `ahead` absent, 3 orphaned worktrees, 2 unknown ones of other tasks and 4 not-landed ones idle for 40 days
When Home is opened, "Review & clean" is pressed, and the Cleanup line is expanded
Then the line reads "12 safe to remove; 9 need a look" (the worktree with `ahead` absent is not safe), says the counts are indicative, "Review & clean" opens Worktrees with chip `safe` showing 12 rows, and the expansion shows the worktree-age bars with the five `age:` links, a text alternative and the theme's colours

### AC: fleet-health-only-when-not-ok

**Requirements:** cockpit-views#req:home-fleet-health

Scenario: Healthy, then problems
Given a fleet in which every machine is live and current, and then one with a machine stale for 25 hours, a machine on an older WB, a repository with a scan error and a machine with `remote_error` `wb_too_old`
When Home is opened for each
Then the first shows no Fleet health line, the second shows one line per problem with "Copy fix command" entries `wb remote publish` and `wb self-update`, each labelled "run on <machine>", `wb fleet status --filter='<owner/repository>'` and the command of the remote error

### AC: home-charts-from-throughput

**Requirements:** cockpit-views#req:home-charts

Scenario: With and without throughput, and non-linking
Given a document with a `throughput` block of 30 days and one without
When Home is opened on a desktop viewport for each, and a bar and a number of the charts are clicked
Then the first shows "Throughput" as the first section of Home, above "Needs you", with "Time to finish" (the slowest five named and the median and 90th percentile in its caption) and "Finished per day" (stacked finished and dropped bars), each with a visually hidden table and theme colours, nothing happens on a click because they do not link, and the heading of "Needs you" does not move when the charts arrive, nor when the first complete document arrives after a warming-up one; the second shows neither chart, has "Needs you" as its first section and ends with the line "No charts: the daemon reports no throughput" in the place Throughput used to be

### AC: home-phone-layout

**Requirements:** cockpit-views#req:home-phone, cockpit-views#req:responsive-to-360

Scenario: 360 px wide
Given a viewport 360 px wide, in hosted mode
When Home and each other page are opened
Then sections 1 to 3 are cards, sections 4 to 6 are behind "more", the Throughput charts are on top, both shown, and compact so that the "Needs you" heading is at or above y 400 at 375 x 812 px, and no page scrolls horizontally or breaks

### AC: repository-identity-merges-local-and-cached

**Requirements:** cockpit-views#req:repository-identity, cockpit-views#req:field-tables

Scenario: Merging local and cached rows
Given the fleet document with `sneat-co/sneat-go` as a local entry carrying host `github.com` and as a cached entry with host `github.com` and name `Sneat-Co/sneat-go`, and a second pair differing only in that one carries no host
When the Repositories page is opened
Then each pair is one row keyed by the lower-cased `owner/name`, with a chip for each machine, the cached one showing its age and a stale mark when stale

### AC: cached-repository-names-are-split-into-host-and-name

**Requirements:** cockpit-views#req:repository-identity, cockpit-views#req:field-tables

Scenario: Splitting a snapshot name in the daemon
Given cached snapshot names `github.com/Sneat-Co/sneat-go`, `sneat-co/sneat-go` and `gitlab.example.com/group/sub/proj`, and a local repository with an origin on `github.com`
When the fleet document is requested
Then the first cached entry has `host` `github.com` and `name` `Sneat-Co/sneat-go`, the second has `name` `sneat-co/sneat-go` and no `host`, the third has `host` `gitlab.example.com` and `name` `group/sub/proj`, and the local entry carries `name` as `owner/name` with `host` separate

### AC: repositories-merge-across-machines

**Requirements:** cockpit-views#req:repositories-list

Scenario: One repository on three machines
Given `sneat-co/sneat-go` checked out on three machines with different code-index states and a code browser configured
When the Repositories page is opened
Then one row shows the repository with three machine chips each linking to that machine's checkout, summed counts, the worst code-index state, the newest activity, and two icon buttons with `aria-label` and tooltip for browsing code and opening on the host, and there is no text "Code" link

### AC: repository-actions-follow-configuration

**Requirements:** cockpit-views#req:repositories-list

Scenario: No code browser, no web address
Given a daemon with no code browser configured, a repository with `remote_url_web` and one without
When the Repositories page is opened
Then the browse-code button is absent, the open-on-host button links to `remote_url_web` with `rel="noopener noreferrer"` for the first repository and is absent for the second

### AC: repositories-sort-presets

**Requirements:** cockpit-views#req:repositories-list

Scenario: One-click switch
Given repositories with differing worktree counts, branch counts and activity
When Recent, Most worktrees and Most branches are chosen in turn and the page is reloaded
Then each reorders the rows by last activity, worktree count and branch count, descending, `sort` in the address holds the choice, and it survives the reload

### AC: repositories-quick-filters

**Requirements:** cockpit-views#req:repositories-list

Scenario: The chips
Given repositories with and without worktrees, agents, pull requests, a scan error, and code-index states `fresh`, `stale`, `diverged`, `failed`, `pending` and `never`
When each of the chips `worktrees`, `agents`, `prs`, `index` and `errors` is toggled in turn
Then each leaves exactly the repositories that satisfy it, and `index` leaves exactly those whose state is `stale`, `diverged` or `failed`

### AC: repository-detail-loads-branches-lazily

**Requirements:** cockpit-views#req:repository-detail

Scenario: Opening a repository
Given the fleet document carries no branches and a repository present on two machines
When `/repositories/github.com/sneat-co/sneat-go` is opened
Then the page shows a merged header and one section per machine, requests `/api/v1/cockpit/branches` for that repository only when it opens, shows skeleton rows until it answers, and then lists the branches; the older repository id still opens the page

### AC: worktree-identity-cell

**Requirements:** cockpit-views#req:worktrees-list

Scenario: Task and repository in one cell
Given a worktree of task `fix-ci` in `sneat-dev/wb`
When the Worktrees page is opened
Then the first column holds `fix-ci` in strong type with `sneat-dev/wb` muted, a click on the task part opens the task page, the open button at the row end opens that worktree's page, a click elsewhere in the row selects it, and there is no Task column

### AC: worktrees-columns-and-badges

**Requirements:** cockpit-views#req:worktrees-list

Scenario: Branch column and sync badges
Given a worktree whose branch equals its task with `ahead` 2 and `behind` 1, and one with `upstream_gone`
When the Worktrees page is opened, and again with a worktree whose branch differs from its task
Then the Branch column is hidden the first time and shown the second, the State column shows `↑2` and `↓1` and "gone" beside the owner state, the sync badges and counts say "this machine", and the default order is last activity, newest first

### AC: worktrees-quick-filters

**Requirements:** cockpit-views#req:worktrees-list

Scenario: Six chips
Given worktrees that are active, orphaned, unpushed, upstream-gone, with a pull request and idle for 31 days
When each of the chips `active`, `orphaned`, `unpushed`, `gone`, `pr` and `idle30` is toggled in turn
Then each leaves exactly the worktrees that satisfy it

### AC: agents-list-describes-the-work

**Requirements:** cockpit-views#req:agents-list

Scenario: A claude agent, an unreported session and a cached agent
Given a running `claude` agent with model `opus` on task `fix-ci` in `sneat-dev/wb` on machine `mac` with activity `working` and session id `wbs-3da4ea95`, a session whose activity is absent, and an agent cached from machine `vm`
When the Agents page is opened and the copy button is pressed
Then the first reads runtime, model and task as its label with the badge "working", the machine and the running time, no row shows a session id, the panel shows it and its copy button puts the full id on the clipboard, the session shows "state not reported", the cached agent shows its snapshot age and no action, and the chips `running`, `blocked` and `runtime-claude` filter the list

### AC: agent-label-fallback

**Requirements:** cockpit-views#req:agents-list, cockpit-views#req:agent-fields, cockpit-views#req:field-tables

Scenario: A session with no work link
Given a registered `claude` session on model `opus` with `state` `live` started 2 hours ago that no worktree's owner or claim names
When the Agents page is opened
Then its row reads "claude · opus" followed by "session, started 2 h ago" and it counts as running

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
Then the first column header reads "Machines" in larger type with no separate section heading, each name links to its page, the cached machines show their age and are marked stale, the older version is marked, and the Load cell reads unknown, with CPU and Memory empty (no bar, no zero), for the machines without metrics

### AC: machines-filter-and-stale-chip

**Requirements:** cockpit-views#req:machines-list

Scenario: Filter box and chips
Given three machines of which two are stale, and one of them runs an older WB than the newest in the fleet
When the filter `mac` is typed and the chips `stale` and `outdated` are toggled in turn
Then the filter narrows the rows by machine name, `stale` leaves exactly the two stale machines and `outdated` exactly the machine on the older WB

### AC: machine-detail-metrics-charts

**Requirements:** cockpit-views#req:machine-detail

Scenario: A reporting machine, with its route
Given the local machine with 360 samples (route `local`) and a `boot_time` 3 days ago
When `/machines/<id>` is opened
Then the page shows OS, architecture, CPU count, WB version, state age and uptime of 3 days, four charts over the last hour (CPU percent, load, memory used against total, disk free) marked `local`, counts linking to the filtered lists, the machine's running agents, its most recent worktrees and the "Raw data" block

### AC: machine-without-metrics-says-so

**Requirements:** cockpit-views#req:machine-detail, cockpit-views#req:machine-metrics-route

Scenario: No source, and a cached sample
Given a machine whose route answers `none` with a reason, and another whose route answers `cached` with one sample 30 minutes old
When each machine's page is opened
Then the first says "metrics are not reported for this machine" and shows no chart and no zero values, and the second shows the sample with its age and `cached` and no history chart

### AC: metrics-poll-only-while-visible

**Requirements:** cockpit-views#req:machine-metrics-polling

Scenario: Visible and hidden pages
Given a fake clock and the machine-metrics route stubbed
When Home is shown for 30 seconds, then the Repositories page is shown for 30 seconds, then a Machines page for 30 seconds
Then the route is requested every 10 seconds during the first and third periods and not at all during the second

### AC: action-area-renders-the-registry-and-vanishes-without-it

**Requirements:** cockpit-views#req:action-slots

Scenario: A registry, a test action, and no registry
Given a fake registry that returns for a worktree `pr.create` enabled, `worktree.discard` disabled with the reason "2 commits are not on the remote" and one extra test action, and for a pull request one landing action, and returns nothing for a task
When a worktree's side panel, a pull request's slot in a "Ready to land" row, a worktree page header and a "Needs you" row are opened, and then the registry route is made absent
Then each area shows exactly the registry's actions with the disabled one disabled and carrying its reason, a task's panel has no action area of its own and shows a slot per pull request or worktree, and with the route absent no action area, placeholder or layout gap is rendered anywhere

### AC: copy-command-uses-only-existing-commands-and-identifiers

**Requirements:** cockpit-views#req:copy-the-command

Scenario: Each entity kind as an anonymous reader
Given a worktree, a pull request, a repository, a branch, a dispatched run, a recorded successor session, an unrecorded session and a worktree on machine `vm`, no owner session, and the manifest `ai/capabilities.json`
When each entity's "Copy command" entries are pressed
Then the clipboard holds exactly the commands listed in the requirement with the identifiers single-quoted and flags written `--flag=value`, the repository's creation command carries `--model` and `--original-prompt-file`, the worktree on `vm` is labelled "run on vm", no copied text contains `--apply` or a filesystem path, the unrecorded session offers no entry and says it cannot be controlled, no entry offers to enter a worktree, and the entries are available without an owner session

### AC: copy-command-templates-match-the-manifest

**Requirements:** cockpit-views#req:copy-the-command

Scenario: A unit test over every template
Given every command template of the requirement and `ai/capabilities.json`
When a test parses each template
Then each command path exists in the manifest, every flag used exists on that command, and every flag the command requires is present in the template

### AC: copy-command-placeholders-are-syntax-errors

**Requirements:** cockpit-views#req:copy-the-command, cockpit-views#req:new-task-form

Scenario: Every template through the shell parser
Given every command template of the requirement rendered with its placeholders (here, with an SSH route and labelled "run on"), and again with benign values, and each placeholder placed first, last, after `--flag=`, before a word and before another placeholder
When `bash -n` and `zsh -n` (and `dash -n`, a shell that is absent being skipped) parse each text
Then every text with a `<<<edit:name>>>` placeholder, and each placeholder in each position, makes the shell exit non-zero with a syntax error, every template without a placeholder exits 0, and each is flagged needing an edit exactly when it holds a placeholder

### AC: copy-command-refuses-hostile-values

**Requirements:** cockpit-views#req:copy-the-command, cockpit-views#req:new-task-form

Scenario: Quotes, control characters, a leading dash, and the picker
Given a task named `a'; rm -rf ~; '`, one named `-x`, one containing a newline, a branch named `--upstream`, and repositories named `owner/na me` and `owner/ok.name`
When "Copy command" entries and the "New task" picker are used on each, and on a value with a zero-width space, a U+2028 or a U+FEFF
Then the value with a quote is copied single-quoted with the embedded quote escaped, the values starting with `-` or containing a control, invisible or line-separator character are refused with an explanation and nothing is copied, the picker offers `owner/ok.name` and not `owner/na me`, and no refused value reaches the clipboard

### AC: owner-gating-is-one-affordance

**Requirements:** cockpit-views#req:owner-gating-is-visible

Scenario: Anonymous reader
Given no owner session, so no action capability
When Home is shown
Then no registry request is made, no disabled action is shown, each place where an action would be shows its "Copy command", the session chip offers "Sign in as owner: run `wb cockpit`", and no button carries its own sign-in message

### AC: new-task-form-produces-commands

**Requirements:** cockpit-views#req:new-task-form

Scenario: The form
Given repositories `sneat-co/sneat-go` and `sneat-co/bots-go`
When "New task" is opened, `sneat-*/*-go` is typed in the picker, both are chosen, the task `fix-ci`, the brief `Fix the flaky CI.`, base `main` and model `opus` are entered
Then the copyable commands are `wb worktree create 'fix-ci' 'sneat-co/sneat-go' 'sneat-co/bots-go' --model='opus' --original-prompt-file=<<<edit:file>>> --base='main'` and `wb agent dispatch --repo='sneat-co/sneat-go' --task='Fix the flaky CI.' --profile=<<<edit:profile>>> --new-worktree='fix-ci' --base='main'` (one per repository), each flagged as needing an edit because its placeholders are written bare and are a shell syntax error (`bash -n` and `zsh -n` exit non-zero), the form refuses to produce a command until a model is entered, and nothing is run

### AC: intent-to-done-budgets-hold

**Requirements:** cockpit-views#req:intent-to-done-budgets

Scenario: Palette and preview
Given a fake registry, a fixture fleet and a network request counter
When the palette is opened, and an action slot is activated
Then the palette is visible with no network request made and no route navigation before it appears, and the action slot opens its preview with no route navigation

### AC: responses-are-gzip-with-etag

**Requirements:** cockpit-views#req:compressed-responses

Scenario: Compressed and conditional
Given a daemon serving the fleet document, the branches route and a static asset
When each is requested with `Accept-Encoding: gzip`, the fleet document is then requested again with the ETag it returned, with the identity ETag, and once with no `Accept-Encoding`
Then every compressed response carries `Content-Encoding: gzip`, an ETag ending in `-gzip` and `Vary: Origin, Accept-Encoding`, both conditional requests are answered `304`, and the request without the header receives an identity body with its own ETag

### AC: gzip-bytes-are-computed-once

**Requirements:** cockpit-views#req:compressed-responses

Scenario: Many requests, one snapshot
Given a snapshot stored once and a counter on the compressor
When the fleet document is requested 100 times with gzip
Then the compressor ran once for that snapshot, and it runs once more after a new snapshot is stored, and passes over an unchanged fleet with every side read on store no snapshot: the ETag and `snapshot_at` stay, a poll with `If-None-Match` is answered `304`, and `X-Wb-Cockpit-Checked-At` moves with each pass

### AC: static-assets-are-precompressed-and-immutable

**Requirements:** cockpit-views#req:compressed-responses

Scenario: Build output
Given a production build
When a content-hashed asset and the index document are requested with gzip
Then the asset is served from a build-time compressed file with `Cache-Control: public, max-age=31536000, immutable`, and the index document carries a fresh style nonce, is `no-cache` and is compressed for that response

### AC: hosted-origin-can-revalidate

**Requirements:** cockpit-views#req:hosted-origin-conditional-requests

Scenario: A preflight and a response from the hosted origin
Given `cockpit.hosted_url` is `https://hosted.example.test/wb/cockpit/`
When the origin `https://hosted.example.test` requests the fleet document and the branches route, with a preflight first, and then repeats with `If-None-Match`
Then the preflight allows `If-None-Match`, the responses expose `ETag` and `X-Wb-Cockpit-Checked-At`, the repeat is answered `304`, and a request from any other foreign origin is still refused with status 403

### AC: branches-leave-the-document

**Requirements:** cockpit-views#req:lazy-branches-route

Scenario: Document, route, cached repository and unknown id
Given a local repository with 12 branches and a repository cached from another machine
When the fleet document is requested, then `/api/v1/cockpit/branches?repository=<local id>` without a session, then the cached repository's id, then an unknown id, with a counter on Git commands
Then the document has no `branches` collection and reports the branch counts, the first route returns the 12 branches with status 200, the cached id returns status 200 with an empty list and a `reason`, the unknown id gets status 404 with no data, and no Git command ran on any request

### AC: repository-entries-carry-activity-and-web-url

**Requirements:** cockpit-views#req:repository-activity-fields

Scenario: Newest branch activity, local and cached
Given a local repository on `github.com` named `sneat-dev/wb` whose newest local-branch activity is at a known time and whose origin URL carries a credential, and a repository cached from another machine
When the fleet document is requested
Then the local entry has `last_activity_at` equal to that time and `remote_url_web` equal to `https://github.com/sneat-dev/wb`, the cached entry has no `last_activity_at`, and no origin URL or credential appears anywhere in the document

### AC: hostile-host-has-no-web-link

**Requirements:** cockpit-views#req:repository-activity-fields

Scenario: Hosts and names that are not safe
Given repositories whose host is `evil.example/x?y=1`, whose host contains a space, and whose name segment is `..`, `a b` or `x%2Fy`, and one valid `github.com/sneat-dev/wb`
When the fleet document is requested
Then only the valid repository has `remote_url_web`, and the others omit the field

### AC: owner-state-mapping

**Requirements:** cockpit-views#req:owner-state-vocabulary, cockpit-views#req:field-tables

Scenario: Liveness locally, normalisation of the rest
Given local worktrees whose recorded owner process is alive, whose recorded owner process is gone, and with no owner process recorded, cached worktrees that published `idle`, `unknown` and a value `sleepy`, and a worktree with no value
When the fleet document is requested twice within one snapshot, with a counter on the liveness probe
Then the local `owner_state` values are `active`, `orphaned` and `unknown`, never `idle` and never from the heartbeat, the cached ones are `idle`, `unknown` and omitted (the out-of-set value is dropped), the last has no `owner_state`, the probe ran once per worktree for the snapshot, and the application's types accept all four values

### AC: worktree-entries-carry-name-and-sync

**Requirements:** cockpit-views#req:worktree-name-and-sync-fields, cockpit-views#req:field-tables

Scenario: Known, unknown and cached sync facts, and lifecycle
Given a local worktree whose branch is 2 ahead and 1 behind, a local one whose upstream is gone, a local one with no upstream configured, a local one with no sync information, a cached one, and a local claim that holds a lifecycle and one that does not
When the fleet document is requested
Then the first has `ahead` 2, `behind` 1 and `has_upstream` true, the second `upstream_gone` true, the third `has_upstream` false, the fourth and the cached one have none of the four fields, every worktree has a `name` equal to its task that contains no path separator, and `lifecycle` is present only where the claim or record already held it

### AC: pull-request-entries-carry-state-and-checks

**Requirements:** cockpit-views#req:pull-request-fields, cockpit-views#req:field-tables

Scenario: Observed, failed read, merged, and over the bound
Given a fake observer returning for pull request 12 state open with 3 passed, 1 skipped, 1 failed named `go-ci / test` and 1 pending check and `mergeable` `blocked`, for pull request 13 a successful then a failing read, for pull request 14 state merged, and 5 bound pull requests with `cockpit.pull_request_limit` 3
When the snapshotter ticks three times with the second tick failing for 13, and the fleet document is requested with a counter on GitHub reads
Then entry 12 carries `state` `open`, `checks_total` 6, `checks_passed` 3, `checks_skipped` 1, `checks_failed` 1, `checks_pending` 1, `failed_check` `go-ci / test`, `mergeable`, `checks_green` false and `checked_at`, entry 13 keeps its first values and `checked_at`, entry 14 is not observed again after one confirmed merged observation, no more than 3 pull requests are observed per tick, the longest due first, and no request caused a GitHub read

### AC: pull-request-state-absent-until-observed

**Requirements:** cockpit-views#req:pull-request-fields

Scenario: Never observed
Given a bound pull request whose observation has never succeeded
When the fleet document is requested and the Tasks page is opened
Then its entry has only `number`, `repository` and `url`, and the application says the pull request's state is not reported and counts it as neither open nor landed

### AC: pull-request-strings-are-hostile-safe

**Requirements:** cockpit-views#req:pull-request-fields, cockpit-views#req:field-tables

Scenario: Hostile names and addresses
Given an observation whose first failing check is named with 300 characters, control characters and a right-to-left override, one whose `mergeable` is `<img onerror=1>`, and pull requests whose `url` is `javascript:alert(1)`, `http://example.com/x`, `https://user@example.com/x`, `https://example.com:8443/x` and `https://github.com/o/r/pull/1`, and a cached pull request whose `state` is `bogus`
When the fleet document is requested and the Tasks page and Home render them
Then `failed_check` is at most 100 characters with the control and bidirectional characters removed and is rendered as text, the `mergeable` value is omitted, only the last `url` is emitted, the cached `state` is omitted, and nothing is rendered as markup

### AC: agent-activity-joins-herdr-by-session

**Requirements:** cockpit-views#req:agent-activity

Scenario: Matched, unmatched, and no herdr
Given a fake herdr whose agent list holds two agents with statuses `blocked` and `working` and harness session ids, a registered session with the first id, a session with no match, and then a daemon with no herdr
When the snapshotter refreshes and the fleet document is requested
Then the matched session has `activity` `blocked`, the unmatched one and every agent without herdr have no `activity`, herdr was listed once per refresh, and no screen text appears anywhere in the document

### AC: agent-entries-carry-run-links

**Requirements:** cockpit-views#req:agent-fields, cockpit-views#req:field-tables

Scenario: Run, finished run, sessions
Given a running dispatched run with a run record naming a repository, a worktree and a start time, a run finished `failed` with exit code 2 at a known time with a free-text failure, a live session that a worktree's owner names, a parked session, and a session that nothing names
When the fleet document is requested
Then the running run has `worktrees`, `task`, `repository` and `started_at`, the finished run also has `finished_at` and `exit_code` 2 but never the free text, the sessions have `state` `live` or `parked`, the claimed session has `started_at` plus that worktree and task, and the bare session has only `started_at` and no worktree, task or repository

### AC: remote-agents-are-capped

**Requirements:** cockpit-views#req:agent-fields, cockpit-views#req:remote-snapshot-agents-and-metrics

Scenario: 500 agents from a snapshot, and at publish
Given a published snapshot of machine `vm` with 500 agents with 5,000-character strings, and a local machine with 500 agents publishing with `remote.publish.agents` true
When the fleet document is read and a snapshot is published
Then at most 200 `vm` agents are in the document and at most 200 are published, every string is length-capped or blanked, and `agents_truncated` is set on the `vm` machine entry (and the published snapshot carries the truncated marker) but not on this machine's document

### AC: machine-entries-carry-hardware-and-no-metrics

**Requirements:** cockpit-views#req:machine-fields, cockpit-views#req:field-tables

Scenario: Local, snapshot with hardware, snapshot without
Given the local machine, a machine whose published snapshot carries `os`, `arch`, `cpu_count` and `boot_time`, and one whose snapshot does not
When the fleet document is requested
Then the first two have the four fields with `boot_time` an RFC 3339 time, the third has none, no entry has `metrics`, and no process list, path or environment value appears

### AC: document-carries-refresh-interval

**Requirements:** cockpit-views#req:refresh-interval-field

Scenario: A configured interval
Given `cockpit.refresh_interval` set to 45 seconds
When the fleet document is requested
Then it carries `refresh_interval_seconds` 45

### AC: field-tables-hold-in-the-document

**Requirements:** cockpit-views#req:field-tables, cockpit-views#req:schema-version-2

Scenario: Every kind, local and cached
Given a fixture with local and cached entries of every kind, including values outside the closed sets
When the fleet document is requested
Then it carries `schema_version` 2, has no `branches` and no `metrics`, every field of every table has the documented type and appears only where the last column allows (the local-only fields are absent on cached entries), `name` is `owner/name` and `host` separate, existing field names are unchanged, and values outside a closed set are dropped

### AC: periodic-publish-runs-after-a-local-scan

**Requirements:** cockpit-views#req:periodic-remote-publish

Scenario: Opt-in, minimum, failure and no store
Given a daemon with a fake remote store and a fake clock, first with `remote.publish.interval` unset, then set to 1 minute, then to 10 minutes, a store that fails once, and a daemon with no remote store
When scans complete and time passes
Then nothing is published while the interval is unset, publishes happen only after a successful scan and never closer than 5 minutes apart, the interval of 1 minute is raised to 5, a failed publish is retried at the next interval and the local snapshot is not delayed, and the daemon with no store publishes nothing

### AC: periodic-scan-reuse-is-bounded-by-the-oldest-kept-read

**Requirements:** cockpit-views#req:periodic-remote-publish

Scenario: A scan that kept an old read is not taken again
Given a publisher with a 1 hour keepalive whose last scan kept a repository read made 50 minutes before that scan, and a source whose change token has not moved
When the next attempt is made 6 minutes later, and again 6 minutes after that
Then the first takes the scan again and runs none, and the second runs a new scan, because the oldest read in the held scan is by then older than the keepalive

### AC: remote-snapshot-carries-optional-agents-and-metrics

**Requirements:** cockpit-views#req:remote-snapshot-agents-and-metrics

Scenario: Flags, old reader, old hub
Given a publisher with `remote.publish.agents` and `remote.publish.metrics` each true and each unset, the current YAML decoder of the previous wb version, a hub provider model that accepts the fields, and an older hub that refuses them with status 400
When snapshots are encoded, decoded by the old decoder and published through each hub
Then agents and metrics are present only when their flag is true, `schema_version` is unchanged, the old decoder returns the snapshot without error and without those fields, the new hub model accepts and stores them, the older hub's 400 is followed by one retry without the optional fields and a recorded diagnostic, and the local fleet document shows that machine's agents as `cached` with the snapshot's age and no actions

### AC: throughput-block-from-sealed-records

**Requirements:** cockpit-views#req:throughput-block

Scenario: Every disposition, two days and an old record
Given sealed terminal records on two days within 30 days: tasks sealed `landed`, `recycled` and `removed` with known claim `recorded_at` times, a task sealed `discarded` and `landed` on the same day, one `orphaned`, one `handoff`, and one landed 40 days ago
When the collector runs twice and the fleet document is requested
Then `throughput` carries `window_days` 30, `per_day` with `finished`, `dropped` and `landed` counts for the two days (one task once a day, finished winning over dropped, `handoff` counted in neither, the old record excluded), `slowest` of at most five finished tasks with `duration_seconds` equal to `sealed_at` minus the claim's `recorded_at`, and the `median_seconds` and `p90_seconds` of the finished durations by nearest rank, and the second run reads no record again

### AC: throughput-is-omitted-without-timestamps

**Requirements:** cockpit-views#req:throughput-block

Scenario: No usable record
Given no sealed terminal record that has both a claim creation time and a sealed time
When the fleet document is requested
Then it has no `throughput` block and invents no value (a record with a `handoff` or unknown disposition, or a missing timestamp, is not usable)

### AC: sampler-fills-a-ring-buffer

**Requirements:** cockpit-views#req:metrics-sampler

Scenario: 400 samples, then no source
Given a sampler with a fake source and a fake clock, and a build for Windows
When 400 ten-second ticks elapse, and separately the source reports unsupported
Then the buffer holds exactly the newest 360 samples oldest first, each tick spaced 10 seconds apart, no sampling ran on a request, the unsupported platform reports that metrics are unsupported without an error, and `GOOS=windows go build ./...` succeeds

### AC: metrics-route-is-compressed-and-revalidatable

**Requirements:** cockpit-views#req:compressed-responses, cockpit-views#req:hosted-origin-conditional-requests, cockpit-views#req:machine-metrics-route

Scenario: The metrics route
Given a daemon serving `machine-metrics?machine=<id>` for the local machine
When it is requested with `Accept-Encoding: gzip` and again with its ETag, and from the hosted origin `https://hosted.example.test` with a preflight and `If-None-Match`
Then the response carries `Content-Encoding: gzip`, an ETag ending in `-gzip` and `Vary: Origin, Accept-Encoding`, the repeats are answered `304`, the preflight allows `If-None-Match`, the responses expose `ETag`, and any other foreign origin is refused with status 403

### AC: metrics-route-serves-each-source

**Requirements:** cockpit-views#req:machine-metrics-route

Scenario: Local, live-remote, cached, none and unknown
Given a sampler holding 5 samples for the local machine, a cached live fetch for machine `vm`, a snapshot sample for machine `old`, a machine with no source, and an unknown id
When `/api/v1/cockpit/machine-metrics?machine=<id>` is requested without a session for each
Then the local answer has `route` `local` and the 5 samples oldest first with only the seven named fields, `vm` has `live-remote` with `fetched_at`, `old` has `cached` with one sample and its `sampled_at`, the machine with no source has `none`, an empty list and a reason with status 200, the unknown id gets 404, and no request-time fetch happened

### AC: configured-target-appears-live-remote

**Requirements:** cockpit-views#req:remote-exporter-transports, cockpit-views#req:remote-http-fetch, cockpit-views#req:remote-entries-replace-cached, cockpit-views#req:machine-metrics-route

Scenario: A healthy HTTP route
Given machine `vm` with an `http` section whose fake server returns an envelope with 3 worktrees, 1 running agent and 360 metric samples, an `ssh` section with a counting fake `remotessh.Runner`, and published-store entries for `vm` that are 25 days old
When the daemon runs through one refresh interval on a fake clock and the fleet document and `machine-metrics?machine=<vm id>` are requested
Then the `vm` worktrees and agent appear with `route` `live-remote`, `transport` `http` and `observed_at` equal to the envelope's time, replacing the cached entries, with their `owner_state`, sync facts and pull request state, the metrics answer has `route` `live-remote` with `fetched_at` and the history, no ssh process was started, no request caused an outbound call, and the browser made no request to `vm`

### AC: response-machine-name-is-ignored-for-placement

**Requirements:** cockpit-views#req:remote-envelope-is-untrusted

Scenario: A payload that names another machine, or this one
Given a target `vm` whose export, over either transport, names machine `mac`, another whose export is the local machine's own (by its name, or by its machine entry's id under another name), and entries for a third machine
When the daemon decodes them
Then the entries are placed on `vm` whatever other machine they name, the local machine's own export is refused whole with `remote_error` `self_export`, nothing is applied to the local machine, the third machine's entries are dropped, and no machine name in the response is used for placement

### AC: failed-export-shows-a-typed-error

**Requirements:** cockpit-views#req:remote-error-is-visible, cockpit-views#req:remote-entries-replace-cached, cockpit-views#req:remote-exporter-transports

Scenario: Each failure, backoff and age
Given machines each with one transport that fails in turn with a refused connection, status 401, an unresolvable host, `Permission denied (publickey)`, a deadline overrun, `wb: command not found`, `unknown command "cockpit export"`, an export of `daemon_not_running`, an export of `export_refused` and an invalid payload, and published-store entries for each
When the daemon fetches repeatedly and the fleet document and Home are read
Then the entries carry `remote_error` `http_unavailable`, `http_auth_failed`, `ssh_unavailable`, `auth_failed`, `timeout`, `wb_missing`, `wb_too_old`, `daemon_not_running`, `export_refused` and `bad_payload` respectively and the published-store entries are shown with their age, the delay between attempts doubles up to 5 minutes, the local snapshot is never delayed, Fleet health names the failed transport and shows the command to copy and no remote stderr or body text, and the next success clears the field

### AC: no-route-configured-makes-no-request

**Requirements:** cockpit-views#req:remote-exporter-transports, cockpit-views#req:remote-ssh-fetch

Scenario: No target, no section, opted out
Given a daemon with no `session_move` section, one whose target has only a `synchestra` section, one with an ssh target and `cockpit.remote_ssh: false`, and one with an http target and `cockpit.remote_http: false`
When each runs for 60 seconds on a fake clock with a counting Runner and a counting HTTP client
Then no ssh process is started and no HTTP request is sent by any, and the metrics route answers from the snapshot or `none`

### AC: hub-export-route-requires-a-machine-bearer

**Requirements:** cockpit-views#req:hub-export-route

Scenario: Credentials, identity, scope of data, and the hosted service
Given a daemon-hosted hub, a machine credential with `machine_snapshot:read` of the host owner's identity, one of another identity, a peer credential with `peer:session` alone, a session cookie, and the hosted multi-identity service
When `GET /v0/workbench/machines/export` and `?metrics_only=1` are requested with each, and with none, and a Cockpit route is requested with `Host: vm.example`
Then only the owner's machine credential receives the envelope of this machine alone (without `fleet` for the metrics-only call), the other identity is refused with 403, the others with 401, the hosted service serves no such route, no new scope exists, the route is not under `/api/v1/cockpit/`, the Cockpit route is still refused with status 421, and a machine with `cockpit.anonymous_metadata: false` answers the owner's credential 403 `export_refused`, which the reading daemon shows as `export_refused`

### AC: bearer-stays-with-the-configured-host

**Requirements:** cockpit-views#req:remote-http-fetch

Scenario: Redirect, plain HTTP, URL validation, wrong host
Given an `http` section whose server answers 302 to another host, one with an `http://` non-loopback URL, one with a URL carrying user information, a path and a query, one with a loopback `http://` URL, and a token file
When the daemon fetches
Then the redirect is not followed and no request reaches the other host, the non-loopback `http://` URL and the URL with user information, a path or a query are refused by `ValidateHubURL` without any request, the loopback one is used, every request carries the bearer only to the configured host, the address comes only from local configuration and never from a snapshot or a response, and the response is capped at 8 MiB

### AC: http-failure-falls-back-to-ssh

**Requirements:** cockpit-views#req:remote-exporter-transports

Scenario: Fallback-class and non-fallback failures
Given a machine with both an `http` and an `ssh` section and a fake Runner, and an HTTP server that answers in turn with a refused connection, a timeout, 401, 403, 404, 429, 500 and a redirect, and then a well-formed envelope that fails validation
When the daemon fetches after each
Then each of the first eight attempts is followed by an SSH export, the entry shows the data with `transport` `ssh` and `remote_error` of the HTTP failure, and the invalid envelope sets `bad_payload` with no SSH attempt

### AC: fallback-cool-down-is-honoured

**Requirements:** cockpit-views#req:remote-exporter-transports

Scenario: Five minutes on SSH, then HTTP again
Given a machine whose HTTP failed once and whose SSH works, on a fake clock
When time advances by 4 minutes and then by 2 minutes more, and HTTP then succeeds
Then no HTTP request is made during the first 5 minutes, one is made after them, and on its success the entry returns to `transport` `http` with `remote_error` cleared

### AC: hostile-payload-is-refused

**Requirements:** cockpit-views#req:remote-envelope-is-untrusted, cockpit-views#req:remote-exporter-transports

Scenario: Oversized, unknown field, long string, filesystem path, bad numbers, future time, too many samples
Given exports, over HTTP and over SSH, of 9 MiB, one with an unknown top-level field, one with a 10,000-byte task name, one whose worktree entry carries a `path`, one with a negative count, a `NaN` percentage and a sample 5 minutes in the future, and one with 361 samples
When each is decoded
Then each is refused with `remote_error` `bad_payload` (the one whose fault is the sample in the future with `clock_skew`), nothing from it is rendered, and the stdout or body buffer never held more than the cap

### AC: metrics-only-export-is-demand-driven

**Requirements:** cockpit-views#req:remote-exporter-transports, cockpit-views#req:machine-metrics-polling

Scenario: Demand and idle
Given a configured target, a client that reads the fleet document throughout, and a fake clock
When no client requests the machine's metrics for 5 minutes, then a client requests them every 10 seconds for 2 minutes, then stops
Then fleet exports run once per refresh interval throughout, metrics-only exports run every 30 seconds only while a request is within the last 60 seconds, and none run after that window closes

### AC: remote-reads-follow-demand

**Requirements:** cockpit-views#req:remote-exporter-transports, cockpit-views#req:remote-entries-replace-cached

Scenario: Idle, viewed, Machines page, refused login, by reader
Given a configured target read over SSH on a fake runner, the default 60 second refresh interval and a fake clock
When nobody reads the fleet document for hours, then an owner session reads it once a minute, then the owner also requests the machine's metrics every 10 seconds, and, on other daemons, the same reads are made by an anonymous reader on this machine, by the hosted page and by the export verb, a machine with both routes is read by each of them with its HTTP route answering and failing, and the machine's SSH login is refused
Then the machine is read 4 times an hour with nobody looking and its entries stay live with their age, the first read of the fleet document by an owner after the quiet time is answered from what is held and starts one export at once, a read by the export verb or by the hosted page starts none and records nothing, the machine is read 60 times an hour while an owner reads the document and 120 times an hour with the metrics requested, an anonymous reader, the hosted page and the export verb leave the SSH logins at 4 an hour over the simulated hour whatever they read, an anonymous reader has a machine with an HTTP route read over HTTP 60 and 120 times an hour with no SSH login while HTTP answers and with 4 SSH logins an hour while it fails, the hosted page raises neither transport, with refresh intervals of 10, 45 and 70 seconds and both demands no two exports of either kind start within 30 seconds of each other, a refused login met by a metrics-only export holds the fleet export back too, the refused login is retried after 2, 4, 8, 16, 32 and then every 60 minutes, and a machine that also has an HTTP route is still asked over HTTP every 5 minutes meanwhile

### AC: ssh-argument-vector-contains-only-configured-values

**Requirements:** cockpit-views#req:remote-ssh-fetch

Scenario: A hostile snapshot and request
Given a fake `remotessh.Runner` recording its arguments, a target whose host, user and `wb_path` are configured, a published snapshot naming a machine `vm; touch x`, and a request with a hostile machine id
When the daemon fetches and the metrics route is requested
Then the argument vector equals `remotessh.BuildWith(options, host, user, [wb_path, "cockpit", "export", "--format", "json"])` with the transport's fixed options (a 5 second connect timeout, no forwarding and the unattended options; with `--metrics-only` for the metrics call) and nothing else, derived from configuration only, `BatchMode=yes` is set, nothing is written to its standard input, and no value from the snapshot or request appears in it

### AC: export-without-a-daemon-fails-and-starts-nothing

**Requirements:** cockpit-views#req:cockpit-export-verb

Scenario: No daemon, and a daemon refusing anonymous reads
Given a machine with no running daemon, and one whose daemon has `cockpit.anonymous_metadata: false`
When `wb cockpit export --format json` is run on each
Then each exits with code 1 printing `{schema_version, error}` with `daemon_not_running` and `export_refused`, no daemon was started, no browser opened, no login code minted, and the command's manifest rows exist

### AC: export-carries-only-the-metadata-set

**Requirements:** cockpit-views#req:cockpit-export-verb

Scenario: A running daemon
Given a running daemon with worktrees, agents, pull requests and 360 samples, and another whose document is over the verb's bound because of the other machines it shows
When `wb cockpit export --format json` and then with `--metrics-only` are run
Then the first prints one envelope with `fleet` and `metrics` within 8 MiB whose fields all belong to the anonymous-readable metadata set, the second omits `fleet`, and neither contains a path, origin URL, free text or process data; the first read this machine's own entries only (`scope=own`) and the second its machine entry only (`scope=machine`); the daemon that shows many other machines is exported like any other; and a daemon that does not know the scope is read as before

### AC: non-loopback-metrics-request-is-refused

**Requirements:** cockpit-views#req:remote-exporter-transports, cockpit-views#req:machine-metrics-route

Scenario: A remote caller of the Cockpit route
Given a daemon listening on loopback
When `/api/v1/cockpit/machine-metrics` is requested with `Host: vm.example`, and with `X-Forwarded-For` and no session
Then the first is refused with status 421, the second with 401, and neither returns a sample, and no Cockpit route serves another machine's data

### AC: copy-command-for-an-ssh-machine

**Requirements:** cockpit-views#req:copy-the-command

Scenario: With and without an SSH route
Given a worktree on machine `vm` that has `ssh` `host` `vm.example`, `user` `alex` and `wb_path` `/usr/local/bin/wb`, and one on machine `old` with none
When the "Copy command" entry `wb worktree list '<task>'` is pressed on each
Then the first copies `ssh alex@vm.example /usr/local/bin/wb worktree list 'fix-ci'` with the interpolated value single-quoted, and the second copies `wb worktree list 'fix-ci'` labelled "run on old"

### AC: fleet-document-fits-the-budget

**Requirements:** cockpit-views#req:fleet-document-size

Scenario: Fixture of 500, 600, 4,000, 3
Given the fixture of 500 repositories, 600 worktrees, 4,000 branches and 3 machines, with realistic names and `code_index` entries that carry statistics
When the fleet document is requested with gzip
Then the response body is at most 150 kB

### AC: initial-script-fits-the-budget

**Requirements:** cockpit-views#req:initial-script-size

Scenario: Production build
Given the production build of `cockpit/web`
When `tools/finish-build.mjs` runs its JavaScript budget check, and Home, then a detail page, are opened
Then the JavaScript needed to render Home, counted over JavaScript files only, is at most 350 kB raw, the build fails when it is larger or cannot be measured, and Chart.js and the detail-page code load as separate chunks only on the routes that use them

### AC: list-never-exceeds-60-row-elements

**Requirements:** cockpit-views#req:bounded-row-elements

Scenario: Largest lists at 1080 px
Given the fixture and a viewport 1080 px high
When the Repositories and Worktrees pages are rendered and scrolled to the end
Then at no moment are there more than 60 row elements in the DOM at 1080 px, and none above 80 at any height

### AC: filtering-5000-rows-is-fast

**Requirements:** cockpit-views#req:fast-filtering

Scenario: Benchmark
Given 5,000 rows
When the matcher and view functions filter them with a multi-term glob query many times, and a key is typed in a filter box
Then the median time is under 30 ms with the CI multiplier of 5 applied, and the list updates on the next animation frame with no debounce timer

### AC: unchanged-snapshot-does-nothing

**Requirements:** cockpit-views#req:no-recompute-when-unchanged

Scenario: Poll returns 304
Given the application showing a snapshot with counters on the derivation functions and on list-row rendering
When the next poll returns `304`, and a later poll returns an identical document, and a relative age text ticks
Then neither poll recomputes a derived collection or re-renders a list row, and only the clock-bound text updates

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
Given a page showing fixed-height skeleton rows and cards
When the data arrives
Then the cumulative layout shift measured by a layout-shift observer is under 0.01

### AC: usable-at-360-wide

**Requirements:** cockpit-views#req:responsive-to-360

Scenario: Phone width
Given a viewport 360 px wide
When Home and each list page are opened
Then there is no horizontal scrolling of the page, the tabs form a scrollable strip, low-priority columns are dropped, and the charts are stacked

### AC: csp-and-canvas-only

**Requirements:** cockpit-views#req:strict-csp-unchanged

Scenario: Charts under the policy
Given Home with charts
When it is loaded and the browser console is read
Then the response policy has no `unsafe-inline` for scripts and styles use a nonce, no policy violation is reported, and the charts are canvas elements

### AC: views-coverage-gates-hold

**Requirements:** cockpit-views#req:views-new-code-fully-covered

Scenario: The gates on a views pull request
Given a pull request that changes Go code and `cockpit/web`
When `wb coverage --changed`, the `cockpit/web` test run with its thresholds, the component-spec check and the stubbed end-to-end run execute
Then every added Go statement is covered, thresholds of 100 hold for statements, branches, functions and lines, every new component has a rendering test, and the end-to-end tests pass against stubbed responses

### AC: derived-collections-computed-once

**Requirements:** cockpit-views#req:derived-collections-memoised

Scenario: Many renders, one snapshot
Given one fleet document and a counter on the derivation functions
When Home, Tasks and Repositories are rendered repeatedly and a filter is typed
Then merged repositories, tasks with state, the "Needs you" items, the ready-to-land list and the cleanup counts were each computed once for that document, and a new document computes them once more

## Open Questions

- `cockpit.anonymous_metadata: false` currently means a machine's metadata is exported by no
  transport, the hub route with the owner's machine bearer included (REQ:hub-export-route; the
  conservative default, ruled by the plan's coordinator on 2026-10-01). The founder may later allow
  a machine bearer of the host owner to override the opt-out, since that credential is not an
  anonymous reader.

- Which herdr server should the daemon read for `activity`, and should it be configurable? Today
  it reads the one its own environment reaches (the default server for a launchd or systemd
  daemon); agents in another herdr server, or another named session, show no activity. Naming a
  socket or session in configuration is not specified here.
- The throughput charts draw `finished` and `dropped`. `per_day[].landed` is now the day's
  proved landings (work-log#req:terminal-disposition-vocabulary; before it, cleanup sealed
  merged work as `removed`: 3,411 of 3,680 terminal records on the founder's machine against
  62 `landed`). Should the charts draw `landed`, and should `removed` (now a worktree that
  never committed, a review checkout or a merge candidate) still count as finished?
- On Windows the owner-process liveness read for `owner_state` cannot tell a gone process from a
  live one (`Signal(0)` is unsupported there), so a Windows machine's local worktrees read as
  `unknown` and its `boot_time` from `GetTickCount64` is not checked for 32-bit truncation.
- Which remote store is the fleet's shared one is undecided: the Mac reads the git
  store and the VM publishes to its own hub. Until it is settled, periodic publish
  uses whatever each machine has configured; it is opt-in and the fallback for machines with
  no live route, which Cockpit reads over HTTP or SSH.
- Founder, 2026-10-01: "its interesting idea to have wb state pushed to some remote ingitdb
  repo". The opt-in periodic publish (REQ:periodic-remote-publish) could target a remote
  inGitDB repository as the shared store. Because Git keeps history, that store could also feed
  trend charts (throughput, worktree debt over time, machine load) and let the hosted or phone
  Cockpit read fleet state without reaching any machine. Open, not specified here: how it relates
  to the existing `remote.provider: git` store (a state repository such as `sneat-dev/wb-state`,
  one snapshot per `<login>/<machine>`, every publish a commit) and to the daemon-hosted hub's
  inGitDB engine (`hub.store.engine: ingitdb`); write frequency versus repository growth, since
  every publish is a commit; and what may leave the machine, that is, the redaction of
  `remote.publish.unpushed` and of the opt-in agents and metrics.
- A daemon run by launchd or systemd may have no SSH agent socket, so the key used for
  `session_move.targets.<machine>.ssh` must work non-interactively (an unencrypted key or
  a key in a keychain the service can read); a failure shows as `auth_failed`.
- The dashboard's `GET /api/v1/log` serves the tail of the daemon's log to an owner session only
  ([cockpit](../cockpit/README.md)#req:daemon-log-is-owner-only), so the log may hold what the
  fleet document may not: the sanitised end of a failed `ssh` call's stderr. Every other writer to
  that log is still expected to keep secrets (tokens, key material) out of it, since the file is
  readable on the machine by its user.
- `auth_failed` is told from `ssh_unavailable` by four fixed OpenSSH phrases on stderr, because
  `ssh` exits 255 for every failure of its own. A client that words them differently is shown as
  `ssh_unavailable`; both codes offer the same command to try by hand.
- A remote wb that names another export `schema_version` is `wb_too_old` over SSH and
  `bad_payload` over HTTP. Making the HTTP transport say `wb_too_old` too is a follow-up.
- Whether Stop and Reply for hand-started sessions should be built on herdr prompts
  is undecided; today only dispatched runs offer Stop and Log.
- Follow-ups required in `cockpit-actions`, to be specified there and not here:
  running "New task" from the Cockpit, Stop, Log and Reply on a run, and the cleanup
  flow (`wb worktree gc` as the dry-run preview and `--apply` as the run, as a daemon
  operation with progress).
- The local `owner_state` of REQ:owner-state-vocabulary is the owner-process liveness, which the
  current local mapper does not read (it reads only the heartbeat). The contract task caches it per
  snapshot and reports the measured cost; the heartbeat fallback is not part of this Feature.
- Elapsed pending time of a pull request's checks is not available (the observation
  carries no start time), so Home shows how long ago the checks were read.

---
*This document follows the https://specscore.md/feature-specification*
