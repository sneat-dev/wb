package main

import (
	"fmt"
	"strings"

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

// skillsBannerSuppressedCommands are persistentCommandID values that must
// never trigger maybeWarnSkillsDrift: the skills family itself ('sync' is
// exactly how a user clears the drift; the banner would be noise inside its
// own output), 'self-update' (it already runs a real sync immediately after
// success, see selfupdate.go, so warning first would be stale by the time
// the command returns), 'version' (a provenance query; polluting it with an
// unrelated hint fights its purpose), and the PreToolUse guard, which runs on
// every single tool call of every agent on the machine and must stay as
// close to free as this map lookup.
var skillsBannerSuppressedCommands = map[string]bool{
	"self-update":              true,
	"version":                  true,
	"hooks agent pre-tool-use": true,
}

// maybeWarnSkillsDrift prints one low-noise line per present harness when
// that harness's installed Agent Skills were synced by a different wb
// version than the one running now (REQ: skills-drift-banner) -- including
// "never synced at all", which is exactly the failure this whole command
// family exists to close: an orchestrator missing a skill because nothing
// ever installed it.
//
// It is called from the root command's PersistentPreRunE, so it runs before
// almost every wb invocation. Every failure mode here -- no home directory,
// no Claude/Cursor/Codex config directory on this machine, an unreadable or
// corrupt marker -- is treated as "say nothing", never as a reason to fail
// the real command the caller actually asked for; a recover() backstops even
// a coding mistake in this best-effort path from ever taking wb down with it.
func maybeWarnSkillsDrift(cmd *cobra.Command) {
	defer func() { _ = recover() }()

	id := persistentCommandID(cmd)
	if strings.HasPrefix(id, "skills") || skillsBannerSuppressedCommands[id] {
		return
	}
	targets, err := presentSkillsTargets()
	if err != nil || len(targets) == 0 {
		// No present harness config directory means no Claude/Cursor/Codex
		// on this machine (or a HOME that resolves somewhere unusual);
		// never nag a caller who was never going to use those skills dirs.
		return
	}
	current := collectVersion().Version
	for _, target := range targets {
		status, statusErr := skills.ReadStatus(target.Dir)
		if statusErr != nil {
			continue
		}
		if !status.Drifted(current) {
			continue
		}
		fmt.Fprintln(cmd.ErrOrStderr(), skillsDriftMessage(target.Dir, status, current)) //nolint:errcheck
	}
}

// skillsDriftMessage is the one line maybeWarnSkillsDrift and 'wb skills
// hook run' both print, so a person or agent sees identical wording however
// they encountered the drift.
func skillsDriftMessage(dir string, status skills.Status, currentVersion string) string {
	if !status.Installed {
		return fmt.Sprintf("wb: Agent Skills are not installed in %s -- run `wb skills sync`", dir)
	}
	return fmt.Sprintf("wb: Agent Skills in %s were synced by wb %s, this is wb %s -- run `wb skills sync`", dir, status.SyncedWBVersion, currentVersion)
}
