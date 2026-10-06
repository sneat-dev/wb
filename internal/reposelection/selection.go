// Package reposelection selects local repositories and dispatches bounded work.
// It has no CLI, process-global configuration or command-family dependencies.
package reposelection

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
)

type Target struct{ Repository, Path string }

// Request preserves selection and execution admission for current consumers.
type Request struct {
	Path, ProjectsRoot, Filter, Match, Regex string
	Fleet, AllowEmpty                        bool
	Parallel, Retry                          int
	Timeout                                  time.Duration
}

type dependencies struct {
	Abs  func(string) (string, error)
	Scan func(string) ([]discover.Repo, error)
}

// Select validates the request before resolving one path or discovering a fleet.
func Select(request Request) ([]Target, error) {
	return selectWith(request, dependencies{Abs: filepath.Abs, Scan: discover.ScanLocal})
}

func selectWith(request Request, deps dependencies) ([]Target, error) {
	if request.Parallel < 1 {
		return nil, fmt.Errorf("parallelism must be at least 1")
	}
	if request.Retry < 0 {
		return nil, fmt.Errorf("retry count must not be negative")
	}
	if request.Timeout < 0 {
		return nil, fmt.Errorf("timeout must not be negative")
	}
	var expression *regexp.Regexp
	if request.Regex != "" {
		compiled, err := regexp.Compile(request.Regex)
		if err != nil {
			return nil, fmt.Errorf("invalid --regex: %w", err)
		}
		expression = compiled
	}
	if request.Match != "" {
		if _, err := path.Match(request.Match, ""); err != nil {
			return nil, fmt.Errorf("invalid --match: %w", err)
		}
	}
	if !request.Fleet {
		if request.Filter != "" {
			return nil, fmt.Errorf("--filter requires fleet mode for owner/repository selection")
		}
		if request.Match != "" || request.Regex != "" {
			return nil, fmt.Errorf("--match and --regex require fleet mode because a direct repository path has no guaranteed owner/repository identity")
		}
		absolute, err := deps.Abs(request.Path)
		if err != nil {
			return nil, err
		}
		target := Target{Repository: filepath.Base(absolute), Path: absolute}
		return []Target{target}, nil
	}
	repositories, err := deps.Scan(request.ProjectsRoot)
	if err != nil {
		return nil, err
	}
	targets := make([]Target, 0, len(repositories))
	for _, repository := range repositories {
		if !Match(repository.Slug(), request.Filter, request.Match, expression) {
			continue
		}
		targets = append(targets, Target{Repository: repository.Slug(), Path: repository.Path})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Repository < targets[j].Repository })
	if len(targets) == 0 && !request.AllowEmpty {
		return nil, fmt.Errorf("no local repositories match the selected filters")
	}
	return targets, nil
}

// Match combines substring, glob and regular-expression repository filters.
// An invalid glob does not match; Select reports invalid syntax before discovery.
func Match(repository, filter, glob string, expression *regexp.Regexp) bool {
	if filter != "" && !strings.Contains(repository, filter) {
		return false
	}
	if glob != "" {
		matched, err := path.Match(glob, repository)
		if err != nil || !matched {
			return false
		}
	}
	return expression == nil || expression.MatchString(repository)
}
