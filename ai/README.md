# WB agent skills

`ai/skills/` is the canonical, harness-neutral source for WB's Agent Skills:

| Skill | Scope |
|---|---|
| `wb-install` | Install and verify an exact WB build |
| `wb-branches` | Inspect and safely retire local and remote branches |
| `wb-worktrees` | Guard, create, inspect, and clean worktrees; move registered sessions |
| `wb-hooks` | Install, inspect, repair, and measure Git hooks |
| `wb-deps` | Graph, set, and bump dependencies |
| `wb-ci` | Audit CI/CD policy |
| `wb-migrate` | Run declarative source migrations |
| `wb-run` | Preview and apply reusable recipes |
| `wb-fleet` | Sync, inspect, and verify repository fleets |
| `wb-change` | Deliver a safe multi-step code change |
| `wb-dependency-campaign` | Propagate releases with minimal CI builds |
| `wb-merge` | Integrate completed work and retire its lifecycle debt |

The command-specific skills are compact references. The workflow skills compose them for
workflows where orchestration saves time or avoids duplicate builds. Detailed
flags live in references and load only when needed.

Claude Code recursively auto-discovers the same skills and root `agents/`
through the plugin described by `.claude-plugin/plugin.json`; the manifest does
not list them again. Codex reads the skills through
`.codex-plugin/plugin.json` and the per-skill `agents/openai.yaml` presentation
metadata. Marketplace packaging should reference this repository without
copying instructions; it is not included here. These checked-in adapters are
source, not proof that a harness has installed them. An installed `wb-merge`
adapter supersedes copied legacy merger prompts, which should be removed or
disabled so they cannot compete with the canonical contract.

## Completion contract

[`wb-change/references/completion.md`](skills/wb-change/references/completion.md)
is the single definition of done for implementation work. It requires an agent
to report the achieved outcome as `implemented`, `published`, `landed`, or
`blocked`; it must never imply a push or merge happened without evidence. The
Codex default prompt routes to `$wb-change`, and Claude Code discovers the same
skill recursively, so neither harness maintains a competing copy.

[`skills/commands.json`](skills/commands.json) maps every public WB top-level
command to at least one skill. `go test ./cmd/wb` enforces that coverage as the
CLI evolves.

## Capability delivery contract

`capabilities.json` is WB's single runtime/help/AI-skill/test delivery view.
Every public CLI leaf must have one conventionally named row; planned product
seams may add rows only when all four surfaces are honestly `Planned` and no
fictional command or example is exposed.

`cli-capability-delivery.schema.json` is a pinned vendored validation input,
not a second schema authority. Its canonical source is
`specscore` commit `e06f6ab`, path
`new/cli-capability-delivery.schema.json`, with schema ID
`https://specscore.md/new/cli-capability-delivery.schema.json`. The WB validator
in `cmd/wb/skills_test.go` pins the exact SHA-256 digest of that file before it
compiles the schema. An upstream schema update therefore fails until an
explicit migration replaces the vendored input, updates the digest, and makes
the manifest pass the new contract.
