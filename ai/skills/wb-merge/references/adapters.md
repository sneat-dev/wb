# Harness adapters

- **Claude Code:** `.claude-plugin/plugin.json` exposes the canonical
  `wb-merge` skill when this repository/plugin is installed. Invoke
  `$wb-merge`; do not copy these instructions into a local merger prompt.
- **Codex:** `.codex-plugin/plugin.json` exposes `ai/skills/`, and this
  skill's `agents/openai.yaml` supplies its presentation metadata when the
  repository plugin is installed. Invoke `$wb-merge`; the harness chooses the
  model.
- **GitHub Copilot CLI:** `.github/agents/wb-merger.agent.md` delegates to
  this skill from a checkout of this repository. Its frontmatter intentionally
  omits `model`.

The adapters are versioned source, not a marketplace distribution channel.
Until a WB distribution is installed in a harness, invoke the canonical skill
from this checkout rather than claiming it is globally available.
