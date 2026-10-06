package checkoutsetup

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkerRunPreservesDiscoveryFailureFilterAndResolvedDedup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "clone")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	refusal := errors.New("scan failed")
	deps := DefaultMarkerRunDependencies()
	deps.ScanLocal = func(got string) ([]discover.Repo, error) {
		if got != root {
			t.Fatal(got)
		}
		return nil, refusal
	}
	if _, err := NewMarkers(deps).Run(t.Context(), MarkerRequest{Fleet: true, Options: checkoutmarker.DescribeOptions{ProjectsRoot: root}}); !errors.Is(err, refusal) || err.Error() != "scan local repositories: scan failed" {
		t.Fatal(err)
	}
	deps.ScanLocal = func(string) ([]discover.Repo, error) {
		return []discover.Repo{{Org: "acme", Name: "skip", Path: path}, {Org: "acme", Name: "app"}, {Org: "acme", Name: "app", Path: path}, {Org: "acme", Name: "app", Path: link}}, nil
	}
	deps.RegisteredWorktrees = func(context.Context, string) []string { return []string{link, path} }
	paths, err := NewMarkers(deps).checkouts(t.Context(), MarkerRequest{Fleet: true, Filter: "acme/app", Options: checkoutmarker.DescribeOptions{ProjectsRoot: root}})
	if err != nil || len(paths) != 1 || paths[0] != path {
		t.Fatal(paths, err)
	}
}

func TestMarkerRefreshMethodsKeepRealPrivateStateAndFailureDiagnostics(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	clone := filepath.Join(root, "acme", "app")
	testenv.CloneWithOrigin(t, t.TempDir(), "app", clone)
	options := checkoutmarker.DescribeOptions{ProjectsRoot: root, BaseBranch: "main", Version: "wb test"}
	refusal := errors.New("injected describe refusal")
	deps := DefaultMarkerRunDependencies()
	actualDescribe := deps.Marker.Describe
	deps.Marker.Describe = func(path string, opts checkoutmarker.DescribeOptions) (checkoutmarker.Inspection, error) {
		if path == clone {
			return checkoutmarker.Inspection{}, refusal
		}
		return actualDescribe(path, opts)
	}
	markers := NewMarkers(deps)
	var diagnostic strings.Builder
	markers.RefreshSynced([]fleetsync.Result{
		{Status: fleetsync.Failed, Repo: discover.Repo{Org: "acme", Name: "app", Path: clone}},
		{Repo: discover.Repo{}}, {Repo: discover.Repo{Org: "acme", Name: "app"}},
		{Repo: discover.Repo{Org: "acme", Name: "app", Path: filepath.Join(root, "missing")}},
		{Repo: discover.Repo{Org: "acme", Name: "app", Path: clone}},
	}, options, &diagnostic)
	if !strings.Contains(diagnostic.String(), "warning: 1 clone(s) could not be given a .worktree.md") {
		t.Fatal(diagnostic.String())
	}
	diagnostic.Reset()
	markers.AfterRename([]worktrees.RenameResult{{Applied: false, NewWorktreeDir: clone}, {Applied: true}, {Applied: true, NewWorktreeDir: clone}}, options, &diagnostic)
	if !strings.Contains(diagnostic.String(), "warning: could not refresh .worktree.md in "+clone+": injected describe refusal") {
		t.Fatal(diagnostic.String())
	}
	diagnostic.Reset()
	markers.AfterRelocate([]worktrees.RelocateResult{{Applied: false, Destination: clone}, {Applied: true, Destination: clone}}, options, &diagnostic)
	if !strings.Contains(diagnostic.String(), "warning: relocated worktree marker "+clone+" was not refreshed: injected describe refusal") {
		t.Fatal(diagnostic.String())
	}
	// On the successful paths all actual Describe/Apply/default filesystem effects run.
	markers = NewMarkers(DefaultMarkerRunDependencies())
	diagnostic.Reset()
	markers.RefreshSynced([]fleetsync.Result{{Repo: discover.Repo{Org: "acme", Name: "app", Path: clone}}}, options, &diagnostic)
	markers.AfterRename([]worktrees.RenameResult{{Applied: true, NewWorktreeDir: clone}}, options, &diagnostic)
	markers.AfterRelocate([]worktrees.RelocateResult{{Applied: true, Destination: clone}}, options, &diagnostic)
	if diagnostic.Len() != 0 {
		t.Fatal(diagnostic.String())
	}
	if _, err := os.Stat(filepath.Join(clone, checkoutmarker.FileName)); err != nil {
		t.Fatal(err)
	}
}

func TestMarkerApplyFailureAndStaleNativeRegistrationArePreserved(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	clone := filepath.Join(root, "acme", "app")
	testenv.CloneWithOrigin(t, t.TempDir(), "app", clone)
	deps := DefaultMarkerDependencies()
	refusal := errors.New("injected apply refusal")
	deps.Apply = func(checkoutmarker.Descriptor, string) (checkoutmarker.Result, error) {
		return checkoutmarker.Result{}, refusal
	}
	outcome := applyMarker(clone, checkoutmarker.DescribeOptions{ProjectsRoot: root}, false, deps)
	if outcome.Error != refusal.Error() || outcome.Path == "" || outcome.MarkerWritten {
		t.Fatal(outcome)
	}
	linked := filepath.Join(root, "stale")
	testenv.Git(t, clone, "worktree", "add", "-b", "stale", linked)
	if err := os.RemoveAll(linked); err != nil {
		t.Fatal(err)
	}
	if paths := registeredWorktrees(t.Context(), clone); len(paths) != 0 {
		t.Fatal(paths)
	}
}

func TestMarkerRunUsesActualDescribeApplyAndMissingExclude(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	clone := filepath.Join(root, "acme", "app")
	testenv.CloneWithOrigin(t, t.TempDir(), "app", clone)
	options := checkoutmarker.DescribeOptions{ProjectsRoot: root, BaseBranch: "main"}
	inspection, err := checkoutmarker.Describe(clone, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(inspection.ExcludePath); err != nil {
		t.Fatal(err)
	}
	marker, exclude := markerWouldChange(inspection)
	if !marker || !exclude {
		t.Fatal(marker, exclude)
	}
	outcomes, err := NewMarkers(DefaultMarkerRunDependencies()).Run(t.Context(), MarkerRequest{Options: options, Paths: []string{clone}})
	if err != nil || len(outcomes) != 1 || outcomes[0].Error != "" || !outcomes[0].MarkerWritten || !outcomes[0].ExcludeWritten {
		t.Fatal(outcomes, err)
	}
	if _, err := os.Stat(filepath.Join(clone, checkoutmarker.FileName)); err != nil {
		t.Fatal(err)
	}
}
