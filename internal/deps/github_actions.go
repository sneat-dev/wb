package deps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

var fullGitSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

type githubActionsAdapter struct{}

func resolveGitHubRef(ctx context.Context, dependency, version string, options Options) (string, error) {
	if fullGitSHA.MatchString(version) {
		return strings.ToLower(version), nil
	}
	if options.ResolveGitHubRef != nil {
		return options.ResolveGitHubRef(ctx, dependency, version)
	}
	repositoryURL := "https://github.com/" + dependency + ".git"
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, options.GitHubDir, "git", "ls-remote", repositoryURL,
		"refs/tags/"+version, "refs/tags/"+version+"^{}")
	if err != nil {
		return "", fmt.Errorf("resolve GitHub tag %s@%s: %w", dependency, version, err)
	}
	var direct, dereferenced string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !fullGitSHA.MatchString(fields[0]) {
			continue
		}
		if strings.HasSuffix(fields[1], "^{}") {
			dereferenced = strings.ToLower(fields[0])
		} else {
			direct = strings.ToLower(fields[0])
		}
	}
	if dereferenced != "" {
		return dereferenced, nil
	}
	if direct != "" {
		return direct, nil
	}
	return "", fmt.Errorf("GitHub tag %s@%s was not found", dependency, version)
}

func (githubActionsAdapter) inspect(ctx context.Context, repositoryDir, base string, target Target, options Options) ([]Decision, error) {
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, repositoryDir, "git", "ls-tree", "-r", "--name-only", base, "--", ".github/workflows")
	if err != nil {
		return nil, err
	}
	var decisions []Decision
	for _, name := range strings.Split(strings.TrimSpace(output), "\n") {
		if !workflowFile(name) {
			continue
		}
		contents, _, err := runCommand(ctx, options.Timeout, options.Retry, repositoryDir, "git", "show", base+":"+name)
		if err != nil {
			return nil, err
		}
		_, found, err := rewriteGitHubActions([]byte(contents), name, target, false, options.AllowDowngrade)
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, found...)
	}
	sort.Slice(decisions, func(i, j int) bool { return decisions[i].File < decisions[j].File })
	return decisions, nil
}

func (githubActionsAdapter) apply(_ context.Context, worktree string, target Target, options Options) ([]Decision, error) {
	root := filepath.Join(worktree, ".github", "workflows")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	type pendingFile struct {
		path     string
		contents []byte
		mode     os.FileMode
	}
	var pending []pendingFile
	var decisions []Decision
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !workflowFile(path) {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(worktree, path)
		if err != nil {
			return err
		}
		updated, found, err := rewriteGitHubActions(contents, filepath.ToSlash(relative), target, true, options.AllowDowngrade)
		if err != nil {
			return err
		}
		decisions = append(decisions, found...)
		if string(updated) == string(contents) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		pending = append(pending, pendingFile{path: path, contents: updated, mode: info.Mode()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, file := range pending {
		if err := writeAtomic(file.path, file.contents, file.mode); err != nil {
			return nil, err
		}
	}
	sort.Slice(decisions, func(i, j int) bool { return decisions[i].File < decisions[j].File })
	return decisions, nil
}

func workflowFile(path string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	return extension == ".yml" || extension == ".yaml"
}

func rewriteGitHubActions(contents []byte, name string, target Target, apply, allowDowngrade bool) ([]byte, []Decision, error) {
	pattern := regexp.MustCompile(`^(\s*(?:-\s*)?uses:\s*)(["']?)(` + regexp.QuoteMeta(target.Dependency) + `)(/[^\s@"']+)?@([^\s#"']+)(["']?)(\s*(?:#.*)?)$`)
	lines := strings.SplitAfter(string(contents), "\n")
	decisions := make([]Decision, 0)
	for index, lineWithEnding := range lines {
		ending := ""
		line := lineWithEnding
		if strings.HasSuffix(line, "\n") {
			line = strings.TrimSuffix(line, "\n")
			ending = "\n"
		}
		if strings.HasSuffix(line, "\r") {
			line = strings.TrimSuffix(line, "\r")
			ending = "\r" + ending
		}
		match := pattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		beforeRef := match[5]
		beforeVersion := githubActionsVersion(beforeRef, match[7])
		decision := Decision{
			Dependency: target.Dependency, File: name, BeforeRef: beforeRef, BeforeVersion: beforeVersion,
			TargetVersion: target.Version, ResolvedRef: target.Resolved,
			AfterRef: target.Resolved, AfterVersion: target.Version,
		}
		if comparableDowngrade(beforeVersion, target.Version) && !allowDowngrade {
			decision.Action = "blocked_downgrade"
			decision.Reason = fmt.Sprintf("target %s is lower than observed version %s; use --allow-downgrade", target.Version, beforeVersion)
			decisions = append(decisions, decision)
			return contents, decisions, fmt.Errorf("%s: %s", name, decision.Reason)
		}
		if beforeRef == target.Resolved && beforeVersion == target.Version {
			decision.Action = "unchanged"
			decision.Reason = "already pinned to the resolved target commit and version"
			decisions = append(decisions, decision)
			continue
		}
		decision.Action = "updated"
		if beforeVersion == "" {
			decision.Reason = "existing opaque reference set to the requested exact version"
		} else if comparableDowngrade(beforeVersion, target.Version) {
			decision.Reason = "explicitly allowed downgrade applied"
		} else {
			decision.Reason = "existing reference set to the requested exact version"
		}
		decisions = append(decisions, decision)
		if !apply {
			continue
		}
		comment := githubActionsComment(target.Version, match[7])
		lines[index] = match[1] + match[2] + match[3] + match[4] + "@" + target.Resolved + match[6] + comment + ending
	}
	return []byte(strings.Join(lines, "")), decisions, nil
}

func githubActionsVersion(reference, comment string) string {
	if semver.IsValid(reference) {
		return reference
	}
	comment = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(comment), "#"))
	if fields := strings.FieldsFunc(comment, func(r rune) bool { return r == ';' || r == ' ' || r == '\t' }); len(fields) > 0 && semver.IsValid(fields[0]) {
		return fields[0]
	}
	return ""
}

func githubActionsComment(version, existing string) string {
	existing = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(existing), "#"))
	if existing == "" || githubActionsVersion("", "# "+existing) != "" {
		return " # " + version
	}
	return " # " + version + "; " + existing
}

func comparableDowngrade(before, target string) bool {
	return semver.IsValid(before) && semver.IsValid(target) && semver.Compare(target, before) < 0
}

func writeAtomic(path string, contents []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".wb-deps-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Chmod(mode.Perm()); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	remove = false
	return nil
}
