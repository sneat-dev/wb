package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func sessionAt(pid int, started time.Time) session.View {
	return session.View{Record: session.Record{PID: pid, StartedAt: started}, State: session.StateLive}
}

func ownerAt(pid int, effort string, at time.Time) worktrees.OwnerView {
	return worktrees.OwnerView{OwnerRegistration: worktrees.OwnerRegistration{PID: pid, Effort: effort, At: at}}
}

func TestAttributeSessionsJoinsByPIDWithReuseGuard(t *testing.T) {
	started := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	views := []session.View{sessionAt(41, started)}
	results := []worktrees.ListResult{
		{Task: "task-7", Branch: "agent/task-7", WorktreeDir: "/wt/task-7/acme/x",
			Owners: []worktrees.OwnerView{
				ownerAt(41, "effort-a", started.Add(time.Minute)),     // counts
				ownerAt(41, "effort-old", started.Add(-time.Hour)),    // pre-registration: PID reuse, excluded
				ownerAt(99, "someone-else", started.Add(time.Minute)), // other PID, excluded
			}},
		{Task: "task-9", Branch: "agent/task-9", WorktreeDir: "/wt/task-9/acme/y",
			Owners: []worktrees.OwnerView{ownerAt(41, "effort-b", started.Add(2*time.Minute))}},
	}
	rows := attributeSessions(views, results)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if !reflect.DeepEqual(row.Efforts, []string{"effort-a", "effort-b"}) {
		t.Fatalf("Efforts = %v (sorted distinct, reuse-guarded)", row.Efforts)
	}
	if !reflect.DeepEqual(row.Worktrees, []string{"/wt/task-7/acme/x", "/wt/task-9/acme/y"}) {
		t.Fatalf("Worktrees = %v", row.Worktrees)
	}
	if !reflect.DeepEqual(row.Branches, []string{"agent/task-7", "agent/task-9"}) {
		t.Fatalf("Branches = %v", row.Branches)
	}
}

func TestAttributeSessionsDedupsAcrossOwnersAndIgnoresEmptyEfforts(t *testing.T) {
	started := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	views := []session.View{sessionAt(41, started)}
	results := []worktrees.ListResult{
		{Task: "t", Branch: "b", WorktreeDir: "/wt/t/a/r",
			Owners: []worktrees.OwnerView{
				ownerAt(41, "e1", started.Add(time.Minute)),
				ownerAt(41, "e1", started.Add(2*time.Minute)), // duplicate effort
				ownerAt(41, "", started.Add(3*time.Minute)),   // empty effort ignored for Efforts, still attributes the worktree
			}},
	}
	row := attributeSessions(views, results)[0]
	if !reflect.DeepEqual(row.Efforts, []string{"e1"}) || len(row.Worktrees) != 1 || len(row.Branches) != 1 {
		t.Fatalf("row = %+v", row)
	}
}

func TestAttributeSessionsEmptyInputs(t *testing.T) {
	started := time.Now().UTC()
	rows := attributeSessions([]session.View{sessionAt(41, started)}, nil)
	if len(rows) != 1 || len(rows[0].Efforts) != 0 || rows[0].Efforts == nil {
		t.Fatalf("want one row with empty non-nil slices, got %+v", rows)
	}
}

func TestCondense(t *testing.T) {
	cases := []struct {
		in   []string
		max  int
		want string
	}{
		{nil, 24, "-"},
		{[]string{"only"}, 24, "only"},
		{[]string{"a", "b", "c"}, 24, "3"},
		{[]string{"a-very-long-effort-identifier-here"}, 10, "a-very-lon…"},
	}
	for _, c := range cases {
		if got := condense(c.in, c.max); got != c.want {
			t.Errorf("condense(%v, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestAttributeSessionsEmptyEffortAloneStillAttributes(t *testing.T) {
	started := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	views := []session.View{sessionAt(41, started)}
	results := []worktrees.ListResult{{Task: "t", Branch: "b", WorktreeDir: "/wt/t/a/r",
		Owners: []worktrees.OwnerView{ownerAt(41, "", started.Add(time.Minute))}}}
	row := attributeSessions(views, results)[0]
	if len(row.Efforts) != 0 || len(row.Worktrees) != 1 || len(row.Branches) != 1 {
		t.Fatalf("empty-effort owner must attribute the worktree without an effort entry: %+v", row)
	}
}

func TestAttributeSessionsExactStartBoundaryCounts(t *testing.T) {
	started := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	views := []session.View{sessionAt(41, started)}
	results := []worktrees.ListResult{{Task: "t", Branch: "b", WorktreeDir: "/wt/t/a/r",
		Owners: []worktrees.OwnerView{ownerAt(41, "e", started)}}}
	if row := attributeSessions(views, results)[0]; len(row.Efforts) != 1 {
		t.Fatalf("an owner entry at exactly StartedAt must attribute: %+v", row)
	}
}

func TestCondenseTruncatesRunesNotBytes(t *testing.T) {
	if got := condense([]string{"héllo wörld effort"}, 11); got != "héllo wörld…" {
		t.Fatalf("condense = %q, want rune-aware truncation", got)
	}
}
