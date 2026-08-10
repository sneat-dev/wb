# Harness adapters

- **Claude Code:** the plugin manifest relies on recursive auto-discovery of
  `ai/skills/wb-merge` and `agents/wb-merger.md`, avoiding duplicate explicit
  lists. When this repository/plugin is installed, invoke the `wb-merger`
  subagent or load `$wb-merge`; do not copy these instructions into a local
  merger prompt.
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
Checked-in adapter files alone do not mean the merger is installed. Once
installed, the WB adapter supersedes copied legacy merger prompts; remove or
disable those copies instead of letting their raw-worktree, prefix-locked, or
ad hoc CI instructions compete with this contract.
