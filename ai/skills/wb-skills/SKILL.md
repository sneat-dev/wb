---
name: wb-skills
description: Install WB's own Agent Skills into this harness's skills directory (Claude Code ~/.claude/skills, Cursor ~/.cursor/skills, Codex ~/.codex/skills) and keep them current. Use at the start of any session where a wb-related skill (wb-worktrees, wb-merge, wb-hooks, wb-install, and the rest) seems to be missing, when `wb` prints a line asking to run `wb skills sync`, right after `wb self-update`, or when setting up a new machine or harness for WB.
---

# WB skills

WB ships its own agent-facing skills under `ai/skills/` in the `sneat-dev/wb`
repository. Claude Code auto-discovers them there through
`.claude-plugin/plugin.json` -- but only for a session working inside that
exact checkout. A session orchestrating any other repository, with `wb`
installed globally (Homebrew, `go install`, `wb self-update`), has never had
them at all: there is nothing to auto-discover outside a `wb` checkout. That
gap is exactly how an orchestrator missed `wb session park` entirely and
hand-rolled parking instead.

## Keep this session's skills current

```sh
wb skills sync
```

Copies every skill this exact `wb` build ships into each harness's skills
directory, one subdirectory per skill. It reads nothing from a source
checkout -- the skills are embedded in the `wb` binary itself -- so it
works from any installed `wb`, in any project, every time.

Known harnesses:

| Harness | Skills directory |
|---|---|
| `claude` | `~/.claude/skills` (or `$CLAUDE_CONFIG_DIR/skills`) |
| `cursor` | `~/.cursor/skills` |
| `codex` | `~/.codex/skills` (or `$CODEX_HOME/skills`) |

With no flags, every *present* harness is synced (its config directory
already exists). If none are present, `claude` is the fallback. Name
harnesses explicitly to set one up that is not installed yet:

```sh
wb skills sync --harness cursor
wb skills sync --harness codex
wb skills sync --harness all
```

It is idempotent: run it whenever in doubt. A second run with nothing new to
ship reports every skill `unchanged` and writes nothing. It reports
`added`/`updated`/`removed` for what changed, and `conflicts` for a directory
name it will never overwrite because something else already owns it.

`wb self-update` already runs this automatically after a successful update.
Run it directly whenever a skill mentioned in `wb` output, in another skill's
text, or in this file, does not resolve as `$wb-<name>` in this harness.

## The drift warning

`wb` compares the wb version that last ran `wb skills sync` against the
version now running, and prints one line on stderr when they disagree:

```
wb: Agent Skills in /home/user/.claude/skills were synced by wb 0.74.0, this is wb 0.75.1 -- run `wb skills sync`
```

or, when skills were never synced on this machine at all:

```
wb: Agent Skills are not installed in /home/user/.claude/skills -- run `wb skills sync`
```

Treat either line as an instruction, not background noise: run
`wb skills sync` before relying on any `$wb-*` skill's exact current
behavior.

## Session-start automation (Claude Code)

```sh
wb skills hook print
```

Prints a `SessionStart` hook snippet for `~/.claude/settings.json`. Installed
(by hand, or via `wb skills hook install`), it runs once at the start of
every session and adds two things to that session's opening context: a
reminder to run `wb session register` for this session, and the same
skills-drift warning above when it applies. `wb` never edits
`~/.claude/settings.json` on its own outside this explicit installer:

```sh
wb skills hook install          # merge the hook in; safe to re-run
wb skills hook install --dry-run  # preview the merged document first
```

The hook cannot run `wb session register` itself -- a `SessionStart` hook has
no reliable way to name the agent process's own PID on its behalf, see
`wb session register --help` -- so when its reminder appears, register the
session as instructed there before any mutating WB command.
