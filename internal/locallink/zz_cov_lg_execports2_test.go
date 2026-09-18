package locallink

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ExcludePath and ExcludedPatterns must report an unreadable exclude file
// rather than treating it as empty.
func TestLgCovExcludeReadFailures(t *testing.T) {
	t.Parallel()
	lgCovRequireGit(t)
	git := ExecGit{Timeout: 30 * time.Second}
	ctx := context.Background()

	root := initRepository(t)
	exclude := filepath.Join(root, ".git", "info", "exclude")
	if err := os.RemoveAll(exclude); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(exclude, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := git.ExcludePath(ctx, root, "/go.work"); err == nil || !strings.Contains(err.Error(), "read "+exclude) {
		t.Fatalf("ExcludePath error = %v, want the unreadable-exclude report", err)
	}
	if _, err := git.ExcludedPatterns(ctx, root); err == nil || !strings.Contains(err.Error(), "read "+exclude) {
		t.Fatalf("ExcludedPatterns error = %v, want the unreadable-exclude report", err)
	}

	if err := os.RemoveAll(exclude); err != nil {
		t.Fatal(err)
	}

	if _, err := git.ExcludedPatterns(ctx, t.TempDir()); err == nil || !strings.Contains(err.Error(), "resolve the exclude file") {
		t.Fatalf("ExcludedPatterns error = %v, want the unresolvable exclude file reported", err)
	}
}

// A working tree whose real index is corrupt still has a readable HEAD, so the
// temporary index succeeds and only the final status probe fails; that failure
// must be reported rather than read as a clean tree.
func TestLgCovContentHashReportsAStatusFailure(t *testing.T) {
	t.Parallel()
	lgCovRequireGit(t)
	root := initRepository(t)
	if err := os.WriteFile(filepath.Join(root, ".git", "index"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	git := ExecGit{Timeout: 30 * time.Second}
	_, _, err := git.ContentHash(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "read status of "+root) {
		t.Fatalf("error = %v, want the status-probe failure reported", err)
	}
}

// An exclude file that cannot be read must be reported: silently treating it as
// empty would drop the pattern the caller asked to record.
//
// This replaces a version that symlinked `.git/info/exclude` to /dev/full to
// make the *append* fail with ENOSPC. /dev/full also READS as an endless stream
// of zero bytes, and ExcludePath reads the exclude file before appending, so the
// read consumed memory until the kernel killed the process. On CI that killed
// the whole coverage job -- as a bare SIGTERM with no output, which is why it
// looked like an infrastructure problem for so long. It never reproduced on
// darwin because the test skipped there.
//
// A directory in place of the file is a portable, root-proof fault that reaches
// the same "do not silently drop the pattern" contract, and reading it cannot
// consume memory.
func TestLgCovExcludePathReportsAnUnreadableExcludeFile(t *testing.T) {
	t.Parallel()
	lgCovRequireGit(t)
	git := ExecGit{Timeout: 30 * time.Second}
	root := initRepository(t)
	exclude := filepath.Join(root, ".git", "info", "exclude")
	if err := os.Remove(exclude); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(exclude, 0o755); err != nil {
		t.Fatal(err)
	}
	err := git.ExcludePath(context.Background(), root, "/go.work")
	if err == nil {
		t.Fatal("an unreadable exclude file was accepted")
	}
}

// A package name whose final component is long enough that the applied-link
// marker exceeds NAME_MAX while the stage still fits: the stage is claimed and
// then the marker cannot be created, so Link must release the stage and say so.
func TestLgCovLinkReportsAnUnrecordablePendingLink(t *testing.T) {
	t.Parallel()
	consumer := t.TempDir()
	// The base is chosen so the stage name (base+19) still fits NAME_MAX=255
	// while the applied-link marker (base+21) does not.
	base := strings.Repeat("x", 235)
	packageName := "@acme/" + base
	node := ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash"}
	_, err := node.Link(context.Background(), consumer, packageName, lgCovDist(t, "@acme/core", "1.1.0-dev"))
	if err == nil || !strings.Contains(err.Error(), "record the pending link") {
		t.Fatalf("error = %v, want the unrecordable-pending-link refusal", err)
	}
	// The claimed stage must not be left behind.
	stage := filepath.Join(consumer, "node_modules", "@acme", "."+base+wbStageSuffix)
	if fileExists(stage) {
		t.Fatalf("the unclaimed stage %s survived the failure", stage)
	}
}

// A directory the package manager already superseded, whose stale backup
// cannot be cleared, must be reported rather than silently consumed.
func TestLgCovUnlinkReportsAnUnclearableDirectorySupersession(t *testing.T) {
	t.Parallel()
	consumer, target, _, _ := lgCovStagedConsumer(t, "@acme/core")
	lgCovWriteFile(t, filepath.Join(target, "package.json"), `{"name":"@acme/core","version":"2.0.0"}`)
	backup := target + linkBackupSuffix
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	lgCovWriteFile(t, filepath.Join(backup, "keep.txt"), "keep\n")

	node := ExecNode{}
	if _, err := node.Unlink(context.Background(), consumer, "@acme/core"); err == nil {
		t.Fatal("Unlink reported success while the superseded directory record could not be cleared")
	}
	if published, err := os.ReadFile(filepath.Join(target, "package.json")); err != nil || !strings.Contains(string(published), "2.0.0") {
		t.Fatalf("the published directory was touched: %s (err %v)", published, err)
	}
}

// A directory-shaped node_modules entry with no published manifest and no
// backups is a record with nothing left to restore; clearing it is the only
// safe action.
func TestLgCovUnlinkClearsADirectoryShapedRecord(t *testing.T) {
	t.Parallel()
	consumer, target, marker, stage := lgCovStagedConsumer(t, "@acme/core")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	node := ExecNode{}
	note, err := node.Unlink(context.Background(), consumer, "@acme/core")
	if err != nil || note != "" {
		t.Fatalf("note = %q, err = %v, want the record cleared silently", note, err)
	}
	if fileExists(marker) || fileExists(stage) {
		t.Error("clearing a directory-shaped record left its stage or marker behind")
	}
}

// lgCovSymlinkInfo lets a test claim a path is a symlink without creating one,
// so nodeLinkStagePath's read failure is reachable.
type lgCovSymlinkInfo struct{}

func (lgCovSymlinkInfo) Name() string       { return "core" }
func (lgCovSymlinkInfo) Size() int64        { return 0 }
func (lgCovSymlinkInfo) Mode() os.FileMode  { return os.ModeSymlink | 0o777 }
func (lgCovSymlinkInfo) ModTime() time.Time { return time.Time{} }
func (lgCovSymlinkInfo) IsDir() bool        { return false }
func (lgCovSymlinkInfo) Sys() any           { return nil }

func TestLgCovNodeLinkStagePathFailurePaths(t *testing.T) {
	t.Parallel()
	t.Run("a claimed symlink that cannot be read", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		target := filepath.Join(consumer, "node_modules", "@acme", "core")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		// The path is a real directory but the caller claims it is a symlink;
		// reading its target then fails, and the failure must be reported.
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := nodeLinkStagePath(consumer, target, lgCovSymlinkInfo{})
		if err == nil || !strings.Contains(err.Error(), "read the installed package link at") {
			t.Fatalf("error = %v, want the unreadable-symlink report", err)
		}
	})

	t.Run("an unresolvable consumer workspace", func(t *testing.T) {
		t.Parallel()
		consumer := filepath.Join(t.TempDir(), "missing-consumer")
		_, err := nodeLinkStagePath(consumer, filepath.Join(consumer, "core"), nil)
		if err == nil || !strings.Contains(err.Error(), "resolve consumer npm workspace") {
			t.Fatalf("error = %v, want the unresolvable-consumer report", err)
		}
	})

	t.Run("an unresolvable installed peer context", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		target := filepath.Join(consumer, "missing-dir", "core")
		_, err := nodeLinkStagePath(consumer, target, nil)
		if err == nil || !strings.Contains(err.Error(), "resolve installed peer context") {
			t.Fatalf("error = %v, want the unresolvable-peer-context report", err)
		}
	})
}

func TestLgCovCopyBuiltPackageFailurePaths(t *testing.T) {
	t.Parallel()
	t.Run("the destination must not already exist", func(t *testing.T) {
		t.Parallel()
		source := t.TempDir()
		lgCovWriteFile(t, filepath.Join(source, "package.json"), `{"name":"@acme/core"}`)
		destination := t.TempDir()
		err := copyBuiltPackage(source, destination)
		if err == nil || !strings.Contains(err.Error(), "file exists") {
			t.Fatalf("error = %v, want the exclusive-destination failure", err)
		}
	})

	t.Run("a source that does not exist is reported", func(t *testing.T) {
		t.Parallel()
		err := validateBuiltPackageSource(filepath.Join(t.TempDir(), "missing"))
		if err == nil || !strings.Contains(err.Error(), "no such file") {
			t.Fatalf("error = %v, want the missing-source report", err)
		}
	})

	t.Run("a nonexistent walk root is reported", func(t *testing.T) {
		t.Parallel()
		err := copyBuiltPackageContents(filepath.Join(t.TempDir(), "missing"), t.TempDir())
		if err == nil {
			t.Fatal("walking a nonexistent source reported success")
		}
	})

	t.Run("a directory that collides with an existing file is reported", func(t *testing.T) {
		t.Parallel()
		source := t.TempDir()
		lgCovWriteFile(t, filepath.Join(source, "sub", "file.txt"), "source\n")
		destination := t.TempDir()
		lgCovWriteFile(t, filepath.Join(destination, "sub"), "already a file\n")
		err := copyBuiltPackageContents(source, destination)
		if err == nil || !strings.Contains(err.Error(), "file exists") {
			t.Fatalf("error = %v, want the colliding-directory failure", err)
		}
	})

	t.Run("a file that collides with an existing entry is reported", func(t *testing.T) {
		t.Parallel()
		source := t.TempDir()
		lgCovWriteFile(t, filepath.Join(source, "file.txt"), "source\n")
		destination := t.TempDir()
		lgCovWriteFile(t, filepath.Join(destination, "file.txt"), "already here\n")
		err := copyBuiltPackageContents(source, destination)
		if err == nil || !strings.Contains(err.Error(), "file exists") {
			t.Fatalf("error = %v, want the colliding-file failure", err)
		}
		contents, readErr := os.ReadFile(filepath.Join(destination, "file.txt"))
		if readErr != nil || string(contents) != "already here\n" {
			t.Fatalf("the existing file was overwritten: %q (err %v)", contents, readErr)
		}
	})
}

func TestLgCovValidateStagedLinkPathFailurePaths(t *testing.T) {
	t.Parallel()
	consumer := t.TempDir()
	target := filepath.Join(consumer, "node_modules", "@acme", "core")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("a stage outside the consumer lexical workspace", func(t *testing.T) {
		t.Parallel()
		stage := filepath.Join(t.TempDir(), ".core"+wbStageSuffix)
		_, err := validateStagedLinkPath(consumer, target, stage)
		if err == nil || !strings.Contains(err.Error(), "outside consumer npm workspace") {
			t.Fatalf("error = %v, want the lexical-escape refusal", err)
		}
	})

	t.Run("an unresolvable consumer workspace", func(t *testing.T) {
		t.Parallel()
		missing := filepath.Join(t.TempDir(), "missing-consumer")
		stage := filepath.Join(missing, "node_modules", ".core"+wbStageSuffix)
		_, err := validateStagedLinkPath(missing, filepath.Join(missing, "node_modules", "@acme", "core"), stage)
		if err == nil || !strings.Contains(err.Error(), "resolve consumer npm workspace") {
			t.Fatalf("error = %v, want the unresolvable-consumer report", err)
		}
	})

	t.Run("an unresolvable stage parent", func(t *testing.T) {
		t.Parallel()
		if err := os.Symlink("loop", filepath.Join(consumer, "node_modules", "loop")); err != nil {
			t.Fatal(err)
		}
		stage := filepath.Join(consumer, "node_modules", "loop", ".core"+wbStageSuffix)
		_, err := validateStagedLinkPath(consumer, target, stage)
		if err == nil || !strings.Contains(err.Error(), "resolve staged package parent") {
			t.Fatalf("error = %v, want the unresolvable-stage-parent report", err)
		}
	})

	t.Run("a stage whose parent resolves outside the consumer", func(t *testing.T) {
		t.Parallel()
		escaped := t.TempDir()
		if err := os.Symlink(escaped, filepath.Join(consumer, "node_modules", "escape")); err != nil {
			t.Fatal(err)
		}
		stage := filepath.Join(consumer, "node_modules", "escape", ".core"+wbStageSuffix)
		_, err := validateStagedLinkPath(consumer, target, stage)
		if err == nil || !strings.Contains(err.Error(), "resolves outside consumer npm workspace") {
			t.Fatalf("error = %v, want the resolved-escape refusal", err)
		}
	})
}

func TestLgCovClearStagedLinkAndSupersededLink(t *testing.T) {
	t.Parallel()
	t.Run("an unremovable marker is reported", func(t *testing.T) {
		t.Parallel()
		marker := filepath.Join(t.TempDir(), "marker")
		if err := os.MkdirAll(marker, 0o755); err != nil {
			t.Fatal(err)
		}
		lgCovWriteFile(t, filepath.Join(marker, "keep.txt"), "keep\n")
		if err := clearStagedLink("", marker); err == nil {
			t.Fatal("clearStagedLink reported success while the marker could not be removed")
		}
		if !fileExists(filepath.Join(marker, "keep.txt")) {
			t.Error("a failed marker removal destroyed the marker's contents")
		}
	})

	t.Run("an unremovable stage is reported", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		blocker := filepath.Join(root, "afile")
		lgCovWriteFile(t, blocker, "x\n")
		// A stage path under a regular file cannot be removed at all.
		if err := clearStagedLink(filepath.Join(blocker, "stage"), ""); err == nil {
			t.Fatal("clearStagedLink reported success while the stage could not be removed")
		}
	})

	t.Run("a stage and marker are both removed", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		stage := filepath.Join(root, "stage")
		marker := filepath.Join(root, "marker")
		lgCovWriteFile(t, filepath.Join(stage, "file.txt"), "x\n")
		lgCovWriteFile(t, marker, "x\n")
		if err := clearStagedLink(stage, marker); err != nil {
			t.Fatal(err)
		}
		if fileExists(stage) || fileExists(marker) {
			t.Error("clearStagedLink left the stage or marker behind")
		}
	})

	t.Run("an unremovable backup is reported", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		target := filepath.Join(root, "core")
		backup := target + linkBackupSuffix
		if err := os.MkdirAll(backup, 0o755); err != nil {
			t.Fatal(err)
		}
		lgCovWriteFile(t, filepath.Join(backup, "keep.txt"), "keep\n")
		if err := clearSupersededLink("", "", target); err == nil {
			t.Fatal("clearSupersededLink reported success while a backup could not be removed")
		}
	})
}

func TestLgCovRunBoundedSuccessTimeoutAndFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	t.Run("a zero timeout uses the default", func(t *testing.T) {
		bin := lgCovFakeBin(t, "lgcov-ok", "printf 'ran with %s' \"$WB_LGCOV_MARKER\"")
		lgCovPrependPath(t, bin)
		t.Setenv("WB_LGCOV_MARKER", "env-applied")
		output, err := runBounded(ctx, 0, dir, nil, "lgcov-ok")
		if err != nil {
			t.Fatalf("runBounded: %v", err)
		}
		if output != "ran with env-applied" {
			t.Fatalf("output = %q, want the process environment preserved", output)
		}
	})

	t.Run("a command that overruns its timeout is reported", func(t *testing.T) {
		bin := lgCovFakeBin(t, "lgcov-slow", "sleep 5")
		lgCovPrependPath(t, bin)
		_, err := runBounded(ctx, 100*time.Millisecond, dir, nil, "lgcov-slow")
		if err == nil || !strings.Contains(err.Error(), "timed out after") {
			t.Fatalf("error = %v, want the timeout report", err)
		}
	})

	t.Run("a missing command is reported with its arguments", func(t *testing.T) {
		t.Parallel()
		_, err := runBounded(ctx, time.Second, dir, nil, "lgcov-definitely-missing", "--flag")
		if err == nil || !strings.Contains(err.Error(), "lgcov-definitely-missing --flag") {
			t.Fatalf("error = %v, want the failing command named", err)
		}
	})
}
