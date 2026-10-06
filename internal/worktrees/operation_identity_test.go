package worktrees

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestOperationWorkLogIdentityKeepsExactImmutableFields(t *testing.T) {
	t.Parallel()
	result := CreateResult{Repository: "acme/app", WorktreeDir: "/owned/checkout", Branch: "wb/task", Base: "main", BaseSHA: strings.Repeat("a", 40)}
	options := WorkLogOptions{EffortID: "task", RunID: "run", Model: "unknown", TaskSummary: " owned task "}
	claim := workLogClaim{EffortID: "task", RunID: "run", Task: "task", Repository: result.Repository, Worktree: result.WorktreeDir, Branch: result.Branch, Base: result.Base, BaseSHA: result.BaseSHA, ClaimID: workLogClaimID("task", result), TaskSummary: "owned task"}
	for _, field := range []string{"matching", "EffortID", "RunID", "Task", "Repository", "Worktree", "Branch", "Base", "BaseSHA", "ClaimID", "TaskSummary", "invalid options", "omitted summary"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			candidate, request := claim, options
			if field == "invalid options" {
				request.Model = "bad model"
			} else if field == "omitted summary" {
				request.TaskSummary = ""
			} else if field != "matching" {
				reflect.ValueOf(&candidate).Elem().FieldByName(field).SetString("different")
			}
			err := validateOperationWorkLogClaim(candidate, "task", result, request)
			if field == "matching" || field == "omitted summary" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatalf("accepted changed immutable %s", field)
			}
		})
	}
}

func TestOperationManifestIdentityPreservesValidLegacyBlanks(t *testing.T) {
	t.Parallel()
	want := newCreatedManifest("task")
	want.Worktree, want.RunID, want.ClaimID = "/owned/checkout", "run", "claim"
	for _, field := range []string{"matching", "EffortID", "Repository", "Branch", "Worktree", "Base", "BaseSHA", "RunID", "ClaimID", "blank legacy", "clean path"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			got := want
			if field == "blank legacy" {
				got.Worktree, got.Base, got.BaseSHA, got.RunID, got.ClaimID = "", "", "", "", ""
			} else if field == "clean path" {
				got.Worktree = "/owned/other/../checkout"
			} else if field != "matching" {
				reflect.ValueOf(&got).Elem().FieldByName(field).SetString("different")
			}
			err := validateOperationManifestIdentity(got, want)
			if field == "matching" || field == "blank legacy" || field == "clean path" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatalf("accepted contradictory %s", field)
			}
		})
	}
}

func TestLegacyBaseRecoveryPreservesNativeQueryAndFailure(t *testing.T) {
	t.Parallel()
	cause := errors.New("owned query refusal")
	for _, mode := range []string{"valid", "query error", "invalid output"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			calls := 0
			base, err := recoverLegacyWorktreeBase("acme/app", "wb/task", "current", func(args ...string) (string, error) {
				calls++
				if !reflect.DeepEqual(args, []string{"merge-base", "refs/heads/wb/task", "current"}) {
					t.Fatalf("native query changed: %v", args)
				}
				if mode == "query error" {
					return "", cause
				}
				if mode == "invalid output" {
					return "not-a-commit", nil
				}
				return strings.Repeat("a", 40), nil
			})
			if calls != 1 {
				t.Fatalf("query count=%d", calls)
			}
			if mode == "valid" {
				if err != nil || base != strings.Repeat("a", 40) {
					t.Fatalf("recovery: %q %v", base, err)
				}
			} else if err == nil || base != "" || (mode == "query error" && !errors.Is(err, cause)) {
				t.Fatalf("lost refusal: %q %v", base, err)
			}
		})
	}
}
