package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRollbackCreatedWorktreePreservesAmbiguousIdentity(t *testing.T) {
	t.Parallel()
	checkout := &os.File{}
	lockCalled := false
	err := rollbackCreatedWorktreeWith(context.Background(), nil, nil, checkout, nil, "", "feature", "base",
		createGitRollbackPorts{
			quarantine: func(*os.File, *os.File) error { return errDirectoryMoveIdentityChanged },
			acquireLock: func(*canonicalRepository) (*repositoryRegistrationLock, error) {
				lockCalled = true
				return nil, nil
			},
		})
	if !errors.Is(err, errDirectoryMoveIdentityChanged) || lockCalled {
		t.Fatalf("ambiguous checkout rollback err=%v lockCalled=%t", err, lockCalled)
	}
}

func TestRollbackCreatedWorktreeAggregatesGitAndRegistrationFailures(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	final := filepath.Join(root, "checkout")
	var calls []string
	ports := createGitRollbackPorts{
		quarantine: func(*os.File, *os.File) error {
			calls = append(calls, "quarantine")
			return errors.New("quarantine failed")
		},
		acquireLock: func(*canonicalRepository) (*repositoryRegistrationLock, error) {
			calls = append(calls, "lock")
			return nil, nil
		},
		prune: func(context.Context, *canonicalRepository) error {
			calls = append(calls, "prune")
			return errors.New("prune failed")
		},
		registered: func(context.Context, *canonicalRepository) (map[string]bool, error) {
			calls = append(calls, "registrations")
			return map[string]bool{"new": true, final: true}, nil
		},
		deleteBranch: func(context.Context, *canonicalRepository, string, string) error {
			calls = append(calls, "branch")
			return errors.New("branch delete failed")
		},
		releaseLock: func(*repositoryRegistrationLock) error {
			calls = append(calls, "unlock")
			return errors.New("unlock failed")
		},
	}
	err := rollbackCreatedWorktreeWith(context.Background(), nil, nil, &os.File{}, map[string]bool{"old": true}, final, "feature", "base", ports)
	for _, fragment := range []string{"quarantine failed", "prune failed", "incomplete worktree remains registered at new", final, "branch delete failed", "unlock failed"} {
		if err == nil || !strings.Contains(err.Error(), fragment) {
			t.Fatalf("rollback error lacks %q: %v", fragment, err)
		}
	}
	if got := strings.Join(calls, ","); got != "quarantine,lock,prune,registrations,branch,unlock" {
		t.Fatalf("rollback order = %q", got)
	}
}

func TestRollbackCreatedWorktreeLockAndRegistryFailures(t *testing.T) {
	t.Parallel()
	lockErr := errors.New("lock unavailable")
	err := rollbackCreatedWorktreeWith(context.Background(), nil, nil, nil, nil, "", "", "",
		createGitRollbackPorts{acquireLock: func(*canonicalRepository) (*repositoryRegistrationLock, error) { return nil, lockErr }})
	if !errors.Is(err, lockErr) {
		t.Fatalf("lock refusal = %v", err)
	}
	err = rollbackCreatedWorktreeWith(context.Background(), nil, nil, nil, nil, "", "", "",
		createGitRollbackPorts{
			acquireLock: func(*canonicalRepository) (*repositoryRegistrationLock, error) { return nil, nil },
			prune:       func(context.Context, *canonicalRepository) error { return nil },
			registered: func(context.Context, *canonicalRepository) (map[string]bool, error) {
				return nil, errors.New("registry unavailable")
			},
			releaseLock: func(*repositoryRegistrationLock) error { return nil },
		})
	if err == nil || !strings.Contains(err.Error(), "registry unavailable") {
		t.Fatalf("registry refusal = %v", err)
	}
}

func TestDeleteCreatedBranchRequiresExactOriginalTip(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		exists     bool
		existsErr  error
		resolveErr error
		tip        string
		deleteErr  error
		wantErr    string
		wantGit    int
	}{
		{name: "missing branch", wantGit: 0},
		{name: "existence refusal", existsErr: errors.New("existence unavailable"), wantErr: "existence unavailable"},
		{name: "tip resolution refusal", exists: true, resolveErr: errors.New("tip unavailable"), wantErr: "tip unavailable", wantGit: 1},
		{name: "moved tip", exists: true, tip: "advanced", wantErr: "it moved", wantGit: 1},
		{name: "compare-and-delete refusal", exists: true, tip: "original", deleteErr: errors.New("delete unavailable"), wantErr: "delete unavailable", wantGit: 2},
		{name: "exact deletion", exists: true, tip: "original", wantGit: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			gitCalls := 0
			err := deleteCreatedBranchCanonicalWith(context.Background(), nil, "feature", "original", createBranchDeletionPorts{
				exists: func(context.Context, *canonicalRepository, string) (bool, error) { return test.exists, test.existsErr },
				git: func(_ context.Context, _ *canonicalRepository, args ...string) (string, error) {
					gitCalls++
					if gitCalls == 1 {
						if len(args) != 2 || args[0] != "rev-parse" || args[1] != "refs/heads/feature" {
							t.Fatalf("tip lookup = %v", args)
						}
						return test.tip, test.resolveErr
					}
					if len(args) != 4 || args[0] != "update-ref" || args[1] != "-d" || args[2] != "refs/heads/feature" || args[3] != "original" {
						t.Fatalf("compare-and-delete = %v", args)
					}
					return "", test.deleteErr
				},
			})
			if gitCalls != test.wantGit || (test.wantErr == "" && err != nil) || (test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr))) {
				t.Fatalf("branch deletion calls=%d err=%v", gitCalls, err)
			}
		})
	}
}
