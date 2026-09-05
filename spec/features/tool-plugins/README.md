---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Tool Plugins

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/tool-plugins?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/tool-plugins?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/tool-plugins?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/tool-plugins?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

WB exposes a small typed registry of local-tool plugins. The first entry is CodeGrapher, enabled by default and reachable through `wb codegrapher`. Plugin registration is not graph orchestration: only explicitly requested lifecycle verbs may run a local tool.

## Problem

An agent that needs CodeGrapher should not rediscover its package channel, platform behavior, executable location, or installed version. A generic plugin system also needs a real default integration to prove its lifecycle rather than being an unused abstraction.

## Behavior

### REQ: typed-registry

`wb plugin list --format=json` MUST expose every built-in plugin with a stable ID, default-enabled state, command path, lifecycle verbs, and summary. The registry MUST be typed data, rather than a free-form command string. `--json` MUST remain a shortcut for `--format=json`.

### REQ: codegrapher-default-plugin

The CodeGrapher plugin MUST be preconfigured and default-enabled. Its lifecycle is exactly `status`, `install`, and `update`; it is operated through `wb codegrapher <verb>`. The registry MUST identify the CodeGrapher repository as `github.com/code-grapher/codegrapher` and its current Go module import path as `github.com/specscore/codegrapher/cmd/codegrapher`.

### REQ: platform-installation

On macOS and Linux, `wb codegrapher install --yes` MUST run `brew update` then install the published `code-grapher/tap/codegrapher` cask. `update` MUST run the same refresh then `brew upgrade --cask codegrapher`. A requested version is refused for the Homebrew path because that package manager cannot reliably pin an arbitrary cask revision.

On Windows, install and update MUST use `go install github.com/specscore/codegrapher/cmd/codegrapher@<version>`, where `latest` is the default and an explicit release version is accepted. WB MUST refuse before mutation when the required package manager is unavailable or the platform is unsupported. Every mutating invocation requires `--yes`; `--dry-run` prints the exact command and changes nothing.

### REQ: installed-provenance

`wb codegrapher status --format=json` MUST run the installed executable's `version` command and report whether it is runnable, its resolved executable path, exact reported version, commit and build time when reported, installation manager classification, CodeGrapher repository, module path, and host platform. An absent executable is a machine-readable finding and exits WB code `1`. An installation or update MUST re-probe this same binary before WB claims success.

### REQ: no-implicit-graph-work

No plugin listing, status, install, update, `wb sync`, dependency command, or daemon action may run `codegrapher init`, `index`, or `sync`; query a hosted CodeGrapher service; upload a graph; or use credentials. A later graph-refresh provider requires an exact repository/ref contract and its own Feature.

### Planned: receipt-backed daemon refresh

When CodeGrapher exposes an exact-source-revision acknowledgement, the WB daemon may accept a completed `wb sync` receipt as a trigger. It will enqueue a coalesced `(repository, exact-target-SHA)` refresh job only after the receipt proves the local sync result. The job will carry the receipt ID, plugin ID, target SHA, queue/coalescing disposition, attempt count, duration, and terminal graph acknowledgement; it will never infer the source revision from a mutable branch name.

The daemon will emit bounded progress events at least every ten seconds while a refresh is queued or running. Those events will be available through `wb monitor --format=jsonl`, the operations journal, and dashboard streams. A graph failure will be reported as plugin telemetry and must not retroactively mark the already-completed WB sync receipt as failed.

This is intentionally not implemented: the current CodeGrapher hosted endpoint accepts work but does not return the required exact-SHA acknowledgement, so WB cannot distinguish a current graph from a stale coalesced one.

## Acceptance Criteria

### AC: deterministic-local-lifecycle

**Requirements:** tool-plugins#req:typed-registry, tool-plugins#req:codegrapher-default-plugin, tool-plugins#req:installed-provenance

**Given** a CodeGrapher executable on PATH that reports CodeGrapher `0.2.2` with a commit and build time
**When** an agent runs `wb plugin list --format=json` and `wb codegrapher status --format=json`
**Then** the first report lists CodeGrapher as default-enabled with only its three lifecycle verbs, and the second reports the exact executable identity and CodeGrapher provenance without a graph operation.

### AC: safe-platform-selection

**Requirements:** tool-plugins#req:platform-installation

**Given** macOS, Linux, Windows, and an unsupported host
**When** each runs `wb codegrapher install --dry-run --format=json`
**Then** macOS/Linux receive the Homebrew cask command, Windows receives the Go module command, and the unsupported host is refused before an installer is started.

## Open Questions

- What exact repository SHA and idempotency contract should a future CodeGrapher graph-refresh provider require before it can be queued after `wb sync`? The current hosted endpoint does not yet return that evidence.
- Which refresh telemetry fields remain local-only and which may be published to a Workbench dashboard after user authentication? The first implementation must retain no source content, command output, or credentials.

---
*This document follows the https://specscore.md/feature-specification*
