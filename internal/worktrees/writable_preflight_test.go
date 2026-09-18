package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sneat-dev/wb/internal/pathguard"
)

// TestCreateDeniedWritablePathFailsBeforeAnyMutation is the proof for
// projects-root-layout#ac:denied-write-names-the-path-and-remedy on the command
// the operator actually runs: a sandbox that grants write access to the
// canonical clone but not to the projects root must produce the actionable
// diagnostic, not a bare "operation not permitted" — and must produce it
// before the first mutation, whichever declared path is the unwritable one.
func TestCreateDeniedWritablePathFailsBeforeAnyMutation(t *testing.T) {
	fixture := newGitFixture(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	state := filepath.Join(fixture.projectsRoot, ".wb")
	store := filepath.Join(fixture.projectsRoot, ".worktrees")
	canonicalGit := filepath.Join(fixture.canonical, ".git")

	for _, testCase := range []struct {
		name   string
		denied []string
		want   []string
	}{
		{
			name:   "state and central store",
			denied: []string{state, store},
			want:   []string{state, store, "private state", "central checkout store"},
		},
		{
			// The canonical Git registration is declared by the same preflight,
			// so denying only it must also refuse before the task hierarchy and
			// the Work Log reservation are created.
			name:   "canonical git registration",
			denied: []string{canonicalGit},
			want:   []string{canonicalGit, "Git registration"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			denied := make(map[string]bool, len(testCase.denied))
			for _, path := range testCase.denied {
				denied[path] = true
			}
			_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot,
				Operation:    "denied-write",
				WorkLog:      WorkLogOptions{Model: "unknown"},
				writableProbe: func(path string) error {
					if denied[path] {
						return &os.PathError{Op: "open", Path: path, Err: syscall.EPERM}
					}
					return nil
				},
			})
			var denial *pathguard.Error
			if !errors.As(err, &denial) {
				t.Fatalf("create = %v, want the declared-writable-path diagnostic", err)
			}
			message := denial.Error()
			for _, want := range append(testCase.want, fixture.projectsRoot, "repository-local") {
				if !strings.Contains(message, want) {
					t.Fatalf("diagnostic %q does not mention %q", message, want)
				}
			}
			if strings.TrimSpace(message) == syscall.EPERM.Error() || !strings.Contains(message, "write") {
				t.Fatalf("diagnostic reads as a bare errno: %q", message)
			}
			// Nothing durable may exist afterwards, including the operation
			// hierarchy and the reserved Work Log that creation would otherwise
			// have written before a late refusal.
			for _, path := range []string{state, store} {
				if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("%s exists after the refusal (stat err = %v); the preflight must run before the first mutation", path, statErr)
				}
			}
		})
	}
}

// TestOpenPrivateChildReadPathPerformsNoMetadataWrite covers the second half of
// projects-root-layout#req:actionable-permission-error: a read must never be
// reported as a denied write. The read path used to fchmod the descriptor it
// had just opened O_RDONLY, which a sandbox refuses — surfacing as
// "inspect existing work-log run before mutation: operation not permitted".
func TestOpenPrivateChildReadPathPerformsNoMetadataWrite(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	run, runPath, err := openWorkLogRun(home, "effort", "run", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Close() }()
	created, err := openPrivateChild(run, "claims", true)
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := created.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	childPath := filepath.Join(runPath, "claims")
	// A directory an earlier release left behind with a wider mode. Reopening
	// it for reading must leave that mode alone: changing it is a metadata
	// write on a read-only descriptor.
	if err := os.Chmod(childPath, 0o755); err != nil {
		t.Fatal(err)
	}
	opened, err := openPrivateChild(run, "claims", false)
	if err != nil {
		t.Fatalf("read path = %v, want the reopened directory", err)
	}
	if closeErr := opened.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	info, err := os.Stat(childPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("read path rewrote the directory mode to %v; reading must not write metadata", info.Mode().Perm())
	}
}

// TestCreateRepositoryLocalDeclaresTheCheckoutRoot covers the other half of the
// declared set in repository-local mode: the checkout lands at
// <canonical>/.worktrees/<task>, and creating it is a mkdirat on the canonical
// clone, so the clone itself has to be writable. Declaring only
// <canonical>/.git would discover that denial after the task hierarchy and the
// reserved Work Log already exist — the same defect class as a late preflight.
func TestCreateRepositoryLocalDeclaresTheCheckoutRoot(t *testing.T) {
	fixture := newStoreModeFixture(t, "app")
	fixture.selectStoreMode(t, StoreModeRepositoryLocal)
	state := filepath.Join(fixture.projectsRoot, ".wb")
	checkoutRoot := filepath.Join(fixture.canonicals["app"], ".worktrees")

	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "local-denied",
		WorkLog:      WorkLogOptions{Model: "unknown"},
		writableProbe: func(path string) error {
			if path == checkoutRoot {
				return &os.PathError{Op: "mkdir", Path: path, Err: syscall.EPERM}
			}
			return nil
		},
	})
	var denial *pathguard.Error
	if !errors.As(err, &denial) {
		t.Fatalf("create = %v, want the repository-local checkout root to be declared", err)
	}
	message := denial.Error()
	if !strings.Contains(message, checkoutRoot) {
		t.Fatalf("diagnostic %q does not name the repository-local checkout root %q", message, checkoutRoot)
	}
	// The role has to be the repository-local store, not the central one, and
	// the remedy has to be one the operator can still take: suggesting
	// repository-local mode to somebody already in it is a no-op.
	if !strings.Contains(message, "repository-local checkout store") {
		t.Fatalf("diagnostic %q does not name the repository-local store role", message)
	}
	if strings.Contains(message, "select repository-local store mode") {
		t.Fatalf("diagnostic %q offers the mode already in force as a remedy", message)
	}
	if _, statErr := os.Lstat(state); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state exists after the refusal (stat err = %v); the preflight must run before the first mutation", statErr)
	}
}

// TestCreateResumeDoesNotRequireAnUnwritableStoreRoot keeps the declared set
// honest about what a resume does: it adopts a checkout that already exists and
// never creates a store root, so demanding write access to one would refuse a
// resume that writes nothing there.
func TestCreateResumeDoesNotRequireAnUnwritableStoreRoot(t *testing.T) {
	fixture := newStoreModeFixture(t, "app")
	fixture.selectStoreMode(t, StoreModeRepositoryLocal)
	ctx := context.Background()
	created, err := Create(ctx, []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "local-resume", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create = %#v, err=%v", created, err)
	}
	checkoutRoot := filepath.Join(fixture.canonicals["app"], ".worktrees")

	resumed, err := Create(ctx, []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "local-resume", Resume: true,
		WorkLog: WorkLogOptions{Model: "unknown"},
		writableProbe: func(path string) error {
			if path == checkoutRoot {
				return &os.PathError{Op: "open", Path: path, Err: syscall.EPERM}
			}
			return nil
		},
	})
	if err != nil || len(resumed) != 1 || resumed[0].Action != "resumed" {
		t.Fatalf("resume = %#v, err=%v; a resume adopts an existing checkout and must not require the store root to be writable", resumed, err)
	}
	if resumed[0].WorktreeDir != created[0].WorktreeDir {
		t.Fatalf("resume moved the checkout: %q became %q", created[0].WorktreeDir, resumed[0].WorktreeDir)
	}
}
