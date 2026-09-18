package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/layout"
)

func newLayoutCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "layout",
		Short: "Audit and clean local clone placement under --projects-root",
		Long: `Inspect whether local clones follow {host}/{owner}/{repository} under --projects-root.

  wb layout audit     report top-level, misowned, bad-host, and ok checkouts
  wb layout clean     remove safe top-level duplicates (dry-run by default)
  wb layout migrate   move legacy {owner}/{repository} clones to {host}/{owner}/{repository}

Canonical fleet members are {host}/{owner}/{repository} directories with a real
.git directory, where {host} is the literal forge hostname. A first-level entry
that is not a valid hostname is reported as a bad-host finding; its clones are
still read in place at the legacy {owner}/{repository} placement, so a fleet
that has not moved yet stays auditable and nothing is "fixed" for it. Linked
worktrees are ignored.`,
	}
	command.AddCommand(newLayoutAuditCmd())
	command.AddCommand(newLayoutCleanCmd())
	command.AddCommand(newLayoutMigrateCmd())
	return command
}

func newLayoutAuditCmd() *cobra.Command {
	var format, reportDir string
	command := &cobra.Command{
		Use:   "audit",
		Short: "Report non-canonical clone placement under --projects-root",
		Long: `Report every canonical clone under --projects-root and the remote URL its
path corresponds to.

A clone at <root>/{host}/{owner}/{repository} inverts to its remote URL by pure
path arithmetic — https://{host}/{owner}/{repository} — so the audit states it
without reading any configuration or the repository's own remote. A clone still
at the legacy {owner}/{repository} placement has no host level to invert and
reports the host its origin remote already names, which is the host level it
must move under.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := layout.Audit(cmd.Context(), projectsRoot)
			if err != nil {
				return err
			}
			if reportDir != "" {
				if err := writeLayoutAuditReports(reportDir, report); err != nil {
					return err
				}
			}
			if err := writeLayoutOutput(cmd, format, report.Markdown(), report); err != nil {
				return err
			}
			if layout.Failed(report) {
				return &exitError{
					code:    exitFindings,
					message: "layout findings reported; see the audit above",
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&reportDir, "report-dir", "", "write layout-audit.md/.yaml/.json to this directory")
	return command
}

func newLayoutCleanCmd() *cobra.Command {
	var (
		format, reportDir     string
		apply                 bool
		allowMissingCanonical bool
	)
	command := &cobra.Command{
		Use:   "clean",
		Short: "Remove safe top-level clones under --projects-root",
		Long: `Remove Git checkouts that sit directly under --projects-root when it is safe.

Safety requires a usable origin, a clean working tree (no dirty/stash/unpushed
state), and a canonical {host}/{owner}/{repository} clone unless
--allow-missing-canonical is set. Default mode is dry-run; pass --apply to
delete. A legacy {owner}/{repository} first level is never treated as a
removable top-level clone.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := layout.Clean(cmd.Context(), projectsRoot, layout.CleanOptions{
				Apply:                 apply,
				AllowMissingCanonical: allowMissingCanonical,
			})
			if err != nil {
				return err
			}
			if reportDir != "" {
				if err := writeLayoutCleanReports(reportDir, report); err != nil {
					return err
				}
			}
			if err := writeLayoutOutput(cmd, format, report.Markdown(), report); err != nil {
				return err
			}
			if layout.CleanFailed(report) {
				return &exitError{
					code:    exitFindings,
					message: "layout clean reported errors; see the actions above",
				}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "remove safe top-level clones (default is dry-run)")
	command.Flags().BoolVar(&allowMissingCanonical, "allow-missing-canonical", false, "allow removing a clean top-level clone even when the canonical path does not exist")
	command.Flags().StringVar(&format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&reportDir, "report-dir", "", "write layout-clean.md/.yaml/.json to this directory")
	return command
}

func newLayoutMigrateCmd() *cobra.Command {
	var (
		format, reportDir, undoID string
		apply, clonesOnly         bool
		includeTasks              []string
		includeActiveTasks        bool
	)
	command := &cobra.Command{
		Use:   "migrate [owner/repository...]",
		Short: "Move legacy clones to the host-level layout and repoint their worktrees",
		Long: `Move each canonical clone found at the legacy <root>/{owner}/{repository}
placement to <root>/{host}/{owner}/{repository}, taking {host} from the
clone's origin remote. With no arguments every legacy clone under the root is
covered; name owner/repository arguments to migrate only those.

Dry-run by default: prints the planned source and destination of every clone
and every linked worktree it would repoint. Pass --apply to move. A clone
already at the host level is reported done and left untouched, so an
interrupted or partial migration is completed by running the same command
again.

A clone is skipped, with a finding naming the reason, when it has no usable
origin, its origin owner/repository differs from its path, its origin host is
not a valid directory name, its destination already exists, a Git operation is
in progress, or a live Work Log claim holds it or a linked worktree.
Uncommitted changes are never a refusal reason. The command exits with the
findings code whenever any clone is skipped or fails.

Pass --include-task <task> (repeatable) or --include-active-tasks to lift only
the live-claim refusal, for the named tasks or for every active task. Every
other refusal (a busy process, a parked session, a Git operation in progress,
and the destination-exists check) still applies, including the re-check just
before each move. An --include-task name matching no live claim in any
resolved home is a usage error before anything moves. When an included
clone moves, its claim's relocation intent and receipt are recorded so land,
guard and cleanup resolve it at its new path; an included active task's
in-clone checkout moves and repoints with its clone but is not relocated to
the store, since relocation stays limited to finished tasks.

After moving clones, this is also the one command for the unified layout: it
relocates every managed task checkout whose placement differs from the
machine's store-mode placement, by calling the existing
` + "`wb worktree relocate`" + ` implementation, never a copy of it. A checkout is
left in place, with a finding, when a clone refusal condition applies to it,
its task lock is held, or its destination already exists — the clone still
migrates. Repository-local store mode leaves in-clone checkouts where they
are. A linked worktree with no WB task identity is repointed only and
reported unmanaged. Pass --clones-only to skip relocation entirely and get
exactly the clone-move-and-repoint behaviour this command had before it.

--apply writes a manifest under <root>/.wb/layout-migrations/<id>/ before its
first move; pass --undo <id> to reverse every clone that manifest records as
done, and every relocation it recorded, reversing relocations before the
clone moves they depend on.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "markdown", "yaml", "json"); err != nil {
				return err
			}
			report, err := layout.Migrate(cmd.Context(), projectsRoot, layout.MigrateOptions{
				Repositories:       args,
				Apply:              apply,
				ClonesOnly:         clonesOnly,
				UndoID:             undoID,
				IncludeTasks:       includeTasks,
				IncludeActiveTasks: includeActiveTasks,
			})
			if err != nil {
				var unknownTask *layout.UnknownIncludeTaskError
				if errors.As(err, &unknownTask) {
					return &exitError{code: exitUsage, message: err.Error()}
				}
				// A manifest-write failure returns the partial report built
				// so far alongside the error: what was already recorded and
				// moved before the write failed must still reach the
				// operator, not be silently discarded behind a bare error.
				if report.SchemaVersion != 0 {
					if writeErr := writeLayoutOutput(cmd, format, report.Markdown(), report); writeErr != nil {
						return errors.Join(err, writeErr)
					}
				}
				return err
			}
			if reportDir != "" {
				if err := writeLayoutMigrateReports(reportDir, report); err != nil {
					return err
				}
			}
			if err := writeLayoutOutput(cmd, format, report.Markdown(), report); err != nil {
				return err
			}
			if layout.MigrateFailed(report) {
				return &exitError{
					code:    exitFindings,
					message: "layout migrate reported findings; see the plan above",
				}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "move eligible clones (default is dry-run)")
	command.Flags().BoolVar(&clonesOnly, "clones-only", false, "move clones and repoint worktrees only; skip relocating managed task checkouts")
	command.Flags().StringVar(&undoID, "undo", "", "reverse the clones a previous --apply's manifest <id> recorded as done")
	command.Flags().StringArrayVar(&includeTasks, "include-task", nil, "lift the live-claim refusal for this task (repeatable); an unknown task is a usage error")
	command.Flags().BoolVar(&includeActiveTasks, "include-active-tasks", false, "lift the live-claim refusal for every active task")
	command.Flags().StringVar(&format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&reportDir, "report-dir", "", "write layout-migrate.md/.yaml/.json to this directory")
	return command
}

func writeLayoutMigrateReports(directory string, report layout.MigrateReport) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "layout-migrate.md"), []byte(report.Markdown()), 0o644); err != nil {
		return err
	}
	raw, err := yaml.Marshal(report)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "layout-migrate.yaml"), raw, 0o644); err != nil {
		return err
	}
	raw, err = json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "layout-migrate.json"), append(raw, '\n'), 0o644)
}

func writeLayoutOutput(cmd *cobra.Command, format, markdown string, value any) error {
	switch format {
	case "markdown":
		_, err := fmt.Fprint(cmd.OutOrStdout(), markdown)
		return err
	case "yaml":
		raw, err := yaml.Marshal(value)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(raw)
		return err
	case "json":
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}

func writeLayoutAuditReports(directory string, report layout.Report) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "layout-audit.md"), []byte(report.Markdown()), 0o644); err != nil {
		return err
	}
	raw, err := yaml.Marshal(report)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "layout-audit.yaml"), raw, 0o644); err != nil {
		return err
	}
	raw, err = json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "layout-audit.json"), append(raw, '\n'), 0o644)
}

func writeLayoutCleanReports(directory string, report layout.CleanReport) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "layout-clean.md"), []byte(report.Markdown()), 0o644); err != nil {
		return err
	}
	raw, err := yaml.Marshal(report)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "layout-clean.yaml"), raw, 0o644); err != nil {
		return err
	}
	raw, err = json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "layout-clean.json"), append(raw, '\n'), 0o644)
}
