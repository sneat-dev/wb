---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Secret vault injection for agent sessions

**Status:** Draft
**Date:** 2026-08-27
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might WB let an agent use or set a secret at execution time without the secret value ever entering the agent's prompt, tool result, log, transcript, or continuation?

## Context

### The problem, from today's evidence

Agents regularly need to **use** or **set** a secret without ever **seeing**
it. There is no mechanism for that today, so the work stops and a human does
it by hand, repo by repo.

The Apple notarization effort (`sneat-co/backstage#45`,
`specscore/specscore-cli#132`) is the motivating case. It needs five secrets
— `MACOS_SIGN_P12`, `MACOS_SIGN_PASSWORD`, `NOTARIZE_ISSUER_ID`,
`NOTARIZE_KEY_ID`, `NOTARIZE_KEY` — configured across seven CLI repos in seven
separate GitHub orgs. Every agent on the rollout was explicitly forbidden from
reading, copying, or inferring any value, so the whole rollout is blocked on
the founder doing it manually in each repo. GitHub Actions secrets are
**write-only**, so even a value already configured in one repo cannot be
copied to another by anyone, human or agent — every repo needs the value
re-supplied from its original source.

### Why this is sharper than ordinary secret hygiene

An agent's context is *persisted and redistributed*, not merely displayed and
discarded. Transcripts are stored, continuations are handed to successor
agents, and under the in-flight unification plan
(`spec/ideas/unify-session-move-and-park-continuation-storage.md`, referenced
here — not modified by this idea) continuations will be published to a Git
repository. A secret that enters an agent's context has not merely been seen —
it has been written down, copied forward, and potentially pushed. This is not
hypothetical: a `wb session move` handover document sat in a **public** repo
for exactly this class of reason, fixed in `sneat-dev/wb` PR #201, with the
underlying behaviour fixed in PR #202.

### Related, not duplicated: the preventive counterpart

The founder separately raised a **deterministic secret scanner** — blocking
known key shapes (Stripe, AWS, GitHub, private-key blocks) at park/publish
time, ideally reusing `gitleaks`'s maintained rule format rather than a
bespoke corpus, failing closed on named patterns, warning only on entropy
heuristics, and never echoing the matched value. That scanner is **not yet
approved or built**. It is the *detective* counterpart to this idea's
*preventive* one: the scanner catches a secret that already leaked into a
diff or transcript; this idea aims to stop the secret from ever reaching the
agent in the first place. They should probably share a redaction/matching
library rather than growing two, but that is a decision for whichever gets
built first.

## Recommended Direction

Add **`wb vault`**: a thin wrapper that speaks an **OpenVaultDB (OVDB)
contract** for secret references, with a pluggable backing implementation.
OVDB here is a contract, not a mandated storage engine — the founder's
guidance is *"it's up to the user to decide how to manage encryption at
rest — OVDB is just a contract."* `wb vault`'s own default backing store for
our fleet is a new **dedicated private repo, `sneat-dev/wb-vault`** — distinct
from `sneat-dev/wb-state` (claims/machine state) and the handoff store used
for continuations, each with its own single purpose. Being a private,
Git-backed store, not macOS Keychain, is what lets `wb vault` work
identically on both fleet machines: the MacBook and the Hetzner VM. The
backend stays pluggable — 1Password CLI (`op run` already does environment
injection well), SOPS, HashiCorp Vault, or macOS Keychain for a purely local
case — `sneat-dev/wb-vault` is only the fleet's own first choice, not the only
implementation the contract allows.

The defining success property: **the secret value never crosses the agent
boundary** — not in a prompt, not in a tool result, not in a log, not in a
transcript, not in a continuation. The agent references a secret by *name*;
something outside the agent resolves it at execution time. Sketch of the
shape, not a finished design: `wb vault use <name> -- <command>` resolves the
value from the backing store and injects it into the **child process's
environment only**, returning the agent an exit code and **scrubbed**
output. Write paths matter as much as read paths: setting a secret (for
example `gh secret set`) should be possible without the agent ever holding
the value — `wb vault` piping from the backing store directly into `gh`,
never through agent-visible stdin/stdout it can see the content of.

Three hard parts make this a real feature rather than a wrapper:

- **Output scrubbing is mandatory and easy to forget.** If the child command
  echoes the secret — a verbose curl, a debug log, an error message quoting a
  token — the value lands back in the agent's context and the whole mechanism
  is defeated. The wrapper must redact it from the child's stdout/stderr
  before returning anything to the caller.
- **Failure must be legible.** "The secret is missing/expired" has to be
  distinguishable from "the command failed" or "the vault backend is
  unreachable," or agents will misdiagnose it endlessly. `wb vault`'s own
  failure modes now include `sneat-dev/wb-vault` (or whichever backend is
  configured) being unreachable — both the VM and the MacBook must degrade
  legibly (a named, distinguishable error) rather than silently falling back
  to an unprotected path such as printing a cached or default value.
- **The vault repo must actually stay unreached by ordinary fleet
  operation.** See the sync-exclusion gap below — this is a prerequisite, not
  an assumption.

### The sync-exclusion gap (verified against current `wb` code)

Storing the backing store as a fleet-visible repo only isolates agent context
if the repo itself is never casually cloned onto a machine. `wb sync` has
**no existing mechanism to exclude a named repo before it is first cloned.**
The only related primitive today is `wb.skip-sync` (`internal/gitops/gitops.go`,
`cmd/wb/repo_ignore.go`): a local git-config flag set *inside an
already-cloned* repository via `wb repo ignore`, which stops `wb sync` from
pulling or (with `--prune-archived`, added in `wb sync` PR #199) deleting that
one local clone. It cannot stop a fresh `wb sync` run on a machine that has
never cloned `sneat-dev/wb-vault` from cloning it for the first time, because
that decision happens during org repo discovery, before any local git config
for that repo exists. A pre-clone, name-based exclusion (an allow/deny
listing `wb sync` consults during discovery, before the clone step) is
therefore a **prerequisite task for this idea**, not something it can assume
is already available.

That exclusion also needs to be **positively enforced, not merely
configured** — the same reasoning as the regression test that now proves
`wb sync` no longer deletes archived clones by default (PR #199). A test
should fail if `wb sync` would ever touch `sneat-dev/wb-vault`, so a future
`wb sync` regression cannot silently reintroduce the exposure. This also
needs to interact correctly with the canonical-clone write-guard work another
lane is building: `wb-vault` must not be treated as an ordinary canonical
clone that guard logic assumes is safe to read/write/sync.

### The at-rest question for the chosen backing store

Because `sneat-dev/wb-vault` is our fleet's chosen backing implementation, one
operational question belongs to it specifically, stated neutrally as a
property to be informed by, not an objection to the design: a private
Git-repo-backed store with **no at-rest encryption** keeps its material in
plaintext in that repo's history on GitHub's servers and in every clone that
has ever existed. Deleting a file does not remove it from history; rotation
of the affected credential is the only remedy after exposure. This is an
ordinary operational decision for whoever stands up `sneat-dev/wb-vault` — it
is not the 2026-07-02 at-rest-encryption ruling reopened. That decision
(`sneat-co/backstage` `docs/roadmaps/ovdb-at-rest-encryption.md`, parked, not
scheduled) was specifically about **not building at-rest encryption into the
OVDB contract/product itself** for user application data; it says nothing
about whether a particular backing implementation behind the contract
encrypts. `wb vault` speaking the OVDB contract does not inherit or reopen
that ruling — the choice of whether `sneat-dev/wb-vault`'s own storage is
encrypted is ours to make when we build it, informed by the plaintext-in-
history fact above.

## Alternatives Considered

- **macOS Keychain only.** Rejected as the sole backend: already present and
  well-integrated on the MacBook, but macOS-only — it solves nothing for the
  Hetzner VM half of the fleet. Remains a plausible additional backend behind
  the same `wb vault` interface for purely local, single-machine secrets.
- **1Password CLI (`op run`) as the whole mechanism.** `op run` already does
  environment injection well and is worth reusing conceptually. Rejected as
  the *only* mechanism because it does not solve output scrubbing (a child
  command can still echo a secret `op run` injected) and adds an external
  paid dependency in the critical path; remains a candidate pluggable
  backend.
- **Ask the founder to keep doing it by hand, indefinitely.** Rejected: it is
  the status quo this idea exists to replace, and it does not scale past the
  current seven-repo notarization rollout.
- **Let agents hold secrets but redact them from stored transcripts after
  the fact.** Rejected as the primary mechanism: that is the deterministic
  secret scanner's job (detective, not preventive) and does not stop the
  value from being live in the agent's own reasoning/context during the
  session, nor from leaking through a channel the scanner does not scan.

## MVP Scope

`wb vault use <name> -- <command>`: resolve one named secret from the
configured backing store (defaulting to `sneat-dev/wb-vault` once it exists),
inject it into the child process's environment only, scrub it from the
child's stdout/stderr, and return a distinguishable exit/status for
"succeeded", "command failed", and "secret missing/expired/vault
unreachable". The pre-clone `wb sync` exclusion (with its regression test) is
in scope as a prerequisite, because without it the isolation the rest of the
feature promises does not hold.

Multi-backend selection beyond one default, `gh secret set` write-path
automation, key rotation tooling, and a UI/CLI for managing grants are
explicitly deferred past the MVP.

## Not Doing (and Why)

- Building the deterministic secret scanner as part of this idea — it is
  separate, not-yet-approved work; this idea only references it so the two
  are not duplicated later.
- Deciding at-rest encryption for `sneat-dev/wb-vault` in this document —
  that is an implementation-time operational decision for whoever builds the
  backing store, not a design choice this idea needs to settle.
- Creating `sneat-dev/wb-vault` or any other repo — this idea is
  documentation only; repo creation is a founder action taken when he decides
  to proceed.
- Supporting every possible backend (Keychain, 1Password, SOPS, HashiCorp
  Vault) in the first slice — the interface is designed to be pluggable, but
  the MVP ships with exactly one working backend.

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | Output scrubbing can reliably catch a secret a child process echoes back, across common failure shapes (verbose curl, debug logs, error messages quoting the value). | Build a fixture command that deliberately echoes an injected value in several shapes and confirm the wrapper redacts every one before returning. |
| Must-be-true | A pre-clone, name-based `wb sync` exclusion can be built and proven with a regression test, so `sneat-dev/wb-vault` is never cloned by ordinary fleet sync. | Add the exclusion, then run the same style of regression test that proves `wb sync` no longer deletes archived clones by default (PR #199), asserting sync never touches the excluded repo. |
| Should-be-true | A Git-repo-backed store can give both the MacBook and the Hetzner VM equivalent, legible access and equivalent legible failure when unreachable. | Exercise `wb vault use` against the same named secret from both machines, including with the backing repo made deliberately unreachable. |
| Might-be-true | The same redaction/matching logic can usefully be shared between `wb vault`'s output scrubbing and the separately-raised deterministic secret scanner. | Once the scanner idea is designed, compare rule/pattern needs and see whether a shared library is a net simplification. |

## SpecScore Integration

- **New Features this would create:** TBD at design time — likely a
  `wb vault` command feature plus a `wb sync` pre-clone exclusion feature
  (the latter may land as a small feature on its own, since other work may
  need it too).
- **Existing Features affected:** [park-and-resume-agent-sessions](../features/park-and-resume-agent-sessions/README.md)
  and [agent-session-move](../features/agent-session-move/README.md) own the
  continuation-handling surface this idea's problem statement is about;
  neither is changed by this idea, but both motivate it.
- **Dependencies:** an OVDB contract implementation to speak to; a
  pre-clone `wb sync` exclusion mechanism (prerequisite, does not exist
  today); the not-yet-approved deterministic secret scanner idea (related,
  not a dependency).

## Open Questions

1. **What backs `sneat-dev/wb-vault`, and does it encrypt at rest?** OVDB is
   a contract; the founder has been clear this is an operational choice for
   whoever implements the backing store, not a reopening of the 2026-07-02
   at-rest-encryption ruling (which was about the OVDB product/contract
   itself, for user application data). The one fact worth deciding against,
   not deciding away: a plain Git-repo-backed store with no encryption keeps
   secret material in plaintext in that repo's history and in every clone
   that ever existed, with rotation as the only remedy after exposure. This
   needs a decision at implementation time, not in this idea.
2. **What is the pre-clone `wb sync` exclusion mechanism's exact shape?** A
   per-repo allow/deny list consulted during org discovery, a config file,
   an org-level convention (e.g. repo topic/description marker read via
   `gh`) — and how does it interact with `--org`/`--filter` and with the
   canonical-clone write-guard work another lane is building, so
   `sneat-dev/wb-vault` is never treated as an ordinary canonical clone?
3. **Which backends does `wb vault` support out of the gate beyond the
   default?** 1Password CLI (`op run`), macOS Keychain (single-machine
   only), SOPS, HashiCorp Vault are all plausible; none is decided here.
4. **What is the exact CLI shape for the write path** (`gh secret set` and
   equivalents for other providers), and how are its failure states
   surfaced distinctly from the read path's?
5. **Should `wb vault`'s output-scrubbing logic and the not-yet-approved
   deterministic secret scanner share a redaction/pattern library**, or are
   their needs different enough (runtime redaction of an unknown-shape
   value vs. static matching of known key shapes) to warrant separate
   implementations?
