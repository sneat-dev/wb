package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

func TestLocalReposFiltersAndSortsWithoutMutatingSharedSource(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, relative := range []string{"z-org/beta/.git", "a-org/alpha/.git", "a-org/ignored"} {
		if err := os.MkdirAll(filepath.Join(root, relative), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		filter string
		want   []string
	}{{"a-org/", []string{"a-org/alpha"}}, {"", []string{"a-org/alpha", "z-org/beta"}}, {"A-ORG", nil}} {
		t.Run(tc.filter, func(t *testing.T) {
			t.Parallel()
			repos, err := LocalRepos(root, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if len(repos) != len(tc.want) {
				t.Fatal(repos)
			}
			for i, want := range tc.want {
				if repos[i].Slug() != want {
					t.Fatal(repos)
				}
			}
		})
	}
}
func TestLocalReposPreservesScanFailure(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("scan failed")
	repos, err := localReposWith("root", "filter", func(root string) ([]discover.Repo, error) {
		if root != "root" {
			t.Fatal(root)
		}
		return nil, sentinel
	})
	if repos != nil || !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "scan local repositories") {
		t.Fatalf("repos=%v err=%v", repos, err)
	}
}
