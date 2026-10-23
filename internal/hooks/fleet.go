package hooks

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/discover"
)

// LocalRepos scans and selects local repositories using hook fleet semantics.
func LocalRepos(root, filter string) ([]discover.Repo, error) {
	return localReposWith(root, filter, discover.ScanLocal)
}
func localReposWith(root, filter string, scan func(string) ([]discover.Repo, error)) ([]discover.Repo, error) {
	repos, err := scan(root)
	if err != nil {
		return nil, fmt.Errorf("scan local repositories: %w", err)
	}
	selected := repos[:0]
	for _, repo := range repos {
		if filter == "" || strings.Contains(repo.Slug(), filter) {
			selected = append(selected, repo)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Slug() < selected[j].Slug() })
	return selected, nil
}
