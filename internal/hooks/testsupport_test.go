package hooks

import "path/filepath"

// Helpers kept for tests only: no production caller remains.

func AppendEvent(path string, event Event) error {
	return AppendEvents(path, []Event{event})
}

// IsPublication reports whether the pushed refs require publication policy.
func (c Classification) IsPublication() bool { return c.Tier >= TierPublication }

// RunLint reports whether the Tier 1 lint/vet block should run.
func (c Classification) RunLint() bool { return c.Tier >= TierLint }

func ReplayPendingMetrics(repoPath, configPath, projectsRoot string) (int, error) {
	policy, err := LoadPolicy(repoPath, configPath)
	if err != nil {
		return 0, err
	}
	layout, err := ResolveExecutionLayout(policy.RepoRoot, projectsRoot)
	if err != nil {
		return 0, err
	}
	if err := ensureExecutionLayout(layout); err != nil {
		return 0, err
	}
	return replayPendingMetrics(policy.Metrics.Path, layout)
}

func writeExecutable(path string, content []byte) error {
	directory, err := openManagedHooksDirectory("", filepath.Dir(path), nil)
	if err != nil {
		return err
	}
	defer directory.close()
	identity, err := managedHookIdentityAt(directory.directory, filepath.Base(path))
	if err != nil {
		return err
	}
	return writeExecutableAt(directory, filepath.Base(path), content, identity, nil)
}
