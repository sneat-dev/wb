package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

// markerOutcome is one checkout's result, in the shape --format json emits.
type markerOutcome struct {
	Path           string `json:"path"`
	Kind           string `json:"kind"`
	Repository     string `json:"repository,omitempty"`
	Task           string `json:"task,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Writable       bool   `json:"writable"`
	MarkerWritten  bool   `json:"marker_written"`
	ExcludeWritten bool   `json:"exclude_written"`
	Error          string `json:"error,omitempty"`
}

func newWorktreeMarkerCmd() *cobra.Command {
	var fleet, dryRun bool
	var format, base string
	command := &cobra.Command{
		Use:   "marker [checkout-path]",
		Short: "Write the per-checkout .worktree.md marker and its ignore rule",
		Long: `Write ` + checkoutmarker.FileName + ` into a checkout, saying what that checkout is.

Every checkout gets one — a canonical clone and a linked worktree alike. The
marker states whether this checkout may be written to, which repository it
belongs to, and, for a worktree, which task and branch it carries. An agent
reads one file and knows where it is.

That universality is the point. A warning file present only in canonical
clones would make a MISSING file read as "nothing objects here", which is the
wrong default for a checkout WB has not reached yet. With a marker everywhere,
absence means "unknown, verify" instead.

The marker is never committed. WB writes it alongside an anchored rule in the
repository's Git exclude file, so ` + "`git status`" + ` stays clean and WB's own hooks
— which refuse an untracked path — are never tripped by it. One rule in the
common Git directory covers the canonical clone and every worktree cut from
it.

Re-running changes nothing once a marker is current: only a marker whose
content would differ by more than its timestamp is rewritten.

--fleet refreshes every canonical clone under --projects-root and every linked
worktree registered to one.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if fleet && len(args) == 1 {
				return fmt.Errorf("--fleet refreshes every checkout; do not also name one")
			}
			checkouts, err := markerCheckouts(cmd.Context(), fleet, args)
			if err != nil {
				return err
			}
			options := checkoutmarker.DescribeOptions{
				ProjectsRoot: projectsRoot,
				BaseBranch:   base,
				Version:      "wb " + buildinfo.Version(),
			}
			outcomes := make([]markerOutcome, 0, len(checkouts))
			failures := 0
			for _, checkout := range checkouts {
				outcome := applyCheckoutMarker(checkout, options, dryRun)
				if outcome.Error != "" {
					failures++
				}
				outcomes = append(outcomes, outcome)
			}
			if err := renderMarkerOutcomes(cmd, format, dryRun, outcomes); err != nil {
				return err
			}
			if failures > 0 {
				return &exitError{code: exitFindings, message: fmt.Sprintf("%d checkout(s) could not be described", failures)}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&fleet, "fleet", false, "refresh every canonical clone under --projects-root and every worktree registered to one")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "report what would change without writing anything")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().StringVar(&base, "base", "main", "protected canonical base branch named in the marker")
	return command
}

// applyCheckoutMarker describes one checkout and writes its marker.
//
// A fleet sweep never stops on one bad checkout. A single unreadable clone
// among forty must not leave the other thirty-nine unmarked, so the failure is
// recorded against that path and the sweep continues; the command still exits
// non-zero so nothing reads a partial refresh as a complete one.
func applyCheckoutMarker(path string, options checkoutmarker.DescribeOptions, dryRun bool) markerOutcome {
	inspection, err := checkoutmarker.Describe(path, options)
	if err != nil {
		return markerOutcome{Path: path, Error: err.Error()}
	}
	descriptor := inspection.Descriptor
	outcome := markerOutcome{
		Path:       descriptor.CheckoutPath,
		Kind:       string(descriptor.Kind),
		Repository: descriptor.Repository,
		Task:       descriptor.Task,
		Branch:     descriptor.Branch,
		Writable:   descriptor.Writable,
	}
	if dryRun {
		outcome.MarkerWritten, outcome.ExcludeWritten = markerWouldChange(inspection)
		return outcome
	}
	result, err := checkoutmarker.Apply(descriptor, inspection.ExcludePath)
	if err != nil {
		outcome.Error = err.Error()
		return outcome
	}
	outcome.MarkerWritten = result.MarkerWritten
	outcome.ExcludeWritten = result.ExcludeWritten
	return outcome
}

// markerWouldChange answers the dry-run question without touching disk.
func markerWouldChange(inspection checkoutmarker.Inspection) (marker, exclude bool) {
	rendered := checkoutmarker.Render(inspection.Descriptor)
	existing, err := os.ReadFile(filepath.Join(inspection.Descriptor.CheckoutPath, checkoutmarker.FileName))
	marker = err != nil || !checkoutmarker.Equivalent(string(existing), rendered)
	excludeContents, err := os.ReadFile(inspection.ExcludePath)
	if err != nil {
		return marker, true
	}
	for _, line := range strings.Split(string(excludeContents), "\n") {
		if strings.TrimSpace(line) == checkoutmarker.ExcludePattern {
			return marker, false
		}
	}
	return marker, true
}

// markerCheckouts resolves which checkouts to mark.
func markerCheckouts(ctx context.Context, fleet bool, args []string) ([]string, error) {
	if !fleet {
		if len(args) == 1 {
			return []string{args[0]}, nil
		}
		return []string{"."}, nil
	}
	repositories, err := discover.ScanLocal(projectsRoot)
	if err != nil {
		return nil, fmt.Errorf("scan local repositories: %w", err)
	}
	seen := map[string]bool{}
	var checkouts []string
	for _, repository := range repositories {
		if filterFlag != "" && !strings.Contains(repository.Slug(), filterFlag) {
			continue
		}
		canonical := filepath.Join(projectsRoot, repository.Org, repository.Name)
		for _, path := range append([]string{canonical}, registeredWorktrees(ctx, canonical)...) {
			// Dedupe on the resolved path. Git reports physical paths, so the
			// canonical clone comes back from `git worktree list` in a
			// different spelling than the one built from --projects-root, and
			// a string-keyed set would visit it twice.
			key := resolvedPath(path)
			if seen[key] {
				continue
			}
			seen[key] = true
			checkouts = append(checkouts, path)
		}
	}
	sort.Strings(checkouts)
	return checkouts, nil
}

// registeredWorktrees lists the linked worktrees a canonical clone owns.
//
// This is the one place the marker work shells out to Git, and it is confined
// to the fleet sweep: only Git knows which worktrees a clone has registered,
// and a directory walk would both miss relocated worktrees and wander into
// build trees. A clone Git cannot read contributes no worktrees rather than
// failing the sweep.
func registeredWorktrees(ctx context.Context, canonical string) []string {
	command := exec.CommandContext(ctx, "git", "-C", canonical, "worktree", "list", "--porcelain")
	command.Env = console.Env()
	output, err := command.Output()
	if err != nil {
		return nil
	}
	var paths []string
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		path, found := strings.CutPrefix(scanner.Text(), "worktree ")
		if !found {
			continue
		}
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "" || resolvedPath(path) == resolvedPath(canonical) {
			continue
		}
		// Git keeps listing a worktree whose directory is gone until someone
		// prunes it. Marking is not the place to report that — `wb worktree
		// orphans` is — and letting stale registrations fail here made a fleet
		// sweep exit non-zero every run, which is how a useful signal gets
		// ignored.
		if _, err := os.Stat(path); err != nil {
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

// resolvedPath follows symlinks so two spellings of one directory compare
// equal. A path that cannot be resolved keeps its cleaned form, which is still
// a usable key.
func resolvedPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

func renderMarkerOutcomes(cmd *cobra.Command, format string, dryRun bool, outcomes []markerOutcome) error {
	out := cmd.OutOrStdout()
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(outcomes)
	}
	changed := 0
	for _, outcome := range outcomes {
		if outcome.Error != "" {
			if err := writeFormat(out, "✗ %s: %s\n", outcome.Path, outcome.Error); err != nil {
				return err
			}
			continue
		}
		state := "current"
		switch {
		case outcome.MarkerWritten && outcome.ExcludeWritten:
			state = "marker + ignore rule"
		case outcome.MarkerWritten:
			state = "marker"
		case outcome.ExcludeWritten:
			state = "ignore rule"
		}
		if state != "current" {
			changed++
			if dryRun {
				state = "would write " + state
			} else {
				state = "wrote " + state
			}
		}
		if err := writeFormat(out, "%s %s: %s\n", markerSymbol(outcome), outcome.Path, state); err != nil {
			return err
		}
	}
	if len(outcomes) > 1 {
		return writeFormat(out, "\n%d checkout(s), %d changed\n", len(outcomes), changed)
	}
	return nil
}

func markerSymbol(outcome markerOutcome) string {
	if outcome.Kind == string(checkoutmarker.KindCanonical) {
		return "🔒"
	}
	return "✎"
}

// markCreatedCheckouts refreshes the marker for every worktree a create call
// produced, and for the canonical clone each was cut from.
//
// It is deliberately best-effort. `wb worktree create` succeeding is the
// result the caller depends on; a marker WB could not write is a diagnostic,
// not a reason to fail a checkout that already exists on disk and already
// carries a claim.
func markCreatedCheckouts(command *cobra.Command, base string, results []worktrees.CreateResult) {
	options := checkoutmarker.DescribeOptions{
		ProjectsRoot: projectsRoot,
		BaseBranch:   base,
		Version:      "wb " + buildinfo.Version(),
	}
	seen := map[string]bool{}
	for _, result := range results {
		paths := []string{result.WorktreeDir}
		if owner, repository, found := strings.Cut(result.Repository, "/"); found {
			paths = append(paths, filepath.Join(projectsRoot, owner, repository))
		}
		for _, path := range paths {
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			if outcome := applyCheckoutMarker(path, options, false); outcome.Error != "" {
				_ = writeFormat(command.ErrOrStderr(), "warning: could not write %s in %s: %s\n",
					checkoutmarker.FileName, path, outcome.Error)
			}
		}
	}
}

// refreshSyncedCheckoutMarkers marks every canonical clone a sync touched.
//
// Only the clones, not their worktrees: sync operates on canonical clones, and
// enumerating every worktree of every repository would turn a fleet sync into
// a second fleet walk for a file that `wb worktree marker --fleet` refreshes
// on demand anyway.
func refreshSyncedCheckoutMarkers(results []fleetsync.Result, projectsRoot string, errOut io.Writer) {
	options := checkoutmarker.DescribeOptions{
		ProjectsRoot: projectsRoot,
		BaseBranch:   "main",
		Version:      "wb " + buildinfo.Version(),
	}
	failures := 0
	for _, result := range results {
		if result.Status == fleetsync.Failed || result.Repo.Org == "" || result.Repo.Name == "" {
			continue
		}
		path := filepath.Join(projectsRoot, result.Repo.Org, result.Repo.Name)
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			continue
		}
		if outcome := applyCheckoutMarker(path, options, false); outcome.Error != "" {
			failures++
		}
	}
	if failures > 0 {
		_, _ = fmt.Fprintf(errOut, "warning: %d clone(s) could not be given a %s; run `wb worktree marker --fleet` for detail\n", failures, checkoutmarker.FileName)
	}
}

// markRenamedCheckouts refreshes the marker of every worktree a recycle moved.
//
// Best-effort for the same reason as create: the move already happened and is
// already recorded, so a marker WB could not rewrite is a diagnostic rather
// than a reason to report a completed rename as a failure.
func markRenamedCheckouts(command *cobra.Command, base string, results []worktrees.RenameResult) {
	options := checkoutmarker.DescribeOptions{
		ProjectsRoot: projectsRoot,
		BaseBranch:   base,
		Version:      "wb " + buildinfo.Version(),
	}
	for _, result := range results {
		if !result.Applied || result.NewWorktreeDir == "" {
			continue
		}
		if outcome := applyCheckoutMarker(result.NewWorktreeDir, options, false); outcome.Error != "" {
			_ = writeFormat(command.ErrOrStderr(), "warning: could not refresh %s in %s: %s\n",
				checkoutmarker.FileName, result.NewWorktreeDir, outcome.Error)
		}
	}
}

// markRelocatedCheckouts refreshes the generated location projection after a
// successful physical move. Failure is a warning: the durable relocation
// receipt and Git registration already establish the completed operation, and
// a later `wb worktree marker --fleet` can safely repair this convenience file.
func markRelocatedCheckouts(command *cobra.Command, results []worktrees.RelocateResult) {
	options := checkoutmarker.DescribeOptions{
		ProjectsRoot: projectsRoot,
		BaseBranch:   "main",
		Version:      "wb " + buildinfo.Version(),
	}
	for _, result := range results {
		if !result.Applied {
			continue
		}
		if outcome := applyCheckoutMarker(result.Destination, options, false); outcome.Error != "" {
			_, _ = fmt.Fprintf(command.ErrOrStderr(), "warning: relocated worktree marker %s was not refreshed: %s\n", result.Destination, outcome.Error)
		}
	}
}
