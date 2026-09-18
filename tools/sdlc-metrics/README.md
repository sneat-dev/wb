# sdlc-metrics

A deterministic, re-runnable extractor that turns raw agent-harness
transcripts, WB state, and GitHub PR/Actions data into a small metrics pack:
`metrics.json` (machine-readable) and `SUMMARY.md` (human-readable). It exists
so a reviewer — human or model — can read a compact pack instead of the raw
logs, to compare the cost and performance of the agent SDLC before and after
a change.

Python 3, standard library only. Streams every input file line by line; never
loads a whole transcript into memory.

## Usage

```
tools/sdlc-metrics/extract.py --since 2026-09-11 --until 2026-09-18 --out <dir>
```

The same inputs (same files on disk, same `--since`/`--until`) produce the
same output, modulo GitHub state changing between runs — raw API responses
are cached under `<out>/raw/`, so a later run can replay them with
`--offline` instead of calling `gh` again.

Flags:

| Flag | Default | Meaning |
|---|---|---|
| `--since` | required | `YYYY-MM-DD`, inclusive, UTC start of day |
| `--until` | required | `YYYY-MM-DD`, inclusive, UTC end of day |
| `--out` | required | output directory for `metrics.json`, `SUMMARY.md`, `raw/` |
| `--home` | `$HOME` | directory to scan under (`<home>/.claude`, `<home>/projects/.wb`) |
| `--repo` | `sneat-dev/wb` | `owner/repo` for GitHub PR and Actions data |
| `--git-repo-path` | `<home>/projects/<repo>` | local clone for `git log` counts |
| `--wb-state-dir` | both `<home>/.wb` and `<home>/projects/.wb` | WB state directory (`worklogs/`, `waits/`); repeatable, replaces the default pair when given |
| `--price-config` | `$SDLC_METRICS_PRICE_CONFIG` or `~/.config/sdlc-metrics/prices.json` if present | JSON file of per-model price overrides (see "Tokens and cost") |
| `--offline` | off | reuse cached `raw/*.json` instead of calling `gh` |

Nothing here is machine-specific: every path defaults from `$HOME` /
`--home`. Most flags can also be set with an `SDLC_METRICS_*` environment
variable of the same name (`--home`, `--repo`, `--git-repo-path`,
`--price-config`; see `--help`) — `--wb-state-dir` and `--offline` do not
have environment-variable equivalents and must be passed as flags.

**Reports belong outside this repository.** Never commit the output of a run;
point `--out` at a scratch or reports directory (for example
`~/.wb/reports/<name>/`).

## What it reads

1. **Claude Code session transcripts**: `<home>/.claude/projects/*/*.jsonl`
   (one file per top-level session) plus subagent transcripts, found in two
   places and de-duplicated by agent id:
   - `<home>/.claude/projects/*/<session>/subagents/agent-<id>.jsonl` (durable);
   - `/tmp/claude-*/*/*/tasks/<id>.output` (ephemeral, same JSONL shape; only
     used for an agent id not already covered by the durable copy).
   Per assistant message: `message.usage` (input/output/cache tokens),
   `message.model`, `timestamp`, `tool_use` blocks, `isSidechain`. Per
   `system` record with `subtype: turn_duration`: one main-loop turn.
   Compactions are `message.isCompactSummary: true`.
2. **WB state**: `<wb-state-dir>/worklogs/*/outbox/*.json` (`worktree.claimed`
   / `worktree.sealed` events — landing outcome and duration) and
   `<wb-state-dir>/waits/*.json` (a snapshot of open waits, not a history).
3. **GitHub**, for `--repo`: `gh api repos/<repo>/pulls` and
   `repos/<repo>/actions/runs`, both paginated and cached under `raw/`.
4. **`git log --since/--until`** in `--git-repo-path`: commit, merge-commit
   and `#<issue>` reference counts only. No diffs, no commit messages beyond
   the reference-number scan.

## Metric definitions

- **Tokens and cost** (`tokens`): priced per message, from each assistant
  message's own `usage` object (or, when present, the sum of its
  `usage.iterations` — never both), then split by day, model family
  (`opus` / `sonnet` / `haiku` / `fable` / `other`, matched by substring
  against the raw model string) and role (`main` loop vs. `subagent`) for
  reporting. `estimated_usd` looks up `PRICE_TABLE_BY_PREFIX` at the top of
  `extract.py` by the message's own model-id prefix (most specific first,
  e.g. `claude-fable-5-1` before `claude-fable-5`), falling back to a coarse
  family bucket, and to a labeled "legacy, verify" rate for an older
  generation (Sonnet 4.x, Opus 4.x) it doesn't recognize. Edit that table
  when prices change; `metrics.json` records the `"prices as of"` date the
  table was built from. It is a hand-maintained estimate, not
  billing-accurate. Override any rate with `--price-config <file.json>`.
- **Orchestrator** (`orchestrator`): turns = count of `turn_duration` system
  events in the main-loop transcript; context size per turn = `input_tokens +
  cache_read_input_tokens + cache_creation_input_tokens` on each assistant
  message (a proxy for context-window occupancy, not the true window size);
  `context_growth_by_session` compares the first and last measured turn per
  session (`growth_ratio`) as the quadratic-cost signal; compactions counted
  via `message.isCompactSummary` (the compacted summary message) and via a
  `system` record with `subtype: compact_boundary` (the marker the harness
  writes when a compaction occurs) — both are counted, since either can be
  present without the other.
- **Subagents** (`subagents`): one `AgentStats` per discovered agent
  transcript. Duration = last usage timestamp minus first, within the window.
  Dispatch = an `Agent` tool_use in a main-loop (or parent-agent) transcript;
  resume = a `SendMessage` tool_use whose input has a `to` field. A **stall**
  is a gap over 5 minutes between an agent's own assistant messages while its
  transcript was still being written; it is separately flagged when the prior
  message's text matched `waiting for (a )?notification`.
- **Tool calls** (`tools`): every `tool_use`/`tool_result` pair, grouped by
  `(role, model family, tool name)`. Result size is the character length of
  the tool_result content (never the content itself); `estimated_total_tokens`
  divides by 4 as a rough English-text heuristic, not a real tokenizer count.
  Error rate reads `tool_result.is_error`; "denied" additionally matches a
  short list of permission-refusal phrases in the (transient, never stored)
  result text. Repeated identical calls are detected by hashing a
  privacy-safe signature of the tool input (the Bash command reduced to
  verb+flags, or a sorted-key JSON dump for other tools) per session — the
  raw input is never written out, only a 16-hex-character digest.
- **Bash, in depth** (`bash`): `by_subverb` groups `wb`, `git`, `gh` and `go`
  commands by their first one-to-three tokens (e.g. `wb pr land`, `git push`,
  `gh run list`, `go test`). A command line is split on `&&`/`||`/`;`/`|`
  into clauses first, and each clause is classified on its own (so a leading
  `cd dir &&` no longer hides the real command). CPU-heavy commands are
  `go test|build|vet`, `golangci-lint`, and `npm|pnpm|bun(x)|yarn (run
  )?build|test`; `wb run` coverage is the share of CPU-heavy clauses that
  ARE prefixed by `wb run --` within that same clause (each clause is
  checked for its own wrapping, not the whole command line), reported split
  by `main` vs. `subagent` role. A hand-rolled loop is `until`/`while`
  combined with `sleep`, a bare
  `kill -0` watch, or a `gh run list` / `gh api ...runs` / `gh pr checks`
  poll. Pipe truncation checks whether any pipeline segment after the first
  starts with `tail` or `head`. Multi-call sequences are 2–4-gram windows
  over each session's ordered, coarsely-classified Bash calls, kept only when
  a sequence recurs (count >= 2) across the window.
- **Pipeline and CI** (`pipeline_and_ci`): PR time-to-merge from
  `created_at`/`merged_at`; Actions run duration from `run_started_at` to
  `updated_at`; wasted minutes = duration of `cancelled` + `failure`
  conclusions. Per-PR failure-round attribution (lint vs. test vs. review) is
  **not** computed — see Known blind spots.
- **WB state** (`wb_state`): landing duration = `worktree.sealed.at` minus
  the matching `worktree.claimed.at` for the same `claim_id`; disposition
  breakdown from `worktree.sealed.disposition`.
- **`unavailable`**: every metric the brief asked for that this pass could
  not compute, with a one-line reason. This list is itself an input to the
  logging-gap analysis, not an apology — read it.

## Additional sections

- **`tools`** and **`bash`**: per-(role, model, tool) call counts, error/denial
  rates, result-size percentiles, repeated-call detection, Bash subverb
  breakdown (`wb pr land`, `git push`, `gh run list`, `go test`, ...),
  `wb run` coverage of CPU-heavy commands, hand-rolled polling loops,
  pipe-truncation, and the most frequent 2-4-call Bash sequences.
- **`adoption`**: direct `specscore`/`codegrapher` CLI, skill, and MCP use,
  plus heuristic "probable substitute" counts -- generic grep/Read calls
  classified `symbol-like`/`regex`/`text` (never the pattern itself), and
  spec-path reads classified by directory (`spec/rules`, `spec/ideas`, ...,
  never the full path) -- and a **current-state** CodeGrapher index check
  for `--git-repo-path`.
- **`codex`**: Codex CLI rollout token totals, kept as a separate harness,
  not merged into the Claude Code token tables.
- Usage is de-duplicated by `requestId` (falling back to `message.id`):
  one API response can appear on several JSONL lines, each repeating the
  same `usage` object, and counting every line overstates tokens.
- Cache-write tokens are priced separately for the 5-minute and 1-hour
  ephemeral kinds (`cache_creation.ephemeral_5m_input_tokens` /
  `ephemeral_1h_input_tokens`); a `usage` object without that split is
  assumed 1h, since that is what this harness's orchestrator writes.
- WB state is read from **both** `<home>/.wb` and `<home>/projects/.wb`
  by default (a fleet may have used either, or both, over time) and
  de-duplicated by `(type, claim_id, run_id)`. Surviving `wb run` command
  receipts (`<worktree>/.wb/local/run/events.jsonl`) and git-hook events
  (`<home>/.local/state/wb/hook-events.jsonl`) are also read; the former
  is a lower bound, since a worktree's receipts are deleted with it.

## Known blind spots

- **`wb report` does not exist.** The installed `wb` CLI has no `report`
  command group; `worklogs`/`waits` files are read directly instead. If a
  `wb report stream|fleet --format json` verb ships later, prefer it.
- **`wb run` admission/queue history is not persisted.** Only the live queue
  state exists; this pass does not query it (and avoids adding load to it).
- **No structured WB refusal-code taxonomy.** Worklog events carry a
  `disposition` string, not a stable refusal-code enum.
- **Per-PR lint/test/review failure-round counts are not inferred.** GitHub's
  Actions API gives run-level `conclusion` only; attributing a failed run to
  a specific cause needs job/step logs, which this pass deliberately does not
  fetch, to stay lightweight and metrics-only.
- **Daily token totals are not split exactly by model** when one session used
  more than one model on the same day; such a day is reported under a
  synthetic `"mixed"` model key rather than an exact per-model split.
- **Tool-result size is a character/4 token estimate**, not a real tokenizer
  count. Token *counts* (from `usage`) are exact; token-equivalents derived
  from result size are directional only.
- **Duration proxies** (Bash wall-clock, hand-rolled-loop duration) come from
  `tool_result.timestamp - tool_use.timestamp` within the same transcript.
  A tool_use with no matching tool_result in the window has no duration
  signal and is simply not counted.
- **`/tmp` subagent transcripts are ephemeral.** If `/tmp` has been cleaned
  since a run, that agent's tool-call/Bash detail is lost even though its
  durable `usage` totals (if any reached `~/.claude/projects/.../subagents/`)
  survive.

## Privacy

The pack is metrics only:

- no prompt or response text;
- no file contents;
- no email addresses or secrets.

Command lines are reduced to their leading verb and first few flags
(`reduce_command`): every absolute-ish path is replaced with `<path>`, every
`--flag=value` becomes `--flag=<val>`, and every quoted string is replaced
with `<str>` before anything is counted. Repeated-call detection hashes that
reduced string (or a sorted-key JSON shape for non-Bash tools) to a 16-hex
digest — the input itself is never written to the pack.

Any filesystem path that reaches `metrics.json` or `SUMMARY.md` (error
messages, checked-repo paths, WB state-dir paths) has its `$HOME` prefix
replaced with `~` first, so it never carries the machine's username.

**`<out>/raw/` is local-only and must not be shared or committed.** It caches
the GitHub API responses this run fetched, trimmed to the fields the
extractor actually reads (PR number and lifecycle timestamps/state for
`pulls.json`; run id, name, event, conclusion, timestamps and `head_sha` for
`actions_runs.json` — never the full PR body or a commit author's email).
That trimming keeps casual disclosure out, but `raw/` is still a cache of
GitHub data scoped to one run, not a publishable artifact: treat the whole
`<out>/` directory, including `raw/`, as private to the person who ran the
extractor, the same as `metrics.json` and `SUMMARY.md`.

Verb/subverb classification (the "top verbs" and `by_subverb` counts) uses a
separate, stricter path: the command is first tokenised with `shlex.split`
(falling back to `<unparsed>` if it can't be lexed), heredoc bodies and
quoted-string arguments are stripped before any word is inspected, and only
a verb on a fixed allowlist of known commands (`git`, `gh`, `go`, `wb`,
`specscore`, `codegrapher`, `grep`, `rg`, `find`, `cat`, `sed`, `ls`,
`python3`, `node`, `pnpm`, `npm`, `bun`, `make`, `jq`, `curl`, ...) is ever
reported by name; anything else is bucketed as `<other>`. This is what keeps
words from inside a quoted commit message or `BODY='...'`/`C='...'` argument
(e.g. `git commit -m "..."`) out of the verb counts. Session and agent
IDs may appear (they are opaque identifiers, not personal data).

## Tests

```
python3 -m unittest
```

`test_extract.py` uses only fabricated JSONL fixtures built inline in the
test file — it never reads real transcripts — and points the `/tmp`
subagent-scan at an empty directory so results are hermetic. It covers
token aggregation by model, stall detection, the privacy reduction of
command lines, Bash verb/subverb classification, and `unavailable`
reporting.
