// Package ai embeds ai/skills -- WB's canonical, harness-neutral Agent
// Skills -- directly into the wb binary.
//
// Every other consumer of ai/skills (Claude Code's own plugin auto-discovery,
// Codex's .codex-plugin manifest) reads it from a checked-out copy of this
// repository. That does nothing for the far more common case: wb installed
// as a standalone CLI (Homebrew, go install, a release archive) and run from
// an arbitrary project directory that is not, and has never been, a checkout
// of sneat-dev/wb. `wb skills sync` (internal/skills) exists to install these
// same skills into a harness's own skills directory (e.g. ~/.claude/skills)
// so they are available everywhere wb is, and it must work from the
// installed binary alone -- so the source of truth is embedded here rather
// than read from disk at a repository path that may not exist.
package ai

import "embed"

// SkillsFS holds every file under skills/ at build time: skills/<name>/
// SKILL.md plus each skill's references/ and agents/ subdirectories. Keep
// this the only //go:embed directive over ai/skills; internal/skills reads
// exclusively through this var so there is one embedded copy to keep in
// sync with the checked-in source.
//
//go:embed all:skills
var SkillsFS embed.FS
