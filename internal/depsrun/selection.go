package depsrun

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/progress"
)

func (service *Service) Select(_ context.Context, request Selection) ([]deps.Repository, error) {
	if request.Parallel < 1 {
		return nil, fmt.Errorf("parallelism must be at least 1")
	}
	if request.Retry < 0 {
		return nil, fmt.Errorf("retry count must not be negative")
	}
	if request.Timeout < 0 {
		return nil, fmt.Errorf("timeout must not be negative")
	}
	expression, err := CompileRegex(request.Regex)
	if err != nil {
		return nil, err
	}
	if request.Match != "" {
		if _, err := path.Match(request.Match, ""); err != nil {
			return nil, fmt.Errorf("invalid --match: %w", err)
		}
	}
	reporter := request.Progress
	progress.Report(reporter, progress.Event{Operation: "deps", Phase: "select_repositories", State: progress.Waiting})
	if !request.Fleet {
		repositoryPath := "."
		if request.RepositoryPath != "" {
			repositoryPath = request.RepositoryPath
		}
		absolute, err := service.deps.Abs(repositoryPath)
		if err != nil {
			return nil, err
		}
		slug, cloneURL, err := service.deps.Identity(absolute, request.ProjectsRoot)
		if err != nil {
			return nil, err
		}
		if !matchesDependencyRepository(slug, request.Match, expression) {
			return nil, fmt.Errorf("repository %s does not match selected filters", slug)
		}
		if request.Filter != "" && !strings.Contains(slug, request.Filter) {
			return nil, fmt.Errorf("repository %s does not match --filter %q", slug, request.Filter)
		}
		progress.Report(reporter, progress.Event{Operation: "deps", Phase: "select_repositories", Repository: slug, State: progress.Completed, Completed: 1, Total: 1})
		return []deps.Repository{{Slug: slug, Path: absolute, CloneURL: cloneURL}}, nil
	}
	selected, err := service.deps.Fleet(request.ProjectsRoot, request.Filter, request.ExtraOrgs)
	if err != nil {
		return nil, err
	}
	repositories := make([]deps.Repository, 0, len(selected))
	for _, repository := range selected {
		if !matchesDependencyRepository(repository.Slug, request.Match, expression) {
			continue
		}
		repositories = append(repositories, deps.Repository{
			Slug: repository.Slug, Path: repository.Path, CloneURL: repository.CloneURL, Archived: repository.Archived,
		})
	}
	sort.Slice(repositories, func(i, j int) bool { return repositories[i].Slug < repositories[j].Slug })
	if len(repositories) == 0 {
		return nil, fmt.Errorf("no repositories match the selected fleet filters")
	}
	progress.Report(reporter, progress.Event{Operation: "deps", Phase: "select_repositories", State: progress.Completed, Completed: len(repositories), Total: len(repositories)})
	return repositories, nil
}
func CompileRegex(value string) (*regexp.Regexp, error) {
	if value == "" {
		return nil, nil
	}
	expression, err := regexp.Compile(value)
	if err != nil {
		return nil, fmt.Errorf("invalid --regex: %w", err)
	}
	return expression, nil
}

func matchesDependencyRepository(slug, glob string, expression *regexp.Regexp) bool {
	if glob != "" {
		matched, err := path.Match(glob, slug)
		if err != nil || !matched {
			return false
		}
	}
	return expression == nil || expression.MatchString(slug)
}

func repositoryIdentity(repositoryPath, root string) (string, string, error) {
	output, err := exec.Command("git", "-C", repositoryPath, "remote", "get-url", "origin").Output()
	if err == nil {
		remote := strings.TrimSpace(string(output))
		if slug := githubSlug(remote); slug != "" {
			return slug, remote, nil
		}
	}
	relative, relErr := filepath.Rel(root, repositoryPath)
	if relErr == nil {
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if len(parts) == 2 && parts[0] != ".." && parts[0] != "." {
			return parts[0] + "/" + parts[1], "", nil
		}
	}
	return "", "", fmt.Errorf("cannot determine GitHub owner/repository identity for %s", repositoryPath)
}

func githubSlug(remote string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	if strings.HasPrefix(trimmed, "git@github.com:") {
		return strings.TrimPrefix(trimmed, "git@github.com:")
	}
	parsed, err := url.Parse(trimmed)
	if err == nil && strings.EqualFold(parsed.Hostname(), "github.com") {
		return strings.TrimPrefix(parsed.Path, "/")
	}
	return ""
}
