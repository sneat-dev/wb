package worktrees

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func syntheticAbortEntry() ListResult {
	return ListResult{Task: "abort-task", Repository: "acme/app", CanonicalDir: "/synthetic/acme/app",
		WorktreesRoot: "/synthetic/.worktrees", WorktreeDir: "/synthetic/.worktrees/abort-task/acme/app",
		Branch: "abort-branch", Base: "main", HeadSHA: strings.Repeat("a", 40), Clean: true}
}

func syntheticAbortMemberPorts(entry ListResult) abortMemberPorts {
	return abortMemberPorts{
		openWorktree: func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error) {
			return &cleanupWorktreeHandle{}, nil
		},
		validateWorktree:  func(*cleanupWorktreeHandle) error { return nil },
		inspect:           func(context.Context, abortMemberInspection) (ListResult, error) { return entry, nil },
		openCanonical:     func(string) (*canonicalRepository, error) { return &canonicalRepository{}, nil },
		validateCanonical: func(*canonicalRepository) error { return nil },
		remoteHead:        func(context.Context, string, string) (string, error) { return "", nil },
		dirtyEvidence: func(context.Context, string) (DirtyWorktreeEvidence, error) {
			return DirtyWorktreeEvidence{SHA256: "digest"}, nil
		},
		captureDirty: func(context.Context, string, string, *DirtyWorktreeEvidence) (*DirtyWorktreeEvidence, error) {
			return &DirtyWorktreeEvidence{SHA256: "digest"}, nil
		},
		preflightWorkLog: func(string, AbortOptions, ListResult) (bool, error) { return false, nil },
		recoverLegacy:    func(string, AbortOptions, ListResult) error { return nil },
		sealDiscarded:    func(string, string, string, *DirtyWorktreeEvidence) error { return nil },
		newBacklog: func(string, ListResult, string) lifecycleBacklogRecord {
			return lifecycleBacklogRecord{ID: "backlog-id"}
		},
		persistBacklog: func(_ string, record *lifecycleBacklogRecord, stage string) error { record.Stage = stage; return nil },
		worktreeGit: func(context.Context, *canonicalRepository, *cleanupWorktreeHandle, string, ...string) error {
			return nil
		},
		canonicalGit:  func(context.Context, *canonicalRepository, ...string) error { return nil },
		validateTask:  func(*cleanupTaskHandle) error { return nil },
		removeAdopted: func(*cleanupTaskHandle, string, string) error { return nil },
	}
}

func TestAbsorbedAbortSafetyKeepsExactEvidence(t *testing.T) {
	t.Parallel()
	planned := syntheticAbortEntry()
	planned.AbsorbedBySHA = strings.Repeat("b", 40)
	refreshed := planned
	refreshed.AbsorbedAtOrigin = true
	for _, tc := range []struct {
		name             string
		change           func(*ListResult, *ListResult)
		absorbedBy, want string
	}{
		{"not requested", nil, "", ""},
		{"clean required", func(_, fresh *ListResult) { fresh.Clean = false }, "77", "clean worktree"},
		{"rejection detail", func(_, fresh *ListResult) { fresh.AbsorbedAtOrigin = false; fresh.AbsorbedByRejection = "stale target" }, "77", "stale target"},
		{"missing proof", func(_, fresh *ListResult) { fresh.AbsorbedAtOrigin = false }, "77", "no longer verifies"},
		{"landing changed", func(_, fresh *ListResult) { fresh.AbsorbedBySHA = "other" }, "77", "landing changed"},
		{"pull request changed", func(old, _ *ListResult) { old.MergedPullRequest = &PullRequest{Number: 77} }, "77", "pull request evidence changed"},
		{"exact proof", nil, "77", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			old, fresh := planned, refreshed
			if tc.change != nil {
				tc.change(&old, &fresh)
			}
			err := absorbedAbortSafety(old, fresh, tc.absorbedBy)
			if tc.want == "" && err != nil {
				t.Fatal(err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestAbortPreflightMemberRefusesChangedEvidence(t *testing.T) {
	t.Parallel()
	entry := syntheticAbortEntry()
	result := AbortResult{ListResult: entry}
	options := AbortOptions{Disposition: AbortDiscarded}
	marker := errors.New("port failure")
	for _, tc := range []struct {
		name   string
		change func(*abortMemberPorts, *AbortResult)
		want   string
	}{
		{"open worktree", func(p *abortMemberPorts, _ *AbortResult) {
			p.openWorktree = func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error) { return nil, marker }
		}, "port failure"},
		{"first descriptor validation", func(p *abortMemberPorts, _ *AbortResult) {
			p.validateWorktree = func(*cleanupWorktreeHandle) error { return marker }
		}, "port failure"},
		{"second descriptor validation", func(p *abortMemberPorts, _ *AbortResult) {
			calls := 0
			p.validateWorktree = func(*cleanupWorktreeHandle) error {
				calls++
				if calls == 2 {
					return marker
				}
				return nil
			}
		}, "port failure"},
		{"inspection", func(p *abortMemberPorts, _ *AbortResult) {
			p.inspect = func(context.Context, abortMemberInspection) (ListResult, error) { return ListResult{}, marker }
		}, "preflight abort"},
		{"head drift", func(p *abortMemberPorts, _ *AbortResult) {
			p.inspect = func(context.Context, abortMemberInspection) (ListResult, error) {
				changed := entry
				changed.HeadSHA = "other"
				return changed, nil
			}
		}, "checkout identity"},
		{"branch drift", func(p *abortMemberPorts, _ *AbortResult) {
			p.inspect = func(context.Context, abortMemberInspection) (ListResult, error) {
				changed := entry
				changed.Branch = "other"
				return changed, nil
			}
		}, "checkout identity"},
		{"repository drift", func(p *abortMemberPorts, _ *AbortResult) {
			p.inspect = func(context.Context, abortMemberInspection) (ListResult, error) {
				changed := entry
				changed.Repository = "acme/other"
				return changed, nil
			}
		}, "checkout identity"},
		{"absorbed proof", func(p *abortMemberPorts, _ *AbortResult) {
			p.inspect = func(context.Context, abortMemberInspection) (ListResult, error) {
				changed := entry
				changed.AbsorbedAtOrigin = false
				return changed, nil
			}
		}, "no longer verifies"},
		{"canonical open", func(p *abortMemberPorts, _ *AbortResult) {
			p.openCanonical = func(string) (*canonicalRepository, error) { return nil, marker }
		}, "open abort canonical"},
		{"canonical validation", func(p *abortMemberPorts, _ *AbortResult) {
			p.validateCanonical = func(*canonicalRepository) error { return marker }
		}, "port failure"},
		{"remote read", func(p *abortMemberPorts, _ *AbortResult) {
			p.remoteHead = func(context.Context, string, string) (string, error) { return "", marker }
		}, "inspect remote branch"},
		{"remote drift", func(p *abortMemberPorts, _ *AbortResult) {
			p.remoteHead = func(context.Context, string, string) (string, error) { return "other", nil }
		}, "expected exact local head"},
		{"work log", func(p *abortMemberPorts, _ *AbortResult) {
			p.preflightWorkLog = func(string, AbortOptions, ListResult) (bool, error) { return false, marker }
		}, "preflight aborted Work Log"},
		{"dirty read", func(p *abortMemberPorts, _ *AbortResult) {
			p.dirtyEvidence = func(context.Context, string) (DirtyWorktreeEvidence, error) { return DirtyWorktreeEvidence{}, marker }
		}, "capture dirty worktree evidence"},
		{"dirty drift", func(_ *abortMemberPorts, r *AbortResult) { r.DirtyCapture = &DirtyWorktreeEvidence{SHA256: "other"} }, "dirty worktree bytes changed"},
		{"success", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ports, selected := syntheticAbortMemberPorts(entry), result
			localOptions := options
			if tc.name == "absorbed proof" {
				localOptions.AbsorbedBy = "77"
				selected.AbsorbedAtOrigin = true
			}
			if tc.change != nil {
				tc.change(&ports, &selected)
			}
			fresh, remote, dirty, recovery, err := preflightAbortRepositoryWithPorts(context.Background(), "/synthetic", localOptions, nil, selected, "/synthetic/.wb", ports)
			if tc.want == "" && (err != nil || fresh.Repository != entry.Repository || remote != "" || dirty == nil || recovery) {
				t.Fatalf("preflight = %+v %q %+v %t, %v", fresh, remote, dirty, recovery, err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func syntheticAbortCoordinatorPorts(t *testing.T, entries []ListResult) abortPorts {
	t.Helper()
	root := t.TempDir()
	layout := wbhome.Layout{Home: filepath.Join(root, ".wb"), WorktreesRoot: filepath.Join(root, ".worktrees")}
	ports := productionAbortPorts()
	ports.resolve = func(string) (wbhome.Resolution, error) {
		return wbhome.Resolution{Write: layout, Read: []wbhome.Layout{layout}, Root: root}, nil
	}
	ports.discoverLocal = func(context.Context, string, string) ([]wbhome.Layout, []ListDiagnostic) { return nil, nil }
	ports.configuredLayouts = func(layouts []wbhome.Layout) ([]wbhome.Layout, error) { return layouts, nil }
	ports.loadBacklog = func(context.Context, string, string, []string, map[string]bool, string, string) ([]lifecycleBacklogRecord, []LifecycleBacklogQuarantine, error) {
		return nil, nil, nil
	}
	ports.list = func(context.Context, ListOptions) (ListOutcome, error) { return ListOutcome{Results: entries}, nil }
	ports.reservations = func(wbhome.Resolution, string, AbortOptions) ([]AbortResult, bool, error) { return nil, false, nil }
	ports.resumeBacklog = func(context.Context, string, *lifecycleBacklogRecord, bool) error { return nil }
	ports.acquireTask = func(_ string, task string) (*cleanupTaskHandle, error) {
		return acquireCleanupTaskAtOrCreate(filepath.Join(root, ".wb", "worktrees"), task)
	}
	ports.preflightWorkLog = func(string, AbortOptions, ListResult) (bool, error) { return false, nil }
	ports.dirtyEvidence = func(context.Context, string) (DirtyWorktreeEvidence, error) {
		return DirtyWorktreeEvidence{SHA256: "digest"}, nil
	}
	ports.preflightMember = func(_ context.Context, _ string, _ AbortOptions, _ *cleanupTaskHandle, result AbortResult, _ string) (ListResult, string, *DirtyWorktreeEvidence, bool, error) {
		return result.ListResult, "", &DirtyWorktreeEvidence{SHA256: "digest"}, false, nil
	}
	ports.applyDiscarded = func(_ context.Context, _ string, _ AbortOptions, _ *cleanupTaskHandle, _ string, result *AbortResult) error {
		result.WorktreeGone = true
		return nil
	}
	ports.transferWorkLog = func(string, string, string, string, string, ClaimExecutionIdentity) error { return nil }
	return ports
}

func TestAbortCoordinatorPreflightsEveryFreshMemberBeforeMutation(t *testing.T) {
	t.Parallel()
	one, two := syntheticAbortEntry(), syntheticAbortEntry()
	two.Repository, two.WorktreeDir = "acme/second", "/synthetic/.worktrees/abort-task/acme/second"
	ports := syntheticAbortCoordinatorPorts(t, []ListResult{one, two})
	var order []string
	ports.preflightMember = func(_ context.Context, _ string, _ AbortOptions, _ *cleanupTaskHandle, result AbortResult, _ string) (ListResult, string, *DirtyWorktreeEvidence, bool, error) {
		order = append(order, "preflight "+result.Repository)
		if result.Repository == two.Repository {
			return ListResult{}, "", nil, false, errors.New("second proof failed")
		}
		return result.ListResult, "", nil, false, nil
	}
	ports.applyDiscarded = func(_ context.Context, _ string, _ AbortOptions, _ *cleanupTaskHandle, _ string, result *AbortResult) error {
		order = append(order, "apply "+result.Repository)
		return nil
	}
	options := AbortOptions{ProjectsRoot: t.TempDir(), Task: one.Task, Disposition: AbortDiscarded, DeleteRemote: true, Apply: true, All: true}
	if _, err := abortWithPorts(context.Background(), options, ports); err == nil || !strings.Contains(err.Error(), "second proof failed") {
		t.Fatalf("second member error = %v", err)
	}
	if !reflect.DeepEqual(order, []string{"preflight acme/app", "preflight acme/second"}) {
		t.Fatalf("mutated before second proof: %v", order)
	}
}

func TestAbortCoordinatorSelectionAndPorts(t *testing.T) {
	t.Parallel()
	entry := syntheticAbortEntry()
	marker := errors.New("port failure")
	base := AbortOptions{ProjectsRoot: t.TempDir(), Task: entry.Task, Disposition: AbortDiscarded, DeleteRemote: true, Apply: true}
	for _, tc := range []struct {
		name   string
		change func(*AbortOptions, *abortPorts)
		want   string
	}{
		{"invalid task", func(o *AbortOptions, _ *abortPorts) { o.Task = "../bad" }, "safe path segment"},
		{"missing task", func(o *AbortOptions, _ *abortPorts) { o.Task = "" }, "task is required"},
		{"invalid disposition", func(o *AbortOptions, _ *abortPorts) { o.Disposition = "other" }, "disposition"},
		{"missing successor", func(o *AbortOptions, _ *abortPorts) { o.Disposition = AbortHandoff }, "successor"},
		{"successor forbidden", func(o *AbortOptions, _ *abortPorts) { o.Successor = "next" }, "successor cannot"},
		{"absorbed only discarded", func(o *AbortOptions, _ *abortPorts) {
			o.Disposition = AbortHandoff
			o.Successor = "next"
			o.AbsorbedBy = "77"
		}, "absorbed-by"},
		{"invalid successor identity", func(o *AbortOptions, _ *abortPorts) { o.Disposition = AbortHandoff; o.Successor = "next" }, "model"},
		{"identity forbidden", func(o *AbortOptions, _ *abortPorts) { o.SuccessorIdentity.Model = "model" }, "cannot be used"},
		{"orphaned without claim", func(o *AbortOptions, _ *abortPorts) { o.Disposition = AbortOrphaned }, "claim"},
		{"claim selector only orphaned", func(o *AbortOptions, _ *abortPorts) { o.ClaimID = "id" }, "valid only"},
		{"home resolution", func(_ *AbortOptions, p *abortPorts) {
			p.resolve = func(string) (wbhome.Resolution, error) { return wbhome.Resolution{}, marker }
		}, "port failure"},
		{"shared layouts", func(_ *AbortOptions, p *abortPorts) {
			p.configuredLayouts = func([]wbhome.Layout) ([]wbhome.Layout, error) { return nil, marker }
		}, "port failure"},
		{"backlog read", func(_ *AbortOptions, p *abortPorts) {
			p.loadBacklog = func(context.Context, string, string, []string, map[string]bool, string, string) ([]lifecycleBacklogRecord, []LifecycleBacklogQuarantine, error) {
				return nil, nil, marker
			}
		}, "port failure"},
		{"configured backlog root", func(_ *AbortOptions, p *abortPorts) {
			p.configuredLayouts = func([]wbhome.Layout) ([]wbhome.Layout, error) {
				return []wbhome.Layout{{WorktreesRoot: "/configured/worktrees"}}, nil
			}
			p.loadBacklog = func(_ context.Context, _, _ string, roots []string, _ map[string]bool, _, _ string) ([]lifecycleBacklogRecord, []LifecycleBacklogQuarantine, error) {
				if !slices.Contains(roots, "/configured/worktrees") {
					return nil, nil, errors.New("configured root omitted")
				}
				return nil, nil, nil
			}
		}, ""},
		{"inventory", func(_ *AbortOptions, p *abortPorts) {
			p.list = func(context.Context, ListOptions) (ListOutcome, error) { return ListOutcome{}, marker }
		}, "port failure"},
		{"remote authorization", func(o *AbortOptions, _ *abortPorts) { o.DeleteRemote = false }, "requires remote"},
		{"task lock", func(_ *AbortOptions, p *abortPorts) {
			p.acquireTask = func(string, string) (*cleanupTaskHandle, error) { return nil, marker }
		}, "port failure"},
		{"member preflight", func(_ *AbortOptions, p *abortPorts) {
			p.preflightMember = func(context.Context, string, AbortOptions, *cleanupTaskHandle, AbortResult, string) (ListResult, string, *DirtyWorktreeEvidence, bool, error) {
				return ListResult{}, "", nil, false, marker
			}
		}, "port failure"},
		{"discard apply", func(_ *AbortOptions, p *abortPorts) {
			p.applyDiscarded = func(context.Context, string, AbortOptions, *cleanupTaskHandle, string, *AbortResult) error {
				return marker
			}
		}, "port failure"},
		{"success", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o, p := base, syntheticAbortCoordinatorPorts(t, []ListResult{entry})
			if tc.change != nil {
				tc.change(&o, &p)
			}
			result, err := abortWithPorts(context.Background(), o, p)
			if tc.want == "" && (err != nil || len(result) != 1 || !result[0].Applied) {
				t.Fatalf("abort = %+v, %v", result, err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestAbortDiscardMemberRefusesEachFailedEffect(t *testing.T) {
	t.Parallel()
	entry := syntheticAbortEntry()
	marker := errors.New("port failure")
	base := AbortResult{ListResult: entry}
	base.RemoteHeadSHA = entry.HeadSHA
	options := AbortOptions{Disposition: AbortDiscarded, DeleteRemote: true, Apply: true}
	for _, tc := range []struct {
		name   string
		change func(*abortMemberPorts, *AbortOptions, *AbortResult)
		want   string
	}{
		{"open worktree", func(p *abortMemberPorts, _ *AbortOptions, _ *AbortResult) {
			p.openWorktree = func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error) { return nil, marker }
		}, "port failure"},
		{"first descriptor", func(p *abortMemberPorts, _ *AbortOptions, _ *AbortResult) {
			p.validateWorktree = func(*cleanupWorktreeHandle) error { return marker }
		}, "port failure"},
		{"inspection", func(p *abortMemberPorts, _ *AbortOptions, _ *AbortResult) {
			p.inspect = func(context.Context, abortMemberInspection) (ListResult, error) { return ListResult{}, marker }
		}, "recheck discarded worktree"},
		{"head drift", func(p *abortMemberPorts, _ *AbortOptions, _ *AbortResult) {
			p.inspect = func(context.Context, abortMemberInspection) (ListResult, error) {
				changed := entry
				changed.HeadSHA = "other"
				return changed, nil
			}
		}, "branch head moved"},
		{"branch drift", func(p *abortMemberPorts, _ *AbortOptions, _ *AbortResult) {
			p.inspect = func(context.Context, abortMemberInspection) (ListResult, error) {
				changed := entry
				changed.Branch = "other"
				return changed, nil
			}
		}, "branch head moved"},
		{"absorbed proof", func(p *abortMemberPorts, o *AbortOptions, r *AbortResult) {
			o.AbsorbedBy = "77"
			r.AbsorbedAtOrigin = true
			p.inspect = func(context.Context, abortMemberInspection) (ListResult, error) {
				changed := entry
				changed.AbsorbedAtOrigin = false
				return changed, nil
			}
		}, "no longer verifies"},
		{"second descriptor", func(p *abortMemberPorts, _ *AbortOptions, _ *AbortResult) {
			calls := 0
			p.validateWorktree = func(*cleanupWorktreeHandle) error {
				calls++
				if calls == 2 {
					return marker
				}
				return nil
			}
		}, "port failure"},
		{"remote read", func(p *abortMemberPorts, _ *AbortOptions, _ *AbortResult) {
			p.remoteHead = func(context.Context, string, string) (string, error) { return "", marker }
		}, "recheck remote branch"},
		{"remote drift", func(p *abortMemberPorts, _ *AbortOptions, _ *AbortResult) {
			p.remoteHead = func(context.Context, string, string) (string, error) { return "other", nil }
		}, "remote branch moved"},
		{"canonical open", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			p.openCanonical = func(string) (*canonicalRepository, error) { return nil, marker }
		}, "open abort canonical"},
		{"canonical validation", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			p.validateCanonical = func(*canonicalRepository) error { return marker }
		}, "port failure"},
		{"dirty evidence", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			p.dirtyEvidence = func(context.Context, string) (DirtyWorktreeEvidence, error) { return DirtyWorktreeEvidence{}, marker }
		}, "inspect dirty worktree"},
		{"dirty changed", func(_ *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			r.DirtyCapture = &DirtyWorktreeEvidence{SHA256: "other"}
		}, "dirty worktree bytes changed"},
		{"capture failed", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			p.inspect = func(context.Context, abortMemberInspection) (ListResult, error) {
				changed := entry
				changed.Clean = false
				return changed, nil
			}
			p.captureDirty = func(context.Context, string, string, *DirtyWorktreeEvidence) (*DirtyWorktreeEvidence, error) {
				return nil, marker
			}
		}, "capture dirty worktree"},
		{"legacy recovery failed", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			r.WorkLogRecoveryPlanned = true
			p.recoverLegacy = func(string, AbortOptions, ListResult) error { return marker }
		}, "recover legacy missing"},
		{"seal failed", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			p.sealDiscarded = func(string, string, string, *DirtyWorktreeEvidence) error { return marker }
		}, "seal discarded"},
		{"sealed backlog failed", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			p.persistBacklog = failAbortBacklogStage(lifecycleStageSealed, marker)
		}, "port failure"},
		{"retiring remote backlog failed", func(p *abortMemberPorts, _ *AbortOptions, _ *AbortResult) {
			p.remoteHead = abortRemoteAt(entry.HeadSHA)
			p.persistBacklog = failAbortBacklogStage(lifecycleStageRetiringRemote, marker)
		}, "port failure"},
		{"third descriptor", func(p *abortMemberPorts, _ *AbortOptions, _ *AbortResult) {
			p.remoteHead = abortRemoteAt(entry.HeadSHA)
			calls := 0
			p.validateWorktree = func(*cleanupWorktreeHandle) error {
				calls++
				if calls == 3 {
					return marker
				}
				return nil
			}
		}, "port failure"},
		{"remote delete failed", func(p *abortMemberPorts, _ *AbortOptions, _ *AbortResult) {
			p.remoteHead = abortRemoteAt(entry.HeadSHA)
			p.worktreeGit = func(_ context.Context, _ *canonicalRepository, _ *cleanupWorktreeHandle, _ string, args ...string) error {
				if args[0] == "push" {
					return marker
				}
				return nil
			}
		}, "delete discarded remote"},
		{"remote retired backlog failed", func(p *abortMemberPorts, _ *AbortOptions, _ *AbortResult) {
			p.remoteHead = abortRemoteAt(entry.HeadSHA)
			p.persistBacklog = failAbortBacklogStage(lifecycleStageRemoteRetired, marker)
		}, "port failure"},
		{"worktree descriptor before removal", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			calls := 0
			p.validateWorktree = func(*cleanupWorktreeHandle) error {
				calls++
				if calls == 3 {
					return marker
				}
				return nil
			}
		}, "port failure"},
		{"removing worktree backlog failed", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			p.persistBacklog = failAbortBacklogStage(lifecycleStageRemovingWorktree, marker)
		}, "port failure"},
		{"worktree removal failed", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			p.worktreeGit = func(_ context.Context, _ *canonicalRepository, _ *cleanupWorktreeHandle, _ string, args ...string) error {
				if args[0] == "worktree" {
					return marker
				}
				return nil
			}
		}, "remove discarded worktree"},
		{"worktree removed backlog failed", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			p.persistBacklog = failAbortBacklogStage(lifecycleStageWorktreeRemoved, marker)
		}, "port failure"},
		{"after removal failed", func(_ *abortMemberPorts, o *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			o.afterAbortWorktreeRemoval = func(string) error { return marker }
		}, "after discarded worktree removal"},
		{"task descriptor", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			p.validateTask = func(*cleanupTaskHandle) error { return marker }
		}, "port failure"},
		{"removing branch backlog failed", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			p.persistBacklog = failAbortBacklogStage(lifecycleStageRemovingLocalBranch, marker)
		}, "port failure"},
		{"local deletion failed", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			p.canonicalGit = func(context.Context, *canonicalRepository, ...string) error { return marker }
		}, "delete discarded branch"},
		{"invalid adopted repository", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			p.inspect = func(context.Context, abortMemberInspection) (ListResult, error) {
				changed := entry
				changed.External = true
				changed.Repository = "bad"
				return changed, nil
			}
		}, "registration identity"},
		{"adopted registration failed", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			p.inspect = func(context.Context, abortMemberInspection) (ListResult, error) {
				changed := entry
				changed.External = true
				return changed, nil
			}
			p.removeAdopted = func(*cleanupTaskHandle, string, string) error { return marker }
		}, "port failure"},
		{"complete backlog failed", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			p.persistBacklog = failAbortBacklogStage(lifecycleStageComplete, marker)
		}, "port failure"},
		{"success with remote", func(p *abortMemberPorts, _ *AbortOptions, _ *AbortResult) {
			p.remoteHead = abortRemoteAt(entry.HeadSHA)
			p.worktreeGit = func(_ context.Context, _ *canonicalRepository, _ *cleanupWorktreeHandle, _ string, args ...string) error {
				if args[0] == "push" && !reflect.DeepEqual(args, []string{"push", "--force-with-lease=refs/heads/" + entry.Branch + ":" + entry.HeadSHA, "origin", ":refs/heads/" + entry.Branch}) {
					return marker
				}
				return nil
			}
			p.canonicalGit = func(_ context.Context, _ *canonicalRepository, args ...string) error {
				if !reflect.DeepEqual(args, []string{"update-ref", "-d", "refs/heads/" + entry.Branch, entry.HeadSHA}) {
					return marker
				}
				return nil
			}
		}, ""},
		{"success detached", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.Branch = ""
			r.RemoteHeadSHA = ""
			p.inspect = func(context.Context, abortMemberInspection) (ListResult, error) {
				changed := entry
				changed.Branch = ""
				return changed, nil
			}
		}, ""},
		{"success dirty and recovered", func(p *abortMemberPorts, _ *AbortOptions, r *AbortResult) {
			r.RemoteHeadSHA = ""
			r.WorkLogRecoveryPlanned = true
			p.inspect = func(context.Context, abortMemberInspection) (ListResult, error) {
				changed := entry
				changed.Clean = false
				return changed, nil
			}
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ports, opts, result := syntheticAbortMemberPorts(entry), options, base
			if tc.change != nil {
				tc.change(&ports, &opts, &result)
			}
			err := applyDiscardedAbortWithPorts(context.Background(), "/synthetic", opts, nil, "/synthetic/.wb", &result, ports)
			if tc.want == "" && (err != nil || !result.WorktreeGone || (result.Branch != "" && !result.BranchDeleted)) {
				t.Fatalf("discard result = %+v, %v", result, err)
			}
			if tc.name == "success with remote" && !result.RemoteDeleted {
				t.Fatalf("exact leased remote deletion was not recorded: %+v", result)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func abortRemoteAt(head string) func(context.Context, string, string) (string, error) {
	return func(context.Context, string, string) (string, error) { return head, nil }
}

func failAbortBacklogStage(failed string, marker error) func(string, *lifecycleBacklogRecord, string) error {
	return func(_ string, record *lifecycleBacklogRecord, stage string) error {
		if stage == failed {
			return marker
		}
		record.Stage = stage
		return nil
	}
}

func TestAbortCoordinatorPlansSelectionAndResumesRecordedBacklog(t *testing.T) {
	t.Parallel()
	entry := syntheticAbortEntry()
	second := entry
	second.Repository, second.WorktreeDir = "acme/other", "/synthetic/.worktrees/abort-task/acme/other"
	marker := errors.New("port failure")
	base := AbortOptions{ProjectsRoot: t.TempDir(), Task: entry.Task, Disposition: AbortDiscarded, DeleteRemote: true, Apply: true}
	for _, tc := range []struct {
		name   string
		change func(*AbortOptions, *abortPorts)
		want   string
	}{
		{"not found", func(_ *AbortOptions, p *abortPorts) {
			p.list = func(context.Context, ListOptions) (ListOutcome, error) { return ListOutcome{}, nil }
		}, "was not found"},
		{"reservation error", func(_ *AbortOptions, p *abortPorts) {
			p.list = func(context.Context, ListOptions) (ListOutcome, error) { return ListOutcome{}, nil }
			p.reservations = func(wbhome.Resolution, string, AbortOptions) ([]AbortResult, bool, error) { return nil, false, marker }
		}, "port failure"},
		{"reservation found", func(_ *AbortOptions, p *abortPorts) {
			p.list = func(context.Context, ListOptions) (ListOutcome, error) { return ListOutcome{}, nil }
			p.reservations = func(wbhome.Resolution, string, AbortOptions) ([]AbortResult, bool, error) {
				return []AbortResult{{ReservationRuns: []string{"run"}}}, true, nil
			}
		}, ""},
		{"multiple members need selection", func(_ *AbortOptions, p *abortPorts) {
			p.list = func(context.Context, ListOptions) (ListOutcome, error) {
				return ListOutcome{Results: []ListResult{entry, second}}, nil
			}
		}, "select a member"},
		{"locked member", func(_ *AbortOptions, p *abortPorts) {
			changed := entry
			changed.Locked = true
			p.list = func(context.Context, ListOptions) (ListOutcome, error) {
				return ListOutcome{Results: []ListResult{changed}}, nil
			}
		}, "cannot be aborted safely"},
		{"unproved absorption", func(o *AbortOptions, _ *abortPorts) { o.AbsorbedBy = "77" }, "proof did not verify"},
		{"dirty absorption", func(o *AbortOptions, p *abortPorts) {
			o.AbsorbedBy = "77"
			changed := entry
			changed.AbsorbedAtOrigin = true
			changed.Clean = false
			p.list = func(context.Context, ListOptions) (ListOutcome, error) {
				return ListOutcome{Results: []ListResult{changed}}, nil
			}
		}, "requires a clean worktree"},
		{"blocking diagnostic", func(_ *AbortOptions, p *abortPorts) {
			p.list = func(context.Context, ListOptions) (ListOutcome, error) {
				return ListOutcome{Results: []ListResult{entry}, Diagnostics: []ListDiagnostic{{Path: entry.WorktreeDir, Message: "malformed"}}}, nil
			}
		}, "malformed worktree"},
		{"excluded diagnostic and dry-run member", func(o *AbortOptions, p *abortPorts) {
			o.Apply, o.Filter = false, "acme/app"
			p.list = func(context.Context, ListOptions) (ListOutcome, error) {
				return ListOutcome{Results: []ListResult{second, entry}, Diagnostics: []ListDiagnostic{{Path: entry.WorktreeDir, Message: "malformed"}}}, nil
			}
		}, ""},
		{"filter excludes live member", func(o *AbortOptions, _ *abortPorts) { o.Filter = "acme/unselected" }, ""},
		{"dry run", func(o *AbortOptions, _ *abortPorts) { o.Apply = false }, ""},
		{"dry-run Work Log refusal", func(o *AbortOptions, p *abortPorts) {
			o.Apply = false
			p.preflightWorkLog = func(string, AbortOptions, ListResult) (bool, error) { return false, marker }
		}, ""},
		{"dry-run dirty refusal", func(o *AbortOptions, p *abortPorts) {
			o.Apply = false
			p.dirtyEvidence = func(context.Context, string) (DirtyWorktreeEvidence, error) { return DirtyWorktreeEvidence{}, marker }
		}, ""},
		{"backlog resume refusal", func(o *AbortOptions, p *abortPorts) {
			o.All = true
			p.loadBacklog = func(context.Context, string, string, []string, map[string]bool, string, string) ([]lifecycleBacklogRecord, []LifecycleBacklogQuarantine, error) {
				return []lifecycleBacklogRecord{{ID: "id", Task: entry.Task, Repository: second.Repository, WorktreeDir: second.WorktreeDir}}, nil, nil
			}
			p.resumeBacklog = func(context.Context, string, *lifecycleBacklogRecord, bool) error { return marker }
		}, "port failure"},
		{"backlog only", func(_ *AbortOptions, p *abortPorts) {
			p.list = func(context.Context, ListOptions) (ListOutcome, error) { return ListOutcome{}, nil }
			p.loadBacklog = func(context.Context, string, string, []string, map[string]bool, string, string) ([]lifecycleBacklogRecord, []LifecycleBacklogQuarantine, error) {
				return []lifecycleBacklogRecord{{ID: "id", Task: entry.Task, Repository: entry.Repository, WorktreeDir: entry.WorktreeDir}}, nil, nil
			}
		}, ""},
		{"backlog filtered out", func(o *AbortOptions, p *abortPorts) {
			o.Filter = "acme/app"
			p.loadBacklog = func(context.Context, string, string, []string, map[string]bool, string, string) ([]lifecycleBacklogRecord, []LifecycleBacklogQuarantine, error) {
				return []lifecycleBacklogRecord{{ID: "id", Task: entry.Task, Repository: second.Repository, WorktreeDir: second.WorktreeDir}}, nil, nil
			}
		}, ""},
		{"quarantine reported", func(_ *AbortOptions, p *abortPorts) {
			p.loadBacklog = func(context.Context, string, string, []string, map[string]bool, string, string) ([]lifecycleBacklogRecord, []LifecycleBacklogQuarantine, error) {
				return nil, []LifecycleBacklogQuarantine{{Reason: "bad record"}}, nil
			}
		}, ""},
		{"handoff transfer failure", func(o *AbortOptions, p *abortPorts) {
			o.Disposition = AbortHandoff
			o.Successor = "next"
			o.SuccessorIdentity.Model = "unknown"
			p.transferWorkLog = func(string, string, string, string, string, ClaimExecutionIdentity) error { return marker }
		}, "transfer resumable"},
		{"handoff transfer success", func(o *AbortOptions, _ *abortPorts) {
			o.Disposition = AbortHandoff
			o.Successor = "next"
			o.SuccessorIdentity.Model = "unknown"
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts, ports := base, syntheticAbortCoordinatorPorts(t, []ListResult{entry})
			if tc.change != nil {
				tc.change(&opts, &ports)
			}
			result, err := abortWithPorts(context.Background(), opts, ports)
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if tc.want == "" && err != nil {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
			if tc.name == "dry-run Work Log refusal" && (len(result) != 1 || result[0].Eligible || !strings.Contains(result[0].Reason, "port failure")) {
				t.Fatalf("dry-run did not report Work Log refusal: %+v", result)
			}
			if tc.name == "dry-run dirty refusal" && (len(result) != 1 || result[0].Eligible || !strings.Contains(result[0].Reason, "port failure")) {
				t.Fatalf("dry-run did not report dirty refusal: %+v", result)
			}
			if tc.name == "excluded diagnostic and dry-run member" && (len(result) != 2 || !result[0].Excluded || result[1].Eligible || result[1].DirtyCapture == nil || result[1].Reason != "task has malformed worktree candidate: "+entry.WorktreeDir) {
				t.Fatalf("excluded member changed selected diagnostic result: %+v", result)
			}
			if tc.name == "backlog only" && (len(result) != 1 || !result[0].Applied || !result[0].BranchDeleted) {
				t.Fatalf("backlog not resumed: %+v", result)
			}
			if tc.name == "quarantine reported" && (len(result) != 1 || len(result[0].Quarantined) != 1) {
				t.Fatalf("quarantine omitted: %+v", result)
			}
		})
	}
}
