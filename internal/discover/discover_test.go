package discover

import "testing"

func TestReconcile(t *testing.T) {
	local := []Repo{
		{Org: "trakhimenok", Name: "workbench", Path: "/p/trakhimenok/workbench"},
		{Org: "dalgo", Name: "only-local", Path: "/p/dalgo/only-local"},
	}
	remote := []Repo{
		{Org: "trakhimenok", Name: "workbench", CloneURL: "git@gh:trakhimenok/workbench.git", Archived: false},
		{Org: "trakhimenok", Name: "only-remote", CloneURL: "git@gh:trakhimenok/only-remote.git", Archived: true},
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

	wb := by["trakhimenok/workbench"]
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

	or := by["trakhimenok/only-remote"]
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
