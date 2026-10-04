// Package checkoutsetup shares checkout-creation hooks and markers below the CLI.
package checkoutsetup

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/worktrees"
	"io"
)

type HookDependencies struct {
	Canonical  func(string, string) (string, error)
	Refresh    func(string, string, string, string) (bool, error)
	Executable func() string
}

func BeforeCreate(root string, repositories []string, deps HookDependencies) error {
	paths := make([]string, 0, len(repositories))
	for _, repo := range repositories {
		path, err := deps.Canonical(root, repo)
		if err != nil {
			return err
		}
		paths = append(paths, path)
	}
	for i, repo := range repositories {
		if _, err := deps.Refresh(paths[i], "", deps.Executable(), root); err != nil {
			return fmt.Errorf("verify hooks for %s before creating a worktree: %w", repo, err)
		}
	}
	return nil
}
func DefaultHookDependencies(executable func() string) HookDependencies {
	return HookDependencies{worktrees.CanonicalRepositoryPath, hooks.RefreshManagedShims, executable}
}

type MarkerDependencies struct {
	Describe func(string, checkoutmarker.DescribeOptions) (checkoutmarker.Inspection, error)
	Apply    func(checkoutmarker.Descriptor, string) (checkoutmarker.Result, error)
}

func DefaultMarkerDependencies() MarkerDependencies {
	return MarkerDependencies{checkoutmarker.Describe, checkoutmarker.Apply}
}

// AfterCreate is best-effort: marker diagnostics never undo an admitted checkout.
func AfterCreate(options checkoutmarker.DescribeOptions, stderr io.Writer, results []worktrees.CreateResult, deps MarkerDependencies) {
	seen := map[string]bool{}
	for _, result := range results {
		paths := []string{result.WorktreeDir}
		if result.CanonicalDir != "" {
			paths = append(paths, result.CanonicalDir)
		}
		for _, path := range paths {
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			outcome := applyMarker(path, options, false, deps)
			if outcome.Error != "" {
				_, _ = fmt.Fprintf(stderr, "warning: could not write %s in %s: %s\n", checkoutmarker.FileName, path, outcome.Error)
			}

		}
	}
}
