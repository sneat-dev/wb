package main

import (
	"github.com/sneat-dev/wb/internal/reposelection"
	"regexp"
)

// Temporary adapters preserve private root fields while status/fleet/remote
// consumers move to their own services. Selection and dispatch live in the leaf.
func qualityTargets(singlePath, root, filter string, options qualityOptions) ([]qualityTarget, error) {
	targets, err := reposelection.Select(reposelection.Request{
		Path: singlePath, ProjectsRoot: root, Filter: filter, Fleet: options.fleet,
		Match: options.match, Regex: options.regex, Parallel: options.parallel,
		Retry: options.retry, Timeout: options.timeout, AllowEmpty: options.allowEmpty,
	})
	if err != nil {
		return nil, err
	}
	result := make([]qualityTarget, len(targets))
	for index, target := range targets {
		result[index] = qualityTarget{repository: target.Repository, path: target.Path}
	}
	return result, nil
}

func matchesQualityTarget(repository, filter, glob string, expression *regexp.Regexp) bool {
	return reposelection.Match(repository, filter, glob, expression)
}

func runTargets(count, parallel int, run func(int)) {
	reposelection.ForEach(count, parallel, run)
}
