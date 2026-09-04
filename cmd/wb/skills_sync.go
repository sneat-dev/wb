package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/ai"
	"github.com/sneat-dev/wb/internal/skills"
)

func newSkillsSyncCmd() *cobra.Command {
	var (
		dir     string
		harness []string
		dryRun  bool
		format  string
	)
	command := &cobra.Command{
		Use:   "sync",
		Short: "Install or update WB's Agent Skills in a harness skills directory",
		Long: `Install or update WB's Agent Skills in a harness skills directory.

Copies every skill embedded in this wb build into each target directory,
one subdirectory per skill, exactly as Agent Skills expect:
<dir>/<skill-name>/SKILL.md plus its references/ and agents/.
It never reads a source checkout -- the embedded copy is the only input --
so it works from an installed binary alone.

Known harnesses and their skills directories:

  claude  ~/.claude/skills   (or $CLAUDE_CONFIG_DIR/skills)
  cursor  ~/.cursor/skills
  codex   ~/.codex/skills    (or $CODEX_HOME/skills)

With no --dir or --harness, every present harness is synced (its config
directory already exists). If none are present, claude is the fallback so
a first sync on a fresh machine still has a well-defined target.
--harness names one or more of those even when the harness is not
installed yet; --harness all selects every known harness.
--dir targets an explicit path and cannot be combined with --harness.

Idempotent: a second run with nothing new to ship reports every skill
unchanged and writes nothing. A skill this build no longer ships, previously
installed by an earlier sync, is removed; a directory that was never
installed by wb -- a name collision this command did not create -- is
reported as a conflict and left untouched rather than overwritten.

A marker file recording the wb version that performed the sync is written
next to the installed skills. 'wb' compares it against the running binary on
every invocation and prints a one-line reminder on stderr when they diverge.

Run this after 'wb self-update' picks up a new wb -- which it already does
automatically -- or any time an agent's skills look stale.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			targets, err := resolveSkillsSyncTargets(dir, harness)
			if err != nil {
				return err
			}
			source, err := fs.Sub(ai.SkillsFS, "skills")
			if err != nil {
				return err
			}
			version := collectVersion().Version
			reports := make([]skills.Report, 0, len(targets))
			for _, target := range targets {
				report, syncErr := skills.Sync(skills.Options{
					Source:    source,
					Dir:       target.Dir,
					WBVersion: version,
					DryRun:    dryRun,
				})
				if syncErr != nil {
					return syncErr
				}
				reports = append(reports, report)
			}
			return writeSkillsSyncReports(cmd, targets, reports, format)
		},
	}
	command.Flags().StringVar(&dir, "dir", "", "explicit harness skills directory (mutually exclusive with --harness)")
	command.Flags().StringArrayVar(&harness, "harness", nil, "named harness to install into: claude, cursor, codex, or all (repeatable; default: every present harness)")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "report what would change without writing anything")
	command.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return command
}

func writeSkillsSyncReports(cmd *cobra.Command, targets []skillsTarget, reports []skills.Report, format string) error {
	if format == "json" {
		if err := writeSkillsSyncJSON(cmd, targets, reports); err != nil {
			return err
		}
	} else {
		for _, report := range reports {
			if err := writeSkillsSyncText(cmd, report); err != nil {
				return err
			}
		}
	}
	return skillsSyncConflictError(targets, reports)
}

func writeSkillsSyncJSON(cmd *cobra.Command, targets []skillsTarget, reports []skills.Report) error {
	if len(reports) == 0 {
		return nil
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	if len(reports) == 1 {
		target := skillsTarget{}
		if len(targets) > 0 {
			target = targets[0]
		}
		return encoder.Encode(skillsSyncPayload(target, reports[0]))
	}
	payloads := make([]skillsSyncJSON, 0, len(reports))
	for i, report := range reports {
		payloads = append(payloads, skillsSyncPayload(targets[i], report))
	}
	return encoder.Encode(skillsSyncMultiJSON{
		DryRun:    reports[0].DryRun,
		WBVersion: reports[0].WBVersion,
		Targets:   payloads,
	})
}

func writeSkillsSyncText(cmd *cobra.Command, report skills.Report) error {
	out := cmd.OutOrStdout()
	verb := "synced"
	if report.DryRun {
		verb = "would sync"
	}
	if _, err := fmt.Fprintf(out, "%s %s: %s\n", "wb skills", verb, report.Dir); err != nil {
		return err
	}
	for _, line := range []struct {
		label  string
		action skills.Action
	}{
		{"added", skills.Added},
		{"updated", skills.Updated},
		{"unchanged", skills.Unchanged},
		{"removed", skills.Removed},
		{"conflicts (left untouched)", skills.Conflict},
	} {
		names := report.Names(line.action)
		if len(names) == 0 {
			continue
		}
		if _, err := fmt.Fprintf(out, "  %s: %s\n", line.label, joinNames(names)); err != nil {
			return err
		}
	}
	if !report.Changed() && len(report.Names(skills.Conflict)) == 0 {
		if _, err := fmt.Fprintln(out, "  nothing to do; skills already match this wb build"); err != nil {
			return err
		}
	}
	return nil
}

func skillsSyncConflictError(targets []skillsTarget, reports []skills.Report) error {
	var dirs []string
	total := 0
	for i, report := range reports {
		n := len(report.Names(skills.Conflict))
		if n == 0 {
			continue
		}
		total += n
		dir := report.Dir
		if i < len(targets) && targets[i].Dir != "" {
			dir = targets[i].Dir
		}
		dirs = append(dirs, dir)
	}
	if total == 0 {
		return nil
	}
	return &exitError{code: exitFindings, message: fmt.Sprintf(
		"skills sync: %d skill(s) could not be installed because a pre-existing, non-wb-managed directory already uses that name; see %s",
		total, strings.Join(dirs, ", "))}
}

func skillsSyncPayload(target skillsTarget, report skills.Report) skillsSyncJSON {
	return skillsSyncJSON{
		Harness:        target.Harness,
		Dir:            report.Dir,
		DryRun:         report.DryRun,
		PriorWBVersion: report.PriorWBVersion,
		WBVersion:      report.WBVersion,
		Added:          report.Names(skills.Added),
		Updated:        report.Names(skills.Updated),
		Unchanged:      report.Names(skills.Unchanged),
		Removed:        report.Names(skills.Removed),
		Conflicts:      report.Names(skills.Conflict),
	}
}

func joinNames(names []string) string {
	return strings.Join(names, ", ")
}

type skillsSyncJSON struct {
	Harness        string   `json:"harness,omitempty"`
	Dir            string   `json:"dir"`
	DryRun         bool     `json:"dry_run"`
	PriorWBVersion string   `json:"prior_wb_version,omitempty"`
	WBVersion      string   `json:"wb_version"`
	Added          []string `json:"added,omitempty"`
	Updated        []string `json:"updated,omitempty"`
	Unchanged      []string `json:"unchanged,omitempty"`
	Removed        []string `json:"removed,omitempty"`
	Conflicts      []string `json:"conflicts,omitempty"`
}

type skillsSyncMultiJSON struct {
	DryRun    bool             `json:"dry_run"`
	WBVersion string           `json:"wb_version"`
	Targets   []skillsSyncJSON `json:"targets"`
}
