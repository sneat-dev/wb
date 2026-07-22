package deps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
)

type goAdapter struct{}

func (goAdapter) inspect(ctx context.Context, repositoryDir, base string, target Target, options Options) ([]Decision, error) {
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, repositoryDir, "git", "ls-tree", "-r", "--name-only", base)
	if err != nil {
		return nil, err
	}
	var decisions []Decision
	for _, name := range strings.Split(strings.TrimSpace(output), "\n") {
		if filepath.Base(name) != "go.mod" || ignoredManifestPath(name) {
			continue
		}
		contents, _, err := runCommand(ctx, options.Timeout, options.Retry, repositoryDir, "git", "show", base+":"+name)
		if err != nil {
			return nil, err
		}
		version, found, err := requiredGoVersion(name, []byte(contents), target.Dependency)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		decision := Decision{
			File: name, BeforeRef: version, BeforeVersion: version,
			TargetVersion: target.Version, ResolvedRef: target.Version,
			AfterRef: target.Version, AfterVersion: target.Version,
		}
		if comparableDowngrade(version, target.Version) && !options.AllowDowngrade {
			decision.Action = "blocked_downgrade"
			decision.Reason = fmt.Sprintf("target %s is lower than observed version %s; use --allow-downgrade", target.Version, version)
			decisions = append(decisions, decision)
			return decisions, fmt.Errorf("%s: %s", name, decision.Reason)
		}
		if version == target.Version {
			decision.Action = "unchanged"
			decision.Reason = "requirement already declares the exact target version"
		} else {
			decision.Action = "planned"
			decision.Reason = "existing Go requirement will be set with official Go tooling"
		}
		decisions = append(decisions, decision)
	}
	sort.Slice(decisions, func(i, j int) bool { return decisions[i].File < decisions[j].File })
	return decisions, nil
}

func (goAdapter) apply(ctx context.Context, worktree string, target Target, options Options) ([]Decision, error) {
	modules, err := goManifests(worktree, target.Dependency)
	if err != nil {
		return nil, err
	}
	decisions := make([]Decision, 0, len(modules))
	for _, module := range modules {
		decision := Decision{
			File: module.relative, BeforeRef: module.version, BeforeVersion: module.version,
			TargetVersion: target.Version, ResolvedRef: target.Version,
		}
		if comparableDowngrade(module.version, target.Version) && !options.AllowDowngrade {
			decision.Action = "blocked_downgrade"
			decision.Reason = fmt.Sprintf("target %s is lower than observed version %s; use --allow-downgrade", target.Version, module.version)
			decisions = append(decisions, decision)
			return decisions, fmt.Errorf("%s: %s", module.relative, decision.Reason)
		}
		decisions = append(decisions, decision)
	}

	var updateErrors []error
	for index, module := range modules {
		decision := &decisions[index]
		if module.version == target.Version {
			decision.AfterRef = module.version
			decision.AfterVersion = module.version
			decision.Action = "unchanged"
			decision.Reason = "requirement already declares the exact target version"
			continue
		}
		if _, _, err := runCommand(ctx, options.Timeout, options.Retry, module.dir, "go", "get", target.Dependency+"@"+target.Version); err != nil {
			decision.Action = "failed"
			decision.Reason = err.Error()
			updateErrors = append(updateErrors, fmt.Errorf("%s: %w", module.relative, err))
			continue
		}
		if _, _, err := runCommand(ctx, options.Timeout, options.Retry, module.dir, "go", "mod", "tidy"); err != nil {
			decision.Action = "failed"
			decision.Reason = err.Error()
			updateErrors = append(updateErrors, fmt.Errorf("%s: %w", module.relative, err))
			continue
		}
		selected, _, err := runCommand(ctx, options.Timeout, options.Retry, module.dir, "go", "list", "-m", "-f", "{{.Version}}", target.Dependency)
		if err != nil {
			decision.Action = "failed"
			decision.Reason = err.Error()
			updateErrors = append(updateErrors, fmt.Errorf("%s: inspect selected version: %w", module.relative, err))
			continue
		}
		selected = strings.TrimSpace(selected)
		decision.AfterRef = selected
		decision.AfterVersion = selected
		if selected != target.Version {
			decision.Action = "failed"
			decision.Reason = fmt.Sprintf("Go module selection produced %s instead of exact target %s", selected, target.Version)
			updateErrors = append(updateErrors, fmt.Errorf("%s: %s", module.relative, decision.Reason))
			continue
		}
		decision.Action = "updated"
		if comparableDowngrade(module.version, target.Version) {
			decision.Reason = "official Go tooling applied the explicitly allowed downgrade and tidied the module"
		} else {
			decision.Reason = "official Go tooling selected the exact target and tidied the module"
		}
	}
	return decisions, errors.Join(updateErrors...)
}

type goManifest struct {
	dir      string
	relative string
	version  string
}

func goManifests(root, dependency string) ([]goManifest, error) {
	var manifests []goManifest
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "node_modules":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if entry.Name() != "go.mod" {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		version, found, err := requiredGoVersion(relative, contents, dependency)
		if err != nil {
			return err
		}
		if found {
			manifests = append(manifests, goManifest{dir: filepath.Dir(path), relative: filepath.ToSlash(relative), version: version})
		}
		return nil
	})
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].relative < manifests[j].relative })
	return manifests, err
}

func requiredGoVersion(name string, contents []byte, dependency string) (string, bool, error) {
	parsed, err := modfile.Parse(name, contents, nil)
	if err != nil {
		return "", false, fmt.Errorf("parse %s: %w", name, err)
	}
	for _, requirement := range parsed.Require {
		if requirement.Mod.Path == dependency {
			return requirement.Mod.Version, true, nil
		}
	}
	return "", false, nil
}

func ignoredManifestPath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		switch part {
		case "vendor", "node_modules", ".git":
			return true
		}
	}
	return false
}
