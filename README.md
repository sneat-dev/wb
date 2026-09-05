# WB — the Workbench

Fleet-wide operations across **your** GitHub repositories, from the
terminal: keep every local clone in sync with GitHub, and run config-driven
recipes across every repo that matches — no per-repo scripting.

Part of [Sneat.work](https://sneat.work/bench). The CLI
and executable stay intentionally short: `wb`.

The canonical public Workbench site is [sneat.work/bench](https://sneat.work/bench).

## Install

On macOS or Linux, install the published Homebrew cask:

```sh
brew install --cask sneat-dev/tap/wb
```

On macOS or Linux, the release installer selects the matching platform and
architecture:

```sh
curl -fsSL https://sneat.work/bench/install/get-cli | sh
```

To build from source with Go instead:

```sh
go install github.com/sneat-dev/wb/cmd/wb@latest
```

On Windows, install [WSL](https://learn.microsoft.com/windows/wsl/install) from
an administrator PowerShell session and complete its one-time setup after any
prompted restart. Then install the supported Linux release through WSL:

```powershell
wsl --install
wsl sh -lc 'curl -fsSL https://sneat.work/bench/install/get-cli | sh'
```

Native Windows releases are not currently published; the supported Windows
path is WB running in WSL.

## Agent skills

The portable [WB Agent Skills](ai/README.md) teach Codex, Claude Code, and other
Agent Skills clients when and how to use every public WB command. Thin command
skills defer detailed flags until needed; workflow skills compose safe code
changes and dependency campaigns without making agents rediscover the process.
Every harness reads the same `ai/skills/*/SKILL.md` files.

Implementation completion is defined once in
[`wb-change`'s completion contract](ai/skills/wb-change/references/completion.md).
WB agents report whether work is `implemented`, `published`, `landed`, or
`blocked`; they do not claim that a change reached `main` without exact merge
and CI evidence.

## Commands

```
wb sync   [flags]            # clone/pull/prune local clones to match GitHub, in parallel (--publish shares state after)
wb run    [recipe] [flags]   # run a fleet-wide recipe defined in config
wb migrate <spec> <roots...> # plan or apply a declarative source migration
wb deps set <kind> <dep>@<v> # set existing dependency references to an exact version
wb deps bump <kind> --changed M@V # propagate published go or npm releases through dependency waves (--latest/--scope, --exclude/--hold)
wb deps graph [path] [flags] # inspect dependency topology and open an SVG report
wb deps drift [path] [flags] # report go/npm dependency convergence, replaces, splits, behind-latest
wb deps peers <pkg> --against <path> # judge a published npm package's peerDependencies against one checkout
wb deps policy <verb> [flags] # enforce which dependencies and import directions are allowed
wb ci audit [path] [flags]   # validate coverage gates and artifact promotion
wb coverage [path] [flags]   # measure Go test coverage for one repo or a local fleet
wb verify [path] [flags]     # run conventional lint, test, and build checks
wb check [path] [flags]      # run a named local CI-equivalent check profile
wb fleet [overview|stats|status] # fleet inventory, counts, or attention worklist
wb layout audit|clean            # audit/clean non-canonical clone placement
wb repo status [path]        # local Git state for one repository
wb status [path] [flags]     # compatibility: fleet worklist, or one repo when a path is given
wb remote publish|status|machines # share fleet state across machines via a git state repo
wb remote claim|release|claims <task> # reserve, give up, or list fleet-wide task claims
wb session register|list|prune # register and inspect stable local agent-session identities
wb session park --context-file <file> # suspend a session and checkpoint every owned worktree
wb session resume <parked-session-id> # resume a parked bundle as one fresh successor
wb session move --to <machine> --handover-file <file> # checkpoint, SSH-deliver, and start a tmux successor
wb session move --resume <handoff-id> # retry the exact request and immutable courier route
wb session receive [--format json] # receive exact stdin bytes and start the pinned successor
wb hooks  <command> [flags]  # install, validate, run, and measure user-owned Git hooks
wb worktree create <task> --original-prompt-file <private-file> # create an audited feature worktree
wb worktree summary <task>   # brief overview of a task's worktrees, branches, optional PRs
wb worktree info [path]      # redacted identity + digests for one worktree
wb worktree log [path]       # dump initial prompt + local work log for an agent
                             # (mutating verbs: init|steer|show|checkpoint|…)
wb worktree list [task]      # inspect local WB task worktrees
wb worktree cleanup <task...> # plan or apply safe merged-task cleanup
wb worktree rename <old> <new> # plan or apply explicit audited worktree recycle
wb worktree abort <task>     # hand off, retain, or discard an interrupted claim
wb plugin list --format=json # typed lifecycle registry for preconfigured local tools
wb codegrapher status|install|update # inspect or manage CodeGrapher (install/update require --yes)
wb self-update [flags]       # update the installed wb binary (alias: wb update)
wb skills sync [flags]       # install/update WB's Agent Skills in a harness skills dir
wb skills hook print|install # print or merge a Claude Code SessionStart hook
```

### Persistent flags

| Flag | Default | Meaning |
|------|---------|---------|
| `--projects-root P` | `~/projects` | Root dir holding `{org}/{repo}` clones. |
| `--filter S` | — | Only process repos whose `org/name` contains `S`. |
| `--org O` | — | Query an additional GitHub owner (repeatable); before `sync`, it has the same restricting selection as command-local `sync --org`. |

### `wb worktree` — isolated feature branches

Keep canonical clones at `<projects-root>/<owner>/<repository>` clean when
possible, but never mutate one to make it eligible for creation. WB leaves its
currently checked-out branch, index, and working tree untouched while it
creates every feature branch in its managed worktree location:

```sh
# From any checkout of sneat-bots; owner/repository is derived from origin.
wb worktree create bots-e2e --original-prompt-file <private-prompt-file>

# Create coordinated branches for a cross-repository change.
wb worktree create bots-e2e sneat-co/sneat-bots sneat-co/sneat-go \
  --original-prompt-file <private-prompt-file>

# Resume an existing exact checkout without re-deriving its branch.
wb worktree create bots-e2e sneat-co/sneat-bots \
  --resume \
  --original-prompt-file <private-prompt-file>
```

Before branching, WB fetches the exact `refs/heads/<base>` from `origin`
(`main` by default), even when the canonical checkout is dirty or off-base. It
creates the new branch from that verified commit without switching, pulling,
resetting, or fast-forwarding the canonical checkout or any local base branch;
this is safe when local `main` is stale, checked out in another worktree, or
contains active local work. By default, a worktree is created at
`<canonical-repository>/.worktrees/<task>`. `WB_HOME` remains the private
authority for Work Logs, task locks, receipts, and reports; setting it never
changes the default checkout placement. To use one shared checkout root across
repositories, set a user-only root in `~/.config/wb/worktrees.yaml`:

```yaml
version: 1
worktrees:
  root: ~/.wb/worktrees
```

WB expands `~` and creates that checkout at
`<root>/<task>/<owner>/<repository>`. The root must be absolute after
expansion; repository policy can configure branch naming but cannot choose a
checkout root. The account running WB therefore needs access to both its
`WB_HOME` state and the selected checkout root. New
work never silently falls back to the historic `<projects-root>/.wb` directory;
existing linked worktrees governed by the same `WB_HOME` remain discoverable
and manageable during migration, and WB never relocates them merely because the
default changed. WB adds a local
Git exclude for the untracked `.worktrees/` directory so Git status stays
clean; scanners and build tools that do not honor Git excludes must still avoid
that directory deliberately.
Existing branches and worktrees are rejected unless `--resume` is explicit.

Resume recovers the registered branch and active Work Log claim before reading
today's branch-prefix policy, so a policy change cannot strand or split an
existing task. An explicit `--branch` is an assertion of that recovered branch;
different run or agent provenance is rejected until an audited handoff is
performed. A successful resume preserves the existing immutable claim and
projection instead of recording a replacement claim with a new timestamp.

By default the task slug is the branch name. For a durable policy, WB layers
`$XDG_CONFIG_HOME/wb/worktrees.yaml` (or `~/.config/wb/worktrees.yaml`) below
the target base object's `.wb/worktrees.yaml`; `worktrees.branch_prefix` may be
an empty string to deliberately disable the lower layer. An exact `--branch`
overrides policy, `--branch-prefix` overrides it for one invocation, and an
explicit empty CLI prefix returns to the task slug. Branch spelling is never
agent provenance: Work Logs record the agent/runtime/model instead.

Every create writes one private Hybrid Work Log claim per repository under
`<wb-home>/worklogs/<effort>/runs/<run>/claims/<claim-id>.json`, where the
claim ID is a portable collision-resistant digest of effort, canonical
repository, branch, and immutable base (never Run ID or an absolute machine
path), plus a small Git-excluded `.wb-worklog/recovery.json` projection in the
worktree, and a typed local
outbox event. Every create requires the exact originating request via
`--original-prompt-file`, either a readable non-empty file or `-` to pipe it on
stdin; WB snapshots its bytes and SHA-256 digest before creating a worktree and
copies them only into the private archive. Piping on stdin is preferred: WB
reads it once and writes the private archive itself, so no caller-managed
staging file exists for a concurrent invocation to overwrite. `--agent`,
`--agent-runtime`, and a mandatory explicit
`--model` add run provenance. The dispatcher supplies the exact child model it
selected or the literal `unknown`; WB never guesses. Pass independent optional
`--cli` and `--provider` when known (provider is routing/billing metadata only,
never a credential). The local journal/outbox remains usable as
recovery evidence when a Synchestra server is down, so server receipt never
blocks safe local work. It is not yet a Git-repository communication fallback
and cannot deliver inter-agent messages.

`wb worktree summary <task>` is the brief task/effort overview across every
live worktree: path, branch, short head, clean/dirty/locked state, and
origin-target integration. Pass `--github` for open or merged PR evidence.
`wb worktree info [path]` is the safe redacted summary: claim identity, prompt
ordinals/digests, and live Git evidence, with prompt bodies omitted.
`wb worktree log [path]` dumps the private local journal for agent bootstrap:
exact original prompt, later steering instructions, claim identity, and live
Git evidence. Do not commit or publish that private output. Mutating verbs
under the same command (`init`, `steer`, `show`, `checkpoint`, `refresh`,
`integrate`, `handoff`, `recover`, `finalize`, `sync`, `archive`) append to
`.wb/local/worklog/` and fence on the Hybrid claim where required. `log show`
stays redacted; `log sync` remains offline until Synchestra is configured.

If Work Log publication fails after Git has published one or more coordinated
worktrees, WB records exact per-repository recovery outcomes, writes durable
cleanup receipts when storage remains available, and rolls back every Git asset
published by that invocation in reverse order. Written claims are terminalized
append-only as failed creation. If safe rollback cannot be proven, the exact
worktree, branch, commit, and recovery receipt remain visible for cleanup.

`wb worktree guard [path]` is the policy check used by agents and Git hooks. It
accepts a clean canonical base checkout for synchronization, or a non-base
linked worktree in a resolver-recognized hierarchy for development. It rejects
feature branches and local changes in canonical clones, arbitrary detached
HEADs, and linked worktrees stored elsewhere. This guard health policy does
not prevent `wb worktree create` from safely fetching a remote base without
mutating an unsafe canonical checkout. A detached linked checkout is allowed
only while Git has a real active `rebase-merge` or `rebase-apply` state.

#### Verify every push

```sh
git push
wb worktree guard . --published
```

Git offers no post-push hook, and it runs `pre-push` only when it has refs to
update — so the most dangerous push is the one that does nothing. A detached
HEAD, or a branch other than the one HEAD is on, makes `git push` print
`Everything up-to-date` while the commit reaches the remote nowhere at all.
That is not hypothetical: it orphaned a finished commit on 2026-09-02.

`--published` fetches this worktree's own branch and compares it to `HEAD`,
exiting `1` with the exact remedy unless `HEAD` is provably at
`origin/<branch>`. Unpublished, never-pushed, behind, and diverged are separate
diagnoses with separate fixes. Anything WB could not observe — offline, a failed
fetch, a ref that moved mid-check — is reported unverified and never assumed
published. Nothing is merged, reset, or fast-forwarded, and the check stays
opt-in so no Git hook depends on reaching origin.

`git push` printing success is not evidence. This is.

Inspect live task worktrees without contacting GitHub:

```sh
wb worktree list                    # includes owner agent/model/PID liveness
wb worktree list --only active      # at least one recorded PID is live
wb worktree list --only orphaned    # no recorded live PID
wb worktree list bots-e2e --github
```

`--format json` returns a versioned envelope containing `results`,
`diagnostics`, and WB lifecycle `artifacts`. Consumers must inspect all three:
an interrupted internal stage can be cleanup backlog even when no live
worktree result remains. This intentionally replaces the legacy bare result
array; JSON consumers must migrate to the envelope and check `schema_version`.

This inventory does not yet join archived Work Logs into the approved
seven-day active/recent/history view.

After every PR in a coordinated task has merged, plan cleanup first:

```sh
wb worktree cleanup bots-e2e
wb worktree cleanup bots-e2e --apply --remote --older-than 0
# Retire an exact completed batch without widening the scope to every merged task.
wb worktree cleanup bots-web bots-api bots-worker --apply --remote --parallel 3
```

Cleanup is a dry run by default. It removes nothing unless every repository in
the task is clean, unlocked, and its exact branch tip is contained in the
freshly fetched `origin/<target>`. A matching merged GitHub PR supplies
merge-age evidence, while an exact direct-push integration is also supported;
a local merge that has not reached the remote target remains `awaiting_push`.
One or more exact task names default to an immediate age window and refuse
`--apply` without `--remote`, because done means the retired source remote
branch is gone as well as the local worktree/branch. A named batch uses the
same bounded scheduler as `wb sync`: independent repositories overlap up to
`--parallel`, while tasks sharing a canonical clone remain serialized and the
report stays in task/repository order. One failed task is reported without
discarding another selected task's safe cleanup. Fleet `--all-merged` sweeping
retains the default 24-hour merged-PR grace window. `--apply` writes an audit report
below the authoritative WB home (normally `~/.wb/reports/worktree-cleanup/`)
before removing exact worktree and branch refs; remote retirement uses
force-with-lease against the observed source-branch SHA.

The inventory walk that `wb worktree list --github` and `wb worktree cleanup`
both build resolves each repository's exact `origin/<target>` over the network,
so a large fleet spends nearly all of its wall time waiting rather than
computing. `--parallel` bounds how many repositories are inspected at once:

```sh
wb worktree cleanup --all-merged --parallel 16
wb worktree cleanup bots-web bots-api bots-worker --parallel 3
wb worktree list --github --parallel 4
wb worktree cleanup --all-merged --parallel 1   # fully sequential
```

It defaults to `8`, matching `wb sync`, which does the same `git`/`gh` work per
candidate. The ceiling is deliberate: unbounded inspection would open one SSH
connection per repository at once and trade a slow sweep for a rate-limited
one. Pair it with `--verbose` to stream per-candidate progress.

`--parallel` bounds the apply phase as well. Removals overlap across canonical
repositories and stay serial within one, because Git allows a single writer per
clone; a task spanning several repositories takes them all in one global order.
The gain is therefore capped by the largest per-repository group — on the
fleet this was measured against, 86 removals over 34 repositories with 14 in
the biggest, worth roughly 3x. Remote branch deletions are bounded more tightly
still, against GitHub's per-account secondary rate limit.

WB now routes its repeated exact-head GitHub GET observations through a shared
observer keyed by repository, target branch, exact head SHA, and request shape.
It writes private `0600` freshness receipts containing the body plus `ETag` and
`Last-Modified`, revalidates stale entries with conditional requests, honours
`Retry-After` and `X-RateLimit-Reset`, and coalesces concurrent identical reads
across processes by letting a waiter reuse an observation completed after that
waiter started.

Not every GitHub read can use the full conditional-cache transport. Commands
such as `gh pr checks`, `gh pr list`, `gh run list`, and `gh run view` still go
through the shared observer boundary for call-site consistency, but GitHub CLI
does not expose stable HTTP validators for those higher-level subcommands, so
they do not get the GET-specific cache and retry layer. WB therefore keeps the
exact-head and final-fresh guarantees on the `gh api` routes that decide merge
safety, and treats the higher-level CLI reads as supporting observations that
may still require a later exact-head API reread before landing.

GitHub webhooks were assessed as an additive wake-up signal only, not the
authority for merge or cleanup decisions: deliveries are best-effort, ordering
is not guaranteed, and a webhook cannot by itself prove the current exact head
or the current required-check policy. WB therefore still finishes every
terminal decision with a fresh exact-head GitHub read rather than replacing
that step with webhook state.

Two properties make the concurrency safe rather than merely fast. The exact
target is resolved once per `(repository, base)` for the whole walk — and that
single-flight is per repository, so N worktrees in one repository cost one
fetch while *different* repositories still overlap. And each fetch is bounded
by a 90-second deadline, so a remote that never answers is reported as
unreachable instead of parking a worker.

Before worktree removal WB also writes a private lifecycle recovery stage under
`<wb-home>/reports/worktree-cleanup/backlog/`. If the process stops after the
worktree disappears but before the exact local ref is deleted, the same named
cleanup dry run shows that backlog and the same `--apply --remote` invocation
finishes it only after proving the worktree path/registration and remote branch
are absent and the local ref still has the recorded SHA.

Cleanup separately classifies WB-owned `.wb-stage-*` and
`.wb-retired-stage-*` entries. A recognized empty stage is reported in the dry
run and descriptor-safely archived outside the active task on apply. A
non-empty, symlinked, or invalid stage remains explicit blocking cleanup
backlog; it is never reinterpreted as a legacy repository worktree or silently
discarded. Run `wb worktree cleanup <task> --recover-stages` for explicit
audited recovery: WB inventories content and Git identity without following
links, emits a deterministic private receipt, and with `--apply` archives the
exact stage before normal cleanup can retire the task. Changed or ambiguous
evidence is left untouched.

`wb worktree rename` is the explicit, audited recycle path. It seals the old
private Work Log before that worktree's projection disappears, then binds the
renamed checkout to a fresh effort/run/claim from a newly fetched base. It
never carries arbitrary local state into the next effort: ignored or untracked
files block recycle unless each retained cache is named with
`--preserve-cache node_modules` (repeatable). Apply requires `--remote`; an
exact new `--original-prompt-file` is mandatory for the reset projection;
exact old remote source branch is retired with force-with-lease, and a normal
failure on any later repository rolls all already-moved repositories back to
active recovery claims so the coordinated operation is retryable. A feature effort is terminal
only after merge to `main` and removal or audited recycle of every related
worktree and branch; a task effort has the same requirement after merge to its
feature branch. A validated branch is not terminal.

If rename stops after reserving a destination prompt but before publishing its
first checkout claim, recover that prompt-only reservation with `wb worktree
abort <next-task> --disposition discarded --apply`. It retains the private
prompt archive and does not require `--remote`, because that reservation has
no branch or remote ref to retire.

Use `wb worktree abort <task> --disposition handoff|not_landed --successor
<agent-or-session> --model <exact-successor-model-or-unknown>` or explicit
`--disposition discarded` for
an interrupted or never-started effort that has no merged PR and therefore is
ineligible for normal cleanup. Its default is a dry-run; `--apply` seals the
local archive and emits an outbox event. Applied `handoff` and `not_landed`
reject an omitted model before sealing the old claim, then bind exactly one
active successor while retaining even dirty resumable state. Pass `--cli` and
`--provider` independently when known—for example `--cli opencode --provider
opencode-go`; the provider is a commercial routing/subscription identifier,
never a credential. Only explicit `discarded --apply --remote` retires an exact
unchanged remote source branch and removes an unlocked worktree/local branch
after a bounded private capture of dirty bytes (when present) and the
archive are durable, with the live checkout revalidated at the deletion
boundary. The same discarded command resumes an exact durable
post-removal branch backlog after interruption; it never relies on live
worktree inventory alone. The persistent `--filter` flag scopes which
repositories in the task abort touches: a repository it excludes is reported,
never mutated, and the task stays non-terminal until a later abort call
resolves it too — so one repository blocked on something abort cannot fix no
longer makes the whole coordinated task un-abortable.

Plan-overlap/migration-scope detection, periodic refresh notifications,
distributed Synchestra fences, and Git-backed communication fallback are
planned capabilities. `wb-merge` is a versioned repository-local merger-agent
contract for Claude, Codex, and GitHub Copilot when this WB plugin/repository
is installed: it inventories active work without a branch prefix assumption,
validates/pushes an exact target receipt, uses bounded foreground `wb ci wait`
slices, then performs audited cleanup. Marketplace distribution to every
harness is still pending: checked-in adapter files alone are not an installed
merger. Once installed, this adapter supersedes copied legacy merger prompts.
The
seven-day active/recent/history inventory, Synchestra authoritative transport,
and authorized encrypted private-prompt export are also planned.

```sh
wb hooks install .
```

Every installation includes the worktree admission guard by default. To opt
out explicitly in a repository that cannot let WB own checkout policy, record
`profiles.exclude: [worktree]` in `.wb/hooks.yaml` and run `wb hooks repair`;
the exclusion remains visible in `wb hooks check`. The guard runs the same
`wb worktree guard` policy at `post-checkout`, `pre-commit`, and `pre-push`.
Git has no pre-checkout hook: `post-checkout` prints a loud warning after an
unmanaged checkout has already happened, then preserves that state for
inspection; `wb worktree rescue <path>` moves any uncommitted work onto a
branch before anything can discard it. The
commit and push guards are the hard boundary that prevents unsafe work from
progressing.

Managed hooks retain no installer executable path. Each invocation prefers an
explicit `WB_EXECUTABLE`, otherwise resolves `wb` from `PATH`, and rejects a
relative, repository-local, non-regular, or non-executable result. A GUI Git
client with a reduced `PATH` should set `WB_EXECUTABLE` to an installed
launcher in its hook environment; package upgrades then do not require a
repository-by-repository repair.

### `wb session move` — checkpoint and start a remote successor

Configure each target by its stable WB machine name. SSH is the implemented
courier; `host` is a safe SSH alias and `wb_path` is an optional trusted
absolute path on that target (an omitted path runs `wb`):

```yaml
session_move:
  targets:
    hetzner-vm1:
      default_courier: ssh
      ssh:
        host: hetzner-vm1
        wb_path: /home/ai/go/bin/wb
```

On the target, the validated `remote.machine` must be `hetzner-vm1`, and
`tmux` plus the selected harness must be available on the remote `PATH`.

Run a same-harness move by omitting `--harness`, or explicitly move between
the two supported harnesses, `codex` and `claude-code`:

```sh
wb session move --to hetzner-vm1 --handover-file handover.md
wb session move --to hetzner-vm1 --via ssh --harness claude-code \
  --handover-file handover.md
```

The source must be a live registered session that owns the active managed
Work Log on a clean named branch. WB creates and pushes one exact tracked
handover checkpoint, persists the selected SSH address as an immutable route,
sends the canonical request bytes only on SSH stdin, and verifies the target
response. The target pins the exact commit, registers the preallocated WB
successor identity, and starts it in detached tmux as
`wb-session-<successor-wb-session-id>`. Same-harness moves retain the source
model; a cross-harness move starts with the target harness's default model.

An SSH error can be ambiguous because the target may already have started.
Retry the reported handoff instead of creating another checkpoint:

```sh
wb session move --resume <handoff-id>
```

Resume sends the byte-identical request through the already-persisted route,
even if `wb.yaml` defaults later change. WB does not fall back to another
courier after an SSH failure. A successful move reports `successor_started`;
the predecessor remains active until a later receipt completes custody
transfer.

### `wb remote` — fleet state across machines

Configure once in `~/.config/wb/wb.yaml`:

```yaml
remote:
  provider: git
  repo: <owner>/<name>
  machine: <unique-name-for-this-machine>
  publish:
    unpushed: subjects   # or: counts
```

`wb remote publish` scans this machine's attention repositories and live task
worktrees and publishes one snapshot keyed `<login>/<machine>`. `wb remote
status` reads that store to show every machine's cross-machine attention
worklist, with `STALE` flags for old snapshots. `wb remote machines` prints
one line per machine with publish age and counts (repos, worktrees). The store
is a private git repository holding one `snapshot.yaml` file per machine, so
its commit history is the audit trail.

Create the store with `gh repo create <owner>/wb-state --private` (no README
needed; the first publish creates `main`), and SSH access to GitHub is
required — the clone URL is `git@github.com:<owner>/<name>.git`.

The same store reserves fleet-wide task claims at `claims/<task>.yaml` — the
file's existence is the claim, and release deletes it, so the store's git
history is the audit trail. `wb remote claim <task> [--note <text>]
[--take-over | --force] [--json]` acquires, refreshes, or takes over a claim
(staleness is derived from the holder machine's publish heartbeat, default
`--stale 24h`, never a separate TTL). `wb remote release <task> [--force]
[--json]` gives one up. `wb remote claims [--json] [--stale <dur>]` lists
every claim with its holder and staleness. `wb worktree create <task>`
claims best-effort automatically (`--no-claim` opts out), and `wb worktree
cleanup`/`abort --apply` release best-effort — neither ever fails the host
command; the create outcome is printed and included in `--format json`
output.

### `wb sync`

Reconciles `~/projects/{org}/{repo}` with GitHub:

- non-archived, missing locally → clone
- non-archived, present locally → pull (skip if the working tree is dirty)
- archived, present + safe (clean, no stash, nothing unpushed) → remove
- archived, present + unsafe → keep, report why
- archived, missing → nothing

`wb sync` is currently the only WB creator for canonical
`<projects-root>/<owner>/<repository>` clones. A deterministic read-only audit
and admission guard for top-level/misowned clones is planned, not implemented;
WB cannot intercept an arbitrary external `git clone`, so agents must not
clone directly below `<projects-root>/<repository>`.

Runs against every repo owned by your GitHub account and every org you
belong to, in parallel, with a live progress UI (overall + per-org bars, a
live tail of in-flight repos). The live UI and final summary separately count
planned, attempted, and successful pull actions, and existing clones whose
checked-out commit actually advanced from the remote; already-current pulls
and dry runs do not inflate the update count. Anything left
needing your attention remains visible as its own summary category. After an
interactive run, the compact, sectioned final summary becomes the navigable
left panel. Selecting any count fills a filterable repository list on the
right; `Tab` moves focus between panes, and selecting a repository shows its
modified/untracked/conflicted files, unpushed commits, stash entries, or errors
below the repository list.
The detail panel wraps and scrolls with Page Up/Page Down, and narrow terminals
stack the list above the details. While the list filter is active, `q` is search
text rather than an accidental quit.
Non-interactive runs (piped output, no TTY) print a plain summary instead
and skip the drill-down.

Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--dry-run`, `-n` | off | Print the plan; change nothing. |
| `--parallel` | `8` | Maximum repositories to inspect concurrently. (`--workers`/`-j` is a deprecated alias.) |
| `--org`, `-o` | — (all your orgs + your account) | Only sync this org (repeatable). Restricts, rather than adds — unlike the persistent `--org` on `run`. |

```sh
wb sync --dry-run              # preview
wb sync -o your-org            # sync only one org
wb sync -j 16                  # more parallelism
```

### `wb run` — governed commands and config-driven recipes

Use `--` to execute a command through WB. The command keeps its stdin, stdout,
stderr, and exit code. This synchronous gateway is compatible with future WB
scheduling and operation receipts, so agents do not need to change command
syntax when those controls are enabled. In a managed worktree it records
privacy-safe requested/terminal events under `.wb/local/run/events.jsonl`,
including wall/CPU duration and an argument digest but never raw arguments or
command output. The child receives its operation ID as `WB_OPERATION_ID`.
CPU-heavy commands share a machine-wide `CPUCount-1` budget through leases under
the projects root, leaving one logical CPU responsive for the harness and OS.

```sh
wb run -- go test ./internal/worktrees -run TestCreate
wb run -- git status --short
wb run --history --days 7
```

`wb run <recipe>` applies one recipe, defined in a YAML config, across every
repo it matches. **Dry-run by default** — pass `--apply` to commit & push.

```sh
wb run --list                     # show configured recipe names
wb run dev-approach               # preview
wb run dev-approach --apply       # land it
wb run some-lint --filter x       # preview, scoped to repos matching "x"
```

Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--apply` | off (dry-run) | Commit & push changes. Without it, only reports what would change. |
| `--config PATH` | `~/.config/wb/wb.yaml` | Path to the recipe config. |
| `--days` | `14` | History window; requires `--history`. |
| `--history` | off | Summarize governed command cost in the current worktree. |
| `--json` | off | Emit the history summary as JSON. |
| `--list` | off | Print configured recipe names and exit. |

Recipe-only flags are rejected in command mode.

#### Config format

One YAML file, `~/.config/wb/wb.yaml` by default (override with `--config`).
Two recipe kinds:

**`template-section`** — merge a versioned block from a template file into a
target file (default `README.md`) in every matching repo:

```yaml
recipes:
  dev-approach:
    type: template-section
    target: README.md                          # default: README.md
    template: ~/path/to/dev-approach.md         # required
    marker: dev-approach                        # default: the recipe's own name
    applies_if: "has_source:go,ts"
```

The template file must contain the block wrapped in
`<!-- {marker}:vN -->` … `<!-- /{marker} -->`. Bumping the version number in
the template propagates it to every repo that already has an older section;
repos with a current-or-newer section, or no target file at all, are left
untouched.

**`command`** — run a shell command in the worktree; "changed" means
`git status --porcelain` is non-empty afterward:

```yaml
recipes:
  some-lint:
    type: command
    command: "some-linter --fix"                 # required
    dry_run_command: "some-linter"                # optional: a read-only preview
    count_regex: '(\d+)\s+problem'                # optional: extract a count from dry_run_command's output
    applies_if: has_file:some-linter.yaml
```

`dry_run_command`'s exit code (not the count) determines whether `--apply`
would do anything; `count_regex` only prettifies the dry-run summary. If
`dry_run_command` is omitted, dry-run mode can only report "would run: ...".

**`applies_if`** (all recipe kinds; default `always`):

- `always`
- `has_file:<path>` — e.g. `has_file:specscore.yaml`
- `has_source:go`, `has_source:ts`, or `has_source:go,ts` (comma = OR)

**Landing options** (all optional, defaulted from the recipe's name):
`commit_message`, `pr_branch`, `pr_title`, `pr_body`.

#### How it lands

Same worktree/commit/push-or-PR flow for both recipe kinds:

1. **Discover** repos across your GitHub orgs, same as `wb sync`.
2. **Skip**: forks, archived repos, local-only clones not under one of your
   owners, and any repo `applies_if` excludes.
3. **Land**, in a detached worktree off the default branch: if the local
   clone is dirty (uncommitted/unpushed) or the default branch is protected
   → push to `{pr_branch}` and open an auto-merge PR; otherwise → push
   directly to the default branch.

`wb` itself ships with **no recipes** — you define your own in
`~/.config/wb/wb.yaml`.

### Fleet coverage and verification

These commands are read-only: they operate on existing local clones and never
fetch, modify source, commit, or push. Without `--fleet` they run against one
repository path (the current directory by default). `--fleet` scans every Git
repository below `--projects-root`.

```sh
# Go coverage for all cloned Sneat repositories, aggregated by statements.
wb coverage --fleet --match 'sneat-co/*' --parallel=2

# Emit a deterministic report for a human or agent.
wb coverage --fleet --regex '^sneat-co/(sneat|bots)' \
  --report-dir /tmp/wb-coverage --format yaml

# Isolate large serial packages into eight deterministic processes, preserve
# the merged profile, and enforce the same threshold as CI.
wb coverage . --test-shards 8 \
  --shard-package ./internal/worktrees \
  --coverage-profile profile.cov --minimum 58

# Run Go vet/test/build and defined Node lint/test/build scripts.
wb verify --fleet --filter sneat-co/ --parallel=2

# Restrict verification to compilation-oriented checks for one repository.
wb verify ~/projects/sneat-co/sneat-bots --checks lint,build

# CI profile adds SpecScore lint for repositories that contain spec/. A
# specscore.yaml file makes that canonical root required, so a missing spec/
# fails closed instead of being treated as non-applicable.
wb check --fleet --match 'sneat-co/*' --profile ci --parallel=2 \
  --timeout 10m --retry 1 --report-dir /tmp/wb-check

# After a partial failure, re-run only prior failed repositories.
wb check --fleet --match 'sneat-co/*' --profile ci \
  --resume --report-dir /tmp/wb-check
```

`--filter` (substring), `--match` (glob), and `--regex` are composed against
the `org/repo` name; every supplied filter must match. Both commands write
Markdown by default, can print YAML or JSON, and can write stable Markdown and
YAML files with `--report-dir`.

Coverage discovers every `go.mod` below a selected repository (excluding
`.git`, `vendor`, and `node_modules`) and uses temporary profiles outside the
repository. Its fleet percentage is statement-weighted, rather than an average
of repository percentages. An explicitly named `--shard-package` can be split
across `--test-shards` isolated Go processes: WB deterministically runs every
top-level test, example, and fuzz target exactly once, runs all other packages
once, and losslessly merges their coverage blocks. Sharding is repository-only
and opt-in because discovery invokes `TestMain` once before every shard invokes
it again. `--coverage-profile` retains the exact merged artifact and
`--minimum` enforces its aggregate floor.
Repository-owned merge validation can opt into the same mechanism with a
tracked `.wb/quality.yaml`:

```yaml
version: 1
go_test:
  shards: 8
  packages: [./internal/worktrees]
```

`wb worktree land` validates the candidate first using this policy, proves the
remote landing receipt, and cleans the source by default. The legacy
`wb worktree merge` spelling keeps cleanup opt-in. Candidate validation runs
the exact target snapshot only if the candidate fails and inherited-failure
comparison is needed, avoiding a redundant full baseline on green candidates.
Verification runs `go vet ./...`, `go test ./...`,
and `go build ./...` for each Go module; for a root Node project it runs only
defined `lint`, `test`, and `build` scripts with the detected package manager.
Other stacks remain explicit, reusable `wb run` recipes.

`wb check` provides stable local CI profiles: `fast` runs lint, `full` (the
default) runs lint/test/build, and `ci` additionally runs `specscore spec lint`
for repositories with `spec/`. `--timeout` applies to each external command;
`--retry=N` retries only failed commands N additional times; and
`--resume --report-dir DIR` selects only repository failures from the previous
YAML report. These controls also apply to `wb coverage` and `wb verify`.
Interactive fleet and single-repository runs show their current repository,
module, and check on stderr without contaminating the report on stdout.

### `wb fleet` / `wb status` — local fleet Git health

The normal fleet question is “which local checkouts need attention?” Prefer the
explicit nouns:

```sh
wb fleet                 # overview: counts + attention worklist
wb fleet overview        # same as wb fleet
wb fleet stats           # inventory / git / worktree counts only
wb fleet status          # attention worklist
wb repo status ~/projects/sneat-co/sneat-bots --details --format yaml
```

`wb status` remains as a compatibility entry point: no path matches
`wb fleet status`; a path matches `wb repo status`. There is intentionally no
`--fleet` flag on these surfaces.

A fleet worklist lists only the repositories needing attention and reports the
clean ones as a count. `--all` lists every inspected repository. Naming a
single repository always reports that repository, clean or not.

```sh
wb fleet status
wb fleet status --all
wb fleet status --filter sneat-co/ --match 'sneat-co/*' --parallel=8
wb status ~/projects/sneat-co/sneat-bots --details --format yaml
```

These commands read only local Git state—never fetch, pull, modify, commit, or
push—and report clean, attention, or inspection-error status. Attention covers
modified, untracked, conflicted, stashed, and unpushed work. Markdown defaults
to concise summaries; YAML/JSON and `--details` provide individual paths and
Git entries. Unpushed commit details identify the local branch and, when it is
checked out, the canonical or linked worktree holding it. Interactive fleet
and single-repository status scans show a live
counter and continuously refreshed elapsed time on stderr; structured output
remains on stdout, and `--non-interactive` suppresses the live line. `wb fleet`
/ `stats` always include layout placement counts and
managed worktree rollups. Pass `--remote` for sync-drift counts or `--hooks`
for managed-hook findings. Use `wb layout audit` for the placement worklist,
`wb sync --dry-run` for GitHub reconciliation, and `wb worktree orphans` for
linked-worktree debt outside managed tasks.

### `wb layout` — clone placement under projects-root

Canonical clones live at `{projects-root}/{owner}/{repository}` with a real
`.git` directory. Audit is read-only; clean is dry-run unless `--apply`.

```sh
wb layout audit
wb layout audit --format json
wb layout clean              # dry-run safe top-level duplicates
wb layout clean --apply      # delete when clean tree + canonical exists
```

### `wb deps set` — one exact dependency version

Use `deps set` when the desired version is already known and must be applied
consistently. It updates existing references only; a repository that does not
already use the dependency is skipped with an explicit reason. Dependency
identities are fully qualified, so WB never guesses that `cicd` means a
particular owner and repository.

```sh
# Inspect one repository without creating a worktree.
wb deps set github-actions strongo/cicd@v1.10.5 \
  ~/projects/sneat-dev/wb --dry-run

# Set an exact reusable-workflow version across the selected fleet, verify
# locally, open PRs, wait for CI, and merge only passing PRs.
wb deps set github-actions strongo/cicd@v1.10.5 --fleet \
  --parallel=2 --commit --push --pr --merge

# Set an existing Go module requirement with go get and go mod tidy.
wb deps set go github.com/dal-go/dalgo@v0.63.1 \
  ~/projects/sneat-co/sneat-go

# Set an existing npm/pnpm dependency reference across every package.json and
# pnpm-workspace.yaml override/catalog entry in a repository.
wb deps set npm @sneat/core@1.4.0 \
  ~/projects/sneat-co/sneat-apps
```

The adapters are `github-actions`, `go`, and `npm`. GitHub Actions tags are
resolved once to immutable commit SHAs; WB preserves the action or reusable
workflow subpath and writes `# <version>` next to the SHA. The Go adapter uses
official Go tooling rather than implementing module selection itself. A
semantic downgrade is rejected unless `--allow-downgrade` is explicit.

The npm adapter updates `dependencies`, `devDependencies`,
`peerDependencies`, and `optionalDependencies` in every `package.json` in a
repository (workspace members included), and, where present, the
`overrides:`/`catalog:`/`catalogs:` blocks of `pnpm-workspace.yaml` — the
version pin pnpm 11 reads instead of the legacy `pnpm.overrides` field in
`package.json`. After writing an exact version it regenerates every affected
lockfile with `pnpm install --lockfile-only` (or `npm install
--package-lock-only` for a plain npm lockfile) and verifies the result with a
frozen-lockfile probe before reporting success, so a change never lands with
a lockfile whose recorded config snapshot no longer matches — the exact
mismatch a skipped regeneration produces as
`ERR_PNPM_LOCKFILE_CONFIG_MISMATCH` in CI. A repository with more than one
independent lockfile (for example a nested `landings/` subtree with its own
`pnpm-workspace.yaml`) has each lockfile scope regenerated and verified
independently.

#### Private Go modules

Use repeatable `--go-private` for module-path patterns that must be fetched
without a public Go proxy or checksum-database lookup. WB merges each pattern
into `GOPRIVATE`, `GONOPROXY`, and `GONOSUMDB` for Go commands only; it does not
modify global Go configuration or accept credentials. Configure Git access
first—for GitHub, `gh auth setup-git` configures Git to use the existing GitHub
CLI authentication.

```sh
wb deps set go github.com/acme/private-sdk@v1.4.0 \
  --go-private github.com/acme

wb deps bump go --fleet \
  --changed github.com/acme/private-sdk@v1.4.0 \
  --go-private github.com/acme --merge
```

Canonical clones remain untouched, including dirty clones. WB fetches
`origin/<ref>` (`main` by default) and creates branches with a checkout at
`<canonical-repository>/.worktrees/<operation>` by default. A user-only
`worktrees.root` setting selects `<root>/<operation>/<org>/<repo>` instead;
`WB_HOME` still holds private lifecycle state. Without publication
flags, verified changes remain in those local worktrees. `--push` implies
`--commit`; `--pr` implies push and commit; and `--merge` implies all prior
stages. Local lint, test, and build checks are enabled by default; use
`--checks`, `--timeout`, and `--retry` to tune them or `--no-verify` to disable
them explicitly.

WB opens all eligible PRs before entering its CI-wait phase, so independent
repository work continues while earlier PRs build. A merge uses the same
bounded exact-head waiter as `wb ci wait`: it reads target policy, verifies
producer-pinned checks against exact-head check runs, proves that the candidate
contains the freshly fetched target, and requires both a nonempty strict
freshness policy and an unchanged terminal reread before issuing an
exact-head-guarded GitHub merge. It does not require current target CI to be
green; the candidate may fix it. Pending, failing, cancelled, conflicted,
checkless, stale, unfenced, and timed-out PRs remain open for an explicit
resume. `--resume` validates and reuses the expected worktree branch and open
PR.

Every run writes `deps-set.md` and `deps-set.yaml` below
`<wb-home>/reports/<operation>` (or `--report-dir`; normally
`~/.wb/reports/...`). Both formats
record observed and target versions, resolved SHAs, reasons, changed files,
verification, commits, PR links, CI checks, and merge outcomes. Git remains the
source of detailed patches; the Markdown report includes the exact diff command.

#### Provider-first ordering across a dependency graph

When a breaking release has to reach dozens of repositories, the order matters.
A repository nine levels down the graph cannot go green until the repositories
it depends on have released, so opening every pull request at once produces
pull requests that no CI run can turn green. `--dependency-order` processes the
selection in the provider-first layers reported by `wb deps graph` instead of
one batch:

```sh
# 1. Read the plan without touching anything.
wb deps set go github.com/dal-go/dalgo@v0.64.2 \
  --fleet --dependency-order --dry-run

# 2. Land layer 0, wait for its releases, then continue with the next layer.
wb deps set go github.com/dal-go/dalgo@v0.64.2 \
  --fleet --dependency-order --layer 0 --pr
wb deps set go github.com/dal-go/dalgo@v0.64.2 \
  --fleet --dependency-order --layer 1 --pr
```

`--dependency-order` **orders; it does not wait.** WB never polls for a
published version here — the operator owns the pause between layers, and
`--layer` (`N`, `N-M`, or `N-`) picks which layer a run may touch. Automatic
waiting, release observation, and requeuing belong to `wb deps bump`; exact set
deliberately has no second release loop.

Inside one layer nothing changes: repositories are independent, keep
`--parallel` concurrency, and complete independently. Across layers the run
fails closed — a later layer is never started after an earlier layer failed,
and its repositories are reported as `blocked` naming the layer that failed,
rather than being cloned and given an unmergeable pull request. Markdown and
YAML gain a **Dependency order** section recording every layer, its
repositories, and whether it completed, failed, was blocked, or was not
selected.

Ordering needs a module graph, so it is Go-only and is rejected for
`github-actions` and together with `--propagate`. Repositories that require
each other share one layer and are reported as a cycle rather than being
dropped.

See the [Exact Dependency Set feature specification](spec/features/dependency-set/README.md)
for synthetic use cases and acceptance criteria. By default, `deps set` does
not discover newer releases or recalculate provider-to-consumer release waves.
For an exact Go target, `--propagate --fleet` delegates to `deps bump` with one
initial release event; `--max-waves` and `--release-poll` tune that delegated
campaign, and `--refresh-after` controls stale-event registry rechecks.

### `wb deps bump` — published-version propagation waves

Use `deps bump` after one or more exact Go module or npm package versions have
been published and their dependants must be moved in provider-first order:

```sh
wb deps bump go \
  --changed github.com/dal-go/record@v0.3.0 \
  --changed github.com/dal-go/dalgo@v0.64.0 \
  --fleet --parallel=2 --merge

# The same planner with one seed release:
wb deps set go github.com/dal-go/record@v0.3.0 \
  --fleet --propagate --parallel=2 --merge

# npm/pnpm fleets propagate the same way, waves and all — including every
# pnpm-workspace.yaml override and every affected lockfile.
wb deps bump npm \
  --changed @sneat/core@1.4.0 \
  --fleet --parallel=2 --merge
```

`--propagate` (`deps set --fleet --propagate`) delegates to `deps bump` and is
Go-only today; `deps bump npm` itself is invoked directly, as above.

`--propagate` is therefore similar to bump limited to one *initial* dependency,
but the campaign is not limited to that dependency. When an updated consumer
is merged and a newer module version is observed, that consumer module becomes
a release event for the next wave. `deps bump` also accepts multiple initial
`--changed` events, which is useful when a coordinated release publishes
several providers together.

Each wave rebuilds the graph from `origin/<ref>` and changes direct consumers
whose requirements are stale. WB assigns an affected repository to its
longest provider path and starts only the earliest pending layer. For example,
if Sneat-Go consumes both Dalgo and Sneat-Bots, it waits for the Sneat-Bots
release and receives both versions in one PR and one CI build instead of being
built once per provider. Deferred repositories are named in the report.
Independent repositories share the same typed
clone/worktree/verification/commit/PR/CI lifecycle used by `deps set`. After
green PRs merge, WB captures an actual newer registry version before touching
downstream repositories; it never invents the next version. If a release is
not visible before `--timeout`, the report remains `awaiting_release` and
`--resume` continues from the persisted pre-merge baseline.

#### Deriving the seed events instead of typing them

A coordinated release of a dozen packages under one scope is a dozen chances to
typo a version or omit a provider — and an omitted provider is not an error,
just a consumer that stays stale. `--latest --scope` reads the modules the
selected repositories declare, keeps the ones a scope glob matches, and asks the
registry (the Go module proxy or the npm registry) for each one's published
latest version:

```sh
wb deps bump npm --fleet --latest --scope '@sneat/*' --dry-run
```

`--scope` is a `path.Match` glob against a module path or package name, exactly
as in `wb deps drift --scope`: `*` never crosses a `/`, so `@sneat/*` matches
`@sneat/core`, and `github.com/dal-go/*` matches `github.com/dal-go/dalgo` but
not a nested `github.com/dal-go/dalgo/x`. `--latest` requires at least one
scope; a scope that matches no declared module, or whose modules have published
nothing, is refused rather than run as an empty campaign that looks like
success.

The report's **Derived scopes** table lists every matched module — including the
ones with no readable published version, which seeded nothing — so a scope's
coverage is auditable rather than assumed. `--changed` composes with `--latest`
under the engine's own rule, the newest version observed for a dependency wins:
a provider release still in flight can be named explicitly alongside a scope
sweep, and a stale hand-typed event is corrected by a newer published one.

#### Choosing which repositories a campaign touches

`--exclude` and `--hold` narrow a campaign in two different ways, and the
difference matters:

| flag | the repository is | ends up |
|---|---|---|
| `--exclude <org/repo glob>` | removed before anything is discovered — no graph entry, no wave, no worktree, no PR | listed under **Excluded repositories** in the report |
| `--hold <org/repo glob>` | bumped, verified, pushed, PR opened, exact PR-head CI waited | **PR left open**, even under `--merge` |

Use `--exclude` for an archived or irrelevant repository. Use `--hold` for one
whose merge is a human decision — a gated deploy repository, for example:

```sh
wb deps bump npm --fleet --changed @sneat/core@1.4.0 --merge \
  --hold sneat-co/sneat-go --hold sneat-co/sneat-apps
```

Both accept `path.Match` globs, where `*` never crosses a `/`, and an exact
`owner/name` always matches itself.

A release that needs a human merge cannot be waited for, so a wave containing a
held repository stops the campaign with status `awaiting_hold_release` and names
the pull requests the remaining waves are waiting on. That is a stopping point,
not a failure: merge the held PRs and `--resume`, which continues by observing
the release that merge published. Excluded slugs stay in the report so "this
repository needed nothing" is never confused with "this repository was never
looked at".

Interactive set/bump campaigns report repository selection immediately, then
their current wave, repository, and lifecycle phase on stderr. The elapsed time
continues to refresh during silent discovery, verification, and release-waiting
phases. Structured reports stay on stdout, and `--non-interactive` disables the
progress renderer.

A campaign can wait long enough for a still newer provider version to appear.
Before starting downstream work, WB rechecks accumulated release events older
than `--refresh-after` (default `5m`). A newer registry version replaces the
stale event and the wave is replanned before any worktree or CI run is created;
`--refresh-after=0` disables these inexpensive rechecks.

A second sweep can traverse repositories that were already updated before the
campaign: WB requires both a current `origin/<ref>` manifest and a published
module whose downloaded `go.mod` contains the seed versions. This evidence
turns the existing consumer release into the next event. Relevant
cross-repository dependency cycles fail before any worktree is created because
they need a separate coordinated-release protocol.

Fleet bump discovery tolerates a broken remote ref only when a recursive local
scan can prove that the repository has no `go.mod`. Such non-Go skips are
audited. A repository containing any Go manifest—or one whose local contents
cannot be inspected—remains a hard blocker, so a relevant service is never
silently omitted.

Without publication flags, the first changed wave stays in local worktrees.
`--commit`, `--push`, `--pr`, and `--merge` are cumulative just as for
`deps set`; automatic downstream propagation requires `--merge` so WB can
associate each next wave with observed publication evidence. Markdown and YAML
state are written as `deps-bump.md` and `deps-bump.yaml` below the operation's
report directory.

See the [Dependency Bump Waves feature specification](spec/features/dependency-bump-waves/README.md)
for synthetic use cases and acceptance criteria.

### `wb deps publish npm` — workflow-owned publication and propagation

Use one explicit campaign when approved npm packages must be published by
their repository-owned GitHub Actions workflows and then propagated through
consumers. WB is plan-only by default; `--apply` is the publication approval.
WB never accepts npm credentials or runs `npm publish` itself.

```sh
# One plan covers the Assetus release and both Eventius packages.
wb deps publish npm \
  --repo sneat-co/assetus \
  --repo sneat-co/eventius --repo sneat-co/eventius \
  --workflow release-frontend.yml \
  --workflow release-frontend.yml --workflow release-frontend.yml \
  --package @sneat/extension-assetus \
  --package @sneat/extension-eventius --package @sneat/extension-eventius-ui \
  --version 0.1.0 --version 0.0.1 --version 0.0.1 \
  --workflow-input 1:package=runtime --workflow-input 2:package=ui \
  --fleet --match 'sneat-co/*' --format json
```

Each repeated provider flag is aligned by tuple. `--workflow-input` uses
`INDEX:KEY=VALUE` (zero-based) so packages sharing one workflow cannot receive
another package's inputs; a bare `KEY=VALUE` is allowed only for one tuple.
`--match 'sneat-co/*'` bounds downstream consumer discovery to the intended
organization instead of scanning unrelated fleet repositories.
The default plan invokes the same shared dependency-wave engine in dry-run
mode, so it retains real fleet findings and the durable wave report. It never
dispatches a release workflow, queries the npm registry, or changes dependency
files. To prevent a plan from overwriting an apply/resume handoff, its wave
report is stored under `<report-dir>/plan`; apply and resume use the base
report directory.
The apply report waits for the exact workflow run at the resolved provider
head, verifies the exact package version in the npm registry, and then invokes
the same recalculated `wb deps bump npm` wave engine. Add `--merge` only as a
separate explicit approval for downstream consumer changes. If publication or
registry evidence times out, retain `--report-dir` and use the same tuples
with `--resume --apply`; WB reuses receipted runs without redispatching them.
Interactive apply/resume runs show head resolution, dispatch, workflow polling,
registry verification, and downstream wave progress on stderr.

See the [NPM release propagation feature specification](spec/features/npm-release-propagation/README.md)
and the [publish-npm reference](ai/skills/wb-deps/references/publish-npm.md)
for the machine-readable receipt and resume contract.

### `wb deps drift` — dependency convergence

Use drift when the question is whether selected checkouts agree on dependency
versions. It is read-only and offline by default, and covers two ecosystems:

- `--ecosystem go` (default) reads every `go.mod` and resolves the selected
  version with `go list -m`.
- `--ecosystem npm` reads every `package.json` dependency field and every
  `pnpm-workspace.yaml` override/catalog entry, and resolves the selected
  version from the governing `pnpm-lock.yaml` or `package-lock.json`. That
  distinction is the point: `^0.30.0` says what a fresh resolve *could* pick,
  the committed lockfile says what CI *will* install.

Fleet reports group each dependency and classify `converged`, `divergent`,
`replaced`, `major_path_split`, and `behind_latest` states. `--fail-on-drift`
turns the first four into an exit gate after the complete report is written;
`--fail-on-behind` gates the last one separately, because a fleet that has not
yet adopted a release is a different finding from one that disagrees with
itself.

Pass `--online` when latest registry versions are required. An online run costs
one registry query per retained dependency, so bound the question with
`--scope` (a `path.Match` glob over dependency names, repeatable) or
`--dependency` (exact, repeatable). `--exclude` drops whole repositories by
`owner/name` glob and lists them in the report, so "clean" is never confused
with "never inspected".

A dependency is only reported as behind when the evidence proves it: a locked
version below latest, or a specifier that provably cannot admit latest (an
exact pin such as `"0.14.0"` against a published `0.14.3`, or `^0.24.1` against
`0.25.0` — npm's caret does not cross a `0.x` minor). WB reads exact pins,
carets, tildes, comparison operators, space-separated conjunctions such as
`>=22.0.0 <23.0.0`, and `||` unions of those. Shapes it does not evaluate —
hyphen ranges, wildcards, comma lists, and the `workspace:`, `catalog:`,
`npm:`, and `file:` protocols — are reported as unevaluated and are never
counted as behind.

Fleet and single-repository drift and graph scans show live selection and
per-repository progress on an interactive terminal. The elapsed time keeps
refreshing during silent phases. The progress line is written to stderr, so
Markdown/YAML/JSON/SVG/HTML stdout remains machine-readable;
`--non-interactive` suppresses it completely.

```sh
wb deps drift .
wb deps drift --fleet --match 'sneat-co/*' --format json
wb deps drift --fleet --fail-on-drift
wb deps drift --fleet --online --dependency example.com/sdk

# "is the fleet on the latest of our own npm packages?"
wb deps drift --fleet --ecosystem npm --online --scope '@sneat/*' --fail-on-behind
```

### `wb deps peers` — can I reuse this package here?

`deps drift` asks whether checkouts agree with each other. `deps peers` asks a
different question about one checkout: does this published npm package's
contract fit it?

That question is normally answered by running the install and reading whatever
the package manager says about peer conflicts — which mutates the checkout to
find out, and whose warnings do not distinguish "you are two majors behind"
from "the publisher marked this peer optional". `deps peers` reads the
published package's own `peerDependencies` and `peerDependenciesMeta`, reads
what the target checkout actually resolves for each of them, and prints the
answer:

```sh
wb deps peers @sneat/core --against ../renewon
wb deps peers @sneat/core@0.31.0 --against ../renewon --format json
```

Each peer is judged against the version the governing `pnpm-lock.yaml` or
`package-lock.json` installs, not the caret range a manifest declares — a range
cannot be judged against another range. Where no lockfile governs the manifest,
the row's source says so rather than presenting a range as an installed
version.

| verdict | meaning |
|---|---|
| `satisfied` | the target's resolved version is admitted by the peer range |
| `unsatisfied` | the target has it, at a version the range rejects |
| `missing` | the target does not have it at all |
| `optional_missing` | the publisher marked it optional; the target omits it |
| `unevaluated` | WB will not guess this specifier shape, and says so |

`unevaluated` is never a pass — WB evaluates the specifier subset the fleet's
manifests actually use (including the `>=22.0.0 <23.0.0` conjunction every
Angular and Ionic peer uses, and `||` unions of supported comparators) and
declines to judge a hyphen range or a `workspace:`/`catalog:` protocol rather
than reporting it as compatible. The
command exits `1` when any required peer is `unsatisfied` or `missing`, and
nothing is installed or written, so it is safe to run against a checkout
someone else is working in.

### `wb deps graph` — one scan, three dependency views

`deps graph` scans one ecosystem's manifests once — Go module declarations
and requirements by default, or `--ecosystem npm` for `package.json`
dependency fields and pnpm-workspace.yaml overrides/catalogs — preserves the
manifest evidence, and derives three views from the same canonical model:

- `--view repos` shows internal provider repository → consumer repository
  edges for release order and propagation blast radius.
- `--view dependencies` shows dependency/module → consuming repository edges,
  including external dependencies.
- `--view selections` shows `dependency@version` → consuming repository edges
  and highlights versions behind the highest comparable version observed in
  this fleet. “Fleet-highest” is deliberately not described as registry-latest.

```sh
# Generate all report artifacts and open the repository view in a browser.
wb deps graph --fleet --match 'dal-go/*' --view repos --open

# Find every selected consumer of one exact module.
wb deps graph --fleet \
  --dependency github.com/dal-go/dalgo \
  --view dependencies

# Inspect one checkout and emit standalone SVG to stdout.
wb deps graph ~/projects/sneat-co/sneat-go \
  --view selections --format svg
```

Every report also carries a **Release order**: the provider-first layering of
the selected repositories, derived from their declared modules and
requirements. Layer 0 depends on no other selected repository, every later
layer depends only on earlier ones, and repositories with no internal
relationship sit in layer 0. It answers "which repositories must release
first" directly, and it is the same layering `deps set --dependency-order`
executes:

```sh
wb deps graph --fleet --match 'sneat-co/*' --format markdown
```

```text
## Release order

| Layer | Repositories | Count |
|---:|---|---:|
| `00` | `strongo/strongoapp` | `1` |
| `01` | `sneat-co/sneat-go-core` | `1` |
```

Repositories that require each other cannot be separated by any order, so they
share one layer and are listed under the table with their cycle path. Layering
never fails or drops a repository because of a cycle.

The default report directory is
`<wb-home>/reports/deps-graph-<ecosystem>` (normally `~/.wb/reports/...`, so
`deps-graph-go` or `deps-graph-npm`; override it with `--report-dir`). Every
run writes:

- `deps-graph.md` — compact human and AI evidence index;
- `deps-graph.yaml` and `deps-graph.json` — deterministic canonical evidence;
- `deps-graph.svg` — accessible standalone rendering of the selected view;
- `deps-graph.html` — self-contained interactive report containing all three
  projections, search, path highlighting, fleet-drift highlighting, zoom/pan,
  organization highlighting, selected-node details, and CodeGrapher drill-down.

`--open` is explicit: headless and CI runs never attempt a GUI action. WB writes
every report before invoking the operating system's browser command, so an open
failure still leaves a usable HTML path. Providers flow left-to-right toward
consumers; direct and indirect requirements have distinct edge styles, and
cross-repository cycles are rendered rather than rejected.

Repository-backed nodes link to both GitHub and
[CodeGrapher](https://codegrapher.dev/), which provides repository-level symbol,
call, import, and impact exploration beneath WB's fleet-level topology. These
links are deterministic and passive: WB does not query CodeGrapher, publish a
snapshot, or trigger indexing while generating a report.

Install and inspect the local CodeGrapher CLI through WB's default tool plugin:

```sh
wb codegrapher status --format=json
wb codegrapher install --yes
wb codegrapher update --yes
```

This local-tool lifecycle does not index or synchronize a repository. A graph
refresh will be added only after CodeGrapher can attest the exact repository
revision it processed.

The first discovery adapter is Go and uses `golang.org/x/mod/modfile`.
Projection and rendering are independent of that adapter so Python and
TypeScript evidence can later feed the same report model.

See the [Dependency Graph feature specification](spec/features/dependency-graph/README.md)
for synthetic use cases and acceptance criteria.

### `wb deps policy` — which dependencies are allowed, not which versions

The other `deps` verbs are about *versions*. This one is about *permission*:
which kinds of repository may depend on which kinds of dependency, and which
direction imports may travel between packages inside a repository.

Most fleets enforce this with a `git grep` in each repository's workflow, with
the allowlist written into the pattern. That cannot express "any *sibling*
implementation repository", knows nothing about the importing package, and — the
part that compounds — makes an exception typographically identical to a rule.
Widening one reviews as a typo fix rather than as taking on architectural debt.

One central document says it instead:

```yaml
groups:                       # ordered — first match wins
  - {name: own-repo,                 match: ["<self>/..."]}
  - {name: extension-contract,       match: ["github.com/acme/ext-*/..."]}
  - {name: extension-implementation, match: ["github.com/acme/*/..."]}
  - {name: dalgo-adapter,            match: ["github.com/dal-go/dalgo{2,4}*/..."]}
  - {name: third-party,              match: ["..."]}

types:                        # also ordered; ext-* above the catch-all
  - name: extension-implementation
    detect: ["github.com/acme/*/backend"]
    scopes:
      source: {allow: [own-repo, extension-contract, third-party]}
      tests:  {allow: [own-repo, extension-contract, dalgo-adapter, third-party]}
      main:   {allow: [own-repo, extension-contract, dalgo-adapter, third-party]}
```

Rules are **allow lists with no deny list**. Anything absent is forbidden, and
an import no group classifies fails closed — so a policy that meets a new kind
of dependency refuses it rather than quietly permitting it. There is no
baseline, no per-repository severity, and no exception mechanism: the only way
to make a forbidden dependency legal is to change the central document.

Three scopes, because the same import can be legitimate in one place and wrong
in another: `source`, `tests` (emulator-backed repository tests do reach for a
concrete driver), and `main` (a composition root wires concrete drivers — that
is what it is for). A direct `go.mod` requirement is judged in the scope it is
actually imported in; indirect requirements are ignored, since no repository
can act on them.

A repository declares two lines, and may tighten but never loosen:

```yaml
# .wb-deps-policy.yaml
policy: acme/cicd//policy/backend.yaml
type: extension-implementation      # optional — detected from the module path
```

`strict: true` is the one other key it may set, and it only ever promotes
report-mode findings to failures. Declaring groups or types, extending an allow
list, or setting a rule mode is refused with exit 2. It names the policy
**source** and never a release: a repository frozen on last quarter's policy
would be carrying an exception nobody wrote down.

#### Gate a repository

```sh
wb deps policy check ./backend --format github
```

The scan is lexical — import blocks and `go.mod`, never a resolved module
graph. No credentials, no downloads, and a verdict even when the build cannot
start, which is when a boundary is most likely to be under discussion. Exit `0`
clean, `1` on an enforcing violation, `2` on an unusable invocation or policy.

#### Understand a verdict

```text
$ wb deps policy explain github.com/acme/ext-cal/backend/dto ./backend
import  github.com/acme/ext-cal/backend/dto
group   extension-contract
        <- pattern #2  "github.com/acme/ext-*/..."
        (pattern #3 "github.com/acme/*/..." would also match, for group
         "extension-implementation" — shadowed)
repo    extension-implementation  (detected from the module path)
source  ALLOWED — extension-contract is in source.allow
```

The shadowed-match line is the point. Groups are first-match-wins, so a broad
pattern above a narrow one silently takes every path the narrow one was written
for. `wb deps policy validate` reports that as an unreachable pattern, and
`wb deps policy test` runs the `expect:` assertions a policy declares about
itself — a policy with none is refused, because nothing else would catch a
classification regression.

#### Layers

Package roles are ordered outermost-first and imports travel down, never up:

```yaml
layers:
  mode: report                # central; a repository cannot demote a rule
  roles: {api: ["api4*"], facade: ["facade4*"], dal: ["dal4*"], dbo: ["dbo4*"]}
  order: [[api], [facade], [dal], [dbo]]
  forbid:
    - {from: api, to: dal, reason: "delivery must go through the facade"}
```

The depth rule alone cannot say "delivery must go through the facade" —
`api → dal` does travel downward — so such edges are named explicitly, with a
reason, in the policy rather than hidden in the tool.

`mode` lives in the central document, so a new rule can ship non-blocking while
a fleet cleans up without any repository being able to opt itself out.

#### Watch the fleet

```sh
wb deps policy report --match 'acme/*'    # burn-down by rule; counts ungoverned modules
wb deps policy drift  --match 'acme/*'    # who is governed, and where a declared type disagrees
wb deps policy impact policy/backend.yaml --match 'acme/*'
```

`report` is what turns a report-mode rule into a number that has to reach zero.
`impact` matters because repositories cannot pin a release: a tightened rule
reaches all of them at once, so the blast radius belongs in the policy's own
pull request rather than in nine repositories on a Friday morning.

See the [Dependency and layering policy feature specification](spec/features/dependency-policy/README.md)
for requirements and acceptance criteria.

### `wb migrate` — declarative source migrations

`wb migrate` is for repeatable codebase migrations rather than arbitrary shell
recipes. An HCL specification, decoded with HashiCorp's official HCL decoder,
declares the intended edit. WB discovers source files below the explicit roots,
produces a deterministic plan, and writes only when `--apply` is passed.

```sh
# Preview a migration; no files are edited.
wb migrate examples/migrations/dalgo-record-v1.hcl ~/projects/sneat-co

# Make the planned edits. `--check` instead exits 1 when drift is found.
wb migrate examples/migrations/dalgo-record-v1.hcl ~/projects/sneat-co --apply
```

Every planned file carries a SHA-256 of the source it was built from. Apply
refuses to overwrite a file changed after planning, and each replacement is
atomic. Migration specs contain no arbitrary commands, which keeps a preview
meaningful and makes the same spec suitable for CI.

#### Review reports

Markdown is the default stdout format. It is a compact index of changed files,
operations, source hashes, local-file links, and the exact `git diff` command
for each file. The detailed patch remains in Git, where humans and AI agents
can inspect it normally after an apply.

Use `--report-dir` to also write both representations:

```sh
wb migrate examples/migrations/dalgo-record-v1.hcl ~/projects/sneat-co \
  --report-dir /tmp/dalgo-record-report
```

- `migration.md` is the linked review index for humans and AI agents.
- `migration.yaml` is the sorted deterministic manifest for tools.
- `--format yaml` writes the same manifest to stdout instead of Markdown.

Reports are opt-in files, so an ordinary dry-run leaves source trees untouched.
Specifications can also declare regex-based `review` rules. They never edit
code; WB indexes matching files and line numbers under **Required review** so
an agent or human can handle semantic changes separately from mechanical ones.

The runner is language-neutral; structural transformations are supplied by
language adapters rather than by regexes. Today the Go adapter supports
syntax-aware `import.replace`, `selector.rewrite`, and `selector.rename`
operations, preserving comments and strings and choosing an import alias when
a name would be shadowed. The generic `text.replace` operation is available for
Go, Python, and TypeScript. Python and TypeScript structural adapters are
intentionally not implemented yet: a spec requesting one fails safely instead
of performing an unsafe text rewrite.

```hcl
format = "https://sneat.dev/workbench/formats/migration/v1"

migration "rename-api-v1" {
  title = "Rename the shared API"

  scope {
    languages = ["go"]
  }

  import_replace "go" {
    from = "example.com/old/api"
    to   = "example.com/new/api"
  }

  selector_rewrite "go" {
    import        = "example.com/old/service"
    add_import    = "example.com/new/model"
    add_import_as = "model"
    rewrites = {
      Record = "model.Record"
    }
  }

  # Repeat this block freely, including with the same "go" label.
  selector_rename "go" {
    import = "example.com/new/model"
    from   = "OldType"
    to     = "NewType"
  }

  composite_field_rename "go" {
    from = "OldEmbeddedField"
    to   = "NewEmbeddedField"
  }
}
```

`format` is the migration-spec contract, not an opaque integer. It is a link
to the format definition at
[`https://sneat.dev/workbench/formats/migration/v1`](https://sneat.dev/workbench/formats/migration/v1).
The first implementation recognises that exact format offline; it does not
fetch the URL while planning a migration.

Every `selector_rename "go"` block is a list entry, not a map entry, so many
blocks with the same language label are valid. It renames a qualified package
member such as `model.OldType`; it does not rename locally declared Go types or
unqualified identifiers. Those need a future type-aware rename operation based
on `go/types` (and corresponding LibCST/TypeScript compiler adapters), rather
than an unsafe text replacement.

`composite_field_rename "go"` renames only identifier keys in explicitly typed
named composite literals, such as `Entry{OldEmbeddedField: value}`. It skips
maps, arrays, slices, elided nested literals, strings, comments, declarations,
and ordinary identifier uses. The instruction is intentionally syntax-aware,
not owner-type-aware; use a distinctive field name and a narrow file scope
when the old name is common.

For a deterministic specification, WB evaluates HCL operation phases in this
order: `text_replace`, `import_replace`, `selector_rewrite`,
`selector_rename`, then `composite_field_rename`. Repeated blocks keep their
source order within a phase. The
separate, future local-type rename is deliberately not accepted until an
adapter can resolve declarations and references across its complete package.

Semantic review rules can omit already-correct forms on the same source line:

```hcl
review "changes-executor" {
  language        = "go"
  pattern         = "[.]ApplyChanges[(]"
  exclude_pattern = "dal[.]ApplyChanges[(]"
  message         = "Call the DAL executor with the record changes envelope."
}
```

`exclude_pattern` is optional and line-scoped. A matching exclusion suppresses
only review matches on that line, so a correct form elsewhere in the file does
not hide a method call that still needs semantic migration.

When a migration introduces a new Go module, declare its version explicitly:

```hcl
go_module_require "github.com/example/new-model" {
  version = "v1.2.3"
}

# Required when a campaign branch that used a local worktree replacement is
# about to become a PR. This version must already be available to remote CI.
go_module_release "github.com/example/new-model" {
  version = "v1.2.3"
}
```

The normal source-only runner leaves this declaration alone. It is consumed by
the hierarchical Go workflow below, which adds the requirement and redirects it
to the campaign's local worktree. `go_module_release` is intentionally
separate: it says which published version replaces that temporary local path
before a PR can be opened.

#### Hierarchical Go campaigns

Use `--hierarchical` when the migration must move a Go dependency graph rather
than one checked-out repository. It reads the source module's `go mod graph`,
finds the reverse dependency closure of the module paths referenced by the
migration, and prepares each GitHub repository independently.

Interactive runs show the current dependency layer, repository, and campaign
phase (prepare, rewrite, manifest update, verification, publication, checks,
and merge) on stderr. The elapsed time refreshes even while a phase emits no
events. `--non-interactive` keeps the same report and exit contract without
terminal progress.

```sh
# Plan only. No clone, fetch, worktree, source, commit, or push occurs.
wb migrate examples/migrations/dalgo-record-v1.hcl \
  ~/projects/sneat-co/sneat-bots \
  --hierarchical

# Apply into dedicated branches and worktrees, verifying every changed Go
# module with `go vet ./...` and `go test ./...` (the default `full` mode).
wb migrate examples/migrations/dalgo-record-v1.hcl \
  ~/projects/sneat-co/sneat-bots \
  --hierarchical --apply

# Commit only after all default verification succeeds. Push is separately
# opt-in and pushes those branches only.
wb migrate examples/migrations/dalgo-record-v1.hcl \
  ~/projects/sneat-co/sneat-bots \
  --hierarchical --apply --commit --push

# Open one PR per changed repository. WB continues with other ready
# repositories while GitHub Actions runs for PRs already opened.
wb migrate examples/migrations/dalgo-record-v1.hcl \
  ~/projects/sneat-co/sneat-bots \
  --hierarchical --apply --pr --parallel=2

# Merge only after every campaign PR has successful required GitHub checks.
# This does not enable auto-merge or bypass protected-branch rules.
wb migrate examples/migrations/dalgo-record-v1.hcl \
  ~/projects/sneat-co/sneat-bots \
  --hierarchical --apply --merge

# Resume partial campaign worktrees on their expected branches.
wb migrate examples/migrations/dalgo-record-v1.hcl \
  ~/projects/sneat-co/sneat-bots \
  --hierarchical --apply --resume

# Remove only clean worktrees for the named migration. No source root is used.
wb migrate examples/migrations/dalgo-record-v1.hcl \
  --hierarchical --cleanup
```

Canonical clones live at `<github-dir>/<org>/<repo>`; `--github-dir` defaults
to `--projects-root`. The campaign creates its worktrees under
`<wb-home>/worktrees/<migration>/<org>/<repo>` from `origin/<ref>`
(`main` by default). A dirty canonical clone is never checked out, reset, or
otherwise modified: WB only fetches `origin`, then branches its dedicated
worktree from the requested remote ref. Missing, resolvable GitHub repositories
are cloned during `--apply`, regardless of organisation.

For changed consumer modules, WB updates `go.mod` requirements declared in the
spec and writes relative `replace` directives to the matching campaign
worktrees. It does not create a shared `go.work` file. This lets dependent
worktrees compile together while keeping the changes reviewable and
committable per repository. Before `--pr` (and therefore `--merge`), WB removes
those temporary replacements, requires an explicit `go_module_release` for
each affected module, runs `go mod tidy`, and reruns the selected verification.
This prevents a PR from containing local paths that GitHub Actions cannot
resolve. If a module has not been released yet, the campaign fails safely
before the affected repository is committed, pushed, or submitted for review.

Verification is enabled by default for every `--apply` campaign:

| Setting | Checks |
|---|---|
| `--verify=compile` | `go test -run=^$ ./...` |
| `--verify=test` | `go test ./...` |
| `--verify=full` (default) | `go vet ./...`, then `go test ./...` |
| `--no-verify` or `--verify=none` | No checks |

`--commit` requires `--apply`. `--push` implies `--commit` and also requires
`--apply`. `--pr` implies `--push`; it opens one ordinary (non-draft) PR per
changed repository, with no auto-merge. `--merge` implies `--pr` and is a
separate final phase: WB first checks every campaign PR's required GitHub
checks, then uses GitHub's normal merge operation in dependency order. It
stops before merging anything when a check is pending or failing, and never
bypasses branch protection.

`--parallel=N` (default `1`) runs independent repositories concurrently. WB
still processes dependency layers in order: a provider's migration and local
verification complete before a consumer that uses its local replacement starts.
Within each layer, WB completes source edits across all repositories before
normalizing manifests, then verifies the layer. This makes cyclic Go module
groups safe because dependency tooling never reads a peer's half-rewritten
source tree.
Once a repository is verified and `--pr` is active, its PR is opened
immediately; WB does not wait for its remote CI before working on later ready
repositories. Only the final `--merge` phase waits for required GitHub checks.
Local campaigns without commit or publishing flags continue verification after
a failure so the final report indexes every failing repository. Publishing
campaigns remain fail-fast before dependent branches can be committed.

`--resume` is an explicit recovery path: it accepts an existing worktree on the
expected campaign branch, preserves partial or manually corrected migration
changes, and verifies those existing changes. Dependency discovery also uses
the validated root campaign worktree, so prerequisite refactors that introduce
modules bring those providers into the next campaign pass automatically. Go's
own module tooling repairs incomplete `go.mod`/`go.sum` metadata during that
apply-only resume discovery. An
apply campaign holds an
exclusive lock under its migration worktree root, so concurrent runs fail safely.
`--cleanup` removes only clean worktrees for that migration; it leaves
canonical clones, branches, and reports intact.

Every hierarchical run writes a linked human index and deterministic manifest
to `<wb-home>/reports/<migration>/campaign.md` and `campaign.yaml`
(or `--report-dir`). Per-module `migration.md` and `migration.yaml` reports
are nested beneath that directory. The campaign index lists every
repository-relative path that differs from its configured base ref, including
committed, staged, unstaged, and untracked files. This cumulative index remains
truthful after an idempotent `--resume`; per-module counts describe only files
rewritten by the current mechanical pass. The Markdown index points at
worktrees and per-module reports, while Git remains the source of the detailed
diff.

On a dry run the campaign deliberately reports `plan_state: deferred` and no
`changed_files` count: WB has not created worktrees or evaluated their source.
Its Markdown index says `unknown (worktree not created)` rather than implying
that no files will change.

Adapter work is deliberately isolated behind the same planning and apply
protocol:

| Language | Structural adapter | Package/manifest work |
|---|---|---|
| Go | Implemented with `go/ast`, `go/types`, and `go/format` | `go.mod` support is implemented; local type rename remains a future type-aware operation |
| Python | Planned with LibCST | `pyproject.toml` |
| TypeScript | Planned with the TypeScript compiler API | `package.json` |

The initial DALgo migration definition is
[`examples/migrations/dalgo-record-v1.hcl`](examples/migrations/dalgo-record-v1.hcl).

### `wb ci audit` — CI/CD policy validation

Audit the current repository, or every local clone, without changing anything:

```sh
wb ci audit --strict
wb ci audit --fleet --strict
wb ci audit --fleet --filter sneat-co/ --json
```

The audit detects Go and frontend stacks independently and requires each to
have an explicit positive coverage threshold. Mixed-stack repositories are
also required to select jobs from changed paths, so a backend-only change does
not start frontend runners (and vice versa). Repeated Playwright setup across
multiple E2E jobs is flagged for consolidation. For deployment workflows it flags
source rebuilds, missing CI artifacts, and artifacts that are downloaded
without source-SHA/checksum verification. `--strict` makes findings fail with a
non-zero exit code, suitable for CI and pre-push hooks; `--json` is intended for
Backstage/ops inventory.

### `wb ci wait` — bounded exact CI receipt

Wait for all observed CI on one exact direct-push target or PR head without a
background watcher:

```sh
wb ci wait --repo sneat-dev/wb --target main --head <exact-sha> --json
wb ci wait --repo sneat-dev/wb --pr <number> --target main --head <exact-sha> --json
```

Each foreground slice is eight minutes by default and never more than nine.
Pending and failed results exit `1`; pending JSON includes `resume_args` for
the same exact identity. Reinvoke those arguments until a terminal result. In
every mode WB combines the exact head's GitHub check runs with legacy commit
statuses. PR mode additionally corroborates the PR identity and GitHub's PR
check views. WB enumerates classic protection and every paginated active branch
rule; a producer-pinned required context must come from that exact GitHub App
in every mode. A same-named PR summary or legacy status cannot substitute for
the pinned producer. PR mode also fetches the exact target SHA, proves it is an
ancestor of the candidate, and requires either classic or ruleset strict
required-status-check policy with at least one required check. It does not wait
for current target CI to turn green: an updated candidate may be the fix for a
red target. Target movement rejects the receipt and requires reintegration.
Merge-group observation for merge queues remains planned and fails closed.
Missing policy authority, unsupported required-workflow names, or incomplete
check/status pagination remain pending or fail closed. A pass requires two
unchanged terminal observations. That is a bounded quiescence receipt, not
proof that an optional workflow cannot register later, so collect separate
repository release evidence before cleanup. Both modes reject identity drift.
Do not replace this with a detached or long-running shell poller.

The default observation interval is 30 seconds while checks are pending. Once
a checks-bearing observation is terminal, its confirming unchanged reread
waits a shorter bounded delay (15 seconds by default) instead of a full
interval — at most one shortened reread per terminal episode, falling back to
the full cadence if the terminal fingerprint churns. The empty
no-applicable-checks receipt always waits the full interval before its
reread, because that gap is its only time-based guard against CI that has
not registered yet. Within one foreground slice,
WB caches the initial branch-protection and active-rules receipt while it polls
the exact mutable PR and commit state; before reporting a pass it fetches that
policy receipt again. This keeps the same fail-closed merge evidence while
reducing a pending PR's normal REST observation rate from seven calls every ten
seconds to four calls every thirty seconds (about an 81% reduction, before any
rules pagination).

### `wb hooks` — consistent, user-owned Git hooks

WB installs small managed shims while you retain control of the scripts they
run. Start conservatively in one repository, then roll the same policy through
all local clones:

```sh
wb hooks install                         # current repository
wb hooks check
wb hooks repair
wb hooks install --fleet                 # every clone below --projects-root
wb hooks check --fleet --filter sneat-co/
wb hooks repair --fleet
```

`install` and `repair` refuse to replace an existing `core.hooksPath` or an
unmanaged active hook. `repair --force` preserves hooks at an old configured
path and backs up any unmanaged collision inside WB's directory before replacing
it. `check` (alias `validate`) detects missing, stale, unexpected, or
non-executable shims; `--json` makes its result consumable by CI or Backstage.
Managed shims also preserve the absolute `--projects-root` and resolved WB
home used at installation, so worktree guards remain correct when Git invokes
them from a non-default projects hierarchy. A shim installed from the normal
default home remains migration-compatible with legacy linked worktrees; an
explicit `WB_HOME` remains isolated.

The managed shim does not retain the executable path used by `hooks install`
or `hooks repair`. At hook runtime it prefers `WB_EXECUTABLE`, otherwise
resolves `wb` from `PATH`, then verifies the physical result is an absolute,
regular, executable file outside the repository before invoking it.

#### Hook policy, detection, and composable profiles

Policy layers in this order: WB's conservative built-ins (including worktree
admission), the user's global
`~/.config/wb/hooks.yaml`, then the repository's `.wb/hooks.yaml`. A repository
entry overrides the same global hook. Automatic profiles are opt-in, so
upgrading WB never adds expensive checks to an existing installation
unexpectedly.

```yaml
version: 1

profiles:
  auto: true                    # detect all built-in and custom definitions
  # include: [sneat-product]    # force a profile even without a match
  # exclude: [node, worktree]   # explicit opt-out of a detected/default profile
  definitions:
    sneat-product:              # custom product/tool/domain profile
      order: 200
      detect:
        any_files:
          - sneat.project.yaml
      hooks:
        pre-push:
          template: templates/sneat-product/pre-push.sh

# A direct hook replaces WB's conservative base block. Setting it disabled
# suppresses the whole hook, including blocks contributed by profiles.
# hooks:
#   pre-push:
#     disabled: true

metrics:
  enabled: true
  # path: ~/.local/state/wb/hook-events.jsonl
  labels:                       # optional, user-chosen pseudonyms
    developer: dev-17
    machine: laptop-a
```

With `profiles.auto: true`, the built-in detectors currently contribute:

| Profile | Detection | Pre-commit block | Pre-push block |
|---|---|---|---|
| `go` | `go.mod` | `gofmt` plus touched-package `go vet` | `go vet ./...`; tests and coverage run during landing/CI |
| `node` | `package.json` | configured changed-file formatting/lint | run `lint` when present; tests and builds run during landing/CI |

A Go-only repository therefore runs the base and Go blocks, a Node-only
repository runs the base and Node blocks, and a mixed repository runs all
relevant blocks. A pure remote-ref deletion has no Go object to publish, so
the Go block records success without running vet; base, worktree, custom, and
metrics policy still run, and any mixed or non-deletion push runs static Go
checks. The classifier retains publication identity for telemetry without
duplicating CI's tests. General deterministic cache and durable metrics write authority for
secure hook execution remains tracked in [#61](https://github.com/sneat-dev/wb/issues/61).
Custom definitions use repository-relative `any_files` and
`all_files` detectors; standard glob patterns are supported. A definition with
the same name as `go` or `node` overrides selected built-in hooks, so users can
replace either language template globally. The base block runs first; profiles
run by ascending `order`, then name. Each pre-push block receives an independent
copy of Git's stdin and execution stops on the first failure.

Relative template paths are resolved from the YAML file that declares them;
templates run with `/bin/sh` and need not be executable. Copy and adapt
[`examples/hooks-policy/`](examples/hooks-policy/). Templates receive
`WB_HOOK`, `WB_PROFILE`, `WB_BLOCK`, `WB_REPO_ROOT`, `WB_REPO_SLUG`,
`WB_HEAD_SHA`, `WB_BRANCH`, `WB_HOOKS_CONFIG`, and `WB_HOOK_METRICS_PATH`, plus
the original Git hook arguments and standard input. `wb hooks check` displays
the detected profiles and exact block order; `--json` exposes the same data.

#### Local user sections around WB

Generated hook files are ordinary shell scripts. WB owns only the delimited
dispatcher and preserves user commands before and after it during install or
repair:

```sh
#!/bin/sh
set -eu

# Local commands that run before WB.

### Start of WB managed hook ###
# The generated resolver selects WB_EXECUTABLE or an absolute `command -v wb`
# result, validates it, and stores the physical result only for this process.
"$_wb_hook_executable" hooks run 'pre-push' -- "$@"
_wb_hook_status=$?
if [ "$_wb_hook_status" -ne 0 ]; then
    exit "$_wb_hook_status"
fi
### End of WB managed hook ###

# Local commands that run after every WB block succeeds.
```

Policy templates are preferable for shared, version-controlled checks. The
outer sections are useful for machine-local behavior and remain untouched as
WB updates only the marked section.

#### Local lifecycle metrics

Once installed, hooks append versioned, local-only JSONL events in one batched
write per WB run. WB records its managed dispatch and per-block
outcomes/durations alongside repository, commit, branch, OS/architecture, and
optional labels—not diffs, filenames, commands, output, credentials, email,
hostname, or source. Machine-local commands outside the WB delimiter are
intentionally not observed or timed. A metrics write failure warns but never
turns a successful WB block into a failed commit or push.

```sh
wb hooks metrics                  # 14-day terminal chart
wb hooks metrics --days 30
wb hooks metrics --repo sneat-go
wb hooks metrics --json
```

Successful commits are counted exactly through `post-commit`. Pushes are
reported as **push attempts**, because Git provides `pre-push` but no
`post-push` confirmation. The default event file is
`~/.local/state/wb/hook-events.jsonl`; set `metrics.enabled: false` to disable
collection or configure a different path. Cross-developer/machine aggregation
is intentionally opt-in through explicit labels and a future exporter.

The broader direction—named build/test spans, cache and machine diagnostics,
local/CI/deployment correlation, CI-minute savings, and privacy-safe team
comparisons—is captured in the SpecScore idea
[`developer-lifecycle-metrics`](spec/ideas/developer-lifecycle-metrics.md).

### `.worktree.md` — every checkout says what it is

WB writes a generated `.worktree.md` at the root of **every** checkout it
manages, canonical clones and worktrees alike. An agent, a human, or any future
tool reads one file and knows where it is:

```yaml
---
wb_checkout: 1
kind: canonical | worktree
writable: false | true
repository: "owner/name"
checkout_path: "…"
canonical_path: "…"
branch: "…"
base_branch: "main"
task: "…"            # worktrees only
worktrees_root: "…"  # worktrees only
generated_by: "wb vX.Y.Z"
generated_at: "…"
---
```

Universality is the design, not a convenience. A warning file dropped only into
canonical clones is a negative signal, so a **missing** file would read as
"nothing objects here, go ahead" — the wrong default for the checkout WB has
not reached yet. With a marker everywhere, absence means *unknown, verify*:
run `wb worktree guard .`, then `wb worktree marker .`.

It also reaches readers a Claude Code hook cannot: Codex, a person, and
whatever comes next.

**It is never committed.** A committed marker that WB rewrites would show as
` M .worktree.md` — a dirty canonical clone, the exact condition this exists to
prevent — and would conflict on any pull that touched it. `.gitignore` cannot
help there: it has no effect on an already-tracked file. So WB generates the
file locally and pairs every write with an anchored rule in the repository's
Git exclude file. One rule in the **common** Git directory covers the canonical
clone and every worktree cut from it, because linked worktrees have no
`info/exclude` of their own — verified against real Git, in both directions.
WB's own hooks read `git status --porcelain`, which never lists an ignored
path, so the marker cannot trip the policy it advertises.

```sh
wb worktree marker .                 # one checkout
wb worktree marker --fleet           # every clone and every registered worktree
wb worktree marker --fleet --dry-run # report only
```

Markers are refreshed on `wb sync` and on `wb worktree create`, and re-running
is free: a marker that would differ only by its timestamp is left alone.

### `wb worktree rescue` — get work out of a canonical clone without losing it

Uncommitted work in a canonical clone is invisible to WB and one routine
checkout away from being destroyed. On 2026-08-27 a complete, unlanded 42-line
lesson sat untracked in one and survived by luck.

```sh
wb worktree rescue --fleet                    # find every dirty canonical clone
wb worktree rescue <path>                     # report; changes nothing
wb worktree rescue <path> --apply --push      # preserve onto a branch, publish it
wb worktree rescue <path> --apply --branch <b> --restore   # then clean the clone
```

**Reporting is the default and discarding is never one.** `--apply` preserves
and stops; returning the clone to a clean checkout is a second decision behind
`--restore`, which refuses unless every path the report named is verifiably
inside the rescue commit *and* the branch is on the remote (or
`--allow-unpushed` accepts that risk). The `git clean` it then runs omits `-x`,
so ignored paths — the generated marker among them — survive.

`--push` uses an attested rescue-only route through WB's managed pre-push
hook. The hook proves the push contains exactly one rescue ref, that its commit
is parented on the canonical `HEAD`, and that its tree equals a fresh complete
capture. It does not disable hooks for an ordinary branch, and WB requires a
fresh exact remote-ref receipt before `--restore` proceeds.

The capture never disturbs the clone. WB copies the clone's index to a scratch
file, stages the working tree into the copy, writes a tree from it, and commits
that tree with `git commit-tree` parented on HEAD. The branch ends up holding
every modified, staged, and untracked path while the clone's HEAD, branch,
index, and working tree are unchanged. `git stash` is deliberately not used:
its stack is shared with every linked worktree, and `git stash create` does not
capture untracked files — which is exactly the content most at risk.

### `wb hooks agent` — refuse an agent write into a canonical clone

A Git hook judges a commit. It cannot see the write that never reaches one,
and a canonical clone is ruined by the write, not by the commit: a
`git checkout origin/main -- .` run to read one file stages the whole tree
against a stale HEAD and discards whatever was sitting there uncommitted. A
hook that does fire can also be walked around — `git -c core.hooksPath=/dev/null
commit` is one line.

`wb hooks agent pre-tool-use` moves the refusal one layer earlier. It reads a
Claude Code PreToolUse payload on stdin and writes a deny document when the
tool call would write inside `<projects-root>/<owner>/<repository>`, naming
`wb worktree create` as the remedy.

```sh
wb hooks agent install                      # register it in ~/.claude/settings.json
wb hooks agent install --dry-run            # show the merged document instead
wb hooks agent pre-tool-use --input p.json  # rehearse one decision
```

**It fails open, without exception.** An unreadable payload, an unrecognised
tool, a shell construct it does not model, a path it cannot resolve, an
internal panic, and a WB too old to know the subcommand all allow the call. The
installed command is `wb hooks agent pre-tool-use 2>/dev/null; exit 0`: Claude
Code blocks a tool call whose PreToolUse hook exits 2, and WB spends exit 2 on
usage errors, so forcing exit 0 leaves the JSON document on stdout as the only
channel through which this hook can ever say no.

What it never refuses:

- any read, of anything, anywhere;
- any write inside a linked worktree, including one nested inside a canonical
  clone such as `.claude/worktrees/<name>`;
- inside a canonical clone: `git fetch`, `git merge --ff-only`, `git pull
  --ff-only`, `git status`, `git log`, `git show`, `git ls-tree`, `git diff`,
  `git push`, `git apply --check`, `git clean --dry-run`, `git stash list`, and
  every unrecognised program.

What it refuses inside a canonical clone: Git subcommands that mutate the tree,
index, or history; output redirections into the clone; `sed -i` and friends;
`rm`/`mv`/`cp`/`tee` and other file mutators naming a path inside it; write
verbs of known generators (`specscore … new`, `go mod tidy`, `pnpm install`,
`gofmt -w`) run with the clone as the working directory; and any Git invocation
that disables the repository's managed hooks.

Bash detection is deliberately partial and documented as such in
`internal/agentguard/bash.go`. It models no shell expansion, so a working
directory reached through a variable and a file written by a script inside a
heredoc both pass. An honest, partial guard that never blocks legitimate work
beats an aggressive one agents learn to route around.

### `wb self-update` — update the installed binary (alias: `wb update`)

`self-update` is the canonical name because in a CLI whose other verbs act on
other repositories (`wb sync`, `wb deps`, `wb migrate`), a bare `update` does
not say *what* gets updated; `wb update` still works as an alias.

```sh
wb self-update --check                  # report availability only; never modifies
wb self-update --check --format json    # machine-readable verdict
wb self-update                          # confirm, then update
wb self-update --yes                    # skip the confirmation prompt
wb self-update --version v0.24.0        # install an exact release instead of latest
wb self-update --version 0.23.2 --allow-downgrade   # roll back
wb self-update --dry-run --format json  # report what would happen; never modifies
```

The command first decides how the running binary was installed. A
Homebrew-managed install (Caskroom or Cellar path, reached through any number
of symlinks) runs the exact `brew upgrade --cask wb` command after confirmation;
Homebrew remains the only writer of its cask binary. `--dry-run` reports that
command without executing it, and a version pin is refused because Homebrew
cannot reliably install an arbitrary release. A manual install (release archive or `go install`, detected
by a `go/bin` or `bin/`-suffixed path) downloads the release asset matching
the host OS/architecture, verifies its sha256 against that release's
published checksums *before* extracting anything, and swaps it in atomically
so a failed or interrupted update always leaves the original binary intact
and runnable. When the install method cannot be confidently classified,
self-update refuses rather than guessing and points at manual-update options.

`--check` performs the same detection and version comparison without
downloading or writing anything, for either install method; `--format json`
emits a single document carrying `current`, `latest`, and a `verdict` of
`up_to_date`, `update_available`, or `undetermined` — the machine-readable way
to tell "an update is available" apart from "the release lookup failed" when
both otherwise report the same exit code. `--version` pins an exact release
tag (leading `v` optional) instead of the latest stable one; pinning to a
version older than the running build refuses unless paired with
`--allow-downgrade`. Replacing the binary always requires confirmation —
either an interactive `y`, or `--yes` — and refuses outright rather than
blocking on input when no terminal is attached and `--yes` was not given, so
scripts and agents driving wb never hang. wb publishes no Windows build, so
the self-replace path is macOS/Linux only; a Windows host reaching it refuses
with a clear message instead of attempting a swap it has no asset for.

### `wb skills` — install WB's Agent Skills into a harness

`ai/skills/*` (this repository's own Agent Skills — `wb-worktrees`,
`wb-merge`, `wb-hooks`, and the rest) auto-discovers for a session working
*inside* a checkout of `sneat-dev/wb`, through `.claude-plugin/plugin.json`.
A session orchestrating any other repository, with `wb` installed globally,
never had them at all: there was no checkout for a harness to discover them
from. `wb skills sync` closes that gap. The skills are embedded in the `wb`
binary itself (`go:embed`), so it installs them from any installed `wb`, in
any project, without a source checkout:

```sh
wb skills sync                        # install/update into every present harness
wb skills sync --harness cursor       # Cursor: ~/.cursor/skills
wb skills sync --harness codex        # Codex: ~/.codex/skills
wb skills sync --harness all          # Claude, Cursor, and Codex
wb skills sync --dir <path>           # target an explicit skills directory
wb skills sync --dry-run              # preview added/updated/removed/conflicts
wb skills sync --format json          # machine-readable report
```

It is idempotent — a repeat run with nothing new to ship reports every skill
`unchanged` and writes nothing — and it never overwrites a directory it did
not itself install: a name collision with something already there is
reported as a `conflict` (exit code 1) and left untouched. A marker file
next to the installed skills records which `wb` version performed the last
sync. `wb self-update` runs `wb skills sync` automatically right after a
successful update, resolving the stable `wb` launcher again after a Homebrew
cask transition; every other `wb` command prints a single line on stderr
when the installed skills and the running `wb` version disagree (one line
per present harness):

```
wb: Agent Skills in ~/.claude/skills were synced by wb 0.74.0, this is wb 0.75.1 -- run `wb skills sync`
```

`wb skills hook print` prints a Claude Code `SessionStart` hook snippet that
reminds a new session to register itself (`wb session register`) and repeats
the drift warning above in its opening context; `wb skills hook install`
merges that hook into `~/.claude/settings.json` (`--dry-run` to preview).
`wb` never edits that file on its own outside this explicit subcommand.

## Operations dashboard

`wb daemon serve` starts the embedded read-only dashboard and versioned JSON
API at `http://127.0.0.1:8766` by default. It shows managed worktrees and
privacy-safe `wb run --` cost from the last 14 days.

```sh
wb daemon serve
curl http://127.0.0.1:8766/api/v1/health
curl http://127.0.0.1:8766/api/v1/overview
```

The command refuses non-loopback listeners. For access from another registered
machine, publish the loopback service through an authenticated Cloudflare
Tunnel. The MVP API is read-only and does not expose arbitrary command
execution.

## Build from source

```sh
go build -o ~/.local/bin/wb ./cmd/wb   # install on PATH
go test ./...                          # run tests
wb sync --dry-run                      # preview a fleet sync
wb run --list                          # see your configured recipes
```

## Adding a new operation

For anything expressible as "detect matching repos, mutate, land the
result," add a recipe to your `wb.yaml` — no code change needed. For
something structurally different (like `sync`, which reconciles local
clones with GitHub existence rather than mutating already-cloned content), a
new fleet command adds a `case` in `cmd/wb`, reusing `internal/discover` and
`internal/gitops`.

## Known limitation

Discovery keys on `org/name` and ignores linked Git worktrees, which are
alternate checkouts rather than additional fleet repositories. If a repo is
cloned locally under a directory
name that differs from its GitHub org (e.g. `~/projects/dalgo/...` vs the
`dal-go` org), the mislabeled local copy is treated as local-only and
skipped, and the correctly-named repo is cloned fresh under
`~/projects/<org>/` during `sync`. Use matching org directory names to avoid
duplicate clones.

## License

MIT — see [LICENSE](LICENSE).
