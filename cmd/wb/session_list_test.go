package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// withSessionWorktreeLister installs a fixture in place of the real
// worktrees.List call for the duration of one test, restoring the original
// afterwards so later tests still exercise the real seam.
func withSessionWorktreeLister(t *testing.T, lister func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error)) {
	t.Helper()
	original := sessionWorktreeLister
	sessionWorktreeLister = lister
	t.Cleanup(func() { sessionWorktreeLister = original })
}

// registerTestSession writes a real session record through the session
// package's own API, so fixtures exercise the same StartedAt stamping
// production sessions get.
func registerTestSession(t *testing.T, dir string, pid int) session.Record {
	t.Helper()
	record, err := session.Register(dir, session.Record{PID: pid, Runtime: "test"})
	if err != nil {
		t.Fatalf("session.Register: %v", err)
	}
	return record
}

func lastFields(row string, n int) []string {
	fields := strings.Fields(row)
	if len(fields) < n {
		return fields
	}
	return fields[len(fields)-n:]
}

func TestSessionListRendersDerivedColumns(t *testing.T) {
	dir := t.TempDir()
	record := registerTestSession(t, dir, os.Getpid())
	results := []worktrees.ListResult{
		{
			Task: "task-7", Branch: "agent/task-7", WorktreeDir: "/wt/task-7/acme/x",
			Owners: []worktrees.OwnerView{
				{OwnerRegistration: worktrees.OwnerRegistration{
					PID: record.PID, Effort: "wb-claims-e2e", At: time.Now().Add(time.Minute),
				}},
			},
		},
	}
	withSessionWorktreeLister(t, func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error) {
		return results, nil
	})

	var out, errOut bytes.Buffer
	if err := runSessionList(dir, "unused", false, false, &out, &errOut); err != nil {
		t.Fatalf("runSessionList: %v", err)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("output lines = %d, want header+row: %q", len(lines), out.String())
	}
	header := lines[0]
	position := func(word string) int {
		index := strings.Index(header, word)
		if index < 0 {
			t.Fatalf("header %q missing %q", header, word)
		}
		return index
	}
	effortsAt, worktreesAt, branchesAt, stateAt := position("EFFORTS"), position("WORKTREES"), position("BRANCHES"), position("STATE")
	if effortsAt >= worktreesAt || worktreesAt >= branchesAt || branchesAt >= stateAt {
		t.Fatalf("header columns out of order: %q", header)
	}

	row := lines[1]
	for _, want := range []string{"wb-claims-e2e", "agent/task-7", "live"} {
		if !strings.Contains(row, want) {
			t.Errorf("row %q missing %q", row, want)
		}
	}
	columns := lastFields(row, 4)
	if columns[1] != "1" {
		t.Errorf("WORKTREES column = %q, want 1: row=%q", columns[1], row)
	}
}

func TestSessionListJSONCarriesFullLists(t *testing.T) {
	dir := t.TempDir()
	registerTestSession(t, dir, os.Getpid())
	withSessionWorktreeLister(t, func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error) {
		return nil, nil
	})

	var out, errOut bytes.Buffer
	if err := runSessionList(dir, "unused", false, true, &out, &errOut); err != nil {
		t.Fatalf("runSessionList: %v", err)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}

	var rows []sessionRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("decode json: %v\n%s", err, out.String())
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Efforts == nil || rows[0].Worktrees == nil || rows[0].Branches == nil {
		t.Fatalf("row lists must be non-nil arrays: %+v", rows[0])
	}
	if !strings.Contains(out.String(), `"efforts":[]`) && !strings.Contains(out.String(), `"efforts": []`) {
		t.Fatalf("json must render an empty array, not null: %s", out.String())
	}
}

func TestSessionListDegradesWhenWorktreesScanFails(t *testing.T) {
	dir := t.TempDir()
	registerTestSession(t, dir, os.Getpid())
	scanErr := errors.New("boom: unreadable worktrees root")
	withSessionWorktreeLister(t, func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error) {
		return nil, scanErr
	})

	var out, errOut bytes.Buffer
	if err := runSessionList(dir, "unused", false, false, &out, &errOut); err != nil {
		t.Fatalf("runSessionList returned error, want nil: %v", err)
	}
	if !strings.Contains(errOut.String(), "derive worktree attribution:") {
		t.Fatalf("stderr = %q, want the derivation warning", errOut.String())
	}
	if !strings.Contains(errOut.String(), scanErr.Error()) {
		t.Fatalf("stderr = %q, want the underlying error", errOut.String())
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("output lines = %d, want header+row: %q", len(lines), out.String())
	}
	columns := lastFields(lines[1], 4)
	if columns[0] != "-" || columns[1] != "-" || columns[2] != "-" {
		t.Fatalf("EFFORTS/WORKTREES/BRANCHES = %v, want all '-': row=%q", columns[:3], lines[1])
	}
	if columns[3] != "live" {
		t.Fatalf("STATE = %q, want live: row=%q", columns[3], lines[1])
	}
}

func TestSessionListNoSessionsSkipsScan(t *testing.T) {
	dir := t.TempDir()
	withSessionWorktreeLister(t, func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error) {
		panic("worktrees.List must not be called when no sessions have registered")
	})

	var out, errOut bytes.Buffer
	if err := runSessionList(dir, "unused", false, false, &out, &errOut); err != nil {
		t.Fatalf("runSessionList: %v", err)
	}
	if !strings.Contains(out.String(), "no session has registered") {
		t.Fatalf("stdout = %q, want the no-sessions message", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
}

// TestSessionListWithRealWorktreesLister exercises the real worktrees.List
// seam (not the fixture indirection) against an empty temp WB_HOME, so the
// production wiring itself — not just the join logic — is proven to degrade
// cleanly when there is simply nothing to find.
func TestSessionListWithRealWorktreesLister(t *testing.T) {
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)
	projectsRoot := t.TempDir()
	dir := t.TempDir()
	registerTestSession(t, dir, os.Getpid())

	var out, errOut bytes.Buffer
	if err := runSessionList(dir, projectsRoot, false, false, &out, &errOut); err != nil {
		t.Fatalf("runSessionList: %v", err)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("output lines = %d, want header+row: %q", len(lines), out.String())
	}
	columns := lastFields(lines[1], 4)
	if columns[0] != "-" || columns[1] != "-" || columns[2] != "-" {
		t.Fatalf("EFFORTS/WORKTREES/BRANCHES = %v, want all '-' on an empty home: row=%q", columns[:3], lines[1])
	}
}

func TestSessionListJSONWithZeroSessionsEmitsEmptyArray(t *testing.T) {
	withSessionWorktreeLister(t, func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error) {
		panic("worktrees.List must not be called when no sessions have registered")
	})
	var out, errOut bytes.Buffer
	if err := runSessionList(t.TempDir(), t.TempDir(), false, true, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("stdout = %q, want []", got)
	}
	if !strings.Contains(errOut.String(), "no session has registered") {
		t.Fatalf("stderr = %q, want guidance", errOut.String())
	}
}
