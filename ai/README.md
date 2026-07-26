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
