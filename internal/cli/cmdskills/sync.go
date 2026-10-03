package cmdskills

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"strings"

	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/skillsync"
	skillscmd "github.com/strongo/cli-helpers/skillsync/cobracmd"
	"github.com/strongo/cli-helpers/skillsync/githubrelease"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/wbskills"
)

func newSkillsSyncCmd(runtime shared.Runtime, deps SyncDependencies) *cobra.Command {
	cfg, cfgErr := deps.Config()
	options := skillscmd.CommandOptions{
		Home: deps.Home, Getenv: deps.Getenv,
		Short:  "Install or update WB's Agent Skills in a harness skills directory",
		Errors: skillsSyncErrors{runtime: runtime},
		Legacy: skillsync.LegacyImport{MarkerFile: wbskills.LegacyMarker, Plugin: wbskills.PluginIdentity()},
		Resolver: skillsync.ReleaseResolver{
			Source:         githubrelease.Source{},
			CurrentVersion: cfg.CurrentVersion,
		},
		Renderer: writeSkillsSyncReports,
	}
	command := skillscmd.NewSync(cfg, options)
	command.Long = `Install or update WB's Agent Skills in a harness skills directory.

The default source is the immutable WB plugin revision embedded in this wb
binary, so an ordinary sync needs no source checkout and no network access.
Use --newer-compatible only to explicitly select a newer compatible plugin
release from GitHub.

Known harnesses and their skills directories:

  claude  ~/.claude/skills   (or $CLAUDE_CONFIG_DIR/skills)
  cursor  ~/.cursor/skills
  codex   ~/.codex/skills    (or $CODEX_HOME/skills)

With no --dir or --harness, every present harness is synced. If none are
present, Claude is the fallback. --harness names one or more targets even when
the harness is not installed; --harness all selects every known harness.
--dir targets an explicit path and cannot be combined with --harness.

The shared strongo/cli-helpers skillsync engine owns target locking, verified
legacy-marker import, plugin-scoped ownership, conflict handling, crash-safe
replacement, and provider-neutral state. WB supplies only its embedded plugin,
command wording, JSON compatibility, and exit-code mapping.`
	if cfgErr != nil {
		command.RunE = func(*cobra.Command, []string) error {
			return skillsSyncErrors{runtime: runtime}.Failure(fmt.Errorf("prepare embedded WB skills: %w", cfgErr))
		}
	}
	return command
}

type skillsSyncErrors struct{ runtime shared.Runtime }

func (e skillsSyncErrors) Failure(err error) error {
	var usage *skillscmd.UsageError
	if errors.As(err, &usage) {
		return e.runtime.ExitError(shared.ExitUsage, err.Error())
	}
	return e.runtime.ExitError(shared.ExitFindings, "skills sync: "+err.Error())
}

func (e skillsSyncErrors) Conflict(report skillsync.Report) error {
	return e.runtime.ExitError(shared.ExitFindings, fmt.Sprintf(
		"skills sync: %d skill(s) could not be installed because another plugin or an unmanaged directory already owns the name; see %s",
		len(report.Names(skillsync.Conflict)), report.Dir))
}

func writeSkillsSyncReports(out io.Writer, results []skillscmd.TargetResult, format string) error {
	if format == "json" {
		return writeSkillsSyncJSON(out, results)
	}
	for _, result := range results {
		if err := writeSkillsSyncText(out, result); err != nil {
			return err
		}
	}
	return nil
}

func writeSkillsSyncJSON(out io.Writer, results []skillscmd.TargetResult) error {
	if len(results) == 0 {
		return nil
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if len(results) == 1 {
		return encoder.Encode(skillsSyncPayload(results[0]))
	}
	payloads := make([]skillsSyncJSON, 0, len(results))
	for _, result := range results {
		payloads = append(payloads, skillsSyncPayload(result))
	}
	return encoder.Encode(skillsSyncMultiJSON{
		DryRun:    results[0].Report.DryRun,
		WBVersion: results[0].Report.CLIVersion,
		Targets:   payloads,
	})
}

func writeSkillsSyncText(out io.Writer, result skillscmd.TargetResult) error {
	report := result.Report
	if result.Err != nil {
		if _, err := fmt.Fprintf(out, "wb skills sync failed: %s\n", result.Dir); err != nil {
			return err
		}
		_, err := fmt.Fprintf(out, "  error: %v\n", result.Err)
		return err
	}
	verb := "synced"
	if report.DryRun {
		verb = "would sync"
	}
	if _, err := fmt.Fprintf(out, "wb skills %s: %s\n", verb, report.Dir); err != nil {
		return err
	}
	for _, line := range []struct {
		label  string
		action skillsync.Action
	}{
		{"added", skillsync.Added},
		{"updated", skillsync.Updated},
		{"unchanged", skillsync.Unchanged},
		{"removed", skillsync.Removed},
		{"conflicts (left untouched)", skillsync.Conflict},
	} {
		names := report.Names(line.action)
		if len(names) == 0 {
			continue
		}
		if _, err := fmt.Fprintf(out, "  %s: %s\n", line.label, strings.Join(names, ", ")); err != nil {
			return err
		}
	}
	if !report.Changed() && len(report.Names(skillsync.Conflict)) == 0 {
		_, err := fmt.Fprintln(out, "  nothing to do; skills already match this wb build")
		return err
	}
	return nil
}

func skillsSyncPayload(result skillscmd.TargetResult) skillsSyncJSON {
	report := result.Report
	payload := skillsSyncJSON{
		Harness:        result.Harness,
		Dir:            result.Dir,
		DryRun:         report.DryRun,
		PriorWBVersion: priorSkillsWBVersion(report),
		WBVersion:      report.CLIVersion,
		Added:          report.Names(skillsync.Added),
		Updated:        report.Names(skillsync.Updated),
		Unchanged:      report.Names(skillsync.Unchanged),
		Removed:        report.Names(skillsync.Removed),
		Conflicts:      report.Names(skillsync.Conflict),
	}
	switch {
	case result.Err != nil:
		payload.Status = "failed"
		payload.Error = result.Err.Error()
	case len(payload.Conflicts) > 0:
		payload.Status = "conflict"
	case report.Changed():
		payload.Status = "changed"
	default:
		payload.Status = "unchanged"
	}
	return payload
}

func priorSkillsWBVersion(report skillsync.Report) string {
	for _, bundle := range report.Bundles {
		if bundle.Plugin == wbskills.PluginIdentity() {
			return bundle.PriorCLIVersion
		}
	}
	return ""
}

type skillsSyncJSON struct {
	Status         string   `json:"status"`
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
	Error          string   `json:"error,omitempty"`
}

type skillsSyncMultiJSON struct {
	DryRun    bool             `json:"dry_run"`
	WBVersion string           `json:"wb_version"`
	Targets   []skillsSyncJSON `json:"targets"`
}
