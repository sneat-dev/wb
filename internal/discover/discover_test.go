package discover

import (
	"os"
	"path/filepath"
	"testing"
)

// installFakeGhRepoView writes a fake `gh` on PATH that answers `gh repo view
// <slug> --json isArchived --jq .isArchived` with the given stdout, or fails
// the invocation entirely when ok is false — simulating GitHub being
// unreachable, unauthenticated, or rate-limited.
func installFakeGhRepoView(t *testing.T, wantSlug, stdout string, ok bool) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	exit := "0"
	if !ok {
		exit = "1"
	}
	content := "#!/bin/sh\nset -eu\n" +
		"if [ \"$1 $2 $3 $4 $5 $6 $7\" != \"repo view " + wantSlug + " --json isArchived --jq .isArchived\" ]; then\n" +
		"  echo \"unexpected gh command: $*\" >&2\n  exit 2\nfi\n" +
		"printf '%s\\n' '" + stdout + "'\nexit " + exit + "\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestIsArchivedTrue(t *testing.T) {
	installFakeGhRepoView(t, "acme/widgets", "true", true)
	archived, err := IsArchived("acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if !archived {
		t.Error("IsArchived() = false, want true")
	}
}

func TestIsArchivedFalse(t *testing.T) {
	installFakeGhRepoView(t, "acme/widgets", "false", true)
	archived, err := IsArchived("acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if archived {
		t.Error("IsArchived() = true, want false")
	}
}

func TestIsArchivedFailsClosedOnGhError(t *testing.T) {
	installFakeGhRepoView(t, "acme/widgets", "", false)
	if _, err := IsArchived("acme/widgets"); err == nil {
		t.Fatal("IsArchived() with a failing gh returned no error")
	}
}

func TestReconcile(t *testing.T) {
	local := []Repo{
		{Org: "sneat-dev", Name: "wb", Path: "/p/sneat-dev/wb"},
		{Org: "dalgo", Name: "only-local", Path: "/p/dalgo/only-local"},
	}
	remote := []Repo{
		{Org: "sneat-dev", Name: "wb", CloneURL: "git@gh:sneat-dev/wb.git", Archived: false},
		{Org: "sneat-dev", Name: "only-remote", CloneURL: "git@gh:sneat-dev/only-remote.git", Archived: true},
		{Org: "dalgo", Name: "only-local", CloneURL: "git@gh:dalgo/only-local.git", IsFork: true},
	}
	got := Reconcile(local, remote)
	if len(got) != 3 {
		t.Fatalf("got %d repos, want 3", len(got))
	}
	by := map[string]Repo{}
	for _, r := range got {
		by[r.Slug()] = r
	}

	wb := by["sneat-dev/wb"]
	if !wb.Local || !wb.Remote {
		t.Errorf("workbench should be both local and remote: %+v", wb)
	}
	if wb.Path == "" || wb.CloneURL == "" {
		t.Errorf("workbench should keep local path and gain clone url: %+v", wb)
	}

	ol := by["dalgo/only-local"]
	if !ol.Local || !ol.Remote {
		t.Errorf("only-local flags wrong: %+v", ol)
	}
	if !ol.IsFork {
		t.Errorf("only-local should inherit IsFork from remote: %+v", ol)
	}

	or := by["sneat-dev/only-remote"]
	if or.Local || !or.Remote || !or.Archived {
		t.Errorf("only-remote flags wrong: %+v", or)
	}
}

func TestReconcileSortedDeterministic(t *testing.T) {
	got := Reconcile([]Repo{{Org: "z", Name: "b"}, {Org: "a", Name: "a"}}, nil)
	if got[0].Slug() != "a/a" || got[1].Slug() != "z/b" {
		t.Errorf("not sorted by slug: %v", got)
	}
}

func TestScanLocalExcludesLinkedWorktrees(t *testing.T) {
	projectsRoot := t.TempDir()
	organization := filepath.Join(projectsRoot, "acme")
	canonical := filepath.Join(organization, "widgets")
	worktree := filepath.Join(organization, "widgets-feature")
	for _, directory := range []string{filepath.Join(canonical, ".git"), worktree} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: ../widgets/.git/worktrees/widgets-feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repositories, err := ScanLocal(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0].Slug() != "acme/widgets" {
		t.Fatalf("ScanLocal() = %+v, want only canonical acme/widgets", repositories)
	}
}
