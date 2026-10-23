package locallink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR8 is task-9 PR-8's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally matches
// a different test's error by coincidence.
var errBoomPR8 = errors.New("pr8 boom")

func TestExecGitExcludePathInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepWrite} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := initRepository(t)
			git := ExecGit{}
			inj := &filewrite.Injector{Step: step, Err: errBoomPR8}
			err := git.excludePathInjected(context.Background(), dir, "*.log", inj)
			if !errors.Is(err, errBoomPR8) {
				t.Fatalf("excludePathInjected(%s failure) = %v, want errBoomPR8", step, err)
			}
		})
	}
}

func TestExecGitExcludePathInjectedAppendsThePattern(t *testing.T) {
	t.Parallel()
	dir := initRepository(t)
	git := ExecGit{}
	if err := git.excludePathInjected(context.Background(), dir, "*.log", nil); err != nil {
		t.Fatal(err)
	}
	excludeFile, err := git.excludeFile(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(excludeFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "*.log") {
		t.Fatalf("exclude file = %q, want it to contain *.log", contents)
	}
	// Calling again with the same pattern must not duplicate it.
	if err := git.excludePathInjected(context.Background(), dir, "*.log", nil); err != nil {
		t.Fatal(err)
	}
	contents, err = os.ReadFile(excludeFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(contents), "*.log") != 1 {
		t.Fatalf("exclude file = %q, want *.log exactly once", contents)
	}
}

func TestCopyBuiltPackageContentsInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepWrite, filewrite.StepClose} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("hello"), 0o644); err != nil {
				t.Fatal(err)
			}
			destination := t.TempDir()
			inj := &filewrite.Injector{Step: step, Err: errBoomPR8}
			err := copyBuiltPackageContentsInjected(source, destination, inj)
			if !errors.Is(err, errBoomPR8) {
				t.Fatalf("copyBuiltPackageContentsInjected(%s failure) = %v, want errBoomPR8", step, err)
			}
		})
	}
}

func TestCopyBuiltPackageContentsInjectedCopiesFileContents(t *testing.T) {
	t.Parallel()
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "sub", "file.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := copyBuiltPackageContentsInjected(source, destination, nil); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(destination, "sub", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "hello world" {
		t.Fatalf("copied contents = %q, want %q", contents, "hello world")
	}
}

func TestLinkInjectedHonoursPendingMarkerFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepWrite, filewrite.StepClose} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			consumer := t.TempDir()
			installed := filepath.Join(consumer, "node_modules", "@acme", "core")
			if err := os.MkdirAll(installed, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(installed, "package.json"), []byte(`{"name":"@acme/core","version":"1.0.0"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			dist := t.TempDir()
			if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(`{"name":"@acme/core","version":"1.1.0-dev"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			node := ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash", Timeout: 30 * time.Second}
			marker := linkAppliedMarkerPath(consumer, "@acme/core")
			inj := &filewrite.Injector{Step: step, Name: marker, Err: errBoomPR8}
			_, err := node.linkInjected(context.Background(), consumer, "@acme/core", dist, inj)
			if !errors.Is(err, errBoomPR8) {
				t.Fatalf("linkInjected(pending marker %s failure) = %v, want errBoomPR8", step, err)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatalf("failed marker write left a visible marker: %v", statErr)
			}
			// The installed package must be untouched: Link must not have
			// gotten far enough to replace it.
			info, statErr := os.Lstat(installed)
			if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
				t.Fatalf("installed package was replaced despite the marker-write failure: %v", statErr)
			}
			// N1: the claimed stage directory must also be gone -- the
			// marker-write failure path removes it in every branch here
			// (StepOpenOrCreate's own cleanup succeeds because stage is
			// still empty; StepWrite/StepClose remove it unconditionally).
			target := filepath.Join(consumer, "node_modules", "@acme", "core")
			stageInfo, lstatErr := os.Lstat(target)
			if lstatErr != nil {
				t.Fatal(lstatErr)
			}
			stage, stageErr := nodeLinkStagePath(consumer, target, stageInfo)
			if stageErr != nil {
				t.Fatal(stageErr)
			}
			if _, statErr := os.Stat(stage); !os.IsNotExist(statErr) {
				t.Fatalf("failed marker write left a visible stage %s: %v", stage, statErr)
			}
		})
	}
}

// TestLinkInjectedReportsUnclaimedStageWhenCleanupFails covers
// writeLinkPendingMarker's stage-cleanup-failure branch (execports.go, the
// `if cleanupErr := os.Remove(stage); cleanupErr != nil` arm): the marker
// create fails, and the Hook that fires immediately before it drops a file
// inside stage, so the following os.Remove(stage) fails with ENOTEMPTY
// instead of succeeding.
func TestLinkInjectedReportsUnclaimedStageWhenCleanupFails(t *testing.T) {
	t.Parallel()
	consumer := t.TempDir()
	installed := filepath.Join(consumer, "node_modules", "@acme", "core")
	if err := os.MkdirAll(installed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "package.json"), []byte(`{"name":"@acme/core","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(`{"name":"@acme/core","version":"1.1.0-dev"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	node := ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash", Timeout: 30 * time.Second}
	marker := linkAppliedMarkerPath(consumer, "@acme/core")
	target := filepath.Join(consumer, "node_modules", "@acme", "core")
	targetInfo, lstatErr := os.Lstat(target)
	if lstatErr != nil {
		t.Fatal(lstatErr)
	}
	stage, stageErr := nodeLinkStagePath(consumer, target, targetInfo)
	if stageErr != nil {
		t.Fatal(stageErr)
	}
	inj := &filewrite.Injector{
		Step: filewrite.StepOpenOrCreate,
		Name: marker,
		Err:  errBoomPR8,
		Hook: func() {
			// Runs after linkInjected has claimed (os.Mkdir'd) stage but
			// before the marker create fails, so the leftover file makes
			// the pending marker's own stage-cleanup os.Remove fail.
			if err := os.WriteFile(filepath.Join(stage, "leftover"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}
	_, err := node.linkInjected(context.Background(), consumer, "@acme/core", dist, inj)
	if !errors.Is(err, errBoomPR8) {
		t.Fatalf("linkInjected(stage-cleanup failure) = %v, want errors.Is errBoomPR8", err)
	}
	if !strings.Contains(err.Error(), "preserve unclaimed stage") {
		t.Fatalf("linkInjected(stage-cleanup failure) = %v, want it to mention the preserved stage", err)
	}
	// The cleanup genuinely failed: stage, and the leftover file that
	// caused the failure, must still be there.
	if _, statErr := os.Stat(filepath.Join(stage, "leftover")); statErr != nil {
		t.Fatalf("stage %s lost its leftover file despite the reported cleanup failure: %v", stage, statErr)
	}
}

func TestLinkInjectedHonoursSymlinkBackupFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepWrite, filewrite.StepClose} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			consumer := t.TempDir()
			store := filepath.Join(consumer, "node_modules", ".pnpm", "@acme+core@1.0.0", "node_modules", "@acme", "core")
			if err := os.MkdirAll(store, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(store, "package.json"), []byte(`{"name":"@acme/core","version":"1.0.0"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(consumer, "node_modules", "@acme", "core")
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			relative, err := filepath.Rel(filepath.Dir(target), store)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(relative, target); err != nil {
				t.Fatal(err)
			}
			dist := t.TempDir()
			if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(`{"name":"@acme/core","version":"1.1.0-dev"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			node := ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash", Timeout: 30 * time.Second}
			symlinkBackup := target + linkSymlinkBackupSuffix
			inj := &filewrite.Injector{Step: step, Name: symlinkBackup, Err: errBoomPR8}
			_, err = node.linkInjected(context.Background(), consumer, "@acme/core", dist, inj)
			if !errors.Is(err, errBoomPR8) {
				t.Fatalf("linkInjected(symlink backup %s failure) = %v, want errBoomPR8", step, err)
			}
			if _, statErr := os.Stat(symlinkBackup); !os.IsNotExist(statErr) {
				t.Fatalf("failed backup write left a visible backup: %v", statErr)
			}
			// The existing symlink must still be intact: Link must not have
			// removed it before the backup that would let it be restored
			// was durably written.
			linkedTarget, readErr := os.Readlink(target)
			if readErr != nil {
				t.Fatalf("existing symlink was removed despite the backup-write failure: %v", readErr)
			}
			if linkedTarget != relative {
				t.Fatalf("existing symlink target = %q, want %q", linkedTarget, relative)
			}
		})
	}
}
