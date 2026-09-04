package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/skills"
)

// newSkillsCmd groups everything about installing WB's own Agent Skills
// (embedded from ai/skills, see package ai) into a harness's skills
// directory -- Claude Code's ~/.claude/skills, Cursor's ~/.cursor/skills,
// Codex's ~/.codex/skills -- so an orchestrating agent has them available
// in any project, not only inside a checkout of sneat-dev/wb.
//
// See ai/skills/wb-skills/SKILL.md for the agent-facing walkthrough this
// command family exists to make discoverable in the first place: the defect
// that motivated it was an orchestrator session missing `wb session park`
// entirely because nothing had ever installed it where that session's
// harness looks for skills.
func newSkillsCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "skills",
		Short: "Install WB's Agent Skills into a harness's skills directory",
		Long: `Install WB's Agent Skills into a harness's skills directory.

WB ships agent-facing skills under ai/skills/ in its own repository, and
Claude Code auto-discovers them there for a session working inside that
checkout. A session orchestrating any other repository, with wb installed
globally, has never had them at all -- there is nothing to auto-discover
outside a wb checkout.

'wb skills sync' closes that gap by copying every shipped skill into each
present harness's skills directory (Claude Code, Cursor, Codex) once, so
it is available everywhere wb is. It runs automatically after
'wb self-update'.`,
	}
	command.AddCommand(newSkillsSyncCmd())
	command.AddCommand(newSkillsHookCmd())
	return command
}

// skillsDriftMessage is printed by the SessionStart hook. Ordinary WB
// invocations stay quiet: repeating one warning on every tool call consumed
// more agent context than the warning protected and made private feature builds
// particularly noisy. A verified self-update still synchronizes skills
// immediately, while SessionStart reports drift once when it can affect work.
func skillsDriftMessage(dir string, status skills.Status, currentVersion string) string {
	if !status.Installed {
		return fmt.Sprintf("wb: Agent Skills are not installed in %s -- run `wb skills sync`", dir)
	}
	return fmt.Sprintf("wb: Agent Skills in %s were synced by wb %s, this is wb %s -- run `wb skills sync`", dir, status.SyncedWBVersion, currentVersion)
}
