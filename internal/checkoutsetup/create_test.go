package checkoutsetup

import (
	"bytes"
	"errors"
	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/worktrees"
	"reflect"
	"testing"
)

func TestBeforeCreateResolvesAllRepositoriesBeforeAnyHookMutation(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("repository refused")
	calls := []string{}
	deps := HookDependencies{Canonical: func(root, repo string) (string, error) {
		calls = append(calls, "resolve:"+repo)
		if root != "root" {
			t.Fatal(root)
		}
		if repo == "bad" {
			return "", sentinel
		}
		return "canonical/" + repo, nil
	}, Executable: func() string { return "private-wb" }, Refresh: func(path, config, exe, root string) (bool, error) {
		calls = append(calls, "refresh:"+path)
		if config != "" || exe != "private-wb" || root != "root" {
			t.Fatal(config, exe, root)
		}
		return true, nil
	}}
	if err := BeforeCreate("root", []string{"good", "bad"}, deps); err != sentinel || !reflect.DeepEqual(calls, []string{"resolve:good", "resolve:bad"}) {
		t.Fatal(err, calls)
	}
	calls = nil
	if err := BeforeCreate("root", []string{"one", "two"}, deps); err != nil || !reflect.DeepEqual(calls, []string{"resolve:one", "resolve:two", "refresh:canonical/one", "refresh:canonical/two"}) {
		t.Fatal(err, calls)
	}
	deps.Refresh = func(string, string, string, string) (bool, error) { return false, sentinel }
	if err := BeforeCreate("root", []string{"one"}, deps); !errors.Is(err, sentinel) || err.Error() != "verify hooks for one before creating a worktree: repository refused" {
		t.Fatal(err)
	}
}
func TestAfterCreateDeduplicatesReportedPathsAndKeepsWarningsBestEffort(t *testing.T) {
	t.Parallel()
	options := checkoutmarker.DescribeOptions{ProjectsRoot: "root", BaseBranch: "main", Version: "wb test"}
	sentinel := errors.New("marker failed")
	seen := []string{}
	deps := MarkerDependencies{Describe: func(path string, got checkoutmarker.DescribeOptions) (checkoutmarker.Inspection, error) {
		seen = append(seen, path)
		if got.ProjectsRoot != options.ProjectsRoot || got.BaseBranch != options.BaseBranch || got.Version != options.Version {
			t.Fatal(got)
		}
		if path == "describe-failed" {
			return checkoutmarker.Inspection{}, sentinel
		}
		return checkoutmarker.Inspection{Descriptor: checkoutmarker.Descriptor{CheckoutPath: path}, ExcludePath: "exclude"}, nil
	}, Apply: func(d checkoutmarker.Descriptor, exclude string) (checkoutmarker.Result, error) {
		if exclude != "exclude" {
			t.Fatal(exclude)
		}
		if d.CheckoutPath == "apply-failed" {
			return checkoutmarker.Result{}, sentinel
		}
		return checkoutmarker.Result{}, nil
	}}
	results := []worktrees.CreateResult{{WorktreeDir: "work", CanonicalDir: "canonical"}, {WorktreeDir: "work", CanonicalDir: "canonical"}, {}, {WorktreeDir: "describe-failed"}, {WorktreeDir: "apply-failed"}}
	var errout bytes.Buffer
	AfterCreate(options, &errout, results, deps)
	if !reflect.DeepEqual(seen, []string{"work", "canonical", "describe-failed", "apply-failed"}) || errout.String() != "warning: could not write .worktree.md in describe-failed: marker failed\nwarning: could not write .worktree.md in apply-failed: marker failed\n" {
		t.Fatal(seen, errout.String())
	}
	AfterCreate(options, failWriter{sentinel}, results, deps)
}

type failWriter struct{ err error }

func (w failWriter) Write([]byte) (int, error) { return 0, w.err }
