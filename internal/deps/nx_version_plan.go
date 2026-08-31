package deps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// generateNxVersionPlan writes the one version plan owned by an npm bump-wave
// candidate. It is deliberately called only by waveHandler: exact deps set
// must keep its existing behavior, and an incomplete wave must not claim a
// release bump when another target failed to apply.
func generateNxVersionPlan(ctx context.Context, worktree, waveID string, decisions []Decision, options Options) error {
	enabled, err := nxVersionPlansEnabled(worktree)
	if err != nil || !enabled {
		return err
	}
	if waveID == "" {
		return nil
	}

	projects, err := changedPublishableNxProjects(ctx, worktree, decisions, options)
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		return nil
	}

	contents := nxVersionPlanContents(projects)
	filename := "wb-" + safeSlug(waveID) + ".md"
	planPath := filepath.Join(worktree, ".nx", "version-plans", filename)
	if existing, readErr := os.ReadFile(planPath); readErr == nil {
		if bytes.Equal(existing, contents) {
			return nil
		}
		// A generated filename is wave-scoped. Refusing to overwrite a file with
		// different contents preserves user-authored plans and makes a collision
		// visible instead of silently publishing an incomplete candidate.
		return fmt.Errorf("Nx version plan %s already exists with different contents", filepath.ToSlash(filepath.Join(".nx", "version-plans", filename)))
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		return err
	}
	return writeAtomic(planPath, contents, 0o644)
}

func nxVersionPlansEnabled(worktree string) (bool, error) {
	contents, err := os.ReadFile(filepath.Join(worktree, "nx.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var configuration struct {
		Release struct {
			VersionPlans bool `json:"versionPlans"`
		} `json:"release"`
	}
	if err := json.Unmarshal(contents, &configuration); err != nil {
		return false, fmt.Errorf("parse nx.json: %w", err)
	}
	return configuration.Release.VersionPlans, nil
}

// changedPublishableNxProjects maps updated package manifests to their owning
// Nx projects. A named non-private package is publishable by npm's own rule;
// requiring a sibling project.json excludes the workspace root and tooling
// manifests Nx may infer but which are not explicit release projects.
func changedPublishableNxProjects(ctx context.Context, worktree string, decisions []Decision, options Options) ([]string, error) {
	manifests := map[string]bool{}
	for _, decision := range decisions {
		if decision.Action == "updated" && filepath.Base(decision.File) == "package.json" {
			manifests[decision.File] = true
		}
	}
	// During a resumed candidate, the first application may already have
	// changed a manifest before the process stopped. The repeat application
	// correctly reports that reference as unchanged, so include the candidate
	// diff as durable evidence rather than accidentally omitting its plan.
	if _, err := os.Lstat(filepath.Join(worktree, ".git")); err == nil {
		output, _, diffErr := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "diff", "--name-only", "HEAD")
		if diffErr != nil {
			return nil, diffErr
		}
		for _, path := range strings.Split(strings.TrimSpace(output), "\n") {
			if filepath.Base(path) == "package.json" {
				manifests[filepath.ToSlash(path)] = true
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	projects := map[string]bool{}
	for manifest := range manifests {
		if manifest == "package.json" {
			continue
		}
		manifestPath := filepath.Join(worktree, filepath.FromSlash(manifest))
		contents, err := os.ReadFile(manifestPath)
		if err != nil {
			return nil, err
		}
		var pkg npmPackageJSONManifest
		if err := json.Unmarshal(contents, &pkg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", manifest, err)
		}
		if pkg.Name == "" || pkg.Private {
			continue
		}
		projectPath := filepath.Join(filepath.Dir(manifestPath), "project.json")
		projectContents, err := os.ReadFile(projectPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var project struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(projectContents, &project); err != nil {
			return nil, fmt.Errorf("parse %s: %w", filepath.ToSlash(filepath.Join(filepath.Dir(manifest), "project.json")), err)
		}
		if project.Name != "" {
			projects[project.Name] = true
		}
	}
	names := make([]string, 0, len(projects))
	for project := range projects {
		names = append(names, project)
	}
	sort.Strings(names)
	return names, nil
}

func nxVersionPlanContents(projects []string) []byte {
	var contents bytes.Buffer
	contents.WriteString("---\n")
	for _, project := range projects {
		// JSON string syntax is valid YAML and retains arbitrary Nx project
		// names exactly rather than relying on YAML's unquoted-key grammar.
		contents.WriteString(strconv.Quote(project))
		contents.WriteString(": patch\n")
	}
	contents.WriteString("---\n\nAutomated dependency release wave.\n")
	return contents.Bytes()
}
