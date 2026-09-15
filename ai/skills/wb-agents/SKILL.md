---
name: wb-agents
description: >-
  Use WB to dispatch, track, and inspect bounded coding work handed to a
  configured agent harness running inside an isolated WB worktree. Use for
  "offload this task", "hand this to a cheaper model", "run this in a
  worktree with model X", "check on that dispatched worker", "wait for the
  worker", or when a supervisor needs to verify what a worker actually did.
  Prefer `wb agent dispatch` over hand-rolling a `codex exec` invocation, and
  `wb agent await` over polling. Never read a worker's whole transcript into a
  parent agent's context: use `wb agent await` for facts and `wb agent logs`
  only when debugging.
---

# WB agents

`wb agent` starts one configured coding harness on one bounded task inside an
isolated WB worktree, and reports execution facts about that run later. WB is
the execution layer: it never judges whether the resulting diff is correct.

## Profiles

An agent profile is a named execution configuration in WB's user config
(`$XDG_CONFIG_HOME/wb/wb.yaml`, else `~/.config/wb/wb.yaml`):

```yaml
agents:
  profiles:
    cheap-coder:
      harness: codex
      provider: deepseek
      model: deepseek-flash
      reasoning: high
```

`harness`, `provider`, and `model` are required. `model` is the provider's own
identifier and is passed through unchanged — WB keeps no model catalogue. On
the native DeepSeek provider the identifier is `deepseek-flash`, which **is**
DeepSeek V4.1 Flash; the versioned legacy spellings are retired aliases.

Provider routing is a small registry WB ships with (`deepseek`, `openrouter`)
that a project may override under `agents.providers`:

```yaml
agents:
  providers:
    my-provider:
      base_url: https://api.example.com
      credential_env: MY_PROVIDER_API_KEY
      wire_api: responses
```

Credentials are read from the environment and are never stored in
configuration, passed on a command line, or written to a run record.

## Start work

Exactly one worktree mode, and exactly one task source:

```sh
wb agent dispatch --new-worktree smoke --profile smoke --task x
```

A real invocation names the work in the task and, for a substantial prompt,
reads it from a file so shell quoting cannot mangle it:

```sh
wb agent dispatch --new-worktree cg-symbol-api --profile cheap-coder \
  --task-file /tmp/implement-symbol-api.md
wb agent dispatch --use-worktree cg-symbol-api --profile reviewer \
  --task "Review the implementation against the original request"
```

- `--new-worktree <name>` creates the checkout through the same code
  `wb worktree create` uses, including its managed-hook refresh and checkout
  marker, and requires a live registered WB session exactly as agent-mode
  worktree creation does.
- `--use-worktree <name>` resolves an existing WB-managed worktree and never
  creates one. If the name is ambiguous across repositories, pass `--repo`.
- `--task-file -` reads the task from stdin. Task bytes go to the harness on
  stdin, so they never appear in a process list.
- `--timeout` bounds the run; on expiry WB terminates the worker's whole
  process group.

Dispatch returns immediately with an agent run ID.

## Inspect

```sh
wb agent status <agent-id>
wb agent await <agent-id> --format json
wb agent list
wb agent logs <agent-id>
wb agent stop <agent-id>
```

`await` blocks until the run reaches a terminal state and then reports state,
exit status, resolved harness/provider/model, worktree, branch, changed files
with line counts, token usage, the worker's own final message, and the log
location. It exits `1` when its wait bound elapses before the run finishes, so
a script can never read "waited" as "succeeded".

States are exactly `running`, `completed`, `failed`, `timeout`, `abandoned`.
`abandoned` means the record's owner and worker are both gone with no terminal
state recorded; it is never reported as `completed`.

`logs` prints the log path and the last few worker actions. Pass `--raw` only
when you actually need the transcript — it is the one way a worker's whole run
can enter your context.

## Boundaries

- The worktree is the artefact. WB never deletes, resets, commits, pushes, or
  merges on the worker's behalf; retire worktrees with the normal WB lifecycle
  commands.
- The worker's harness is isolated: per-process provider/model configuration,
  an ephemeral session, a private per-run harness home, and an allowlisted
  environment. Your own harness configuration and session are untouched.
- WB reports facts only. `PASS` / `FAIL` / `ESCALATE` is the caller's verdict,
  not WB's.
