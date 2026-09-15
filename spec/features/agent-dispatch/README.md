---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Agent Dispatch

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/agent-dispatch?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/agent-dispatch?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/agent-dispatch?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/agent-dispatch?op=request-change) |
**Status:** Draft
**Source Ideas:** —
**Depends On:** [Worktree Lifecycle](../worktree-lifecycle/README.md) (reuses worktree creation and resolution)

## Summary

`wb agent dispatch` starts one configured coding-agent harness on one bounded
task inside an isolated WB worktree, returns immediately with a stable agent
run ID, and leaves the harness running detached. `wb agent status` and
`wb agent await` report execution facts — state, exit status, resolved
profile, worktree, branch, changed files, token usage, the worker's own final
message, and the on-demand log location — without pushing the worker's
transcript into the caller's context.

An agent profile is a named execution configuration
(`harness` + `provider` + `model` + optional `reasoning`). Each run records the
*resolved* configuration, so editing a profile later never rewrites what a
finished run actually executed.

The feature exists so a strong parent agent (Codex Astra, Claude Opus) can
delegate a bounded coding task through a cheap native supervisor subagent to a
cheaper worker model and receive a compact, trustworthy result. DeepSeek-backed
Codex is the first provider proven this way; nothing DeepSeek-specific may
leak into the run, status, or dispatch code paths.

Any command in the family may also address **another configured machine** over
SSH (`--to <machine>`, or a `machine:agent-id` reference). The remote machine
performs the same dispatch locally, so its record and the worktree it produces
stay on the machine that owns them, and this machine keeps no mirror that could
go stale.

## Problem

WB already isolates workspaces (worktrees) and records agent sessions, and it
already starts interactive `codex`/`claude` harnesses for session handoff. What
it does not have is a *bounded, resumable* execution handle for one coding
task: something a separate process can `status` and `await` after the
dispatching process is gone, holding a declared and snapshotted execution
configuration, and running an isolated child harness.

Delegating such a task today means the parent agent either does the work in its
own context — spending frontier-model tokens on a mechanical change — or
hand-rolls a `codex exec` invocation with provider flags, a log file, a process
group, and a timeout, none of which is recorded anywhere afterwards.

WB must stay the deterministic execution layer. Judging whether the worker's
diff satisfies the task is a semantic decision and belongs to the supervisor
above it, not to WB.

## Behavior

### Three separated layers

The division of responsibility is part of the contract, not an implementation
detail.

#### REQ: supervisor-supervises-wb-executes

WB MUST implement exactly: profile resolution, worktree creation or
resolution, harness launch, process and run tracking, and result/status
reporting. WB MUST NOT implement semantic LLM review, acceptance judging, or a
`PASS`/`FAIL`/`ESCALATE` verdict on dispatched work. No WB output field may
express a judgement about whether a diff is correct.

### Agent profiles

#### REQ: profile-is-an-execution-configuration

An agent profile MUST be a named agent execution configuration. The profile
schema is exactly:

```yaml
agents:
  profiles:
    deepseek-v4-1-flash:
      harness: codex
      provider: deepseek
      model: deepseek-flash
      reasoning: high
```

`harness`, `provider`, and `model` MUST be required; `reasoning` MUST be
optional. No profile field outside this set may be required for dispatch, and
fields such as `skills`, `resources`, `cpu`, or `memory` MUST NOT be introduced:
capability matching and resource declaration have no concrete need here.

#### REQ: model-is-provider-scoped-and-passes-through-verbatim

A profile's `model` names a model **within its provider's catalogue**, so the
same underlying model has a different identifier per provider and WB MUST NOT
normalise, alias, or rewrite it. WB MUST pass the configured value through to
the harness unchanged. WB MUST NOT carry a model catalogue, a model alias map,
or a per-model capability table: it cannot know a provider's catalogue, and a
catalogue would make every model release a WB release.

WB MUST NOT validate `reasoning` against a hardcoded level list for the same
reason — the levels are model-specific and drift. It MUST validate only that
the value is a non-empty safe execution identifier and pass it through; the
harness and provider remain the authority on which levels exist.

The built-in providers and their corresponding Flash identifiers, as verified
against the providers' own catalogues, are:

| provider | base URL | model for DeepSeek V4.1 Flash | credential env var |
|---|---|---|---|
| `deepseek` | `https://api.deepseek.com` | `deepseek-flash` | `DEEPSEEK_API_KEY` |
| `openrouter` | `https://openrouter.ai/api/v1` | `deepseek/deepseek-v4.1-flash` | `OPENROUTER_API_KEY` |

Two consequences MUST be preserved by any change to these defaults:

- On the native provider, `deepseek-flash` **is** DeepSeek-V4.1-Flash; the
  versioned legacy names are retired aliases that the provider still serves
  from the V4.1-Flash model. A profile MUST NOT use an identifier the provider
  does not accept — the native provider rejects a dotted version form such as
  `deepseek-v4.1-flash`.
- On OpenRouter, the unversioned Flash identifier resolves to an **older**
  snapshot than `deepseek/deepseek-v4.1-flash`. Where a specific Flash version
  matters, the versioned identifier is the one that pins it.

#### REQ: profile-configuration-lives-in-existing-wb-config

Profiles MUST live in WB's existing user configuration file
(`$XDG_CONFIG_HOME/wb/wb.yaml`, otherwise `~/.config/wb/wb.yaml`), under an
`agents` mapping resolved through the existing `internal/wbconfig` path
resolution. The feature MUST NOT introduce a second configuration hierarchy, a
per-repository profile layer, profile inheritance, templates, or override
rules.

#### REQ: profile-required-and-resolved

`wb agent dispatch` MUST require `--profile`. A missing or unknown profile
name MUST be refused as a usage-class error, before any worktree is created or
any process is started, naming the requested profile and the configuration
file consulted. Resolution MUST fail closed when a required field is missing or
carries an unsupported value, and MUST reject a `model` or `provider` value
that is not a non-secret execution identifier, reusing WB's existing
execution-identifier validation.

#### REQ: no-credentials-in-profile-configuration

Profile configuration MUST NOT carry credentials. A profile references a
provider by name, and the provider names a credential *source*: either the
environment variable holding it (`credential_env`) or an absolute path to a
private file holding it (`credential_file`). Naming both MUST be refused rather
than silently resolved, and naming neither MUST be refused. The credential value
is read at launch time only and is never persisted.

#### REQ: credential-file-is-private-or-refused

A file-sourced credential MUST follow WB's existing credential-file convention:
a path in configuration, the secret in a private file under the WB configuration
directory. WB MUST refuse a credential file that does not exist, is not a
regular file, is a symlink, is empty, or is readable by group or others, and it
MUST name the file and the remedy in that refusal. A file-sourced credential
MUST be injected into the harness under one name WB controls, so it does not
depend on anything the machine happens to export — which is what makes remote
dispatch work on a machine reachable only over non-interactive SSH.

### Harness, provider, and model

#### REQ: harness-provider-model-separation

The resolved configuration MUST keep three distinct concepts:

```text
agent harness  →  inference provider  →  model
```

The harness supplies the coding-agent environment and tool loop; the provider
and model supply inference. WB MUST NOT implement its own coding-agent loop,
and MUST NOT collapse provider or model into the harness name. The MVP harness
set is exactly `codex`; an unresolvable harness MUST be refused with an
actionable message rather than silently substituted.

#### REQ: provider-registry-is-small-and-closed

Provider routing MUST come from a small registry keyed by provider name whose
entries carry exactly: `base_url`, `credential_env`, and `wire_api`. `wire_api`
MUST be a closed enum of the wire protocols the Codex harness supports. The
registry MUST have built-in entries for the proven providers so the common case
needs no provider configuration, and a user MUST be able to override or add an
entry in the same `agents` mapping without a WB code change. Provider
conditionals MUST NOT be spread through dispatch, status, or run-record code.
The registry MUST NOT grow beyond this: no model catalogue, no capability
negotiation, no plugin loading.

#### REQ: reasoning-reaches-the-harness

A configured `reasoning` value MUST be passed to the Codex harness as that
harness's reasoning-effort configuration, which MUST be delivered per process
and not by writing any file the harness reads globally. When `reasoning` is
absent, WB MUST NOT pass a reasoning override, leaving the harness default.

#### REQ: no-global-harness-configuration-mutation

Dispatching a worker MUST NOT read, write, or otherwise depend on mutating the
user's global harness configuration. The child harness MUST receive its
provider, model, credential reference, sandbox policy, and reasoning per
process, and MUST run with a private per-run harness home so it neither reads
nor writes the user's harness state. It MUST run ephemerally so it cannot join,
resume, or disturb any parent harness session or daemon. The parent agent's
provider/model session MUST remain usable while a worker runs, and the user's
global harness configuration file MUST be byte-identical after a dispatch.

### Dispatch surface and repository selection

#### REQ: dispatch-starts-execution-now

`dispatch` MUST mean *start execution now*. It MUST NOT enqueue, defer, or
schedule. It MUST NOT be implemented as "submit and maybe execute later"; the
queued future form (`submit` → queue → dispatch) is explicitly out of scope.

#### REQ: repository-selection-is-explicit

Dispatch MUST operate on exactly one repository. The repository MUST be
resolved from the invoking checkout's origin when not supplied, reusing WB's
existing origin-slug resolution, and MAY be overridden by an explicit
repository argument. Projects root MUST come from WB's existing persistent
`--projects-root` flag rather than a new selector. WB MUST refuse, naming the
missing prerequisite, when no canonical clone of the resolved repository exists
under the projects root.

#### REQ: dispatch-returns-before-completion

`dispatch` MUST return as soon as the run is admitted and the worker has been
started, without waiting for the worker to finish, and without requiring the
invoking process to remain alive or attached to the worker's stdout. The
returned run MUST be identifiable by a stable agent ID that a separate process
can pass to `status` and `await`.

#### REQ: dispatch-task-input

Dispatch MUST accept the task inline with `--task <text>` and from a file with
`--task-file <path>`. Exactly one of the two MUST be required. `--task-file -`
MUST read the task from standard input. Task bytes MUST reach the harness
without appearing on the worker's command line or in WB's own command line, and
MUST NOT be echoed into normal command output.

### Worktree selection

The two modes have materially different intent and MUST stay explicit.

#### REQ: exactly-one-worktree-mode

`wb agent dispatch` MUST accept exactly one of `--new-worktree <name>` or
`--use-worktree <name>`. Supplying neither, or both, MUST be refused as a
usage-class error before any mutation or launch.

#### REQ: new-worktree-reuses-wb-worktree-creation

`--new-worktree` MUST create the checkout by calling WB's existing worktree
creation service, `internal/worktrees.Create`, in process — never by shelling
out to `wb worktree create`, and never by a second implementation of branch or
worktree semantics inside the agent code. Dispatch MUST therefore supply what
that service actually requires: the projects root, a safe operation name, one
or more owner/repository coordinates with an existing canonical clone, and Work
Log options carrying the resolved model and provider plus the exact task bytes
captured in memory as the private original prompt.

Branch naming, base selection, and their configured defaults MUST remain the
worktree service's own policy. Dispatch MUST NOT reimplement them; it MAY pass
an explicit branch or base through when the caller supplies one, exactly as
`wb worktree create` does. WB MUST NOT reuse an existing branch or worktree
without an explicit resume request.

#### REQ: new-worktree-reuses-the-create-side-effects

The steps `wb worktree create` performs around the creation service — the
managed-hook refresh before creation and the checkout marker afterwards — MUST
be reused rather than skipped, so an agent-created worktree is
indistinguishable from a manually created one. Reuse MUST be by extracting the
existing logic into a shared helper, not by copying it.

#### REQ: new-worktree-requires-a-live-session

`--new-worktree` MUST run under WB's existing agent-mode admission: the
invoking process MUST belong to a live registered WB session, and dispatch MUST
refuse with the existing actionable registration message when it does not. The
feature MUST NOT weaken or bypass that gate, and MUST NOT silently fall back to
synthesizing a session.

#### REQ: new-worktree-conflict-refuses

When the requested worktree name conflicts with an existing worktree or branch
under current WB worktree rules, dispatch MUST fail with that creation error
and MUST start no worker. It MUST NOT reuse, replace, or adopt the conflicting
checkout.

#### REQ: use-worktree-resolves-only

`--use-worktree` MUST resolve an existing WB-managed worktree by name and MUST
NOT create one implicitly. It MUST reuse WB's existing worktree inventory
rather than computing a path independently. When the name resolves to zero
worktrees, dispatch MUST fail with an actionable message naming what was
searched. When the name resolves to more than one worktree — a task spanning
several repositories, or a shared worktree root — dispatch MUST refuse and name
the candidates rather than choosing one silently.

### Run identity and persistence

#### REQ: stable-agent-id

Every dispatch MUST mint a stable agent ID following WB's existing
random-suffix convention: a short lowercase type prefix for the agent run and a
random hexadecimal suffix, unique per run. The ID MUST NOT be derived from a
PID, worktree name, or timestamp.

#### REQ: durable-run-record

Each run MUST persist, under WB's resolved home directory, one private record
containing at least: agent ID; requested profile name; the original task;
repository; worktree name, mode (new versus existing), directory, branch, base,
and base revision where relevant; start and finish times; state; resolved
harness, provider, model, and reasoning; worker process identity; exit status;
usage counters when the harness reports them; the worker's final message when
the harness supplies one; and the log location. The record MUST be readable
without any live process, MUST be written atomically, and MUST be created
before the worker starts so a failed launch still leaves a diagnosable record.

#### REQ: run-record-references-the-work-log-claim

When `--new-worktree` publishes a Work Log claim, the run record MUST reference
that claim, and the resolved harness, provider, and model MUST be recorded in
the claim through the worktree service's existing execution-identity fields.
The feature MUST NOT publish a second, unreferenced claim for the same run.

#### REQ: resolved-profile-snapshot

Dispatch MUST persist the *resolved* execution configuration alongside the
requested profile name. Later edits to the profile configuration MUST NOT
change the recorded resolved configuration of an existing run.

#### REQ: closed-run-state-vocabulary

The persisted run state MUST be exactly one of: `running`, `completed`,
`failed`, `timeout`, `abandoned`. `completed`, `failed`, `timeout`, and
`abandoned` are terminal; `running` is the only non-terminal state. `completed`,
`failed`, and `timeout` MUST be written by the run owner — no other process may
claim a run finished. `abandoned` is a *derived* projection rather than a
persisted transition: it is the honest rendering of a run that is persisted as
`running` and whose owner is gone, and it MUST be documented as such so a reader
of the raw record is not misled.

#### REQ: abandoned-runs-are-never-reported-completed

A run for which no terminal state was written and whose recorded owner is gone
MUST be reported as `abandoned` — never as `completed`, and never as `running`
indefinitely. The owner is the deciding process, because it is the only one that
will ever write a terminal state; a worker that outlived its owner MUST still be
surfaced separately so a caller knows a stray process exists and can stop it.

Detecting this MUST rely on liveness evidence WB already has (recorded process
identity), not on a timeout heuristic that would misreport a slow worker. Exactly
one bounded exception is permitted: a run persisted milliseconds ago whose owner
has not yet recorded itself is *being admitted*, and MUST be reported as
`running` for a short, documented admission window. That window can never
mislabel a slow worker, because a slow worker still has a live owner.

Owner liveness MUST be evaluated through WB's single process-liveness
implementation, so every platform answers the same question the same way.

### The detached run owner

#### REQ: detached-execution-owner-reuses-wb-self-exec

Because the dispatching process is short-lived, the run MUST have a detached
owner that outlives it and records the worker's terminal state, exit status,
and finish time. That owner MUST be a new private internal WB command built on
WB's existing self-exec convention — a private command-line argument handled in
`cmd/wb/main.go` before normal command dispatch — and MUST be detached using
WB's existing detached-worker pattern: a new session for the child, standard
streams redirected away from the caller, and the process released rather than
waited on. The feature MUST NOT add a daemon and MUST NOT depend on
`wb daemon` or `wb worker` queue machinery.

#### REQ: dispatch-timeout-is-bounded

A run MUST carry a bounded timeout: a non-positive timeout MUST be refused
rather than interpreted as "unbounded", because a run nobody is waiting for is
how a stuck worker becomes a stray process. On expiry the owner MUST terminate
the worker's whole process group — including descendants that ignore a graceful
signal — using WB's existing process-tree cancellation helper, mark the run
terminal with the `timeout` state, record the finish time, and retain the log
for diagnosis. The bound MUST be selectable and MUST have a positive default.
Timeout enforcement MUST NOT be left to the harness.

Because the owner is what enforces the bound, an owner that is killed while its
worker keeps running voids it. WB MUST therefore report such a run as
`abandoned` with the stray worker surfaced, so the bound can be re-established
by stopping it explicitly, rather than pretending the run is still bounded.

### Harness launch contract

#### REQ: harness-launch-is-per-process-and-ephemeral

The Codex harness MUST be launched as a non-interactive child process with an
ephemeral session so no session file is persisted, with the user's global
configuration not loaded, with a private per-run harness home, with the run's
worktree as the working root, and with the workspace-write sandbox and no
interactive approval prompts. Provider, model, credential variable *name*, and
reasoning MUST be supplied as per-process configuration. The exact flag and key
spellings are the harness's contract and MUST be isolated in one place so a
harness version change is a one-file change.

A non-fatal harness diagnostic about missing custom-model catalogue metadata is
expected on this path and MUST be recorded in the run log like any other
harness output rather than hidden or surfaced as a run failure.

#### REQ: task-reaches-the-harness-on-stdin

The task bytes MUST be delivered to the harness on standard input, not as a
command-line argument, so the task never appears in the process table.

#### REQ: harness-executable-resolution-is-substitutable

The harness executable MUST be resolved by name through the process `PATH` so
that a deterministic test can substitute a fake harness without a production
override flag or a special test hook. A missing executable MUST produce an
actionable run failure naming the harness and the looked-up name.

#### REQ: harness-output-is-captured-as-a-log-not-a-result

The harness's structured event stream MUST be captured to a private per-run log
file and MUST NOT be re-emitted on any routine result path. The log MUST be
readable by a later process and MUST be bounded against unbounded growth.

#### REQ: usage-and-result-metadata-come-from-the-harness

Token usage, tool-call activity, and the worker's final message MUST be derived
from the harness's own structured output and its last-message channel when it
supplies them. A field the harness does not report MUST be absent from the run
record and from output, and MUST NOT be estimated, defaulted to zero, or
invented. Reported usage MUST be recorded as the harness reported it for the
token fields WB models; a harness field WB does not model is not part of this
contract and MAY be dropped.

### Status, await, and logs

#### REQ: status-reports-execution-facts

`wb agent status <agent-id>` MUST report, from the durable record: state,
requested profile, resolved harness, provider, model and reasoning, worktree
and branch, worker process status, start and finish timestamps, exit status,
and concise result metadata. An unknown agent ID MUST be a findings-class error
naming the ID, because the invocation was admitted and then found nothing —
not a usage error.

#### REQ: await-blocks-efficiently

`wb agent await <agent-id>` MUST block until the run reaches a terminal state
or a caller-supplied bound elapses, and then report at least: terminal state,
exit status, worktree, branch, a changed-files/diff summary where practical,
the worker's own final message when the harness supplied one, test evidence
where reliably extractable, token usage where the harness reported it, and the
location of the full log. `await` MUST NOT require an LLM or an agent to poll
`status` in a loop, MUST NOT busy-spin, and MUST resolve `abandoned` runs rather
than blocking on them. When the caller's bound elapses before a terminal state,
`await` MUST report the still-non-terminal state truthfully and MUST NOT claim
success.

#### REQ: concise-result-not-transcript

Neither `status`, `await`, nor dispatch output MAY include the worker's raw
transcript by default. The worker's final message MAY be included because the
harness reports it as a bounded single message, and it MUST be presented as the
worker's own statement — never as a WB verdict.

#### REQ: structured-output

`status` and `await` MUST support WB's standard machine-readable output
selector (`--format json`, with `--json` as the exact shortcut) in addition to
text, and MUST emit identical field names and values in both.

#### REQ: transcript-available-on-demand-only

The complete worker transcript MUST be preserved for debugging and MUST be
reachable through an explicit logs surface that identifies its location and
supports bounded inspection. Routine result paths MUST NOT embed it.

### Remote dispatch over SSH

A machine is a first-class dimension of the same command family, not a second
feature. Everything above still holds on the machine that runs the work; this
section governs only how a caller reaches it.

#### REQ: remote-machine-resolution-reuses-wb-machine-map

A remote machine MUST be resolved from WB's existing configured machine map
(`session_move.targets`, keyed by machine name, with its `ssh.host`, `ssh.user`,
and `ssh.wb_path`). The feature MUST NOT introduce a second host list: a machine
and its courier address are one fact about the fleet, and two lists are two
places to be wrong. An unconfigured machine name MUST be refused as a
usage-class error naming the machines that are configured.

#### REQ: remote-request-travels-on-stdin

The request MUST travel to the other machine as exact bytes on standard input.
It MUST NOT be interpolated into the remote command line, because OpenSSH joins
the remote arguments into one string that the remote login shell then parses;
the remote command line MUST consist only of fixed constants and validated
configuration. Every part of the request that originates with the caller — the
task above all — therefore never appears in a process table on either machine.

#### REQ: remote-request-is-validated-identically

The receiving machine MUST validate the request with the same constraints it
applies to its own command line, so a hand-written or hostile request cannot
reach a state a local invocation could not. The request MUST carry an explicit
schema version and MUST be refused when the version is unsupported, so a
version skew fails as a clear refusal rather than as a misread document.

#### REQ: remote-records-stay-on-the-owning-machine

A remote run's record, log, harness home, and worktree MUST live on the machine
that ran it. This machine MUST NOT keep a local copy of a remote run, because a
local mirror can only go stale; a remote result MUST instead be labelled with
the machine that owns it, and a caller MUST be able to name that machine again
with either an explicit option or a machine-qualified reference.

#### REQ: remote-refusal-is-not-a-transport-failure

A refusal decided on the other machine MUST reach the caller as a structured
refusal carrying that machine's own message, distinguishable from a transport
failure. An unreachable machine, a dropped connection, a target too old to
understand the request, and an oversized answer MUST each be reported as such,
quoting a bounded, sanitised remote diagnostic rather than raw remote bytes.

#### REQ: remote-await-holds-one-connection

Waiting on another machine MUST occupy one connection for the whole wait rather
than one call per polling interval: the remote WB performs the waiting. The
transport bound MUST outlast the caller's wait bound, so a slow but healthy
worker cannot be mistaken for a dropped connection, and an elapsed wait bound
MUST still be reported as a non-terminal outcome rather than as success.

#### REQ: local-vocabulary-is-authoritative-over-the-wire

The receiving machine's answer supplies a state; whether that state is terminal
MUST be re-derived locally from this WB's closed vocabulary rather than trusted
from the wire, so a version skew can never make a finished run read as pending
or a pending run read as finished.

#### REQ: remote-credentials-are-not-forwarded

Dispatching to another machine MUST NOT copy this machine's provider
credentials to it. Each machine authenticates with its own credential, and a
machine that lacks one MUST fail with its own actionable message naming the
variable.

#### REQ: remote-transcript-is-bounded-and-refused-not-truncated

Fetching another machine's transcript MUST be bounded. A transcript larger than
the bound MUST be refused with an instruction to read it on that machine, rather
than silently truncated: a truncated transcript is a misleading transcript.

### Security boundaries

#### REQ: no-secret-in-argv-logs-or-records

Credentials MUST NOT appear in the worker's command line, in WB's own command
line, in any log WB writes, or in any persisted run record. Only the *name* of
the credential environment variable may be passed to the harness.

#### REQ: filtered-child-environment

The worker's environment MUST be built from WB's existing detached-worker
environment convention — an explicit allowlist of the variables a worker
genuinely needs, not the whole ambient environment — and the credential the
resolved provider names MUST then be re-added explicitly. Ambient
agent-identity variables MUST NOT be inherited. A new whole-environment
sensitive-name deny-list MUST NOT be invented when an allowlist already
achieves the same guarantee.

The allowlist MUST include the non-secret transport configuration the harness's
own process needs to reach its provider (proxy variables and extra certificate
authorities). Withholding those buys no confidentiality — they carry no
credentials — and makes a dispatched worker unreachable on a proxied or
private-CA machine.

#### REQ: harness-shell-environment-excludes-secrets

The harness's own tool subprocesses MUST be configured not to receive the
provider credential or other secret-shaped variables, so a worker shell command
cannot read the credential that the harness itself authenticates with.

#### REQ: delegated-worker-trust-boundary-is-explicit

The specification and the user-facing documentation MUST state the trust
boundary plainly: a delegated worker runs with the invoking operator's
privileges and can read what that operator can read, including files outside its
worktree, and the provider it calls sees whatever the worker reads. Secret
handling in this feature is about not *handing* a worker credentials, not about
confining a worker that goes looking for them. Full read confinement is out of
scope and MUST be recorded as such rather than implied.

### Worktree custody and concurrency

#### REQ: dispatch-never-clears-the-resulting-worktree

Dispatch and run completion MUST NOT delete, clean, reset, or relocate the
worktree, and MUST NOT commit, push, or merge on the worker's behalf. The
resulting worktree is the primary artefact of the offloaded task and MUST
remain available for supervisor inspection, a follow-up worker, tests, diff,
and existing WB lifecycle commands.

#### REQ: independent-concurrent-runs

Two dispatches MUST be able to run concurrently in independent worktrees using
existing WB process and worktree mechanisms. The feature MUST NOT add a
scheduler, queue, resource accounting, or concurrency limit.

### Offload supervisor workflow

#### REQ: offload-skill-drives-dispatch-and-await

The `/offload` Agent Skill MUST drive the supervised flow:

```text
parent agent
    ↓
cheap native supervisor subagent
    ↓
wb agent dispatch
    ↓
wb agent await
    ↓
verification against the original request
    ↓
parent agent
```

The skill MUST hand the supervisor the original bounded task, its acceptance
criteria, the requested WB profile, and the requested worktree name and mode,
and MUST instruct it to return one concise outcome. The skill MUST NOT require
the parent to read the worker transcript.

#### REQ: supervisor-is-cheap-and-native

The skill MUST express the supervisor as the harness's own cheap/fast subagent
role rather than pinning an exact model name in WB. The strong parent MUST
perform any difficult decomposition before invoking the skill; the supervisor
MUST NOT be asked to resolve ambiguous architecture.

#### REQ: supervisor-verdicts-are-conservative

The skill MUST require exactly one of three verdicts. `PASS` only when the
worker appears to have completed the bounded task and the relevant validation
supports it. `FAIL` when the worker clearly did not complete the task or
introduced an obvious failure. `ESCALATE` when the supervisor cannot
confidently determine correctness, or finds something needing stronger
judgement — an architectural decision, an unclear requirement, a
security-sensitive concern, a surprisingly broad diff, conflicting approaches,
or tests insufficient to establish correctness.

#### REQ: no-automatic-repair-loop

The skill MUST NOT implement an automatic worker → reviewer → worker repair
loop.

### Failure handling and repository integration

#### REQ: deterministic-tests-and-opt-in-live-smoke-test

The default test suite MUST be deterministic: it MUST exercise dispatch,
status, and await against a fake harness with no network, no credentials, and
no real model. A live end-to-end run against a real provider MAY exist only as
an explicitly opt-in check that CI does not require.

#### REQ: actionable-visible-failures

Unknown profile, invalid profile configuration, unresolved repository,
worktree creation failure, unresolvable existing worktree, missing harness
executable, unavailable provider credential, worker start failure, non-zero
worker exit, malformed harness output, and timeout MUST each produce an
actionable failure, and MUST persist enough information in the run record to
diagnose it. WB MUST NOT claim `completed` for a run whose owner vanished, and
MUST NOT invent crash recovery beyond what WB already provides.

#### REQ: new-command-family-satisfies-wb-command-contracts

The new public command family MUST satisfy WB's existing command contracts: an
Agent Skill that covers it, an entry in the machine-readable command-coverage
map, a capability row in the capability manifest, a line in the documented
persistent-flag support matrix, and registration in the persistent-flag
support table for every persistent flag the commands actually consume.
`--projects-root` MUST be declared for the commands that resolve WB state
through it, and MUST be rejected elsewhere rather than silently accepted.

## Acceptance Criteria

### AC: dispatch-starts-one-isolated-worker (verifies REQ:exactly-one-worktree-mode, REQ:new-worktree-reuses-wb-worktree-creation, REQ:new-worktree-reuses-the-create-side-effects, REQ:new-worktree-requires-a-live-session, REQ:dispatch-returns-before-completion, REQ:dispatch-starts-execution-now, REQ:repository-selection-is-explicit, REQ:dispatch-task-input, REQ:harness-executable-resolution-is-substitutable)

Scenario: Dispatch into a new worktree
Given a registered WB session, an existing canonical clone of the resolved repository, and a configured profile whose harness executable is replaced on `PATH` by a deterministic fake
When `wb agent dispatch --new-worktree smoke --profile deepseek-coder --task "change one file"` runs
Then WB creates the checkout through `internal/worktrees.Create` with the resolved model recorded in its Work Log claim, refreshes the managed hooks and writes the checkout marker exactly as `wb worktree create` does, starts exactly one worker inside the new checkout, prints an agent ID, and returns without waiting for the worker

Scenario: Unregistered session
Given no live registered WB session for the invoking process
When `wb agent dispatch --new-worktree smoke --profile deepseek-coder --task "…"` runs
Then WB refuses with the existing actionable registration message, creates no worktree, and starts no worker

Scenario: Task input is required and never on argv
Given the task is supplied with `--task-file -` on standard input
When dispatch starts the worker
Then the run's original task is exactly those bytes, the bytes appear in neither the worker's argument list nor normal command output, and invoking dispatch with both `--task` and `--task-file`, or with neither, is refused as a usage error

Scenario: Dispatch executes immediately
Given a dispatched run
When dispatch returns
Then the run is `running` and its worker process has already started; no queued or deferred state is observable, and no scheduler, queue, or capacity limit participates

### AC: worktree-mode-is-unambiguous (verifies REQ:exactly-one-worktree-mode, REQ:new-worktree-conflict-refuses, REQ:use-worktree-resolves-only)

Scenario: Both or neither mode
Given an existing worktree named `smoke`
When dispatch is invoked with both `--new-worktree smoke` and `--use-worktree smoke`, or with neither
Then WB exits with the usage code and creates no worktree and starts no process

Scenario: Conflicting or unresolvable name
Given an existing worktree named `smoke`
When dispatch uses `--new-worktree smoke`
Then WB fails with the worktree creation conflict and starts no worker
When dispatch uses `--use-worktree missing`
Then WB fails naming the unresolvable worktree and creates nothing

Scenario: Ambiguous name across repositories
Given the same task name resolves to two worktrees in two repositories
When dispatch uses `--use-worktree <name>`
Then WB refuses and names both candidates rather than choosing one

### AC: profile-resolution-fails-closed (verifies REQ:profile-required-and-resolved, REQ:no-credentials-in-profile-configuration, REQ:profile-is-an-execution-configuration, REQ:profile-configuration-lives-in-existing-wb-config, REQ:reasoning-reaches-the-harness)

Scenario: Missing, unknown, and invalid profile
Given a configuration file with one valid profile
When dispatch omits `--profile`, names a profile that is not configured, or names a profile whose `model` looks like a credential
Then WB refuses before mutating anything, names the requested profile and the configuration file consulted, and creates no run record or process

Scenario: Reasoning is passed per process
Given a profile with `reasoning: high`
When dispatch launches the worker
Then the reasoning level is passed as per-process harness configuration and no file the harness reads globally is written

### AC: credential-source-is-declared-and-private (verifies REQ:credential-file-is-private-or-refused)

Scenario: A private credential file
Given a provider naming `credential_file` at an absolute path holding a 0600 regular file
When dispatch resolves the provider
Then the credential is read from that file, injected into the harness under WB's own variable name, and appears in no argument list, log, or run record

Scenario: A credential file WB would not have written
Given a credential file that is missing, empty, a symlink, a directory, or readable by group or others
When dispatch resolves the provider
Then WB refuses before creating anything and names the file and the remedy

Scenario: Two or no credential sources
Given a provider naming both `credential_env` and `credential_file`, or a new provider naming neither
When the configuration is loaded
Then it is refused rather than silently resolved

### AC: provider-registry-is-not-hardcoded (verifies REQ:provider-registry-is-small-and-closed, REQ:harness-provider-model-separation, REQ:model-is-provider-scoped-and-passes-through-verbatim)

Scenario: A second Codex-compatible provider
Given an `agents.providers` entry that overrides a built-in provider's base URL and credential variable name
When dispatch resolves a profile naming that provider
Then the resolved provider routing comes from the registry, no dispatch, status, or run-record code branches on the provider name, and an unsupported `wire_api` or an unknown harness is refused before launch

Scenario: The model identifier is passed through untouched
Given two profiles for the same underlying model under two different providers, one using the native unversioned Flash identifier and one using a versioned third-party identifier
When each is dispatched
Then each run records and passes to the harness exactly the configured identifier, with no aliasing, normalisation, or catalogue substitution, and an identifier the provider rejects surfaces as a run failure rather than being retried under a different name

### AC: resolved-profile-is-snapshotted (verifies REQ:resolved-profile-snapshot, REQ:durable-run-record, REQ:run-record-references-the-work-log-claim, REQ:stable-agent-id)

Scenario: Profile edited after dispatch
Given a dispatched run recorded with resolved model `deepseek-flash`
When the profile is later changed to a different model and provider
Then `wb agent status` for that run still reports the original resolved harness, provider, model, and reasoning, and the run record still names the original agent ID

### AC: status-and-await-report-facts (verifies REQ:status-reports-execution-facts, REQ:await-blocks-efficiently, REQ:structured-output, REQ:concise-result-not-transcript, REQ:transcript-available-on-demand-only, REQ:usage-and-result-metadata-come-from-the-harness, REQ:harness-output-is-captured-as-a-log-not-a-result, REQ:actionable-visible-failures)

Scenario: Awaited completion
Given a fake worker that modifies a tracked file, runs a command, emits harness usage events naming token counts, writes a final message, and exits zero
When `wb agent await <agent-id> --format json` runs
Then the document reports terminal state `completed`, exit status zero, the worktree and branch, a changed-file summary, the resolved model, the reported token usage, the worker's final message, and a log location — and contains no worker transcript

Scenario: Failing worker
Given a fake worker that exits non-zero after emitting an error event
When the run is awaited
Then the reported state is `failed` with the non-zero exit status and the log location

Scenario: Await bound elapses
Given a fake worker that never exits
When `wb agent await <agent-id> --wait-timeout 1s` runs
Then the command returns reporting the still-non-terminal state, does not claim success, and does not busy-spin

Scenario: Invalid harness output
Given a fake worker that emits malformed structured output and exits zero
When the run is awaited
Then the reported state is `failed` or `completed` according to the harness's own terminal event, no field is fabricated from the unparsable output, the malformed-output diagnosis is persisted, and the log is retained

### AC: default-suite-is-deterministic (verifies REQ:deterministic-tests-and-opt-in-live-smoke-test)

Scenario: No network and no credentials
Given the default test suite runs with the provider credential unset and no network access
When the dispatch, status, and await tests execute against the fake harness on `PATH`
Then they pass, and the live provider check is skipped rather than failed

### AC: dead-owner-runs-are-reported-honestly (verifies REQ:closed-run-state-vocabulary, REQ:abandoned-runs-are-never-reported-completed, REQ:detached-execution-owner-reuses-wb-self-exec)

Scenario: Owner killed before recording a terminal state
Given a dispatched run whose recorded owner is gone and whose record has no terminal state, and whose admission window has passed
When `wb agent status` and `wb agent await` run
Then both report `abandoned` rather than `running` or `completed`, and neither blocks indefinitely

Scenario: A worker outlived its owner
Given a run whose persisted owner is gone but whose recorded worker process is still alive
When the run is inspected
Then it is reported `abandoned` with the live worker surfaced, so a caller can stop the stray process instead of waiting for an outcome that will never be written

Scenario: Admission in flight
Given a run persisted moments ago that has not yet recorded any process
When it is inspected
Then it is reported `running`, not `abandoned`

Scenario: A dispatcher that died before starting any owner
Given a persisted run past the admission window that has never recorded a process
When it is inspected
Then it is reported `abandoned`, never `running` indefinitely

### AC: timeout-terminates-the-worker-tree (verifies REQ:dispatch-timeout-is-bounded)

Scenario: Worker ignores graceful termination
Given a worker that ignores graceful termination and spawns a child
When the run's timeout elapses
Then WB terminates the worker's whole process group, records state `timeout` with a finish time, retains the log, and starts no further work

### AC: worker-cannot-disturb-its-parent (verifies REQ:no-global-harness-configuration-mutation, REQ:no-secret-in-argv-logs-or-records, REQ:filtered-child-environment, REQ:harness-shell-environment-excludes-secrets, REQ:task-reaches-the-harness-on-stdin, REQ:harness-launch-is-per-process-and-ephemeral, REQ:delegated-worker-trust-boundary-is-explicit)

Scenario: Isolated child harness launch
Given a machine with a global harness configuration file and a provider credential in the ambient environment
When dispatch launches a worker
Then the global configuration file is byte-identical afterwards, the credential value appears in neither the worker's argv, WB's argv, any WB log, nor the run record, the child environment contains only allowlisted variables plus that credential, the harness's own shell tooling is configured to exclude secret-shaped variables, the task text does not appear in the process arguments, and the worker runs in its own session and process group

Scenario: The trust boundary is documented, not implied
Given the feature's specification and its user-facing skill
When a reader looks for what a delegated worker can reach
Then both state plainly that the worker runs with the invoking operator's privileges and can read what that operator can read, and that read confinement is out of scope

Scenario: Missing credential and missing harness
Given the provider credential variable is unset
When dispatch runs
Then the run fails actionably naming the variable, with no worker started
Given no harness executable resolves on `PATH`
When dispatch runs
Then the run fails actionably naming the harness and creates no worker

### AC: dispatch-preserves-the-worktree (verifies REQ:dispatch-never-clears-the-resulting-worktree, REQ:independent-concurrent-runs, REQ:use-worktree-resolves-only)

Scenario: Two concurrent dispatches
Given two dispatches into two different new worktrees issued without waiting for the first
When both runs finish
Then both worktrees still exist with the workers' changes present, and no branch was committed, pushed, or merged by WB

Scenario: Follow-up run into an existing worktree
Given a completed run whose worktree still exists
When dispatch runs with `--use-worktree <that name>` and a second task
Then the second worker runs in the same checkout without WB creating or deleting anything there

### AC: wb-command-contracts-are-satisfied (verifies REQ:new-command-family-satisfies-wb-command-contracts)

Scenario: New public command family
Given the `wb agent` commands are registered
When WB's own command-contract tests run
Then every new public leaf has Agent Skill coverage, a command-coverage entry, a capability row, and a persistent-flag support declaration, and `wb agent status --projects-root` is either honoured or rejected with the usage code rather than silently ignored

### AC: offload-skill-uses-dispatch (verifies REQ:offload-skill-drives-dispatch-and-await, REQ:supervisor-is-cheap-and-native, REQ:supervisor-verdicts-are-conservative, REQ:no-automatic-repair-loop, REQ:supervisor-supervises-wb-executes)

Scenario: Supervised offload
Given the `/offload` skill and a bounded task with acceptance criteria
When a parent agent runs the skill
Then it delegates to the harness's cheap supervisor subagent, which dispatches through `wb agent dispatch`, awaits with `wb agent await`, verifies against the original request, and returns exactly one of `PASS`, `FAIL`, or `ESCALATE` with supporting evidence, without the parent consuming the worker transcript, and without WB itself emitting any verdict

### AC: remote-dispatch-runs-on-the-named-machine (verifies REQ:remote-machine-resolution-reuses-wb-machine-map, REQ:remote-request-travels-on-stdin, REQ:remote-records-stay-on-the-owning-machine, REQ:remote-request-is-validated-identically)

Scenario: Dispatch to a configured machine
Given a configured machine with an SSH address and a wb that supports this feature
When `wb agent dispatch --to <machine> --new-worktree <name> --profile <profile> --task-file <brief>` runs
Then the request is delivered on standard input, the remote command line contains only constants, that machine creates the worktree and the run record, and this machine reports the run under a machine-qualified reference

Scenario: The request never reaches a remote command line
Given a task containing shell metacharacters
When it is dispatched to another machine
Then the task appears in neither the local nor the remote process argument list

Scenario: A machine that is not configured
When dispatch names an unconfigured machine
Then WB refuses with a usage-class error listing the configured machines

Scenario: A target that predates the feature
Given a machine whose wb does not understand the request
When it is dispatched to
Then the failure names that machine and quotes its diagnostic, rather than reporting a run that never started

### AC: remote-inspection-asks-the-owning-machine (verifies REQ:remote-records-stay-on-the-owning-machine, REQ:remote-refusal-is-not-a-transport-failure, REQ:local-vocabulary-is-authoritative-over-the-wire, REQ:remote-transcript-is-bounded-and-refused-not-truncated)

Scenario: Inspecting a remote run
Given a run dispatched to another machine
When status, await, list, logs, or stop names that run — by `--to`, or by a `machine:agent-id` reference — with an unreachable machine
Then the failure is reported as a transport failure naming the machine

Scenario: A refusal arrives as a refusal
Given a remote machine that answers a well-formed refusal
When the caller inspects a run
Then the caller reports that machine's own message and never presents it as a transport failure

Scenario: A remote answer cannot redefine a terminal state
Given a remote answer whose terminal flag disagrees with the state it reports
When the local side renders it
Then the local closed vocabulary decides, so a finished run is never reported as pending and a pending run is never reported as finished

Scenario: A transcript too large to ship
Given a remote transcript larger than the bound
When logs are requested
Then WB refuses and points at the machine that holds it instead of truncating

### AC: remote-await-waits-on-the-other-machine (verifies REQ:remote-await-holds-one-connection, REQ:remote-credentials-are-not-forwarded)

Scenario: One connection for a long wait
Given a run on another machine that has not finished
When `wb agent await <machine>:<agent-id> --wait-timeout <bound>` runs
Then exactly one remote call is made, the transport bound exceeds the wait bound, and an elapsed bound is reported as a non-terminal outcome with the findings exit code

Scenario: Credentials are not carried across
Given a machine whose provider credential is absent from its environment
When work is dispatched to it
Then that machine fails with its own message naming the missing variable, and no credential from this machine is sent

### AC: live-deepseek-worker-round-trip (verifies REQ:harness-launch-is-per-process-and-ephemeral, REQ:no-global-harness-configuration-mutation)

Scenario: DeepSeek-backed Codex smoke test
Given a DeepSeek credential in the environment and the Codex CLI on `PATH`
When a bounded task is dispatched that requires the worker to read a file, modify it, and run a command
Then WB launches a separate Codex process using the configured DeepSeek provider and model without altering the parent's global Codex configuration, the file is changed in the run's worktree, and `wb agent await` reports the captured result and usage

This scenario MUST remain opt-in and MUST NOT be required for the default suite to pass.

## Non-goals

Deliberately excluded. Each is a plausible later feature; none may inflate this
one.

- `wb agent submit`, a queued state, a scheduler, or a priority queue.
- CPU-aware, memory-aware, or any capacity-aware scheduling; worker pools;
  distributed execution; choosing a machine by load, cost, or capability; or
  fanning one task across several machines. A caller names one machine
  explicitly; WB never picks, ranks, or balances machines.
- Profile `skills`, capability matching, resource declarations, inheritance,
  templates, or per-repository profiles.
- Automatic model selection, fleet-wide propagation, or cost optimisation.
- Automatic repair loops after a `FAIL` verdict.
- Automatic worktree cleanup or deletion on run completion. Existing WB
  lifecycle commands remain the only way a worktree is retired.
- CodeGrapher or any impact-analysis integration. CodeGrapher decides what code
  is affected; WB manages execution and workspaces.
- Any harness other than `codex` in this MVP. The launch boundary MUST NOT
  preclude another harness later, but adding one is a separate feature.
- A general plugin framework: no model catalogue, no capability negotiation, no
  dynamic provider or harness loading.
- Prompts or semantic review performed by WB, and any WB-level judgement of
  whether a diff is correct.
- A daemon, a queue, or reuse of the `wb daemon` / `wb worker` scheduler.
- Per-model dollar cost accounting beyond the token counts the harness reports.
- Resuming, forking, or messaging a dispatched worker's harness session.

## Open Questions

- Whether a follow-up run into an existing worktree should reuse the original
  run's recorded base revision for its diff summary, or recompute the base from
  the canonical repository at dispatch time. Observed in the first end-to-end
  offload: `base_sha` is empty for `--use-worktree`, so a supervisor must derive
  a base itself before diffing. The supervisor coped by using the merge base
  with `origin/main`, but the gap is real and recurring enough to deserve a
  decision rather than a workaround.
- Whether `wb agent` should eventually absorb the peer-session offload path
  (`wb task offload`) so a portion of work has exactly one launch mechanism.
  The peer-session path additionally supports cross-machine continuation and an
  addressable successor session, which `dispatch` deliberately does not.
- Whether the timeout belongs in the profile as well as per invocation. Left
  out until a concrete need appears.
- Process liveness is a PID check, so a recycled PID could in principle make an
  abandoned run look alive. Closing that properly needs a recorded process start
  time (platform-specific) rather than a process registry; deferred until it is
  observed in practice.
- Whether a per-run `CODEX_HOME` would isolate the child harness further than
  the ephemeral, no-user-config launch already does. Deliberately not adopted
  before it has been verified against a real Codex version; the ephemeral
  no-user-config launch is the mechanism with observed evidence behind it.
- Why profiles exist at all rather than three flat flags. The MVP brief makes
  `--profile` mandatory and the resolved-profile snapshot is the feature's
  stated value, so the indirection is kept: one token for the supervisor to
  pass, and one place a project pins its worker execution configuration.

---
*This document follows the https://specscore.md/feature-specification*
