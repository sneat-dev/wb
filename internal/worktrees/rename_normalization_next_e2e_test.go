//go:build e2e

package worktrees

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestE2ERenameNormalizationPreservesBranchRefusalsAndCallerPolicies(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := RenameOptions{ProjectsRoot: root, OldTask: "old", NewTask: "new", WorkLog: WorkLogOptions{Model: "unknown", RunID: "fixed"}}
	for _, tc := range []struct {
		name    string
		mutate  func(*RenameOptions)
		wantErr string
	}{
		{"missing root", func(o *RenameOptions) { o.ProjectsRoot = "" }, "projects root is required"},
		{"missing old task", func(o *RenameOptions) { o.OldTask = "" }, "old task is required"},
		{"unsafe new task", func(o *RenameOptions) { o.NewTask = "../escape" }, "one safe path segment"},
		{"unchanged task", func(o *RenameOptions) { o.NewTask = o.OldTask }, "must differ from old task"},
		{"prefix before conflicting flags", func(o *RenameOptions) { o.Branch = "feature/task"; o.BranchPrefix = " feature/" }, "branch prefix must not have surrounding whitespace"},
		{"conflict before empty branch", func(o *RenameOptions) { o.BranchChosen = true; o.BranchPrefixChosen = true }, "--branch and --branch-prefix cannot be used together"},
		{"explicit empty branch", func(o *RenameOptions) { o.BranchChosen = true }, "--branch must not be empty when explicitly provided"},
		{"legacy padded empty branch", func(o *RenameOptions) { o.Branch = " " }, "--branch must not be empty when explicitly provided"},
		{"invalid base before selection", func(o *RenameOptions) { o.Base = "invalid base"; o.BranchPrefix = " padded/" }, "invalid base branch"},
		{"invalid feature", func(o *RenameOptions) { o.Branch = "invalid feature" }, "invalid feature branch"},
		{"feature equals base", func(o *RenameOptions) { o.Base = "main"; o.Branch = "main" }, "feature branch must differ from base branch"},
		{"unsafe preserve path", func(o *RenameOptions) { o.PreserveCachePaths = []string{"../escape"} }, "unsafe segment"},
		{"apply without remote", func(o *RenameOptions) { o.Apply = true }, "recycle apply requires --remote"},
		{"missing model", func(o *RenameOptions) { o.WorkLog.Model = "" }, "--model is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := base
			tc.mutate(&options)
			got, err := normalizeRenameOptions(options)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got=%+v err=%v want refusal=%q", got, err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, RenameOptions{}) {
				t.Fatalf("refused options escaped=%+v", got)
			}
		})
	}
}

func TestE2ERenameNormalizationPreservesLegacyAndEmptyPrefixSelection(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, tc := range []struct {
		name             string
		branch, prefix   string
		prefixChosen     bool
		wantBranch       string
		wantBranchChosen bool
		wantPrefixChosen bool
	}{
		{name: "defaults"},
		{name: "legacy branch", branch: " feature/task ", wantBranch: "feature/task", wantBranchChosen: true},
		{name: "legacy prefix", prefix: "feature/", wantPrefixChosen: true},
		{name: "explicit empty prefix", prefixChosen: true, wantPrefixChosen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := RenameOptions{ProjectsRoot: root, OldTask: " old ", NewTask: " new ", Branch: tc.branch, BranchPrefix: tc.prefix, BranchPrefixChosen: tc.prefixChosen, WorkLog: WorkLogOptions{Model: "unknown", RunID: "fixed"}}
			got, err := normalizeRenameOptions(options)
			if err != nil {
				t.Fatal(err)
			}
			if got.Now == nil || got.Now().IsZero() {
				t.Fatal("default clock absent")
			}
			got.Now = nil
			want := RenameOptions{ProjectsRoot: root, OldTask: "old", NewTask: "new", Base: "main", Branch: tc.wantBranch, BranchChosen: tc.wantBranchChosen, BranchPrefix: tc.prefix, BranchPrefixChosen: tc.wantPrefixChosen, DeleteOldBranch: true, WorkLog: options.WorkLog}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("normalized=%+v want=%+v", got, want)
			}
		})
	}
	t.Run("generated run and supplied clock", func(t *testing.T) {
		t.Parallel()
		clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		got, err := normalizeRenameOptions(RenameOptions{ProjectsRoot: root, OldTask: "old", NewTask: "new", WorkLog: WorkLogOptions{Model: "unknown"}, Now: func() time.Time { return clock }})
		if err != nil || !strings.HasPrefix(got.WorkLog.RunID, "wb-") || got.Now == nil || !got.Now().Equal(clock) {
			t.Fatalf("normalized=%+v err=%v", got, err)
		}
		if _, err := time.Parse("20060102T150405.000000000Z", strings.TrimPrefix(got.WorkLog.RunID, "wb-")); err != nil {
			t.Fatalf("generated run id=%q err=%v", got.WorkLog.RunID, err)
		}
	})
}
