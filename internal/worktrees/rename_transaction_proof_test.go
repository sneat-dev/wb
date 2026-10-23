package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestRenameSourceProofPhases(t *testing.T) {
	t.Parallel()
	const head = "0123456789abcdef0123456789abcdef01234567"
	plan := &renamePlan{entry: ListResult{Repository: "owner/repo", WorktreeDir: "/old", HeadSHA: head}, refreshed: ListResult{HeadSHA: head}}
	options := RenameOptions{PreserveCachePaths: []string{"node_modules"}}
	probeErr := errors.New("probe failed")
	cases := []struct {
		name       string
		phase      renameSourceProofPhase
		result     ListResult
		inspectErr error
		cacheErr   error
		want       string
		cachePath  string
		cacheCalls int
	}{
		{name: "preflight inspect failure", phase: renamePreflightProof, inspectErr: probeErr, want: "preflight owner/repo: probe failed"},
		{name: "apply inspect failure", phase: renameApplyProof, inspectErr: probeErr, want: "recheck owner/repo before renaming: probe failed"},
		{name: "preflight dirty", phase: renamePreflightProof, result: ListResult{Repository: "owner/repo", HeadSHA: head}, want: "preflight owner/repo: worktree/head changed"},
		{name: "preflight moved head", phase: renamePreflightProof, result: ListResult{Repository: "owner/repo", Clean: true, HeadSHA: "changed"}, want: "preflight owner/repo: worktree/head changed"},
		{name: "preflight cache failure", phase: renamePreflightProof, result: ListResult{Repository: "owner/repo", Clean: true, HeadSHA: head, WorktreeDir: "/refreshed"}, cacheErr: probeErr, want: "preflight owner/repo: probe failed", cachePath: "/refreshed", cacheCalls: 1},
		{name: "preflight success", phase: renamePreflightProof, result: ListResult{Repository: "owner/repo", Clean: true, HeadSHA: head, WorktreeDir: "/refreshed"}, cachePath: "/refreshed", cacheCalls: 1},
		{name: "apply dirty", phase: renameApplyProof, result: ListResult{Repository: "owner/repo", HeadSHA: head}, want: "worktree has local changes"},
		{name: "apply moved head", phase: renameApplyProof, result: ListResult{Repository: "owner/repo", Clean: true, HeadSHA: "changed"}, want: "branch head moved"},
		{name: "apply cache failure", phase: renameApplyProof, result: ListResult{Repository: "owner/repo", Clean: true, HeadSHA: head}, cacheErr: probeErr, want: "prepare owner/repo for recycle: probe failed", cachePath: "/old", cacheCalls: 1},
		{name: "apply coordinated head changed", phase: renameApplyProof, result: ListResult{Repository: "owner/repo", Clean: true, HeadSHA: head}, want: "after coordinated preflight", cachePath: "/old", cacheCalls: 1},
		{name: "apply success", phase: renameApplyProof, result: ListResult{Repository: "owner/repo", Clean: true, HeadSHA: head}, cachePath: "/old", cacheCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			localPlan := *plan
			if tc.name == "apply coordinated head changed" {
				localPlan.refreshed.HeadSHA = "old-preflight"
			}
			cacheCalls := 0
			result, err := proveRenameSource(context.Background(), options, &localPlan, tc.phase, renameSourceProofPorts{
				Inspect: func(context.Context, RenameOptions, *renamePlan) (ListResult, error) { return tc.result, tc.inspectErr },
				VerifyCache: func(_ context.Context, path string, paths []string) error {
					cacheCalls++
					if path != tc.cachePath || len(paths) != 1 || paths[0] != "node_modules" {
						t.Fatalf("cache proof path=%q paths=%v", path, paths)
					}
					return tc.cacheErr
				},
			})
			if tc.want == "" {
				if err != nil || result.HeadSHA != tc.result.HeadSHA {
					t.Fatalf("proof result=%+v error=%v", result, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("proof error=%v, want %q", err, tc.want)
			}
			if cacheCalls != tc.cacheCalls {
				t.Fatalf("cache calls=%d, want %d", cacheCalls, tc.cacheCalls)
			}
		})
	}
}

func TestRenameSharedDestinationProbeFaults(t *testing.T) {
	t.Parallel()
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	probeErr := errors.New("injected probe failure")
	for _, tc := range []struct {
		name      string
		mkdirErr  error
		unlinkErr error
		want      string
	}{
		{name: "create", mkdirErr: probeErr, want: "verify shared rename destination write access"},
		{name: "remove", unlinkErr: probeErr, want: "remove shared rename destination probe"},
		{name: "success"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			created := false
			removed := false
			err := probeRenameSharedDestination(root, func(fd int, name string, mode uint32) error {
				if fd != int(root.Fd()) || !strings.HasPrefix(name, ".wb-rename-probe-") || mode != 0o700 {
					t.Fatalf("mkdir fd=%d name=%q mode=%o", fd, name, mode)
				}
				created = true
				return tc.mkdirErr
			}, func(fd int, name string, flags int) error {
				if fd != int(root.Fd()) || !strings.HasPrefix(name, ".wb-rename-probe-") || flags != unix.AT_REMOVEDIR {
					t.Fatalf("unlink fd=%d name=%q flags=%d", fd, name, flags)
				}
				removed = true
				return tc.unlinkErr
			})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) || !errors.Is(err, probeErr) {
				t.Fatalf("probe error=%v, want %q", err, tc.want)
			}
			if !created || removed != (tc.mkdirErr == nil) {
				t.Fatalf("probe call order create=%t remove=%t", created, removed)
			}
		})
	}
}

func TestRenameSharedPhysicalDestinationFailureBoundaries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, tc := range []struct {
		name      string
		root      string
		dest      string
		task      string
		setup     func(*testing.T, string)
		wantError string
	}{
		{name: "mixed relative and absolute", root: "relative", dest: filepath.Join(root, "task", "acme", "app"), task: "task", wantError: "resolve shared rename destination"},
		{name: "wrong task child", root: root, dest: filepath.Join(root, "other", "acme", "app"), task: "task", wantError: "not below its task directory"},
		{name: "task is file", root: root, dest: filepath.Join(root, "task", "acme", "app"), task: "task", setup: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "task"), []byte("occupied"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantError: "create shared rename task destination"},
		{name: "owner is file", root: root, dest: filepath.Join(root, "task", "acme", "app"), task: "task", setup: func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, "task"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "task", "acme"), []byte("occupied"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantError: "create shared rename destination parent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Each case needs its own root because destination preparation is
			// intentionally allowed to create intermediate task directories.
			caseRoot := tc.root
			caseDest := tc.dest
			if tc.root == root {
				caseRoot = t.TempDir()
				caseDest = filepath.Join(caseRoot, strings.TrimPrefix(tc.dest, root+string(filepath.Separator)))
			}
			if tc.setup != nil {
				tc.setup(t, caseRoot)
			}
			plan := &renamePlan{destinationRoot: caseRoot, result: RenameResult{NewWorktreeDir: caseDest}}
			if err := prepareRenamePhysicalDestination(t.Context(), tc.task, plan); err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("prepare error=%v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestRenamePreflightPolicyFaultMatrix(t *testing.T) {
	t.Parallel()
	const head = "0123456789abcdef0123456789abcdef01234567"
	probeErr := errors.New("injected policy failure")
	for _, tc := range []struct {
		name         string
		failAt       string
		baseMoved    bool
		branchMoved  bool
		branchExists bool
		unmerged     bool
		force        bool
		remoteHead   string
		deleteRemote bool
		want         string
		stopAt       string
	}{
		{name: "sync error", failAt: "sync", want: "fetch base before recycling", stopAt: "sync"},
		{name: "base moved", baseMoved: true, want: "advanced", stopAt: "sync"},
		{name: "branch policy error", failAt: "branch", want: "injected policy failure", stopAt: "branch"},
		{name: "branch policy changed", branchMoved: true, want: "branch policy changed", stopAt: "branch"},
		{name: "local branch query error", failAt: "local", want: "injected policy failure", stopAt: "local"},
		{name: "local branch occupied", branchExists: true, want: "already exists", stopAt: "local"},
		{name: "ancestry query error", failAt: "ancestor", want: "injected policy failure", stopAt: "ancestor"},
		{name: "unmerged source refused", unmerged: true, want: "not integrated", stopAt: "ancestor"},
		{name: "remote query error", failAt: "remote", want: "inspect old remote branch", stopAt: "remote"},
		{name: "remote head changed", remoteHead: "other", want: "expected exact old head", stopAt: "remote"},
		{name: "remote requires authority", remoteHead: head, want: "rerun recycle with --remote", stopAt: "remote"},
		{name: "claim preflight error", failAt: "claim", deleteRemote: true, want: "injected policy failure", stopAt: "claim"},
		{name: "destination preflight error", failAt: "destination", deleteRemote: true, want: "injected policy failure", stopAt: "destination"},
		{name: "merged source accepted", deleteRemote: true},
		{name: "force accepted after unmerged proof", unmerged: true, force: true, deleteRemote: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan := &renamePlan{entry: ListResult{Repository: "acme/app"}, baseRevision: "base", result: RenameResult{NewBranch: "new"}}
			refreshed := ListResult{Repository: "acme/app", Branch: "old", HeadSHA: head}
			options := RenameOptions{Base: "main", Force: tc.force, DeleteRemote: tc.deleteRemote}
			stages := []string{}
			visit := func(name string) error {
				stages = append(stages, name)
				if name == tc.failAt {
					return probeErr
				}
				return nil
			}
			ports := renamePreflightPolicyPorts{
				SyncBase: func() (string, error) {
					err := visit("sync")
					if tc.baseMoved {
						return "other-base", err
					}
					return "base", err
				},
				DeriveBranch: func(base string) (string, error) {
					if base != "base" {
						t.Fatalf("unpinned base %q", base)
					}
					err := visit("branch")
					if tc.branchMoved {
						return "other-branch", err
					}
					return "new", err
				},
				LocalBranchExists:    func() (bool, error) { return tc.branchExists, visit("local") },
				IsAncestor:           func() (bool, error) { return !tc.unmerged, visit("ancestor") },
				RemoteHead:           func() (string, error) { return tc.remoteHead, visit("remote") },
				PreflightClaim:       func() error { return visit("claim") },
				PreflightDestination: func() error { return visit("destination") },
			}
			base, remote, err := proveRenamePreflightPolicy(options, plan, refreshed, ports)
			if tc.want == "" {
				if err != nil || base != "base" || remote != tc.remoteHead {
					t.Fatalf("preflight proof base=%q remote=%q error=%v", base, remote, err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.want) || base != "" || remote != "" {
					t.Fatalf("preflight proof base=%q remote=%q error=%v, want %q", base, remote, err, tc.want)
				}
				if tc.failAt != "" && !errors.Is(err, probeErr) {
					t.Fatalf("preflight lost original failure: %v", err)
				}
			}
			if tc.stopAt != "" && stages[len(stages)-1] != tc.stopAt {
				t.Fatalf("stages=%v continued beyond %q", stages, tc.stopAt)
			}
			if tc.stopAt == "" && (len(stages) != 7 || stages[len(stages)-1] != "destination") {
				t.Fatalf("successful stages=%v", stages)
			}
			if plan.baseRevision != "base" || plan.remoteHead != "" {
				t.Fatalf("policy proof mutated plan before all members passed: %#v", plan)
			}
		})
	}
}

func TestRenameRemoteRetirementExactLeaseMatrix(t *testing.T) {
	t.Parallel()
	const head = "0123456789abcdef0123456789abcdef01234567"
	probeErr := errors.New("injected remote failure")
	for _, tc := range []struct {
		name         string
		planned      string
		current      string
		queryErr     error
		deleteErr    error
		deleteRemote bool
		want         string
		deleteCalls  int
		deleted      bool
	}{
		{name: "no remote remains", planned: "", current: ""},
		{name: "query fails", queryErr: probeErr, want: "recheck remote branch"},
		{name: "remote appears", current: head, want: "remote branch moved"},
		{name: "remote disappears", planned: head, want: "remote branch moved"},
		{name: "remote advances", planned: head, current: "other", want: "remote branch moved"},
		{name: "explicit authority required", planned: head, current: head, want: "requires explicit --remote"},
		{name: "exact deletion fails", planned: head, current: head, deleteRemote: true, deleteErr: probeErr, want: "retire old remote branch", deleteCalls: 1},
		{name: "exact deletion succeeds", planned: head, current: head, deleteRemote: true, deleteCalls: 1, deleted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan := &renamePlan{entry: ListResult{Repository: "acme/app", Branch: "old"}, remoteHead: tc.planned}
			deleteCalls := 0
			err := retireRenameRemote(RenameOptions{DeleteRemote: tc.deleteRemote}, plan, ListResult{HeadSHA: head}, renameRemoteRetirementPorts{
				CurrentHead: func() (string, error) { return tc.current, tc.queryErr },
				DeleteExact: func(source string) error {
					deleteCalls++
					if source != head {
						t.Fatalf("deletion source %q, want immutable %q", source, head)
					}
					return tc.deleteErr
				},
			})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("remote retirement error=%v, want %q", err, tc.want)
			}
			if tc.queryErr != nil || tc.deleteErr != nil {
				if !errors.Is(err, probeErr) {
					t.Fatalf("lost original remote error: %v", err)
				}
			}
			if deleteCalls != tc.deleteCalls || plan.remoteDeleted != tc.deleted || plan.result.OldRemoteDeleted != tc.deleted {
				t.Fatalf("remote result calls=%d deleted=%t report=%t, want %+v", deleteCalls, plan.remoteDeleted, plan.result.OldRemoteDeleted, tc)
			}
		})
	}
}

func TestRenameRemoteRestoreRecordedSourceMatrix(t *testing.T) {
	t.Parallel()
	const head = "0123456789abcdef0123456789abcdef01234567"
	probeErr := errors.New("injected remote failure")
	for _, tc := range []struct {
		name          string
		before        string
		after         string
		firstQueryErr error
		pushErr       error
		postQueryErr  error
		want          string
		queries       int
		pushes        int
	}{
		{name: "prequery fails", firstQueryErr: probeErr, want: "inspect remote before recycle rollback", queries: 1},
		{name: "foreign remote prevents push", before: "foreign", want: "another actor created it", queries: 1},
		{name: "exact push fails", pushErr: probeErr, want: "restore retired remote branch", queries: 1, pushes: 1},
		{name: "postpush query fails", postQueryErr: probeErr, want: "injected remote failure", queries: 2, pushes: 1},
		{name: "postpush SHA changed", after: "foreign", want: "expected " + head, queries: 2, pushes: 1},
		{name: "exact receipt", after: head, queries: 2, pushes: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan := &renamePlan{entry: ListResult{Branch: "old", HeadSHA: head}}
			queries, pushes := 0, 0
			err := restoreRetiredRenameRemote(plan, renameRemoteRestorePorts{
				RemoteHead: func() (string, error) {
					queries++
					if queries == 1 {
						return tc.before, tc.firstQueryErr
					}
					return tc.after, tc.postQueryErr
				},
				PushRecorded: func() error { pushes++; return tc.pushErr },
			})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("restore error=%v, want %q", err, tc.want)
			}
			if tc.firstQueryErr != nil || tc.pushErr != nil || tc.postQueryErr != nil {
				if !errors.Is(err, probeErr) {
					t.Fatalf("lost remote failure: %v", err)
				}
			}
			if queries != tc.queries || pushes != tc.pushes {
				t.Fatalf("remote operations queries=%d pushes=%d, want %d/%d", queries, pushes, tc.queries, tc.pushes)
			}
		})
	}
}

func TestRenameRemoteAdaptersRejectMissingCanonicalBeforePush(t *testing.T) {
	t.Parallel()
	plan := &renamePlan{entry: ListResult{CanonicalDir: filepath.Join(t.TempDir(), "missing"), Branch: "old"}}
	if err := productionRenameRemoteRetirementPorts(t.Context(), plan).DeleteExact("head"); err == nil {
		t.Fatal("retirement opened a missing canonical repository")
	}
	if err := productionRenameRemoteRestorePorts(t.Context(), plan).PushRecorded(); err == nil {
		t.Fatal("restore opened a missing canonical repository")
	}
}

func TestRenameRollbackClaimAuthorityBeforeCompensation(t *testing.T) {
	t.Parallel()
	probeErr := errors.New("injected claim failure")
	for _, tc := range []struct {
		name       string
		claimID    string
		readErr    error
		failAt     string
		want       string
		wantStages string
	}{
		{name: "explicit absence", readErr: errWorkLogProjectionNotFound, wantStages: "read"},
		{name: "unreadable evidence", readErr: probeErr, want: "inspect fresh recycle projection", wantStages: "read"},
		{name: "original claim remains", claimID: "prior", wantStages: "read"},
		{name: "head unavailable", claimID: "fresh", failAt: "head", want: "injected claim failure", wantStages: "read,head"},
		{name: "fresh seal fails", claimID: "fresh", failAt: "seal", want: "injected claim failure", wantStages: "read,head,seal"},
		{name: "projection removal fails", claimID: "fresh", failAt: "remove", want: "injected claim failure", wantStages: "read,head,seal,remove"},
		{name: "fresh claim sealed before removal", claimID: "fresh", wantStages: "read,head,seal,remove"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan := &renamePlan{priorProjection: workLogProjection{ClaimID: "prior"}}
			stages := []string{}
			visit := func(stage string) error {
				stages = append(stages, stage)
				if stage == tc.failAt {
					return probeErr
				}
				return nil
			}
			err := retireFreshRenameClaimOnRollback(plan, renameRollbackClaimPorts{
				ReadProjection: func() (workLogProjection, error) {
					_ = visit("read")
					return workLogProjection{ClaimID: tc.claimID}, tc.readErr
				},
				CurrentHead: func() (string, error) { return "recorded-head", visit("head") },
				SealFresh: func(head string) error {
					if head != "recorded-head" {
						t.Fatalf("sealed head %q", head)
					}
					return visit("seal")
				},
				RemoveFresh: func() error { return visit("remove") },
			})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("claim rollback error=%v, want %q", err, tc.want)
			}
			if tc.readErr == probeErr || tc.failAt != "" {
				if !errors.Is(err, probeErr) {
					t.Fatalf("lost claim error: %v", err)
				}
			}
			if got := strings.Join(stages, ","); got != tc.wantStages {
				t.Fatalf("claim operations=%q, want %q", got, tc.wantStages)
			}
		})
	}
}

func TestRenameFailedBranchRemovalRetainsCanonicalAndExactTip(t *testing.T) {
	t.Parallel()
	probeErr := errors.New("injected branch failure")
	for _, tc := range []struct {
		name       string
		exists     bool
		failAt     string
		head       string
		want       string
		wantStages string
	}{
		{name: "query fails", failAt: "exists", want: "injected branch failure", wantStages: "exists"},
		{name: "branch absent", wantStages: "exists"},
		{name: "canonical open fails", exists: true, failAt: "open", want: "injected branch failure", wantStages: "exists,open"},
		{name: "tip query fails", exists: true, failAt: "head", want: "injected branch failure", wantStages: "exists,open,head"},
		{name: "advanced tip refused", exists: true, head: "foreign", want: "refuse to remove failed recycle branch", wantStages: "exists,open,head"},
		{name: "exact CAS fails", exists: true, head: "base", failAt: "delete", want: "injected branch failure", wantStages: "exists,open,head,delete"},
		{name: "exact CAS succeeds", exists: true, head: "base", wantStages: "exists,open,head,delete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan := &renamePlan{baseRevision: "base", result: RenameResult{NewBranch: "new"}}
			canonical := &canonicalRepository{}
			stages := []string{}
			visit := func(stage string) error {
				stages = append(stages, stage)
				if stage == tc.failAt {
					return probeErr
				}
				return nil
			}
			err := removeFailedRenameBranch(plan, renameFailedBranchPorts{
				Exists: func() (bool, error) { return tc.exists, visit("exists") },
				Open:   func() (*canonicalRepository, error) { return canonical, visit("open") },
				Head: func(got *canonicalRepository) (string, error) {
					if got != canonical {
						t.Fatal("query lost retained canonical handle")
					}
					return tc.head, visit("head")
				},
				DeleteExact: func(got *canonicalRepository) error {
					if got != canonical {
						t.Fatal("CAS lost retained canonical handle")
					}
					return visit("delete")
				},
			})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("branch rollback error=%v, want %q", err, tc.want)
			}
			if tc.failAt != "" && !errors.Is(err, probeErr) {
				t.Fatalf("lost branch failure: %v", err)
			}
			if got := strings.Join(stages, ","); got != tc.wantStages {
				t.Fatalf("branch operations=%q, want %q", got, tc.wantStages)
			}
		})
	}
}

func TestRenameTaskMemberPlanningFaultAndCollisionMatrix(t *testing.T) {
	t.Parallel()
	probeErr := errors.New("injected planning failure")
	for _, tc := range []struct {
		name      string
		failAt    string
		local     bool
		occupied  bool
		members   int
		want      string
		statCalls int
		collision bool
	}{
		{name: "branch query fails", failAt: "branch", members: 1, want: "injected planning failure"},
		{name: "placement fails", failAt: "placement", members: 1, want: "injected planning failure"},
		{name: "destination path fails", failAt: "path", members: 1, want: "injected planning failure"},
		{name: "destination stat fails", failAt: "stat", members: 1, want: "inspect destination task", statCalls: 1},
		{name: "shared collision blocks task", occupied: true, members: 2, statCalls: 1, collision: true},
		{name: "shared task checked once", members: 2, statCalls: 1},
		{name: "local destinations checked separately", local: true, members: 2, statCalls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			options := RenameOptions{OldTask: "old", NewTask: "new", Base: "main"}
			entries := make([]ListResult, tc.members)
			for i := range entries {
				entries[i] = ListResult{Repository: fmt.Sprintf("acme/app%d", i), WorktreeDir: fmt.Sprintf("/old/app%d", i), Branch: "old", Clean: true}
			}
			statCalls := 0
			ports := renamePlanningPorts{
				ResolveBranch: func(ListResult) (string, string, error) {
					if tc.failAt == "branch" {
						return "", "", probeErr
					}
					return "new", "base", nil
				},
				ResolvePlacement: func(_ ListResult, base string) (WorktreePlacement, error) {
					if base != "base" {
						t.Fatalf("placement base %q", base)
					}
					if tc.failAt == "placement" {
						return WorktreePlacement{}, probeErr
					}
					return WorktreePlacement{Root: root, RepositoryLocal: tc.local}, nil
				},
				Path: func(_ WorktreePlacement, repository string) (string, error) {
					if tc.failAt == "path" {
						return "", probeErr
					}
					return filepath.Join(root, "new", filepath.Base(repository)), nil
				},
				Lstat: func(string) (os.FileInfo, error) {
					statCalls++
					if tc.failAt == "stat" {
						return nil, probeErr
					}
					if tc.occupied {
						return nil, nil
					}
					return nil, os.ErrNotExist
				},
			}
			plans, reason, err := planRenameTaskMembers(options, entries, ports)
			if tc.want == "" {
				if err != nil || len(plans) != tc.members {
					t.Fatalf("plans=%d reason=%q err=%v", len(plans), reason, err)
				}
				if tc.collision != strings.Contains(reason, "destination task already exists") {
					t.Fatalf("collision reason=%q", reason)
				}
				for i, plan := range plans {
					if plan.result.Repository != entries[i].Repository || plan.result.NewBranch != "new" || !plan.result.Eligible || plan.baseRevision != "base" || plan.destinationLocal != tc.local {
						t.Fatalf("plan %d = %#v", i, plan)
					}
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) || len(plans) != 0 || reason != "" || !errors.Is(err, probeErr) {
				t.Fatalf("failed planning plans=%v reason=%q err=%v", plans, reason, err)
			}
			if statCalls != tc.statCalls {
				t.Fatalf("stat calls=%d, want %d", statCalls, tc.statCalls)
			}
		})
	}
}

func TestRenameTaskApplicationLocksPreflightAndRollbackFaultMatrix(t *testing.T) {
	t.Parallel()
	probeErr := errors.New("injected rename stage failure")
	for _, tc := range []struct {
		name          string
		failAt        string
		reportFailAt  string
		occupancyAt   int
		noEligible    bool
		noReport      bool
		skipSecond    bool
		want          string
		wantPreflight int
		wantApply     int
		wantRollback  int
	}{
		{name: "planned report fails", failAt: "report-planned", want: "injected rename stage failure"},
		{name: "no eligible member", noEligible: true, want: "no repository under task"},
		{name: "failed report error retained", noEligible: true, reportFailAt: "failed", want: "write failed rename report"},
		{name: "old task root fails", failAt: "prepare-old", want: "open task"},
		{name: "old task lock fails", failAt: "lock-old", want: "lock task"},
		{name: "first preflight fails", failAt: "preflight-1", want: "injected rename stage failure", wantPreflight: 1},
		{name: "second preflight fails before apply", failAt: "preflight-2", want: "injected rename stage failure", wantPreflight: 2},
		{name: "first destination inventory fails", failAt: "inventory-1", want: "inspect destination task", wantPreflight: 2},
		{name: "first destination inventory occupied", occupancyAt: 1, want: "destination task already exists", wantPreflight: 2},
		{name: "new task root fails", failAt: "prepare-new", want: "injected rename stage failure", wantPreflight: 2},
		{name: "new task lock fails", failAt: "lock-new", want: "lock task", wantPreflight: 2},
		{name: "locked inventory fails", failAt: "inventory-2", want: "while locked", wantPreflight: 2},
		{name: "locked inventory occupied", occupancyAt: 2, want: "destination task already exists", wantPreflight: 2},
		{name: "reservation fails", failAt: "reserve", want: "reserve new private Work Log prompt", wantPreflight: 2},
		{name: "postreservation interruption", failAt: "after-reservation", want: "after pre-apply rename reservation", wantPreflight: 2},
		{name: "first apply fails", failAt: "apply-1", want: "injected rename stage failure", wantPreflight: 2, wantApply: 1, wantRollback: 1},
		{name: "second apply rolls back first", failAt: "apply-2", want: "injected rename stage failure", wantPreflight: 2, wantApply: 2, wantRollback: 1},
		{name: "rollback failure is folded", failAt: "apply-2", reportFailAt: "rollback", want: "coordinated rollback failed", wantPreflight: 2, wantApply: 2, wantRollback: 1},
		{name: "applied report failure preserves result", failAt: "report-applied", want: "injected rename stage failure", wantPreflight: 2, wantApply: 2},
		{name: "eligible member only", skipSecond: true, wantPreflight: 1, wantApply: 1},
		{name: "success with report", wantPreflight: 2, wantApply: 2},
		{name: "success without report", noReport: true, wantPreflight: 2, wantApply: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := RenameOptions{OldTask: "old", NewTask: "new", ReportDir: "/reports"}
			if tc.noReport {
				options.ReportDir = ""
			}
			if tc.failAt == "after-reservation" {
				options.afterPreApplyReservation = func() error { return probeErr }
			}
			plans := []renamePlan{
				{entry: ListResult{Repository: "acme/one"}, result: RenameResult{Repository: "acme/one", Eligible: !tc.noEligible}},
				{entry: ListResult{Repository: "acme/two"}, result: RenameResult{Repository: "acme/two", Eligible: !tc.noEligible && !tc.skipSecond}},
			}
			if tc.noEligible {
				plans[0].result.Reason = "not clean"
			}
			preflightCalls, applyCalls, rollbackCalls, inventoryCalls := 0, 0, 0, 0
			stages := []string{}
			visit := func(stage string) error {
				stages = append(stages, stage)
				if tc.failAt == stage || tc.reportFailAt == stage {
					return probeErr
				}
				return nil
			}
			ports := renameTaskApplicationPorts{
				Report: func(stage string, _ []RenameResult, _ []ListDiagnostic) (string, error) {
					name := "report-" + stage
					if tc.reportFailAt == stage {
						return "", visit("failed")
					}
					return "/reports/" + stage, visit(name)
				},
				PrepareTask: func(task string) (preparedOperationRoot, error) {
					return preparedOperationRoot{}, visit("prepare-" + task)
				},
				AcquireLock: func(_ preparedOperationRoot, task string) (operationLock, error) {
					return operationLock{}, visit("lock-" + task)
				},
				Inventory: func(task string) (ListOutcome, error) {
					if task != "new" {
						t.Fatalf("inventory task %q", task)
					}
					inventoryCalls++
					err := visit(fmt.Sprintf("inventory-%d", inventoryCalls))
					if tc.occupancyAt == inventoryCalls {
						return ListOutcome{Results: []ListResult{{Repository: "foreign/repo"}}}, err
					}
					return ListOutcome{}, err
				},
				Preflight: func(*renamePlan) error {
					preflightCalls++
					return visit(fmt.Sprintf("preflight-%d", preflightCalls))
				},
				Reserve: func() error { return visit("reserve") },
				Apply: func(plan *renamePlan) error {
					applyCalls++
					if err := visit(fmt.Sprintf("apply-%d", applyCalls)); err != nil {
						return err
					}
					plan.result.Applied = true
					return nil
				},
				Rollback: func(prior []renamePlan) error {
					rollbackCalls++
					if tc.failAt == "apply-2" && len(prior) != 1 {
						t.Fatalf("rollback prior=%d, want first member", len(prior))
					}
					return visit("rollback")
				},
			}
			outcome, err := applyRenameTask(options, plans, nil, ports)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("task error=%v, want %q; stages=%v", err, tc.want, stages)
			}
			if preflightCalls != tc.wantPreflight || applyCalls != tc.wantApply || rollbackCalls != tc.wantRollback {
				t.Fatalf("member calls preflight=%d apply=%d rollback=%d; want %d/%d/%d; stages=%v", preflightCalls, applyCalls, rollbackCalls, tc.wantPreflight, tc.wantApply, tc.wantRollback, stages)
			}
			if len(outcome.Results) != 2 {
				t.Fatalf("lost member reports: %#v", outcome.Results)
			}
			if applyCalls > 0 && preflightCalls != tc.wantPreflight {
				t.Fatalf("applied before all preflights: %v", stages)
			}
			if inventoryCalls > 0 && preflightCalls != tc.wantPreflight {
				t.Fatalf("inventoried before all preflights: %v", stages)
			}
		})
	}
}

func TestRenameAdmissionStopsBeforePlanningOnEveryFailure(t *testing.T) {
	t.Parallel()
	probeErr := errors.New("injected admission failure")
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		failAt string
		apply  bool
		stages string
	}{
		{name: "normalize", failAt: "normalize", stages: "normalize"},
		{name: "home", failAt: "home", stages: "normalize,home"},
		{name: "prompt", failAt: "prompt", stages: "normalize,home,prompt"},
		{name: "capability", failAt: "capability", apply: true, stages: "normalize,home,prompt,capability"},
		{name: "inventory dry", failAt: "inventory", stages: "normalize,home,prompt,inventory"},
		{name: "inventory apply", failAt: "inventory", apply: true, stages: "normalize,home,prompt,capability,inventory"},
		{name: "dry success", stages: "normalize,home,prompt,inventory"},
		{name: "apply success", apply: true, stages: "normalize,home,prompt,capability,inventory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := RenameOptions{ProjectsRoot: "/projects", OldTask: "old", NewTask: "new", Base: "main", Apply: tc.apply, Now: func() time.Time { return now }}
			stages := []string{}
			visit := func(stage string) error {
				stages = append(stages, stage)
				if tc.failAt == stage {
					return probeErr
				}
				return nil
			}
			request, err := admitRenameInvocation(options, renameAdmissionPorts{
				Normalize: func(got RenameOptions) (RenameOptions, error) {
					if got.NewTask != "new" {
						t.Fatalf("normalization input %#v", got)
					}
					return got, visit("normalize")
				},
				ResolveHome: func(root string) (wbhome.Resolution, error) {
					if root != "/projects" {
						t.Fatalf("home root %q", root)
					}
					return wbhome.Resolution{Write: wbhome.Layout{Home: "/home"}}, visit("home")
				},
				PrepareLog: func(root, task string, log WorkLogOptions) (WorkLogOptions, error) {
					if root != "/projects" || task != "new" {
						t.Fatalf("prompt root=%q task=%q", root, task)
					}
					log.Model = "prepared"
					return log, visit("prompt")
				},
				Capability: func() error { return visit("capability") },
				Inventory: func(got ListOptions) (ListOutcome, error) {
					if got.ProjectsRoot != "/projects" || got.Task != "old" || got.Base != "main" || got.GitHub {
						t.Fatalf("inventory options %#v", got)
					}
					return ListOutcome{Results: []ListResult{{Repository: "acme/app"}}}, visit("inventory")
				},
			})
			if tc.failAt == "" {
				if err != nil || request.options.WorkLog.Model != "prepared" || !request.now.Equal(now) || request.home != "/home" || len(request.listed.Results) != 1 {
					t.Fatalf("admission request=%#v error=%v", request, err)
				}
				if tc.apply != (request.options.ReportDir != "") {
					t.Fatalf("default report path %q for apply=%t", request.options.ReportDir, tc.apply)
				}
			} else if !errors.Is(err, probeErr) || len(request.listed.Results) != 0 {
				t.Fatalf("failed admission request=%#v error=%v", request, err)
			}
			if got := strings.Join(stages, ","); got != tc.stages {
				t.Fatalf("admission stages=%q, want %q", got, tc.stages)
			}
		})
	}
}

func TestRenameFacadeCoordinatesAdmissionPlanAndApply(t *testing.T) {
	t.Parallel()
	probeErr := errors.New("injected facade failure")
	for _, tc := range []struct {
		name            string
		failAt          string
		apply           bool
		noMembers       bool
		diagnosticOnly  bool
		collisionReason string
		want            string
		wantPlan        int
		wantApply       int
	}{
		{name: "admission fails", failAt: "admit", want: "injected facade failure"},
		{name: "source task absent", noMembers: true, want: "was not found"},
		{name: "planning fails", failAt: "plan", want: "injected facade failure", wantPlan: 1},
		{name: "diagnostic-only source returns dry report", diagnosticOnly: true, wantPlan: 1},
		{name: "destination collision blocks dry report", collisionReason: "occupied", wantPlan: 1},
		{name: "dry plan succeeds", wantPlan: 1},
		{name: "apply fails with caller error", apply: true, failAt: "apply", want: "injected facade failure", wantPlan: 1, wantApply: 1},
		{name: "apply succeeds", apply: true, wantPlan: 1, wantApply: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plans, applies := 0, 0
			request := renameInvocation{options: RenameOptions{OldTask: "old", NewTask: "new", Apply: tc.apply}}
			if !tc.noMembers && !tc.diagnosticOnly {
				request.listed.Results = []ListResult{{Repository: "acme/app"}}
			}
			if tc.diagnosticOnly {
				request.listed.Diagnostics = []ListDiagnostic{{Path: "bad"}}
			}
			outcome, err := renameWithPorts(RenameOptions{}, renameFacadePorts{
				Admit: func(RenameOptions) (renameInvocation, error) {
					if tc.failAt == "admit" {
						return renameInvocation{}, probeErr
					}
					return request, nil
				},
				Plan: func(_ RenameOptions, entries []ListResult) ([]renamePlan, string, error) {
					plans++
					if tc.failAt == "plan" {
						return nil, "", probeErr
					}
					if tc.diagnosticOnly {
						if len(entries) != 0 {
							t.Fatalf("diagnostic-only entries=%v", entries)
						}
						return nil, "", nil
					}
					return []renamePlan{{result: RenameResult{Repository: "acme/app", Eligible: true}}}, tc.collisionReason, nil
				},
				ApplyTask: func(_ renameInvocation, members []renamePlan) (RenameOutcome, error) {
					applies++
					if tc.failAt == "apply" {
						return RenameOutcome{}, probeErr
					}
					if len(members) != 1 {
						t.Fatalf("apply members=%v", members)
					}
					return RenameOutcome{Results: collectRenameResults(members)}, nil
				},
			})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("facade error=%v, want %q", err, tc.want)
			}
			if (tc.failAt == "admit" || tc.failAt == "plan" || tc.failAt == "apply") && !errors.Is(err, probeErr) {
				t.Fatalf("lost facade error: %v", err)
			}
			if plans != tc.wantPlan || applies != tc.wantApply {
				t.Fatalf("plan/apply=%d/%d, want %d/%d", plans, applies, tc.wantPlan, tc.wantApply)
			}
			if tc.collisionReason != "" && (len(outcome.Results) != 1 || outcome.Results[0].Eligible) {
				t.Fatalf("collision did not block report: %#v", outcome.Results)
			}
			if tc.diagnosticOnly && len(outcome.Diagnostics) != 1 {
				t.Fatalf("lost diagnostic: %#v", outcome)
			}
		})
	}
}

func TestRenameMemberFinishKeepsBranchAndClaimOrder(t *testing.T) {
	t.Parallel()
	probeErr := errors.New("injected finish failure")
	for _, tc := range []struct {
		name       string
		failAt     string
		deleted    bool
		want       string
		wantStages string
		wantNew    bool
		wantOld    bool
		wantApply  bool
	}{
		{name: "checkout fails", failAt: "checkout", want: "check out new branch", wantStages: "checkout"},
		{name: "guard refuses", failAt: "guard", want: "failed guard", wantStages: "checkout,guard", wantNew: true},
		{name: "old branch deletion fails", failAt: "delete", want: "injected finish failure", wantStages: "checkout,guard,delete", wantNew: true},
		{name: "old branch not deleted", want: "recycle is incomplete", wantStages: "checkout,guard,delete", wantNew: true},
		{name: "bind preflight refuses", failAt: "before-bind", deleted: true, want: "bind preflight", wantStages: "checkout,guard,delete,before-bind", wantNew: true, wantOld: true},
		{name: "durable bind fails", failAt: "bind", deleted: true, want: "bind recycled worktree", wantStages: "checkout,guard,delete,bind", wantNew: true, wantOld: true},
		{name: "all stages complete", deleted: true, wantStages: "checkout,guard,delete,bind", wantNew: true, wantOld: true, wantApply: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan := &renamePlan{entry: ListResult{Repository: "acme/app", Branch: "old"}, result: RenameResult{NewBranch: "new", NewWorktreeDir: "/new"}}
			stages := []string{}
			visit := func(stage string) error {
				stages = append(stages, stage)
				if tc.failAt == stage {
					return probeErr
				}
				return nil
			}
			options := RenameOptions{}
			if tc.failAt == "before-bind" {
				options.beforeRenameBind = func(repo string) error {
					if repo != "acme/app" {
						t.Fatalf("bind repository %q", repo)
					}
					return visit("before-bind")
				}
			}
			err := finishRenameMember(options, plan, renameFinishPorts{
				Checkout:        func() error { return visit("checkout") },
				Guard:           func() error { return visit("guard") },
				DeleteOldBranch: func() (bool, error) { return tc.deleted, visit("delete") },
				BindWorkLog:     func() error { return visit("bind") },
			})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("finish error=%v, want %q", err, tc.want)
			}
			if tc.failAt != "" && !errors.Is(err, probeErr) {
				t.Fatalf("lost finish error: %v", err)
			}
			if got := strings.Join(stages, ","); got != tc.wantStages {
				t.Fatalf("finish stages=%q, want %q", got, tc.wantStages)
			}
			if plan.newBranchCreated != tc.wantNew || plan.oldBranchDeleted != tc.wantOld || plan.result.OldBranchDeleted != tc.wantOld || plan.result.Applied != tc.wantApply {
				t.Fatalf("finish result=%#v new=%t old=%t, want %+v", plan.result, plan.newBranchCreated, plan.oldBranchDeleted, tc)
			}
		})
	}
}

func TestRenameFinishAdapterRejectsMissingCanonicalBeforeOldRefDeletion(t *testing.T) {
	t.Parallel()
	plan := &renamePlan{entry: ListResult{CanonicalDir: filepath.Join(t.TempDir(), "missing"), Branch: "old"}}
	ports := productionRenameFinishPorts(t.Context(), t.TempDir(), RenameOptions{}, plan, ListResult{HeadSHA: "recorded"})
	if deleted, err := ports.DeleteOldBranch(); err == nil || deleted {
		t.Fatalf("missing canonical old branch deletion=%t/%v", deleted, err)
	}
}

func TestRenamePreflightPolicyAdapterRefusesUnusableEvidenceAndDestination(t *testing.T) {
	t.Parallel()
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	plan := &renamePlan{entry: ListResult{Repository: "acme/app"}}
	ports := productionRenamePreflightPolicyPorts(t.Context(), RenameOptions{ProjectsRoot: loop}, plan, ListResult{}, nil)
	if err := ports.PreflightClaim(); err == nil {
		t.Fatal("cyclic projects root resolved for claim preflight")
	}

	root := t.TempDir()
	worktree := t.TempDir()
	projectionDir := filepath.Join(worktree, workLogProjectionDirectory)
	if err := os.Mkdir(projectionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectionDir, workLogProjectionName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	ports = productionRenamePreflightPolicyPorts(t.Context(), RenameOptions{ProjectsRoot: root}, plan, ListResult{WorktreeDir: worktree}, nil)
	if err := ports.PreflightClaim(); err == nil || !strings.Contains(err.Error(), "preflight Work Log") {
		t.Fatalf("malformed Work Log preflight = %v", err)
	}

	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan.destinationRoot = blocked
	ports = productionRenamePreflightPolicyPorts(t.Context(), RenameOptions{ProjectsRoot: root, NewTask: "new"}, plan, ListResult{}, nil)
	if err := ports.PreflightDestination(); err == nil || !strings.Contains(err.Error(), "preflight destination") {
		t.Fatalf("blocked destination preflight = %v", err)
	}
}

func TestRenamePreflightEntryStopsBeforePolicyWithoutSourceOrCanonical(t *testing.T) {
	t.Parallel()
	probeErr := errors.New("injected preflight entry failure")
	for _, stage := range []string{"source", "canonical"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			opened := false
			plan := &renamePlan{entry: ListResult{CanonicalDir: "/canonical"}}
			err := preflightRenameWithPorts(t.Context(), RenameOptions{}, plan, renamePreflightEntryPorts{
				SourceProof: func(context.Context, RenameOptions, *renamePlan) (ListResult, error) {
					if stage == "source" {
						return ListResult{}, probeErr
					}
					return ListResult{HeadSHA: "head"}, nil
				},
				OpenCanonical: func(path string) (*canonicalRepository, error) {
					opened = true
					if path != "/canonical" {
						t.Fatalf("canonical path %q", path)
					}
					return nil, probeErr
				},
			})
			if !errors.Is(err, probeErr) || opened != (stage == "canonical") || plan.refreshed.HeadSHA != "" {
				t.Fatalf("preflight entry stage=%q opened=%t plan=%#v err=%v", stage, opened, plan, err)
			}
		})
	}
}

func TestRenameClaimCutoverSealsBeforeRemovingProjection(t *testing.T) {
	t.Parallel()
	probeErr := errors.New("injected cutover failure")
	for _, tc := range []struct {
		name       string
		failAt     string
		want       string
		wantStages string
		sealed     bool
	}{
		{name: "seal failure leaves projection", failAt: "seal", want: "seal previous work log", wantStages: "seal"},
		{name: "remove failure retains rollback authority", failAt: "remove", want: "injected cutover failure", wantStages: "seal,remove", sealed: true},
		{name: "durable cutover", wantStages: "seal,remove", sealed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stages := []string{}
			visit := func(stage string) error {
				stages = append(stages, stage)
				if stage == tc.failAt {
					return probeErr
				}
				return nil
			}
			plan := &renamePlan{}
			err := sealPriorRenameClaimAndRemoveProjection(plan, ListResult{Repository: "acme/app", HeadSHA: "recorded"}, renameClaimCutoverPorts{
				Seal: func(head string) error {
					if head != "recorded" {
						t.Fatalf("sealed head %q", head)
					}
					return visit("seal")
				},
				Remove: func() error {
					if !plan.sealed {
						t.Fatal("removed projection before durable terminal")
					}
					return visit("remove")
				},
			})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) || !errors.Is(err, probeErr) {
				t.Fatalf("claim cutover error=%v, want %q", err, tc.want)
			}
			if got := strings.Join(stages, ","); got != tc.wantStages || plan.sealed != tc.sealed {
				t.Fatalf("cutover stages=%q sealed=%t, want %q/%t", got, plan.sealed, tc.wantStages, tc.sealed)
			}
		})
	}
}
