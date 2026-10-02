---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Quiet Verbs and Masked Exit Status Guard

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/quiet-verbs-and-masked-status-guard?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/quiet-verbs-and-masked-status-guard?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/quiet-verbs-and-masked-status-guard?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/quiet-verbs-and-masked-status-guard?op=request-change) |
**Status:** Draft
**Source Ideas:** —

## Summary

State-changing wb verbs gain a `--quiet` outcome-only mode, and the agent
pre-tool-use hook refuses a shell command in which such a verb's output is piped
into another command, because the pipe hides the verb's exit status.

## Problem

On 2026-10-02 a coordinator agent ran a chain shaped like
`wb worktree create <task> <repo> … 2>&1 | tail -1 && cd <worktree> && <edit> &&
git commit … && wb pr create`. `wb worktree create` correctly refused (`error:
worktree already exists … (use --resume or choose another operation)`), but a
pipeline reports the exit status of its last command, so the `tail` made the
pipeline succeed, the `&&` chain continued, and `wb pr create` opened a
duplicate pull request.

The agent piped because the lifecycle verbs print progress lines (CI-wait
heartbeats, remote-claim notes, landing phases) and it wanted only the outcome.
The caller-side cause is the pipe. The reason to pipe is that there is no
supported way to ask a verb for just its outcome. Both have to go: remove the
reason, and refuse the pattern.

## Behavior

### `--quiet`: outcome and refusals only

`--quiet` (or `WB_QUIET=1`) is a root flag that the lifecycle verbs consume:
`create`, `worktree create`, `land`, `worktree land`, `worktree merge` (and its
`prepare`, `land`, `resume` and `revert` leaves), `pr create`, `pr land`, and
`worktree cleanup`. It follows the repository's persistent-flag contract: a verb
that does not consume it rejects the flag with a usage error instead of
ignoring it, and the environment variable is ignored by verbs that do not
consume it. `wb run --quiet` and `wb worktree guard --quiet` keep their own
local meaning.

Under `--quiet` the verb writes the same stdout outcome lines it writes without
it (including the refusal reason, `refusal:` code, and `resolve with:` line a
refused verb prints), the same exit code, and the same `--format json` document.
It suppresses the stderr commentary that is not an outcome: live progress and
heartbeat lines, remote-claim success notes (`acquired`, `refreshed`,
`released`), per-candidate inspection progress, informational `info:` lines, and
the `suggestion:` courtesy line. Warnings, errors, the remote-claim note
that a claim is held by someone else, and the `info: cleanup …` line of an
artifact a run actually changed (`applied=true`, the only line that says a
mutation was applied) are never suppressed.

`--quiet` is distinct from `--non-interactive`: `--non-interactive` removes
terminal-only UI, and a non-terminal agent still receives newline-delimited
progress so a long CI wait is visibly alive. `--quiet` removes that progress too.

### The agent hook refuses a masked verb status

`wb hooks agent pre-tool-use` refuses a Bash command in which a state-changing
wb verb is not the last command of a pipeline (`|` or `|&`), because the
pipeline then reports the status of the command after it. State-changing verbs
are the lifecycle verbs above plus `pr update`, `worktree end`, `remote
claim|release`, `session park|move`, `agent dispatch`, `deps bump`, `repo
init-remote`, `sync`, `hooks install|repair`, `self-update`, and the `--apply`
forms of the verbs that plan by default (the destructive maintenance verbs,
`worktree adopt`, `branch quarantine`, `fleet merge-policy`, `migrate`).
`internal/agentguard/pipeline.go` holds the table and a test checks every path in
it against the real command tree. Read-only verbs (`list`,
`status`, `commands`, `--help`, a dry-run cleanup) piped into `tail` or `grep`
are allowed, as is a state-changing verb that is the last command of a pipeline
(`printf … | wb worktree create … --original-prompt-file -`), where the
pipeline's status is the verb's own.

The command is allowed when pipefail is in effect in the shell that runs the
pipeline: `set -o pipefail` (or `setopt pipefail`) earlier in the same shell, or
a `bash -o pipefail -c` wrapper. Reading `PIPESTATUS`/`pipestatus` is not
accepted: by the time anything reads it the `&&` chain has already run on, and
in zsh `${PIPESTATUS[0]}` is empty. Pipefail counts only when the hook can tell
it is in effect where the pipeline runs, and otherwise the command is refused:

- a `-c` payload of `bash`, `zsh`, `sh` and the like starts with pipefail off,
  because a child shell does not inherit the parent's; only its own options
  (`-o pipefail`) or its own body turn it on;
- a `set -o pipefail` inside `( )`, `$( )` or a backtick substitution is gone
  once that group closes, and one behind `&&`, `||` (also at the end of the
  previous line), a pipe or `&`, in a `then`, `else`, `do` or `case` body, in a
  function body, or in a group that is, may not have run, so only an
  unconditional top-level `set` or `setopt` counts;
- any other command that names pipefail in any spelling or runs `emulate`
  (`if x; then set +o pipefail; fi`, `command set +o pipefail`, `eval 'set +o
  pipefail'`, `shopt -u -o pipefail`, `emulate sh`) leaves it unknown, which
  counts as off, and bash's option name is case sensitive (`set -o PIPEFAIL` is
  not a switch-on);
- the last option wins (`set -o pipefail +o pipefail`, `setopt nopipefail`,
  `NO_PIPE_FAIL`), and words after `--` are positional parameters;
- a group whose output is piped (`{ cmd; } 2>&1 | tail`, `(cmd) | tail`) hides
  the status of the group, so the pipefail in effect where the group opened
  decides, and backtick and double-quoted `$( )` substitutions are read like
  `$( )`;
- a piped `done`, `fi` or `esac` pipes the whole loop, conditional or `case`,
  like a piped group, and a piped `bash -c`, `zsh -lc` or other shell wrapper
  hides the status of whatever its payload runs, so a watched verb in either is
  refused unless pipefail is in effect where the pipe runs;
- a flag's value is never read as a marker: in `wb pr create --title --help` the
  title is `--help`, and `--resume` and `--verify` take a value only on the
  verbs where they do (`session move`, `migrate`); `sync -n` and `self-update
  --check` are read-only requests, like `--dry-run`;
- the text of a heredoc inside a double-quoted `$( )` (a commit message or pull
  request body quoting `wb pr create | tail` in backticks) is prose, not a
  command; a real substitution in the same `$( )` still is one.

There is no other escape hatch and no environment override. The refusal explains
the hazard in two lines and names the fix: `--quiet` where the verb has it, or,
for a `--format json` run feeding a parser, capture then parse
(`x=$(wb … --format json) && jq … <<<"$x"`), or pipefail.

### What the hook does not see through, by design

The hook reads the command text and models no expansion. It does not see through
`eval`, `script`, `unbuffer`, `$WB` or any other variable used as the command,
shell function wrappers, `env -S`, `go run ./cmd/wb`, a renamed or copied binary,
or a remote shell such as `ssh`. Those resolve to "allowed", like every other
construct the agent guard cannot model. Gating read verbs (`wb run`, `wb ci
wait`, `wb check`) are not in the verb table either: their status gates a
follow-up step but they change nothing, so they stay maskable.

## Requirements

#### REQ: quiet-flag-on-lifecycle-verbs

`--quiet` MUST be a persistent root flag consumed by `create`, `worktree
create`, `land`, `worktree land`, `worktree merge` and its `prepare`, `land`,
`resume` and `revert` leaves, `pr create`, `pr land`, and `worktree cleanup`,
and rejected with a usage error by every other command that does not define its
own local `--quiet`. `WB_QUIET` MUST have the same effect as the flag on the
consuming verbs and no effect elsewhere.

#### REQ: quiet-prints-outcome-and-refusals-only

With `--quiet` the consuming verbs MUST keep their stdout outcome lines,
refusal reason, refusal code, `resolve with:` line, exit code, warnings and
errors, and `--format json` document unchanged, and MUST NOT write progress,
heartbeat, remote-claim success, inspection-progress, `info:` (except the
`info: cleanup …` line of an artifact with `applied=true`) or `suggestion:`
lines.

#### REQ: quiet-is-discoverable

`wb <verb> --help` for every consuming verb MUST document `--quiet`, and
`wb commands --search quiet` MUST return every consuming verb.

#### REQ: masked-pipeline-refused

The agent pre-tool-use hook MUST deny a Bash command in which a state-changing
wb verb (including through a path to the binary, an environment prefix, a
subshell, a `bash -c` payload, a command substitution, or `2>&1 |` and `|&`) is
followed by a pipe, unless pipefail is in effect in the shell that runs the
pipeline, as the behavior section defines it. Reading PIPESTATUS MUST NOT make
the command allowed.

#### REQ: masked-pipeline-allows-everything-else

The hook MUST NOT deny: a read-only wb verb in a pipeline; a state-changing verb
that is the last command of its pipeline; a pipe character inside a quoted
string or heredoc; any other program in a pipeline; or `--help`/`--dry-run`
forms.

#### REQ: masked-pipeline-refusal-text

The refusal MUST state in two lines that a pipeline reports only its last
command's status and so a refusal from the verb is lost, and MUST name the fix:
drop the pipe and use `--quiet` where the verb supports it, or, when the command
asks for `--format json`, capture the output and parse it afterwards. It MUST
NOT recommend reading PIPESTATUS.

#### REQ: no-bypass-for-masked-pipeline

No flag, environment variable, or marker comment MAY disable the masked-pipeline
refusal; only pipefail in effect where the pipeline runs makes the status
observable.

#### REQ: skills-name-the-pipe-trigger

The `wb` entry-point skill and the worktree/landing lifecycle skills MUST carry
one trigger line that names the pipe hazard and the `--quiet` fix.

#### REQ: cleanup-hint-names-an-existing-flag

The `landed + residue` cleanup finding MUST name the verb that retires the
checkout with the residue discarded, `wb worktree gc <task> --allow-residue
--apply`, and MUST NOT name a flag the verb it was printed by does not accept.

## Acceptance Criteria

### AC: quiet-lifecycle-verbs-print-outcome-only

**Requirements:** quiet-verbs-and-masked-status-guard#req:quiet-flag-on-lifecycle-verbs, quiet-verbs-and-masked-status-guard#req:quiet-prints-outcome-and-refusals-only

With `--quiet` (or `WB_QUIET=1`) each consuming verb writes no progress or
heartbeat line while its stdout outcome, refusal block, warnings, errors, and
exit code are unchanged. A verb that does not consume the flag exits 2 and names
the unsupported flag. **Verifies:** per-verb quiet tests in `cmd/wb` and the
persistent-flag matrix test.

### AC: quiet-is-documented-where-agents-look

**Requirements:** quiet-verbs-and-masked-status-guard#req:quiet-is-discoverable

Every consuming verb's `--help` lists `--quiet`, and `wb commands --search quiet`
returns each of them. **Verifies:** help and catalog tests in `cmd/wb`.

### AC: hook-refuses-a-piped-state-changing-verb

**Requirements:** quiet-verbs-and-masked-status-guard#req:masked-pipeline-refused, quiet-verbs-and-masked-status-guard#req:masked-pipeline-refusal-text, quiet-verbs-and-masked-status-guard#req:no-bypass-for-masked-pipeline

The exact 2026-10-02 command shape is denied with the two-line explanation and
the `--quiet` fix. A command that sets pipefail in the shell that runs the
pipeline is allowed; one that only reads PIPESTATUS, or whose pipefail is in a
closed subshell, behind `&&`, switched off again or in another shell, is
refused. **Verifies:** a table-driven `internal/agentguard` test.

### AC: hook-allows-every-non-masking-shape

**Requirements:** quiet-verbs-and-masked-status-guard#req:masked-pipeline-allows-everything-else

Read-only verbs piped to `tail`/`grep`, a verb that ends its pipeline, a quoted
pipe, and non-wb programs are all allowed. **Verifies:** the same table-driven
test's near-miss rows.

### AC: skills-carry-the-pipe-trigger

**Requirements:** quiet-verbs-and-masked-status-guard#req:skills-name-the-pipe-trigger

The `wb`, `wb-worktrees` and `wb-merge` skills each carry the trigger line and
stay inside their size budget. **Verifies:** the skills size and parity tests.

### AC: residue-hint-names-an-existing-verb

**Requirements:** quiet-verbs-and-masked-status-guard#req:cleanup-hint-names-an-existing-flag

The landed-with-residue finding names `wb worktree gc <task> --allow-residue
--apply`, a verb and flag that exist. **Verifies:** `internal/worktreelanding`
and `wb worktree gc` tests.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
