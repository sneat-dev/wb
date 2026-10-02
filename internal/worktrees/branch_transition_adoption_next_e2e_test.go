//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestE2EBranchTransitionAdoptionRegistrationRetainsIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	held, err := openAbsoluteDirectoryNoFollow(root, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	swapped := ""
	got, err := createAdoptionRegistrationWithObservation(held, root, "acme", "app", "/actual/worktree", time.Now(), func(path string) {
		swapped = path
		if err := os.Rename(path, path+"-held"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "occupant"), []byte("replacement"), 0600); err != nil {
			t.Fatal(err)
		}
	})
	if got != "" || swapped == "" || err == nil || !strings.Contains(err.Error(), "registration path changed") {
		t.Fatalf("native registration namespace refusal = %q, %v", got, err)
	}
	for _, path := range []string{swapped, swapped + "-held"} {
		if _, err := os.Lstat(filepath.Join(path, adoptedWorktreePointerName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("redirected publication at %s: %v", path, err)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(swapped, "occupant")); err != nil || string(raw) != "replacement" {
		t.Fatalf("replacement bytes changed: %q, %v", raw, err)
	}
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB environment.
func TestE2EBranchTransitionClaimSummaryOrdering(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "z-last", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil || len(created) != 1 {
		t.Fatalf("native current-home claim = %+v, %v", created, err)
	}
	legacyHome := filepath.Join(os.Getenv("HOME"), ".wb")
	if err := os.MkdirAll(filepath.Join(legacyHome, "worktrees"), 0700); err != nil {
		t.Fatal(err)
	}
	worktree := fixture.externalWorktree(t, "feature/legacy-first")
	base := gitTestOutput(t, fixture.canonical, "rev-parse", "main")
	if _, err := recordWorkLogWithHooks(legacyHome, "a-first", CreateResult{Repository: "acme/app", WorktreeDir: worktree, Branch: "feature/legacy-first", Base: "main", BaseSHA: base}, WorkLogOptions{EffortID: "a-first", RunID: "run", Model: "unknown"}, workLogPublicationHooks{}); err != nil {
		t.Fatal(err)
	}
	// The write-home-first input is deliberately reverse task order. Without
	// ListActiveClaimSummaries' task comparator, the final assertion fails.
	current, err := listActiveClaimSummariesInHome(fixture.home, "acme/app")
	if err != nil || len(current) != 1 || current[0].Task != "z-last" {
		t.Fatalf("native write-home ordering control = %+v, %v", current, err)
	}
	legacy, err := listActiveClaimSummariesInHome(legacyHome, "acme/app")
	if err != nil || len(legacy) != 1 || legacy[0].Task != "a-first" {
		t.Fatalf("native legacy-home ordering control = %+v, %v", legacy, err)
	}
	listed, err := ListActiveClaimSummaries(fixture.projectsRoot, "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	tasks := []string{}
	for _, summary := range listed {
		tasks = append(tasks, summary.Task)
	}
	if !reflect.DeepEqual(tasks, []string{"a-first", "z-last"}) {
		t.Fatalf("same-repository tasks order = %v", tasks)
	}
}
