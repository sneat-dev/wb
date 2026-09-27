package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The server update is a sequence of separately observed identities. A failed
// observation must leave a durable unverified receipt and must not sync a
// checkout whose head WB has not proved.
func TestUpdatePullRequestRecordsEachUnverifiedServerAdvance(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		change     func(*pullRequestUpdateOps)
		wantReason string
	}{
		{"intent write", func(ops *pullRequestUpdateOps) {
			ops.persist = func(PullRequestUpdateResult) error { return errors.New("disk full") }
		}, "record PR update intent"},
		{"server refusal", func(ops *pullRequestUpdateOps) {
			ops.update = func(context.Context, string, string, string) (string, string) { return "", "permission denied" }
		}, "permission denied"},
		{"server returns old head", func(ops *pullRequestUpdateOps) {
			ops.update = func(context.Context, string, string, string) (string, string) { return "old", "" }
		}, "distinct exact head"},
		{"updated head cannot be reread", func(ops *pullRequestUpdateOps) {
			reads := 0
			ops.read = func(_ context.Context, _, _ string) (PullRequestView, error) {
				reads++
				if reads == 1 {
					return prUpdateView(t, "old"), nil
				}
				return PullRequestView{}, errors.New("read failed")
			}
		}, "re-read updated PR"},
		{"updated head changed", func(ops *pullRequestUpdateOps) {
			reads := 0
			ops.read = func(_ context.Context, _, _ string) (PullRequestView, error) {
				reads++
				if reads == 1 {
					return prUpdateView(t, "old"), nil
				}
				return prUpdateView(t, "foreign"), nil
			}
		}, "identity/head changed"},
		{"parents unreadable", func(ops *pullRequestUpdateOps) {
			ops.parents = func(context.Context, string, string) ([]string, error) { return nil, errors.New("parents unavailable") }
		}, "merge parents are unverified"},
		{"target parent missing", func(ops *pullRequestUpdateOps) {
			ops.parents = func(context.Context, string, string) ([]string, error) { return []string{"old", ""}, nil }
		}, "merge parents are unverified"},
		{"target parent comparison failed", func(ops *pullRequestUpdateOps) {
			calls := 0
			ops.contains = func(context.Context, string, string, string) (bool, string) {
				calls++
				if calls == 1 {
					return false, ""
				}
				return false, "compare failed"
			}
		}, "target parent does not contain"},
		{"merge tree differs", func(ops *pullRequestUpdateOps) {
			ops.proveTree = func(context.Context, PullRequestUpdateOptions, string, string, string, string, string) (bool, error) {
				return false, nil
			}
		}, "merge tree could not be proved"},
		{"current target unreadable", func(ops *pullRequestUpdateOps) {
			calls := 0
			ops.target = func(context.Context, string, string) (string, string) {
				calls++
				if calls == 1 {
					return "target", ""
				}
				return "", "read failed"
			}
		}, "re-read current target"},
		{"current target missing", func(ops *pullRequestUpdateOps) {
			calls := 0
			ops.target = func(context.Context, string, string) (string, string) {
				calls++
				if calls == 1 {
					return "target", ""
				}
				return "", ""
			}
		}, "current target returned no SHA"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options, ops, saved := prUpdateFake(t)
			reads := 0
			ops.read = func(_ context.Context, _, _ string) (PullRequestView, error) {
				reads++
				if reads == 1 {
					return prUpdateView(t, "old"), nil
				}
				return prUpdateView(t, "new"), nil
			}
			ops.syncLocal = func(context.Context, PullRequestUpdateOptions, string, string) string {
				t.Fatal("unverified head reached local checkout")
				return ""
			}
			tc.change(&ops)
			result, err := updatePullRequestWith(context.Background(), options, ops)
			if err == nil || !strings.Contains(err.Error(), tc.wantReason) {
				t.Fatalf("error = %v, want %q", err, tc.wantReason)
			}
			if tc.name != "intent write" && (result.Status != "unverified" || len(*saved) < 2 || (*saved)[len(*saved)-1].Status != "unverified") {
				t.Fatalf("unverified advance not retained: result=%+v saved=%+v", result, *saved)
			}
		})
	}
}

func TestUpdatePullRequestRejectsInvalidPreflightWithoutWritingReceipt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		change     func(*PullRequestUpdateOptions, *pullRequestUpdateOps)
		wantReason string
	}{
		{"invalid number", func(options *PullRequestUpdateOptions, _ *pullRequestUpdateOps) { options.PullRequest = "07" }, "positive decimal"},
		{"PR read failed", func(_ *PullRequestUpdateOptions, ops *pullRequestUpdateOps) {
			ops.read = func(context.Context, string, string) (PullRequestView, error) {
				return PullRequestView{}, errors.New("PR unavailable")
			}
		}, "PR unavailable"},
		{"closed PR", func(_ *PullRequestUpdateOptions, ops *pullRequestUpdateOps) {
			ops.read = func(context.Context, string, string) (PullRequestView, error) {
				view := prUpdateView(t, "old")
				view.State = "closed"
				return view, nil
			}
		}, "not the expected open"},
		{"target read failed", func(_ *PullRequestUpdateOptions, ops *pullRequestUpdateOps) {
			ops.target = func(context.Context, string, string) (string, string) { return "", "target unavailable" }
		}, "target unavailable"},
		{"target omitted SHA", func(_ *PullRequestUpdateOptions, ops *pullRequestUpdateOps) {
			ops.target = func(context.Context, string, string) (string, string) { return "", "" }
		}, "returned no SHA"},
		{"comparison failed", func(_ *PullRequestUpdateOptions, ops *pullRequestUpdateOps) {
			ops.contains = func(context.Context, string, string, string) (bool, string) { return false, "comparison unavailable" }
		}, "comparison unavailable"},
		{"receipt path failed", func(_ *PullRequestUpdateOptions, ops *pullRequestUpdateOps) {
			ops.newReceipt = func(PullRequestUpdateOptions) (string, error) { return "", errors.New("cannot create receipt") }
		}, "cannot create receipt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options, ops, saved := prUpdateFake(t)
			tc.change(&options, &ops)
			_, err := updatePullRequestWith(context.Background(), options, ops)
			if err == nil || !strings.Contains(err.Error(), tc.wantReason) || len(*saved) != 0 {
				t.Fatalf("error = %v, receipts = %+v; want %q and no receipt", err, *saved, tc.wantReason)
			}
		})
	}
}

func TestUpdatePullRequestNoOpRecordsUnverifiedRead(t *testing.T) {
	t.Parallel()
	options, ops, saved := prUpdateFake(t)
	ops.contains = func(context.Context, string, string, string) (bool, string) { return true, "" }
	reads := 0
	ops.read = func(_ context.Context, _, _ string) (PullRequestView, error) {
		reads++
		if reads == 1 {
			return prUpdateView(t, "old"), nil
		}
		return PullRequestView{}, errors.New("PR reread unavailable")
	}
	ops.target = func(context.Context, string, string) (string, string) { return "target", "" }
	ops.syncLocal = func(context.Context, PullRequestUpdateOptions, string, string) string {
		t.Fatal("unverified no-op reached local checkout")
		return ""
	}
	result, err := updatePullRequestWith(context.Background(), options, ops)
	if err != nil || result.Status != "unverified" || !strings.Contains(result.Reason, "PR reread unavailable") || len(*saved) != 1 {
		t.Fatalf("result=%+v error=%v receipts=%+v", result, err, *saved)
	}
}

func prUpdateView(t *testing.T, head string) PullRequestView {
	t.Helper()
	var view PullRequestView
	data := `{"number":7,"state":"open","head":{"ref":"feature","sha":"` + head + `","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":"target","repo":{"full_name":"acme/app"}}}`
	if err := json.Unmarshal([]byte(data), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func prUpdateFake(t *testing.T) (PullRequestUpdateOptions, pullRequestUpdateOps, *[]PullRequestUpdateResult) {
	t.Helper()
	options := PullRequestUpdateOptions{Repository: "acme/app", PullRequest: "7", ProjectsRoot: t.TempDir()}
	saved := []PullRequestUpdateResult{}
	targetReads := 0
	ops := pullRequestUpdateOps{
		read: func(_ context.Context, _, _ string) (PullRequestView, error) { return prUpdateView(t, "old"), nil },
		target: func(_ context.Context, _, _ string) (string, string) {
			targetReads++
			if targetReads == 1 {
				return "target", ""
			}
			return "target-parent", ""
		},
		contains: func(_ context.Context, _, target, head string) (bool, string) {
			return target == "target" && head == "target-parent", ""
		},
		update:  func(_ context.Context, _, _, _ string) (string, string) { return "new", "" },
		parents: func(_ context.Context, _, _ string) ([]string, error) { return []string{"old", "target-parent"}, nil },
		proveTree: func(_ context.Context, _ PullRequestUpdateOptions, _, _, _, _, _ string) (bool, error) {
			return true, nil
		},
		syncLocal: func(_ context.Context, _ PullRequestUpdateOptions, _, _ string) string {
			return "fast-forwarded worktree"
		},
		newReceipt: func(PullRequestUpdateOptions) (string, error) {
			return filepath.Join(options.ProjectsRoot, "receipt.json"), nil
		},
		persist: func(receipt PullRequestUpdateResult) error { saved = append(saved, receipt); return nil },
	}
	return options, ops, &saved
}

func TestUpdatePullRequestReturnsReceiptBackedNoOpWithoutMutation(t *testing.T) {
	t.Parallel()
	options, ops, saved := prUpdateFake(t)
	ops.target = func(context.Context, string, string) (string, string) { return "target", "" }
	ops.contains = func(_ context.Context, _, _, _ string) (bool, string) { return true, "" }
	ops.update = func(context.Context, string, string, string) (string, string) {
		t.Fatal("no-op called update-branch")
		return "", ""
	}
	ops.syncLocal = func(_ context.Context, _ PullRequestUpdateOptions, branch, head string) string {
		if branch != "feature" || head != "old" {
			t.Fatalf("no-op synced wrong branch/head: %q %q", branch, head)
		}
		return "fast-forwarded worktree"
	}
	result, err := updatePullRequestWith(context.Background(), options, ops)
	if err != nil || result.Status != "unchanged" || result.AfterSHA != "old" || len(*saved) != 1 {
		t.Fatalf("result = %+v, err = %v, receipts = %+v", result, err, *saved)
	}
}

func TestUpdatePullRequestNoOpReportsTargetRaceWithoutMutation(t *testing.T) {
	t.Parallel()
	options, ops, saved := prUpdateFake(t)
	ops.contains = func(_ context.Context, _, _, _ string) (bool, string) { return true, "" }
	ops.update = func(context.Context, string, string, string) (string, string) {
		t.Fatal("target race called update-branch against stale observation")
		return "", ""
	}
	result, err := updatePullRequestWith(context.Background(), options, ops)
	if err != nil || result.Status != "unchanged_partial" || result.TargetBeforeSHA != "target" || result.TargetCurrentSHA != "target-parent" || len(*saved) != 1 {
		t.Fatalf("result = %+v, err = %v, receipts = %+v", result, err, *saved)
	}
}

func TestUpdatePullRequestNoOpReportsHeadRaceWithoutLocalSync(t *testing.T) {
	t.Parallel()
	options, ops, _ := prUpdateFake(t)
	ops.target = func(context.Context, string, string) (string, string) { return "target", "" }
	ops.contains = func(context.Context, string, string, string) (bool, string) { return true, "" }
	reads := 0
	ops.read = func(_ context.Context, _, _ string) (PullRequestView, error) {
		reads++
		if reads == 1 {
			return prUpdateView(t, "old"), nil
		}
		return prUpdateView(t, "foreign"), nil
	}
	ops.syncLocal = func(context.Context, PullRequestUpdateOptions, string, string) string {
		t.Fatal("stale head was used for local sync")
		return ""
	}
	result, err := updatePullRequestWith(context.Background(), options, ops)
	if err != nil || result.Status != "unchanged_partial" || !strings.Contains(result.Reason, "head") {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestUpdatePullRequestNeedsNoLocalCheckout(t *testing.T) {
	t.Parallel()
	options, ops, _ := prUpdateFake(t)
	reads := 0
	ops.read = func(_ context.Context, _, _ string) (PullRequestView, error) {
		reads++
		if reads == 1 {
			return prUpdateView(t, "old"), nil
		}
		return prUpdateView(t, "new"), nil
	}
	ops.syncLocal = func(context.Context, PullRequestUpdateOptions, string, string) string {
		return "local sync not applicable: no linked worktree holds feature"
	}
	result, err := updatePullRequestWith(context.Background(), options, ops)
	if err != nil || result.Status != "updated" || !strings.Contains(result.LocalSync, "no linked worktree") {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestUpdatePullRequestNoOpNeedsNoLocalCheckout(t *testing.T) {
	t.Parallel()
	options, ops, _ := prUpdateFake(t)
	ops.target = func(context.Context, string, string) (string, string) { return "target", "" }
	ops.contains = func(context.Context, string, string, string) (bool, string) { return true, "" }
	ops.syncLocal = func(context.Context, PullRequestUpdateOptions, string, string) string {
		return "local sync not applicable: no linked worktree holds feature"
	}
	result, err := updatePullRequestWith(context.Background(), options, ops)
	if err != nil || result.Status != "unchanged" || !strings.Contains(result.LocalSync, "not applicable") {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestUpdatePullRequestRejectsUnsafeRepositoryBeforeAnyRead(t *testing.T) {
	t.Parallel()
	options, ops, _ := prUpdateFake(t)
	options.Repository = "../app"
	ops.read = func(context.Context, string, string) (PullRequestView, error) {
		t.Fatal("unsafe repository reached GitHub")
		return PullRequestView{}, nil
	}
	if _, err := updatePullRequestWith(context.Background(), options, ops); err == nil {
		t.Fatal("unsafe repository accepted")
	}
}

func TestUpdatePullRequestPinsHeadProvesMergeAndRecordsBeforeLocalSync(t *testing.T) {
	t.Parallel()
	options, ops, saved := prUpdateFake(t)
	reads := 0
	ops.read = func(_ context.Context, _, _ string) (PullRequestView, error) {
		reads++
		if reads == 1 {
			return prUpdateView(t, "old"), nil
		}
		return prUpdateView(t, "new"), nil
	}
	ops.update = func(_ context.Context, repo, number, expected string) (string, string) {
		if repo != "acme/app" || number != "7" || expected != "old" || len(*saved) != 1 || (*saved)[0].Status != "requested" {
			t.Fatalf("update invoked without durable old-head lease: repo=%q number=%q head=%q saved=%+v", repo, number, expected, *saved)
		}
		return "new", ""
	}
	ops.proveTree = func(_ context.Context, _ PullRequestUpdateOptions, target, branch, old, targetParent, updated string) (bool, error) {
		if target != "main" || branch != "feature" || old != "old" || targetParent != "target-parent" || updated != "new" {
			t.Fatalf("wrong proof identity: %q %q %q %q %q", target, branch, old, targetParent, updated)
		}
		return true, nil
	}
	ops.syncLocal = func(_ context.Context, _ PullRequestUpdateOptions, branch, head string) string {
		if branch != "feature" || head != "new" || len(*saved) < 3 || (*saved)[len(*saved)-1].Status != "updated" {
			t.Fatalf("local sync preceded verified receipt: saved=%+v", *saved)
		}
		return "fast-forwarded worktree"
	}
	result, err := updatePullRequestWith(context.Background(), options, ops)
	if err != nil || result.Status != "updated" || result.BeforeSHA != "old" || result.AfterSHA != "new" || result.TargetBeforeSHA != "target" || result.TargetParentSHA != "target-parent" || len(*saved) != 4 {
		t.Fatalf("result = %+v, err = %v, receipts = %+v", result, err, *saved)
	}
}

func TestUpdatePullRequestRefusesUnprovedServerAdvanceBeforeLocalSync(t *testing.T) {
	t.Parallel()
	options, ops, saved := prUpdateFake(t)
	reads := 0
	ops.read = func(_ context.Context, _, _ string) (PullRequestView, error) {
		reads++
		if reads == 1 {
			return prUpdateView(t, "old"), nil
		}
		return prUpdateView(t, "new"), nil
	}
	ops.parents = func(context.Context, string, string) ([]string, error) {
		return []string{"foreign", "target-parent"}, nil
	}
	ops.syncLocal = func(context.Context, PullRequestUpdateOptions, string, string) string {
		t.Fatal("unproved remote head touched local checkout")
		return ""
	}
	result, err := updatePullRequestWith(context.Background(), options, ops)
	if err == nil || result.Status != "unverified" || result.AfterSHA != "new" || (*saved)[len(*saved)-1].Status != "unverified" {
		t.Fatalf("result = %+v, err = %v, receipts = %+v", result, err, *saved)
	}
}

func TestUpdatePullRequestReportsDirtyLocalCheckoutAsPartial(t *testing.T) {
	t.Parallel()
	options, ops, _ := prUpdateFake(t)
	reads := 0
	ops.read = func(_ context.Context, _, _ string) (PullRequestView, error) {
		reads++
		if reads == 1 {
			return prUpdateView(t, "old"), nil
		}
		return prUpdateView(t, "new"), nil
	}
	ops.syncLocal = func(context.Context, PullRequestUpdateOptions, string, string) string {
		return "local worktree not fast-forwarded: /checkout: uncommitted changes"
	}
	result, err := updatePullRequestWith(context.Background(), options, ops)
	if err != nil || result.Status != "updated_partial" || !strings.Contains(result.LocalSync, "uncommitted changes") {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestPullRequestUpdateReceiptPersistsPrivateExactSHAs(t *testing.T) {
	t.Parallel()
	options := PullRequestUpdateOptions{Repository: "acme/app", PullRequest: "7", ProjectsRoot: t.TempDir()}
	path, err := newPullRequestUpdateReceiptPath(options)
	if err != nil {
		t.Fatal(err)
	}
	receipt := PullRequestUpdateResult{SchemaVersion: 1, Repository: options.Repository, PullRequest: options.PullRequest,
		BeforeSHA: "old", TargetBeforeSHA: "target", AfterSHA: "new", TargetParentSHA: "target-parent", Status: "updated", ReceiptPath: path}
	if err := persistPullRequestUpdateReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got PullRequestUpdateResult
	if err := json.Unmarshal(contents, &got); err != nil || got.AfterSHA != "new" || got.TargetParentSHA != "target-parent" {
		t.Fatalf("receipt = %+v, err = %v", got, err)
	}
	stat, err := os.Stat(path)
	if err != nil || stat.Mode().Perm() != 0o600 {
		t.Fatalf("receipt permissions = %v, err = %v", stat.Mode(), err)
	}
}
