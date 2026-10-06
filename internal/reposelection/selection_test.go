package reposelection

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

func TestSelectionRejectsInvalidOptionsBeforeEffects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		request Request
		want    string
	}{
		{"parallel", Request{Parallel: 0, Retry: -1, Timeout: -1, Regex: "[", Match: "["}, "parallelism must be at least 1"},
		{"retry", Request{Parallel: 1, Retry: -1, Timeout: -1, Regex: "[", Match: "["}, "retry count must not be negative"},
		{"timeout", Request{Parallel: 1, Timeout: -1, Regex: "[", Match: "["}, "timeout must not be negative"},
		{"regex", Request{Parallel: 1, Regex: "[", Match: "["}, "invalid --regex:"},
		{"glob", Request{Parallel: 1, Match: "["}, "invalid --match:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deps := dependencies{Abs: func(string) (string, error) { t.Fatal("Abs before validation"); return "", nil }, Scan: func(string) ([]discover.Repo, error) { t.Fatal("Scan before validation"); return nil, nil }}
			_, err := selectWith(tc.request, deps)
			if err == nil {
				t.Fatal("invalid options accepted")
			}
			if tc.name == "regex" || tc.name == "glob" {
				if !strings.HasPrefix(err.Error(), tc.want) {
					t.Fatalf("error=%v", err)
				}
			} else if err.Error() != tc.want {
				t.Fatalf("error=%v want=%s", err, tc.want)
			}
		})
	}
}

func TestSelectionSupportsRealFleetGlobRegexFilterAndSorting(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, repo := range []string{"sneat-co/bots", "sneat-co/core", "other/tools"} {
		if err := os.MkdirAll(filepath.Join(root, repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	request := Request{ProjectsRoot: root, Fleet: true, Match: "sneat-co/*", Regex: "(bots|core)$", Parallel: 1}
	targets, err := Select(request)
	if err != nil {
		t.Fatal(err)
	}
	want := []Target{{Repository: "sneat-co/bots", Path: filepath.Join(root, "sneat-co/bots")}, {Repository: "sneat-co/core", Path: filepath.Join(root, "sneat-co/core")}}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets=%#v want=%#v", targets, want)
	}
	request.Match = ""
	request.Regex = ""
	request.Filter = "sneat-co/core"
	filtered, err := Select(request)
	if err != nil || len(filtered) != 1 || filtered[0].Repository != "sneat-co/core" {
		t.Fatalf("filtered=%#v error=%v", filtered, err)
	}
}

func TestDirectPathRejectsOwnerRepositorySelectors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, filter, match, regex, want string }{
		{"filter", "acme/repo", "", "", "--filter requires fleet mode for owner/repository selection"},
		{"match", "", "acme/*", "", "--match and --regex require fleet mode because a direct repository path has no guaranteed owner/repository identity"},
		{"regex", "", "", "^acme/", "--match and --regex require fleet mode because a direct repository path has no guaranteed owner/repository identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Select(Request{Path: t.TempDir(), ProjectsRoot: t.TempDir(), Parallel: 1, Filter: tc.filter, Match: tc.match, Regex: tc.regex})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestDirectPathResolvesIdentityWithoutDiscovery(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	targets, err := Select(Request{Path: filepath.Join(path, "..", filepath.Base(path)), Parallel: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := []Target{{Repository: filepath.Base(path), Path: path}}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets=%#v", targets)
	}
}

func TestSelectionPropagatesAbsAndScanErrorsWithoutProcessMutation(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("operation failed")
	for _, fleet := range []bool{false, true} {
		t.Run(map[bool]string{false: "abs", true: "scan"}[fleet], func(t *testing.T) {
			t.Parallel()
			deps := dependencies{Abs: func(path string) (string, error) {
				if fleet || path != "requested" {
					t.Fatalf("unexpected Abs(%q)", path)
				}
				return "", sentinel
			}, Scan: func(root string) ([]discover.Repo, error) {
				if !fleet || root != "projects" {
					t.Fatalf("unexpected Scan(%q)", root)
				}
				return nil, sentinel
			}}
			targets, err := selectWith(Request{Path: "requested", ProjectsRoot: "projects", Fleet: fleet, Parallel: 1}, deps)
			if err != sentinel || targets != nil {
				t.Fatalf("targets=%v error=%v", targets, err)
			}
		})
	}
}

func TestFleetEmptyPermissionAndUnmatchedFilter(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_, err := Select(Request{ProjectsRoot: root, Fleet: true, Parallel: 1})
	if err == nil || err.Error() != "no local repositories match the selected filters" {
		t.Fatalf("empty error=%v", err)
	}
	targets, err := Select(Request{ProjectsRoot: root, Fleet: true, Parallel: 1, AllowEmpty: true})
	if err != nil || len(targets) != 0 {
		t.Fatalf("allowed empty=%v error=%v", targets, err)
	}
	// Remote supplies AllowEmpty only when no filter was requested.
	_, err = Select(Request{ProjectsRoot: root, Fleet: true, Parallel: 1, Filter: "acme", AllowEmpty: false})
	if err == nil {
		t.Fatal("unmatched requested filter accepted")
	}
}

func TestFleetSortsScannerResultsInsteadOfDependingOnScanOrder(t *testing.T) {
	t.Parallel()
	targets, err := selectWith(Request{Fleet: true, ProjectsRoot: "requested", Parallel: 2}, dependencies{Scan: func(root string) ([]discover.Repo, error) {
		if root != "requested" {
			t.Fatalf("root=%s", root)
		}
		return []discover.Repo{{Org: "z", Name: "last", Path: "last"}, {Org: "a", Name: "first", Path: "first"}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(targets, []Target{{Repository: "a/first", Path: "first"}, {Repository: "z/last", Path: "last"}}) {
		t.Fatalf("targets=%#v", targets)
	}
}

func TestMatchCombinesFiltersAndRejectsInvalidGlobs(t *testing.T) {
	t.Parallel()
	expression := regexp.MustCompile("core$")
	for _, tc := range []struct {
		repo, filter, glob string
		regex              *regexp.Regexp
		want               bool
	}{
		{"acme/core", "", "", nil, true}, {"acme/core", "other", "", nil, false}, {"acme/core", "acme", "acme/*", expression, true},
		{"acme/bots", "acme", "acme/*", expression, false}, {"acme/core", "", "other/*", nil, false}, {"acme/core", "", "[", nil, false},
	} {
		if got := Match(tc.repo, tc.filter, tc.glob, tc.regex); got != tc.want {
			t.Fatalf("Match(%q,%q,%q)=%v", tc.repo, tc.filter, tc.glob, got)
		}
	}
}
