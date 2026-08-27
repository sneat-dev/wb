# Worktree ownership and liveness

WB records worktree ownership as append-only local metadata. An owner record
contains the agent/runtime, model, effort, the declared agent session PID, the
WB version and command that wrote it, and the time. It never replaces the
creator or an earlier session: each attachment adds another record, so a
worktree keeps its full chain of custody.

## Declare who is working

The PID is the **agent session's**, never WB's own. WB is a short-lived
command: its process is dead moments after it runs, and a recycled id would
later report an abandoned worktree as active. Only the driving session knows
its identity, so it declares it.

Once per session, through the environment — every later WB command picks it up:

```sh
export WB_AGENT_PID=$$ WB_AGENT_RUNTIME=claude-code
export WB_AGENT_MODEL=<model> WB_AGENT_ID=<session-id>
```

Best of all, register the session once at start-up and let WB attribute
everything afterwards:

```sh
wb session register --pid $PPID --runtime claude-code --model <model>
wb session register --pid 12345 --runtime claude-code --model claude-sonnet-5
```

`$PPID` from a harness tool call is the agent process itself. WB then resolves
later writes by matching its own ancestors against registered sessions — which
confirms a declaration rather than guessing an owner, since an unregistered
ancestor is never treated as one.

Registration assigns a stable WB session ID and records the local machine.
The ID is independent of the PID and any harness-native session ID, and is
preserved when the same PID re-registers. A WB-managed successor supplies its
preallocated identity and lineage explicitly:

```sh
wb session register --pid 12345 --wb-session-id wbs-successor \
  --machine hetzner-vm1 --runtime codex --native-harness-id native-123 \
  --tmux-name wb-session-wbs-successor \
  --predecessor-wb-session-id wbs-source --handoff-id handoff-123
```

`--agent-id` remains a legacy alias for `--native-harness-id`.

A start-up hook cannot do this on the session's behalf: hooks run in an
isolated subprocess whose parent is an intermediate shell, not the agent, and
they cannot export variables into the session either. A hook should prompt the
agent to register rather than invent a PID.

Inspect what registered:

```sh
wb session list
wb session list --live
wb session prune
```

## Write a safe continuation

`wb session move --handover-file` and `wb session park --context-file` both
take agent-authored continuation text. Treat what you write there as already
published: it is persisted to disk, handed verbatim to a successor agent (a
different session, possibly a different harness or machine), and under the
handoff-storage plan moves into a durable Git-backed store. Nobody re-reviews
it for secrets before that happens. Write it that way, not as a scratch note
to yourself.

**A deterministic scanner also runs on every `move`/`park` before anything is
written** (gitleaks-derived named patterns: `sk_live_`, `AKIA`, `ghp_`,
`github_pat_`, `xox[baprs]-`, PEM private-key headers, and more). This
guidance lowers the odds you write a secret into a continuation in the first
place; the scanner is the actual gate. Both exist because guidance alone fails
silently — a leaked key in a continuation reads exactly like a good one — so
never rely on care alone, and never treat a scanner refusal as a bug to route
around. This scanner is the *detective* counterpart: it catches a secret that
already reached a continuation. `spec/ideas/secret-vault-injection.md` is the
*preventive* counterpart, aiming to let an agent use or set a secret without
the value ever entering its context in the first place.

Include, concretely:

- **The goal and current state.** What this effort is for, and exactly how
  far it got — not "made progress," but the specific state a successor would
  otherwise have to reconstruct from scratch.
- **Decisions made, and their reasons.** Not just "used approach X" but why X
  over the alternatives, so a successor does not silently re-litigate and
  reverse a settled call.
- **What was verified versus assumed.** Say which claims you watched pass
  (a command, a test, a CI run) and which you believe but did not check. A
  successor treats these very differently.
- **The next concrete step.** One thing to do next, phrased as an action, not
  a topic.
- **Traps and dead ends already discovered.** The approach you tried that
  looked reasonable and failed, and why — this is the single most expensive
  thing to omit, because a successor without it re-spends the time you already
  spent finding out.
- **Exact identifiers a successor needs**: branch name, PR number, task/effort
  ID, run ID, worktree path. Precise and pasteable, not "the branch I was on."

Exclude, always:

- **Credentials, tokens, keys, or anything resembling them** — even a
  half-remembered fragment, a "just for reference" example value, or a
  redacted-looking placeholder that is actually still live. If a step
  required a secret, name which one (`GITHUB_TOKEN`, `the deploy key in 1Password`)
  and how to obtain it, never the value.
- **Full file dumps.** Name the file and the relevant lines; a successor with
  repo access can read it, and a large paste is exactly where a stray secret
  hides unnoticed.
- **Raw command output.** Summarize what it showed. Raw logs and stack traces
  routinely carry tokens, internal hostnames, or other material nobody meant
  to persist.
- **The whole conversation transcript.** A continuation is a briefing, not a
  recording — see the goal/decisions/traps list above for what actually earns
  a place in it.
- **Anything the successor can cheaply re-derive from the repo itself**
  (current file contents, `git log`, `git diff`) — restate only what is not
  otherwise recoverable: judgment, reasoning, and what failed.

If the scanner refuses your continuation, it prints the matched rule, its
location, and a redacted fingerprint — never the matched value. Redact the
flagged text and retry; only reach for
`--override-secret <rule-id>:<fingerprint>` (exact key from the refusal, one
finding at a time) after confirming it is genuinely not a secret. An override
is logged as an advisory on the command's own output — it is an acknowledged
exception, never a silent bypass.

## Move a registered session over SSH

## Park and resume a registered session

`wb session park --context-file <file>` records an append-only checkpoint
containing every worktree owned by the active session. The context file may be
`-` for stdin and is read as a bounded, regular, no-follow private input. It
preserves dirty local work and does not commit, push, or remove any worktree.
Local `wb session resume <parked-session-id>` is currently fail-closed until
coordinator launch is wired; remote resume remains explicitly gated.

## Receive a parked session bundle

`wb session receive-park` accepts one bounded canonical parked-session envelope
on stdin. It authenticates the local target and returns only a durable target
receipt; private continuation is never printed. The public command is
implemented, but source resume and coordinator launch remain gated.

```sh
wb session receive-park --format json
```

Session movement is fail-closed. The invoking process must belong to a live
registered session that owns the worktree's active managed Work Log, and the
worktree must be clean on a named branch that can advance `origin` without a
force push. Supply the agent-authored continuation from a regular file (or use
`--handover-file -` with piped stdin):

```sh
wb session move --to hetzner-vm1 --handover-file handover.md
wb session move --to hetzner-vm1 --via ssh --harness claude-code \
  --handover-file handover.md
```

Omit `--harness` to continue in the source runtime. The only supported
harnesses are `codex` and `claude-code`; same-harness moves retain the source
model, while cross-harness moves use the target harness's default model.

Before checkpoint mutation, WB validates the requested harness and resolves
the configured courier. It preallocates the successor identity, generates and
commits only `.wb/handoffs/<handoff-id>.md`, pushes normally, verifies that
exact commit as the remote branch tip, and records an offer. The request
carries the validated, credential-free fetch URL; an independently configured
push URL must identify the same logical repository, and WB publishes through
the already-validated exact push URL without putting that URL in the handover.

After the checkpoint is durable, WB persists the selected SSH host and optional
remote `wb_path` as the handoff's immutable courier route. It sends the
canonical request bytes only on SSH stdin, with batch mode, no TTY, bounded
timeouts, and no request data in remote argv. An SSH failure never falls back
to another courier. The target's validated `remote.machine` must match the
request, with `tmux` and the selected harness available on its `PATH`.

Successful delivery means the target has verified the exact pinned worktree,
registered the preallocated WB successor identity, and started the selected
harness in detached tmux named
`wb-session-<successor-wb-session-id>`. The reported phase is
`successor_started`; the predecessor remains active until a later valid
receipt transfers custody.

An interrupted SSH connection is ambiguous: the target may already be live.
Use the exact handoff ID printed by WB:

```sh
wb session move --resume <handoff-id>
```

Resume does not checkpoint again. It reloads the byte-identical admitted
request and immutable courier address, so a later `wb.yaml` host/default change
cannot redirect the handoff. Do not start a fresh move to recover an ambiguous
attempt.

## Receive a pinned target checkpoint

`wb session receive` is the fixed target boundary used by couriers. Feed the
exact request bytes on stdin; do not parse and re-encode them or add a digest or
machine flag:

```sh
wb session receive --format json < exact-request.json
```

WB computes the digest from those bytes, verifies the request's target against
the local validated `remote.machine`, and replays an existing receipt when one
already exists. Otherwise it serializes execution for the handoff, verifies
the canonical clone's full remote identity (or securely clones it when
missing), fetches the declared branch directly, and requires the live tip to
equal the exact bundle commit. It then verifies source ancestry and the tracked
handover blob before creating or reusing one clean linked worktree pinned to
that commit. It then selects a fixed Codex or Claude Code argv, creates or
reuses the deterministic tmux session, registers the preallocated WB identity
against the exact pane PID, and releases that process to `exec` the harness
only after registration and PID binding are durable.

A moved branch, wrong remote, missing/non-ancestor commit, digest mismatch, or
unsafe existing worktree records an actionable failed phase before any
successor can be released. Identical retries serialize by handoff ID and reuse
the matching worktree, launch state, tmux session, and successor PID; a
completed `successor_started` replay performs no Git fetch. The receiver does
not create a receipt or change predecessor custody in this stage.

## Message a recorded successor and request handoff back

After a completed move, address the successor only by the stable WB session ID
printed in the receipt. WB resolves the immutable successor address and its
recorded SSH or Synchestra courier; there is no host, runner, tmux, or PID
override on these commands.

```sh
wb session send <successor-wb-session-id> --message-file message.txt
wb session send <successor-wb-session-id> --message-file - < message.txt
wb session request-handoff <successor-wb-session-id>
```

`send` accepts exactly one bounded `--message` or `--message-file` input. The
standard `request-handoff` message has an empty body; its typed kind and
`reply_to_wb_session_id` identify the predecessor to which control should
return. WB durably records the exact canonical JSON before courier use, and the
receiver pastes those exact typed bytes through a verified named tmux buffer.
Acknowledgement proves durable recording and paste to the recorded live pane;
it does not assert that the agent processed the input.

If delivery is ambiguous, use the exact successor and message ID printed by
WB. Resume reloads the already-durable bytes and rejects a replacement body:

```sh
wb session send <successor-wb-session-id> --resume <message-id>
wb session request-handoff <successor-wb-session-id> --resume <message-id>
```

Never start a fresh message to recover an ambiguous attempt. The target will
not automatically paste again when a durable paste intent exists without a
receipt; inspect the recorded pane and inbox before manual recovery.

`wb session receive-message` is the fixed courier boundary. It consumes exact
canonical message JSON on stdin and, for `--format json`, returns only the
canonical recorded-and-pasted receipt:

```sh
wb session receive-message --format json < exact-message.json
```

`wb session list` joins each session to the worktree owner entries recorded
under its declared PID (guarded by registration time, so an entry from a
previous holder of a recycled PID is never attributed to the new session) and
adds three columns: `EFFORTS`, `WORKTREES`, and `BRANCHES`. In text mode each
shows `-` for none, the single value (effort ID or branch name, truncated to
24 runes) when there is exactly one, or a plain count when there are several;
`WORKTREES` is always a count (or `-`). `--format json` carries the full
sorted, distinct lists for all three, untruncated. Attribution matches owner
entries by the session's declared PID, recorded at or after the session's
registration time; re-registering a session (same PID) re-stamps that start
time, so entries written before the re-registration stop counting toward its
columns.

Or for a single worktree:

```sh
wb worktree own <worktree-path> --pid <agent-pid> --runtime <harness> --model <model>
wb worktree own . --pid 12345 --runtime claude-code --model claude-sonnet-5
```

WB carries the chain forward by itself: any command that writes to a worktree
records the current identity first, appending only when custody actually
changed, so a session doing repeated work leaves one record rather than a
command trace. Writing without a declaration is allowed — the entry still
carries the WB version and command — but WB warns on stderr and the owner's
liveness stays `unknown`.

Create with explicit execution identity:

```sh
wb worktree create <task> <owner/repository> \
  --agent <session-or-agent-id> --agent-runtime <codex-or-claude> \
  --model <exact-model-or-unknown> \
  --original-prompt-file <private-prompt-file>
```

When another agent/session takes over an existing checkout, attach it before
editing so its PID and identity are preserved:

```sh
wb worktree log init <worktree-path> \
  --agent <session-or-agent-id> --agent-runtime <codex-or-claude> \
  --model <exact-model-or-unknown>
```

Inspect one worktree without exposing prompt bodies:

```sh
wb worktree info <worktree-path>
wb worktree info <worktree-path> --format json
```

`info` lists every recorded owner and evaluates each PID at read time:

- `active` — the PID currently exists (or exists but cannot be inspected).
- `orphaned` — the PID is conclusively gone.
- `unknown` — the PID was absent or liveness could not be determined.

PID liveness is a current local signal, not proof that a PID has not been
reused for another process. Treat it as triage evidence and use the Work Log,
Git state, and a handoff record to decide ownership.

Inventory globally, per task, or per repository filter:

```sh
wb worktree list
wb worktree list <task>
wb --filter <owner/repository> worktree list
wb worktree list --only active
wb worktree list --only orphaned
```

`--only active` returns worktrees with at least one live declared owner PID.
`--only orphaned` returns worktrees whose declared owners have all exited.

A worktree nobody ever declared is `unknown`, and both filters exclude it: it
needs review, not a verdict. Silence is not evidence of abandonment, and an
entry WB wrote carrying only provenance is not a dead session. These are the
same three states `wb worktree orphans` reports as live, gone, and unstated —
the two commands answer one question and always agree.

## How triage uses owner state

`wb worktree orphans` prefers a declared owner over the commit-age heuristic,
because one is proof and the other is a guess. Each row is marked
`owner live`, `owner gone`, or `owner unstated`.

| Owner state | Disposition | Basis |
|---|---|---|
| live | `active` | a declared session is running |
| gone | `decide` | its session exited, leaving unmerged work |
| unstated | falls back to commit age | inference, and the evidence says so |

A live owner outranks the age heuristic *and* the no-commit case: a session
that has not committed yet is working, not abandoned. `dirty` and `merged`
still outrank owner state — uncommitted work is most at risk exactly when its
session exits, and merged work is removable whoever owns it.

`unstated` is not the same as `gone`. Never having said who you are is not the
same as having said so and exited, and an entry carrying only WB provenance is
not a dead session. A worktree created with plain `git worktree add` is
`unstated` until someone registers, which is why the evidence for those rows
names `wb worktree own` as the fix.
