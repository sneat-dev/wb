package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRollbackPublishedCreateRefusesMissingOrChangedIdentity(t *testing.T) {
	t.Parallel()
	if err := rollbackPublishedCreateWith(context.Background(), nil, preparedOperationRoot{}, nil, createPublishedRollbackPorts{}); err == nil || !strings.Contains(err.Error(), "identity is unavailable") {
		t.Fatalf("missing publication = %v", err)
	}
	publication := &createdWorktreePublication{ownerDirectory: &os.File{}, worktreeDirectory: &os.File{}, finalPath: "/owner/checkout"}
	err := rollbackPublishedCreateWith(context.Background(), nil, preparedOperationRoot{}, publication, createPublishedRollbackPorts{
		matches: func(string, *os.File) bool { return false },
	})
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("changed publication identity = %v", err)
	}
}

func TestRollbackPublishedCreateReportsEveryStageBoundary(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name          string
		makeError     bool
		identityError bool
		openError     bool
		wrapNil       bool
		moveError     bool
		moveRetained  bool
		rollbackError bool
		want          string
		wantMove      bool
		wantRollback  bool
	}{
		{name: "stage creation", makeError: true, want: "create Work Log rollback stage"},
		{name: "stage identity", identityError: true, want: "identity unavailable"},
		{name: "stage open", openError: true, want: "open unavailable"},
		{name: "stage wrapping", wrapNil: true, want: "wrap Work Log rollback stage"},
		{name: "move before retained identity", moveError: true, want: "move published worktree into rollback quarantine", wantMove: true},
		{name: "move with retained identity", moveError: true, moveRetained: true, want: "move published worktree into rollback quarantine", wantMove: true},
		{name: "Git rollback", rollbackError: true, want: "Git rollback unavailable", wantMove: true, wantRollback: true},
		{name: "complete", wantMove: true, wantRollback: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			operation, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = operation.Close() })
			publication := &createdWorktreePublication{ownerDirectory: operation, worktreeDirectory: operation, finalPath: filepath.Join(root, "checkout"), branch: "feature"}
			var moved, rolledBack bool
			ports := createPublishedRollbackPorts{matches: func(string, *os.File) bool { return true }}
			if test.makeError {
				ports.makeStage = func(*os.File) (string, error) { return "", errors.New("create unavailable") }
			}
			if test.identityError {
				ports.stageIdentity = func(int, string) (secureDirectoryIdentity, error) {
					return secureDirectoryIdentity{}, errors.New("identity unavailable")
				}
			}
			if test.openError {
				ports.openStage = func(int, string) (int, error) { return -1, errors.New("open unavailable") }
			}
			if test.wrapNil {
				ports.wrapStage = func(uintptr, string) *os.File { return nil }
			}
			ports.move = func(*createdWorktreePublication, *os.File) (*os.File, error) {
				moved = true
				if test.moveError {
					if test.moveRetained {
						return &os.File{}, errors.New("move unavailable")
					}
					return nil, errors.New("move unavailable")
				}
				return &os.File{}, nil
			}
			ports.rollback = func(_ context.Context, _ *canonicalRepository, _ *os.File, _ *os.File, _ map[string]bool, _, _, _ string) error {
				rolledBack = true
				if test.rollbackError {
					return errors.New("Git rollback unavailable")
				}
				return nil
			}
			err = rollbackPublishedCreateWith(context.Background(), nil, preparedOperationRoot{Directory: operation}, publication, ports)
			if moved != test.wantMove || rolledBack != test.wantRollback || (test.want == "" && err != nil) || (test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want))) {
				t.Fatalf("rollback err=%v moved=%t rolledBack=%t", err, moved, rolledBack)
			}
		})
	}
}
