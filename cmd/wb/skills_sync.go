package main

import (
	"encoding/json"
	"fmt"
	"io/fs"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/ai"
	"github.com/sneat-dev/wb/internal/skills"
)

func newSkillsSyncCmd() *cobra.Command {
	var (
		dir    string
		dryRun bool
		format string
	)
	command := &cobra.Command{
		Use:   "sync",
		Short: "Install or update WB's Agent Skills in a harness skills directory",
		Long: `Install or update WB's Agent Skills in a harness skills directory.

Copies every skill embedded in this wb build into the target directory
(default ~/.claude/skills), one subdirectory per skill, exactly as Claude
Code expects: <dir>/<skill-name>/SKILL.md plus its references/ and agents/.
It never reads a source checkout -- the embedded copy is the only input --
so it works from an installed binary alone.

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
			target := dir
			if target == "" {
				resolved, err := defaultHarnessSkillsDir()
				if err != nil {
					return fmt.Errorf("resolve default skills directory: %w", err)
				}
				target = resolved
			}
			source, err := fs.Sub(ai.SkillsFS, "skills")
			if err != nil {
				return err
			}
			report, err := skills.Sync(skills.Options{
				Source:    source,
				Dir:       target,
				WBVersion: collectVersion().Version,
				DryRun:    dryRun,
			})
			if err != nil {
				return err
			}
			return writeSkillsSyncReport(cmd, report, format)
		},
	}
	command.Flags().StringVar(&dir, "dir", "", "harness skills directory to install into (default ~/.claude/skills)")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "report what would change without writing anything")
	command.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return command
}

func writeSkillsSyncReport(cmd *cobra.Command, report skills.Report, format string) error {
	if format == "json" {
		payload := skillsSyncJSON{
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
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(payload); err != nil {
			return err
		}
	} else {
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
	}
	if len(report.Names(skills.Conflict)) > 0 {
		return &exitError{code: exitFindings, message: fmt.Sprintf(
			"skills sync: %d skill(s) could not be installed because a pre-existing, non-wb-managed directory already uses that name; see %s",
			len(report.Names(skills.Conflict)), report.Dir)}
	}
	return nil
}

func joinNames(names []string) string {
	joined := ""
	for index, name := range names {
		if index > 0 {
			joined += ", "
		}
		joined += name
	}
	return joined
}

type skillsSyncJSON struct {
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
