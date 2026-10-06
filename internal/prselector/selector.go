package prselector

import (
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

func Parse(selector string) (repository, number string, err error) {
	value := strings.TrimSpace(selector)
	repository, rest, found := strings.Cut(value, "#")
	if !found {
		if url := strings.TrimSpace(value); strings.Contains(url, "/pull/") {
			repository, urlErr := githubchecks.RepositoryFromPullRequestURL(url)
			if urlErr != nil {
				return "", "", urlErr
			}
			number, numberErr := githubchecks.PullRequestNumber(url)
			return repository, number, numberErr
		}
		return "", "", fmt.Errorf("selector %q must be owner/repository#number or a pull request URL", selector)
	}
	repository = strings.TrimSpace(repository)
	if strings.Count(repository, "/") != 1 || strings.HasPrefix(repository, "/") || strings.HasSuffix(repository, "/") {
		return "", "", fmt.Errorf("selector %q must name owner/repository before the #", selector)
	}
	number, err = githubchecks.PullRequestNumber(rest)
	return repository, number, err
}
