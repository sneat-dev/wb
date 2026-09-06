package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/skillsync"
	skillscmd "github.com/strongo/cli-helpers/skillsync/cobracmd"
	"github.com/strongo/cli-helpers/skillsync/githubrelease"

	"github.com/sneat-dev/wb/ai"
)

const (
	wbSkillsLegacyMarker  = ".wb-skills-sync.json"
	wbSkillsPluginVersion = "0.0.0"
	wbSkillsUnknownSource = "0000000000000000000000000000000000000000"
)

var (
	wbSkillsCLI    = skillsync.Identity{Publisher: "sneat-dev", Name: "wb"}
	wbSkillsPlugin = skillsync.PluginIdentity{Publisher: "sneat-dev", Name: "wb"}
)

func newSkillsSyncCmd() *cobra.Command {
	cfg, cfgErr := newSkillsSyncConfig()
	options := skillscmd.CommandOptions{
		Short:  "Install or update WB's Agent Skills in a harness skills directory",
		Errors: skillsSyncErrors{},
		Legacy: skillsync.LegacyImport{MarkerFile: wbSkillsLegacyMarker, Plugin: wbSkillsPlugin},
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
			return skillsSyncErrors{}.Failure(fmt.Errorf("prepare embedded WB skills: %w", cfgErr))
		}
	}
	return command
}

func newSkillsSyncConfig() (skillsync.Config, error) {
	source, err := fs.Sub(ai.SkillsFS, "skills")
	if err != nil {
		return skillsync.Config{}, err
	}
	digest, err := skillsync.Digest(source)
	if err != nil {
		return skillsync.Config{}, err
	}
	build := collectVersion()
	revision := build.Revision
	if len(revision) != 40 {
		revision = wbSkillsUnknownSource
	}
	pluginVersion := build.Version
	if _, err := skillsync.CompareVersions(pluginVersion, pluginVersion); err != nil {
		pluginVersion = wbSkillsPluginVersion
	}
	bundle, err := skillsync.EmbeddedBundle(skillsync.BundleDescriptor{
		Plugin: wbSkillsPlugin,
		Source: skillsync.Source{
			Repository: "github.com/sneat-dev/wb",
			Path:       "ai/skills",
			Revision:   revision,
			Version:    pluginVersion,
			Digest:     digest,
		},
	}, source)
	if err != nil {
		return skillsync.Config{}, err
	}
	return skillsync.Config{
		CLI:            wbSkillsCLI,
		CurrentVersion: build.Version,
		Bundles:        []skillsync.Bundle{bundle},
	}, nil
}

type skillsSyncErrors struct{}

func (skillsSyncErrors) Failure(err error) error {
	var usage *skillscmd.UsageError
	if errors.As(err, &usage) {
		return &exitError{code: exitUsage, message: err.Error()}
	}
	return &exitError{code: exitFindings, message: "skills sync: " + err.Error()}
}

func (skillsSyncErrors) Conflict(report skillsync.Report) error {
	return &exitError{code: exitFindings, message: fmt.Sprintf(
		"skills sync: %d skill(s) could not be installed because another plugin or an unmanaged directory already owns the name; see %s",
		len(report.Names(skillsync.Conflict)), report.Dir)}
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
		if bundle.Plugin == wbSkillsPlugin {
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
