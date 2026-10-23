package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktreeretire"
)

func retireRemovedFixture(t *testing.T) (string, RetireOptions, retireRemovedPorts, RetireResult) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	result := RetireResult{Version: 1, Task: "task", Repository: "acme/app", ArchiveRepository: "acme/archive", Branch: "topic",
		Canonical: filepath.Join(home, "canonical"), Worktree: filepath.Join(home, "absent-worktree"), WorktreesRoot: filepath.Join(home, "worktrees"),
		OriginalRemoteSHA: sha, DeleteIntentSHA: sha, SourceSHA: sha, ArchiveSHA: strings.Repeat("b", 40),
		RetiredRef: "retired/20260930-topic-aaaaaaa", ClaimID: "claim", EffortID: "effort", RunID: "run", Phase: "original_deleted"}
	result.ArchiveRef = worktreeretire.ArchiveRef(result)
	result.ReportPath = retireReportPath(home, result)
	if err := os.MkdirAll(filepath.Dir(result.ReportPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result.ReportPath, []byte("held report"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := RetireOptions{Task: result.Task, Repository: result.Repository, ArchiveRemote: "archive", RemoteOwnership: func(context.Context, string) error { return nil }}
	ports := retireRemovedPorts{
		readDir:       os.ReadDir,
		readReport:    func(string) (RetireResult, error) { return result, nil },
		validateClaim: func(string, RetireResult) error { return nil },
		archivePlan: func(context.Context, string, RetiredArchiveInspector) (RetiredArchivePlan, error) {
			return RetiredArchivePlan{Outcome: "planned", ArchiveRepository: result.ArchiveRepository}, nil
		},
		acquireTask:  acquireCleanupTaskAtOrCreate,
		validateTask: (*cleanupTaskHandle).validate,
		remote: worktreeretire.TransactionPorts{RemoteSHA: func(_ context.Context, _, remote, ref string) (string, error) {
			switch {
			case remote == "origin" && ref == worktreeretire.SourceRef(result):
				return result.SourceSHA, nil
			case remote != "origin" && ref == "refs/heads/"+result.ArchiveRef:
				return result.ArchiveSHA, nil
			case remote == "origin" && ref == worktreeretire.DeletionProofRef(result):
				return result.SourceSHA, nil
			default:
				return "", nil
			}
		}},
		lstat: os.Lstat,
		git: func(_ context.Context, _ string, args ...string) (string, error) {
			if args[0] == "rev-parse" {
				return result.SourceSHA, nil
			}
			return "", nil
		},
		openCanonical: func(string) (*canonicalRepository, error) { return &canonicalRepository{}, nil },
		branchExists:  func(context.Context, string, string) (bool, error) { return true, nil },
		deleteBranch: func(_ context.Context, _ *canonicalRepository, actual RetireResult) error {
			if actual.SourceSHA != result.SourceSHA {
				return errors.New("changed branch")
			}
			return nil
		},
		writeReport: func(RetireResult) error { return nil },
	}
	return home, options, ports, result
}

func TestRetirementRemovedRecoveryChecksEachHeldBoundary(t *testing.T) {
	t.Parallel()
	boom := errors.New("injected recovery failure")
	cases := []struct {
		name   string
		change func(string, *RetireOptions, *retireRemovedPorts, *RetireResult)
		want   string
	}{
		{"directory unreadable", func(_ string, _ *RetireOptions, p *retireRemovedPorts, _ *RetireResult) {
			p.readDir = func(string) ([]os.DirEntry, error) { return nil, boom }
		}, "no managed checkout"},
		{"multiple reports", func(_ string, o *RetireOptions, _ *retireRemovedPorts, r *RetireResult) {
			o.Repository = ""
			_ = os.WriteFile(filepath.Join(filepath.Dir(r.ReportPath), "other.json"), []byte("other"), 0o600)
		}, "exactly one receipt"},
		{"repository filtered", func(_ string, o *RetireOptions, _ *retireRemovedPorts, _ *RetireResult) { o.Repository = "acme/other" }, "exactly one receipt"},
		{"report unreadable", func(_ string, _ *RetireOptions, p *retireRemovedPorts, _ *RetireResult) {
			p.readReport = func(string) (RetireResult, error) { return RetireResult{}, boom }
		}, "injected recovery failure"},
		{"receipt repository mismatch", func(_ string, _ *RetireOptions, p *retireRemovedPorts, r *RetireResult) {
			r.Repository = "acme/other"
			p.readReport = func(string) (RetireResult, error) { return *r, nil }
		}, "repository mismatch"},
		{"receipt path mismatch", func(_ string, _ *RetireOptions, p *retireRemovedPorts, r *RetireResult) {
			r.ReportPath = "other"
			p.readReport = func(string) (RetireResult, error) { return *r, nil }
		}, "does not authorize"},
		{"immutable claim mismatch", func(_ string, _ *RetireOptions, p *retireRemovedPorts, _ *RetireResult) {
			p.validateClaim = func(string, RetireResult) error { return boom }
		}, "injected recovery failure"},
		{"private archive changed", func(_ string, _ *RetireOptions, p *retireRemovedPorts, _ *RetireResult) {
			p.archivePlan = func(context.Context, string, RetiredArchiveInspector) (RetiredArchivePlan, error) {
				return RetiredArchivePlan{Outcome: "refused"}, nil
			}
		}, "private archive preflight"},
		{"default archive remote", func(_ string, o *RetireOptions, p *retireRemovedPorts, _ *RetireResult) {
			o.ArchiveRemote = ""
			p.acquireTask = func(string, string) (*cleanupTaskHandle, error) { return nil, boom }
		}, "injected recovery failure"},
		{"task lock", func(_ string, _ *RetireOptions, p *retireRemovedPorts, _ *RetireResult) {
			p.acquireTask = func(string, string) (*cleanupTaskHandle, error) { return nil, boom }
		}, "injected recovery failure"},
		{"held task changed", func(_ string, _ *RetireOptions, p *retireRemovedPorts, _ *RetireResult) {
			p.validateTask = func(*cleanupTaskHandle) error { return boom }
		}, "injected recovery failure"},
		{"remote owner missing", func(_ string, o *RetireOptions, _ *retireRemovedPorts, _ *RetireResult) { o.RemoteOwnership = nil }, "authoritative remote owner"},
		{"remote owner changed", func(_ string, o *RetireOptions, _ *retireRemovedPorts, _ *RetireResult) {
			o.RemoteOwnership = func(context.Context, string) error { return boom }
		}, "remote owner recheck"},
		{"source receipt changed", func(_ string, _ *RetireOptions, p *retireRemovedPorts, _ *RetireResult) {
			p.remote.RemoteSHA = func(context.Context, string, string, string) (string, error) { return "wrong", nil }
		}, "retired source receipt changed"},
		{"checkout still present", func(_ string, _ *RetireOptions, p *retireRemovedPorts, _ *RetireResult) {
			p.lstat = func(string) (os.FileInfo, error) { return nil, nil }
		}, "checkout path still exists"},
		{"registration unreadable", func(_ string, _ *RetireOptions, p *retireRemovedPorts, _ *RetireResult) {
			p.git = func(context.Context, string, ...string) (string, error) { return "", boom }
		}, "injected recovery failure"},
		{"checkout still registered", func(_ string, _ *RetireOptions, p *retireRemovedPorts, r *RetireResult) {
			p.git = func(_ context.Context, _ string, args ...string) (string, error) {
				if args[0] == "worktree" {
					return "worktree " + r.Worktree + "\n", nil
				}
				return r.SourceSHA, nil
			}
		}, "still registered"},
		{"canonical changed", func(_ string, _ *RetireOptions, p *retireRemovedPorts, _ *RetireResult) {
			p.openCanonical = func(string) (*canonicalRepository, error) { return nil, boom }
		}, "injected recovery failure"},
		{"branch inventory unreadable", func(_ string, _ *RetireOptions, p *retireRemovedPorts, _ *RetireResult) {
			p.branchExists = func(context.Context, string, string) (bool, error) { return false, boom }
		}, "injected recovery failure"},
		{"local branch changed", func(_ string, _ *RetireOptions, p *retireRemovedPorts, _ *RetireResult) {
			p.git = func(_ context.Context, _ string, args ...string) (string, error) {
				if args[0] == "rev-parse" {
					return "wrong", nil
				}
				return "", nil
			}
		}, "local source branch changed"},
		{"exact local deletion failed", func(_ string, _ *RetireOptions, p *retireRemovedPorts, _ *RetireResult) {
			p.deleteBranch = func(context.Context, *canonicalRepository, RetireResult) error { return boom }
		}, "injected recovery failure"},
		{"completion report failed", func(_ string, _ *RetireOptions, p *retireRemovedPorts, _ *RetireResult) {
			p.writeReport = func(RetireResult) error { return boom }
		}, "injected recovery failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, options, ports, result := retireRemovedFixture(t)
			tc.change(home, &options, &ports, &result)
			_, err := retireResumeRemovedWithPorts(context.Background(), home, options, ports)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("recovery error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestRetirementRemovedRecoveryCompletesOnlyExactLocalBranch(t *testing.T) {
	t.Parallel()
	for _, branchExists := range []bool{true, false} {
		t.Run(map[bool]string{true: "exact branch", false: "already absent"}[branchExists], func(t *testing.T) {
			t.Parallel()
			home, options, ports, _ := retireRemovedFixture(t)
			deleted := false
			ports.branchExists = func(context.Context, string, string) (bool, error) { return branchExists, nil }
			ports.deleteBranch = func(context.Context, *canonicalRepository, RetireResult) error { deleted = true; return nil }
			result, err := retireResumeRemovedWithPorts(context.Background(), home, options, ports)
			if err != nil || result.Phase != "complete" || deleted != branchExists {
				t.Fatalf("recovery=%+v err=%v deleted=%v", result, err, deleted)
			}
		})
	}
}
