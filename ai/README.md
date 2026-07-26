# WB agent skills

[`skills/wb-worktrees/SKILL.md`](skills/wb-worktrees/SKILL.md) is the
canonical, harness-neutral Agent Skill for safe branch creation with WB. Keep
the workflow here so Codex, Claude Code, and other Agent Skills clients execute
the same policy.

Claude Code reads the skill through the repository's
`.claude-plugin/plugin.json`; the Sneat AI marketplace indexes this repository
without copying the skill. Codex-specific presentation metadata lives beside
the same `SKILL.md` in `agents/openai.yaml` and does not fork the instructions.
