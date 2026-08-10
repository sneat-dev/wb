# WB agent skills

`ai/skills/` is the canonical, harness-neutral source for WB's Agent Skills:

| Skill | Scope |
|---|---|
| `wb-install` | Install and verify an exact WB build |
| `wb-worktrees` | Guard, create, inspect, resume, and clean isolated worktrees |
| `wb-hooks` | Install, inspect, repair, and measure Git hooks |
| `wb-deps` | Graph, set, and bump dependencies |
| `wb-ci` | Audit CI/CD policy |
| `wb-migrate` | Run declarative source migrations |
| `wb-run` | Preview and apply reusable recipes |
| `wb-fleet` | Sync, inspect, and verify repository fleets |
| `wb-change` | Deliver a safe multi-step code change |
| `wb-dependency-campaign` | Propagate releases with minimal CI builds |

The first eight are compact command skills. The final two compose them for
workflows where orchestration saves time or avoids duplicate builds. Detailed
flags live in references and load only when needed.

Claude Code reads the same files through `.claude-plugin/plugin.json`. Codex
reads them through `.codex-plugin/plugin.json` and the per-skill
`agents/openai.yaml` presentation metadata. The Sneat AI marketplace indexes
this repository without copying instructions.

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
