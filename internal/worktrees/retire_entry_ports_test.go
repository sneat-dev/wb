package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func retireEntryFixture(t *testing.T) (RetireOptions, retireEntryPorts) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	entry := ListResult{Task: "task", Repository: "acme/app", Branch: "topic", WorktreeDir: filepath.Join(root, "checkout"), CanonicalDir: filepath.Join(root, "canonical"), WorktreesRoot: filepath.Join(root, ".worktrees"), HeadSHA: sha}
	home := filepath.Join(root, ".wb")
	options := RetireOptions{ProjectsRoot: root, Task: "task", Apply: true, ArchiveRemote: "archive", RemoteOwnership: func(context.Context, string) error { return nil }, OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil }}
	ports := retireEntryPorts{
		absoluteRoot: func(string) (string, error) { return root, nil },
		resolve: func(string) (wbhome.Resolution, error) {
			return wbhome.Resolution{Write: wbhome.Layout{Home: home, WorktreesRoot: entry.WorktreesRoot}}, nil
		},
		inventory: func(context.Context, ListOptions) (ListOutcome, error) {
			return ListOutcome{Results: []ListResult{entry}}, nil
		},
		checkOwner: func(ListResult) error { return nil },
		archivePlan: func(context.Context, string, RetiredArchiveInspector) (RetiredArchivePlan, error) {
			return RetiredArchivePlan{Outcome: "planned", ArchiveRepository: "acme/archive"}, nil
		},
		checkPR: func(context.Context, ListResult, RetireOptions) error { return nil },
		readClaim: func(string, string) (workLogClaim, workLogProjection, error) {
			return workLogClaim{Task: entry.Task, Repository: entry.Repository, Branch: entry.Branch, ClaimID: "claim", EffortID: "effort", RunID: "run"}, workLogProjection{ClaimID: "claim"}, nil
		},
		checkIgnored:  func(context.Context, string) error { return nil },
		remoteSHA:     func(context.Context, string, string, string) (string, error) { return sha, nil },
		readReport:    func(string) (RetireResult, error) { return RetireResult{}, os.ErrNotExist },
		checkAncestor: func(context.Context, string, string, string) error { return nil },
		acquireTask:   acquireCleanupTaskAtOrCreate,
		validateTask:  (*cleanupTaskHandle).validate,
		openWorktree: func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error) {
			return &cleanupWorktreeHandle{}, nil
		},
		validateHeld: func(*cleanupWorktreeHandle) error { return nil },
		ownerViews:   func(string) ([]OwnerView, error) { return nil, nil },
		git: func(_ context.Context, _ string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "branch" {
				return entry.Branch, nil
			}
			return sha, nil
		},
	}
	return options, ports
}

func TestRetirementEntryPortsStopAtEachAuthorityBoundary(t *testing.T) {
	t.Parallel()
	boom := errors.New("injected authority failure")
	cases := []struct {
		name   string
		change func(*RetireOptions, *retireEntryPorts)
		want   string
	}{
		{"projects root", func(_ *RetireOptions, p *retireEntryPorts) {
			p.absoluteRoot = func(string) (string, error) { return "", boom }
		}, "injected authority failure"},
		{"home resolution", func(_ *RetireOptions, p *retireEntryPorts) {
			p.resolve = func(string) (wbhome.Resolution, error) { return wbhome.Resolution{}, boom }
		}, "injected authority failure"},
		{"inventory", func(_ *RetireOptions, p *retireEntryPorts) {
			p.inventory = func(context.Context, ListOptions) (ListOutcome, error) { return ListOutcome{}, boom }
		}, "injected authority failure"},
		{"owner first", func(_ *RetireOptions, p *retireEntryPorts) { p.checkOwner = func(ListResult) error { return boom } }, "injected authority failure"},
		{"archive target", func(_ *RetireOptions, p *retireEntryPorts) {
			p.archivePlan = func(context.Context, string, RetiredArchiveInspector) (RetiredArchivePlan, error) {
				return RetiredArchivePlan{}, boom
			}
		}, "injected authority failure"},
		{"default pull request reader", func(o *RetireOptions, p *retireEntryPorts) {
			o.OpenPullRequests = nil
			p.checkPR = func(_ context.Context, _ ListResult, actual RetireOptions) error {
				if actual.OpenPullRequests == nil {
					return errors.New("default reader missing")
				}
				return boom
			}
		}, "injected authority failure"},
		{"claim unavailable", func(_ *RetireOptions, p *retireEntryPorts) {
			p.readClaim = func(string, string) (workLogClaim, workLogProjection, error) {
				return workLogClaim{}, workLogProjection{}, boom
			}
		}, "corroborated active Work Log"},
		{"claim mismatch", func(_ *RetireOptions, p *retireEntryPorts) {
			p.readClaim = func(string, string) (workLogClaim, workLogProjection, error) {
				return workLogClaim{Task: "other"}, workLogProjection{}, nil
			}
		}, "does not bind"},
		{"ignored files first", func(_ *RetireOptions, p *retireEntryPorts) {
			p.checkIgnored = func(context.Context, string) error { return boom }
		}, "injected authority failure"},
		{"remote observation first", func(_ *RetireOptions, p *retireEntryPorts) {
			p.remoteSHA = func(context.Context, string, string, string) (string, error) { return "", boom }
		}, "injected authority failure"},
		{"prior report malformed", func(_ *RetireOptions, p *retireEntryPorts) {
			p.readReport = func(string) (RetireResult, error) { return RetireResult{}, boom }
		}, "injected authority failure"},
		{"task lock", func(_ *RetireOptions, p *retireEntryPorts) {
			p.acquireTask = func(string, string) (*cleanupTaskHandle, error) { return nil, boom }
		}, "acquire retirement task lock"},
		{"held task changed", func(_ *RetireOptions, p *retireEntryPorts) {
			p.validateTask = func(*cleanupTaskHandle) error { return boom }
		}, "injected authority failure"},
		{"remote owner under lock", func(o *RetireOptions, _ *retireEntryPorts) {
			calls := 0
			o.RemoteOwnership = func(context.Context, string) error {
				calls++
				if calls == 2 {
					return boom
				}
				return nil
			}
		}, "remote owner recheck"},
		{"held checkout open", func(_ *RetireOptions, p *retireEntryPorts) {
			p.openWorktree = func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error) { return nil, boom }
		}, "injected authority failure"},
		{"held checkout changed", func(_ *RetireOptions, p *retireEntryPorts) {
			p.validateHeld = func(*cleanupWorktreeHandle) error { return boom }
		}, "injected authority failure"},
		{"owner refresh", func(_ *RetireOptions, p *retireEntryPorts) {
			p.ownerViews = func(string) ([]OwnerView, error) { return nil, boom }
		}, "injected authority failure"},
		{"owner after lock", func(_ *RetireOptions, p *retireEntryPorts) {
			calls := 0
			p.checkOwner = func(ListResult) error {
				calls++
				if calls == 2 {
					return boom
				}
				return nil
			}
		}, "injected authority failure"},
		{"head changed", func(_ *RetireOptions, p *retireEntryPorts) {
			p.git = func(context.Context, string, ...string) (string, error) { return "changed", nil }
		}, "checkout HEAD changed"},
		{"branch changed", func(_ *RetireOptions, p *retireEntryPorts) {
			p.git = func(_ context.Context, _ string, args ...string) (string, error) {
				if args[0] == "branch" {
					return "other", nil
				}
				return strings.Repeat("a", 40), nil
			}
		}, "checkout branch changed"},
		{"remote changed under lock", func(_ *RetireOptions, p *retireEntryPorts) {
			calls := 0
			p.remoteSHA = func(context.Context, string, string, string) (string, error) {
				calls++
				if calls == 2 {
					return "changed", nil
				}
				return strings.Repeat("a", 40), nil
			}
		}, "original remote branch changed"},
		{"ancestor under lock", func(_ *RetireOptions, p *retireEntryPorts) {
			calls := 0
			p.checkAncestor = func(context.Context, string, string, string) error {
				calls++
				if calls == 2 {
					return boom
				}
				return nil
			}
		}, "injected authority failure"},
		{"pull request under lock", func(_ *RetireOptions, p *retireEntryPorts) {
			calls := 0
			p.checkPR = func(context.Context, ListResult, RetireOptions) error {
				calls++
				if calls == 2 {
					return boom
				}
				return nil
			}
		}, "injected authority failure"},
		{"ignored files under lock", func(_ *RetireOptions, p *retireEntryPorts) {
			calls := 0
			p.checkIgnored = func(context.Context, string) error {
				calls++
				if calls == 2 {
					return boom
				}
				return nil
			}
		}, "injected authority failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options, ports := retireEntryFixture(t)
			tc.change(&options, &ports)
			_, err := retireWithEntryPorts(context.Background(), options, ports)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("retirement error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestRetirementTransactionAdapterRechecksPRAndRemoteOwner(t *testing.T) {
	t.Parallel()
	entry := ListResult{Task: "task", Repository: "acme/app", Branch: "topic", WorktreeDir: t.TempDir()}
	prError := errors.New("PR inventory changed")
	options := RetireOptions{Task: entry.Task, OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, prError }, RemoteOwnership: func(context.Context, string) error { return nil }}
	operation := retireTransactionOperation(options, "", entry, nil, nil)
	if err := operation.Apply.BeforeDeletion(context.Background()); !errors.Is(err, prError) {
		t.Fatalf("PR refusal = %v", err)
	}
	ownerError := errors.New("remote owner changed")
	options.OpenPullRequests = func(context.Context, string, string, string) (bool, error) { return false, nil }
	options.RemoteOwnership = func(context.Context, string) error { return ownerError }
	operation = retireTransactionOperation(options, "", entry, nil, nil)
	if err := operation.Apply.BeforeDeletion(context.Background()); !errors.Is(err, ownerError) {
		t.Fatalf("owner refusal = %v", err)
	}
}
