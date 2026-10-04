package checkoutsetup

import (
	"bufio"
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/worktrees"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type MarkerOutcome struct {
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

type MarkerRequest struct {
	Options       checkoutmarker.DescribeOptions
	Filter        string
	Fleet, DryRun bool
	Paths         []string
}
type MarkerRunDependencies struct {
	Marker              MarkerDependencies
	ScanLocal           func(string) ([]discover.Repo, error)
	RegisteredWorktrees func(context.Context, string) []string
}
type Markers struct{ deps MarkerRunDependencies }

func NewMarkers(deps MarkerRunDependencies) *Markers { return &Markers{deps: deps} }
func DefaultMarkerRunDependencies() MarkerRunDependencies {
	return MarkerRunDependencies{DefaultMarkerDependencies(), discover.ScanLocal, registeredWorktrees}
}
func (s *Markers) Run(ctx context.Context, req MarkerRequest) ([]MarkerOutcome, error) {
	paths, err := s.checkouts(ctx, req)
	if err != nil {
		return nil, err
	}
	outcomes := make([]MarkerOutcome, 0, len(paths))
	for _, path := range paths {
		outcomes = append(outcomes, applyMarker(path, req.Options, req.DryRun, s.deps.Marker))
	}
	return outcomes, nil
}
func applyMarker(path string, options checkoutmarker.DescribeOptions, dryRun bool, deps MarkerDependencies) MarkerOutcome {
	inspection, err := deps.Describe(path, options)
	if err != nil {
		return MarkerOutcome{Path: path, Error: err.Error()}
	}
	descriptor := inspection.Descriptor
	outcome := MarkerOutcome{
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
	result, err := deps.Apply(descriptor, inspection.ExcludePath)
	if err != nil {
		outcome.Error = err.Error()
		return outcome
	}
	outcome.MarkerWritten = result.MarkerWritten
	outcome.ExcludeWritten = result.ExcludeWritten
	return outcome
}

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

func (s *Markers) checkouts(ctx context.Context, req MarkerRequest) ([]string, error) {
	if !req.Fleet {
		if len(req.Paths) == 1 {
			return []string{req.Paths[0]}, nil
		}
		return []string{"."}, nil
	}
	repositories, err := s.deps.ScanLocal(req.Options.ProjectsRoot)
	if err != nil {
		return nil, fmt.Errorf("scan local repositories: %w", err)
	}
	seen := map[string]bool{}
	var checkouts []string
	for _, repository := range repositories {
		if req.Filter != "" && !strings.Contains(repository.Slug(), req.Filter) {
			continue
		}
		// Use the path discovery actually found. A canonical clone may live at
		// the literal host level (<root>/<host>/<org>/<repo>) or at the legacy
		// two-level placement, so rebuilding a flat path here would silently
		// skip every host-level clone.
		canonical := repository.Path
		if canonical == "" {
			continue
		}
		for _, path := range append([]string{canonical}, s.deps.RegisteredWorktrees(ctx, canonical)...) {
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

func resolvedPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

func (s *Markers) RefreshSynced(results []fleetsync.Result, options checkoutmarker.DescribeOptions, errOut io.Writer) {
	failures := 0
	for _, result := range results {
		if result.Status == fleetsync.Failed || result.Repo.Org == "" || result.Repo.Name == "" {
			continue
		}
		// The clone's real path, as discovery found it: see the fleet-marker
		// loop above for why a rebuilt flat path would skip host-level clones.
		path := result.Repo.Path
		if path == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			continue
		}
		if outcome := applyMarker(path, options, false, s.deps.Marker); outcome.Error != "" {
			failures++
		}
	}
	if failures > 0 {
		_, _ = fmt.Fprintf(errOut, "warning: %d clone(s) could not be given a %s; run `wb worktree marker --fleet` for detail\n", failures, checkoutmarker.FileName)
	}
}

func (s *Markers) AfterRename(results []worktrees.RenameResult, options checkoutmarker.DescribeOptions, stderr io.Writer) {
	for _, result := range results {
		if !result.Applied || result.NewWorktreeDir == "" {
			continue
		}
		if outcome := applyMarker(result.NewWorktreeDir, options, false, s.deps.Marker); outcome.Error != "" {
			_, _ = fmt.Fprintf(stderr, "warning: could not refresh %s in %s: %s\n",
				checkoutmarker.FileName, result.NewWorktreeDir, outcome.Error)
		}
	}
}

func (s *Markers) AfterRelocate(results []worktrees.RelocateResult, options checkoutmarker.DescribeOptions, stderr io.Writer) {
	for _, result := range results {
		if !result.Applied {
			continue
		}
		if outcome := applyMarker(result.Destination, options, false, s.deps.Marker); outcome.Error != "" {
			_, _ = fmt.Fprintf(stderr, "warning: relocated worktree marker %s was not refreshed: %s\n", result.Destination, outcome.Error)
		}
	}
}
