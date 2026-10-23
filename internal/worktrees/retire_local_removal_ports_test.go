package worktrees

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRetirementLocalRemovalPortsKeepProofBeforeCheckoutMutation(t *testing.T) {
	t.Parallel()
	boom := errors.New("injected local removal failure")
	for _, stage := range []string{"open", "validate", "canonical", "head", "changed head", "clean", "dirty", "remove", "after", "branch", "parent", "success", "success without hook"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			result := RetireResult{Canonical: "canonical", Worktree: "checkout", Branch: "topic", SourceSHA: strings.Repeat("a", 40), Phase: "original_deleted"}
			var events []string
			ports := retireLocalRemovalPorts{
				openWorktree: func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error) {
					events = append(events, "open")
					if stage == "open" {
						return nil, boom
					}
					return &cleanupWorktreeHandle{}, nil
				},
				validateHeld: func(*cleanupWorktreeHandle) error {
					events = append(events, "validate")
					if stage == "validate" {
						return boom
					}
					return nil
				},
				openCanonical: func(string) (*canonicalRepository, error) {
					events = append(events, "canonical")
					if stage == "canonical" {
						return nil, boom
					}
					return &canonicalRepository{}, nil
				},
				head: func(context.Context, string) (string, error) {
					events = append(events, "head")
					if stage == "head" {
						return "", boom
					}
					if stage == "changed head" {
						return "changed", nil
					}
					return result.SourceSHA, nil
				},
				clean: func(context.Context, string) (bool, error) {
					events = append(events, "clean")
					if stage == "clean" {
						return false, boom
					}
					if stage == "dirty" {
						return false, nil
					}
					return true, nil
				},
				removeWorktree: func(context.Context, *canonicalRepository, *cleanupWorktreeHandle, string) error {
					events = append(events, "remove")
					if stage == "remove" {
						return boom
					}
					return nil
				},
				deleteBranch: func(_ context.Context, _ *canonicalRepository, actual RetireResult) error {
					events = append(events, "branch")
					if actual.SourceSHA != result.SourceSHA {
						return errors.New("changed exact branch SHA")
					}
					if stage == "branch" {
						return boom
					}
					return nil
				},
				removeParent: func(*cleanupWorktreeHandle) error {
					events = append(events, "parent")
					if stage == "parent" {
						return boom
					}
					return nil
				},
			}
			after := func(phase string) error {
				events = append(events, "after")
				if phase != "worktree_removed" {
					t.Fatalf("wrong phase %s", phase)
				}
				if stage == "after" {
					return boom
				}
				return nil
			}
			if stage == "success without hook" {
				after = nil
			}
			err := retireRemoveLocalWithPorts(context.Background(), nil, ListResult{}, &result, after, ports)
			if stage == "success" || stage == "success without hook" {
				if err != nil || result.Phase != "complete" {
					t.Fatalf("completion=%+v, %v", result, err)
				}
				want := "open,validate,canonical,head,clean,remove,after,branch,parent"
				if after == nil {
					want = "open,validate,canonical,head,clean,remove,branch,parent"
				}
				if got := strings.Join(events, ","); got != want {
					t.Fatalf("order=%s, want %s", got, want)
				}
				return
			}
			if err == nil || result.Phase != "original_deleted" {
				t.Fatalf("failure=%+v, %v", result, err)
			}
			if (stage == "head" || stage == "changed head" || stage == "clean" || stage == "dirty") && strings.Contains(strings.Join(events, ","), "remove") {
				t.Fatal("checkout removed before exact HEAD and clean recheck")
			}
		})
	}
}
