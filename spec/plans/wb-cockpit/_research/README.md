# WB Cockpit — discovery note

What the code on `origin/main` does today, read on 2026-10-01 for the
[WB Cockpit master plan](../README.md). Paths are relative to the repository
root. Line numbers drift; the function and type names are the stable anchor.

## What exists and can be reused

- **Loopback server.** `serveDashboard` in `cmd/wb/daemon.go` builds it;
  default listen address `127.0.0.1:8766`; `requireLoopbackAddress` refuses
  anything else. Routes are registered in `internal/dashboard/dashboard.go`:
  `/`, `/metrics`, `/coverage`, `/api/v1/health`, `/api/v1/overview`,
  `/api/v1/log`, `/api/v1/peers`. `/workbench/` and `/v0/workbench/` are
  mounted only when `wb.yaml` has a `hub:` section (`cmd/wb/daemon_hub.go`).
- **Owner credential.** A second listener on a unix socket serves the
  connect-go `DaemonService` (`proto/wb/daemon/v1/daemon.proto`) and the peer
  admin routes behind a bearer owner token (`authenticatedDaemonHandler`,
  `cmd/wb/daemon_rpc.go`).
- **Operation queue.** `daemon.Service` (`internal/daemon/service.go`) has
  Submit, Get, Wait and Cancel with states queued, running, succeeded, failed,
  cancelled and recovery_required.
- **Embedding pattern.** `hub/web/embed.go` embeds a build output, serves a
  one-line page when it is absent, and is built by the goreleaser before-hook.
- **Git and worktree state.** `gitops.RepoStatus`, `gitops.TrackingState` and
  `gitops.UnpushedWork` give dirty, untracked, stash, ahead, behind, diverged
  and upstream-gone. `worktrees.ListResult` gives clean, head and remote head,
  integration at origin, open and merged pull request, and owner state.
  `worktrees.BranchEntry` covers branches without a worktree.
- **Operations to wrap.** `orchestrate.CreatePullRequest`,
  `orchestrate.LandPullRequest`, `orchestrate.RunWorktreeMerge`,
  `worktrees.Abort`, `worktrees.Cleanup`, `worktrees.BranchCleanup`,
  `gitops.AddCommit`, `gitops.Push`, `gitops.PushSetUpstream`. All are library
  functions taking an options struct.
- **Other machines.** `remotestate.Snapshot` carries per-repository Git state
  and per-worktree state; `remotestate.ReadStatus` reads every machine's.
- **Sessions and agent runs.** `session.Record` and the run records under
  `<wb home>/agents/<id>/`.
- **Command conventions.** A new leaf needs a row in `ai/capabilities.json`,
  an entry in `ai/skills/commands.json`, a line in `docs/cli-flag-matrix.md`
  and a persistent-flag declaration; `cmd/wb/skills_test.go` and
  `cmd/wb/main_test.go` enforce the first, second and fourth.
- **Work Log manifest.** `worktrees.Manifest` (`internal/worktrees/journal.go`)
  is version 1 YAML read non-strictly, so an optional field can be added
  without a version bump.

## Gaps Cockpit has to fill

- **No request protection on the loopback listener.** `/api/v1/*` checks
  neither `Host` nor `Origin`. Only a content security policy is sent.
- **No session of any kind.** `wb dashboard --admin`, the admin cookie and
  `/workbench/admin/login` exist in `peer-connectivity` only, not in code.
- **No Git state in the dashboard or the index.** `Overview.Worktree` has
  task, repository, branch, owner and age. The fingerprinted inventory
  (`internal/discover/local_index.go`) lists repositories only and calls
  itself "discovery data, never mutation evidence".
- **The operation queue is not typed.** `SubmitOperationRequest` carries an
  argument vector. A typed action layer is new work, and it must be the only
  thing a browser can reach.
- **Wiring in the command layer.** Admission, host-load and landing-lane
  handling for landing sit in `cmd/wb`, not in the library functions.
- **No event stream in the daemon.**
- **No Angular code anywhere in the repository.** `hub/web` is Astro with no
  UI framework: five pages, three components.

## Facts that bear on later slices

- **The peer link is not built.** `hub/http.go` answers 501
  `peer_session_not_implemented` to a WebSocket upgrade.
- **Hub and hosted snapshots carry no Git state.** Only the richer
  `remotestate.Snapshot` does.
- **Dispatch is Codex-only, detached and has no herdr.** `agents.Dispatch`
  spawns a detached owner process (`internal/agents/owner.go`).
  `internal/herdr` exists as an adapter and is used only for a
  binary-availability check.
- **The Work Log already has a hierarchy.** `effort_id` is a dot-separated
  path; `ParentEffort` derives the parent lexically and `EffortKindFor` calls
  a root path a feature effort and a nested one a task effort.
- **`wb dashboard` never opens `/workbench/`.** `--local` opens the pure-Go
  index.
