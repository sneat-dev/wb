//go:build linux

package agents

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNormalizeContainerWorkspaceRefusesUnresolvedRoots proves the workspace
// plan refuses a missing owner home, an empty git common directory and an
// invalid scoped input before planning any mount.
func TestNormalizeContainerWorkspaceRefusesUnresolvedRoots(t *testing.T) {
	t.Parallel()
	worktree, gitCommon, _ := testContainerWorkspaceDirs(t)

	t.Run("owner home cannot be resolved", func(t *testing.T) {
		t.Parallel()
		homeFailure := errors.New("the owner home is unavailable")
		host := &containerHost{userHome: func() (string, error) { return "", homeFailure }}
		_, err := normalizeContainerWorkspace(&ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon}, host)
		if !errors.Is(err, homeFailure) {
			t.Fatalf("error = %v, want %v", err, homeFailure)
		}
		if !strings.Contains(err.Error(), "resolve the owner home directory") {
			t.Fatalf("error = %v, want the unresolved home named", err)
		}
	})

	t.Run("git common directory is empty", func(t *testing.T) {
		t.Parallel()
		_, err := normalizeContainerWorkspace(&ContainerWorkspace{WorktreeDir: worktree}, testContainerWorkspaceHost())
		if err == nil {
			t.Fatal("an empty git common directory must be refused")
		}
		if !strings.Contains(err.Error(), "git common directory") {
			t.Fatalf("error = %v, want the git common directory named", err)
		}
	})

	t.Run("input directory is relative", func(t *testing.T) {
		t.Parallel()
		_, err := normalizeContainerWorkspace(&ContainerWorkspace{
			WorktreeDir:  worktree,
			GitCommonDir: gitCommon,
			InputDir:     "relative/input",
		}, testContainerWorkspaceHost())
		if err == nil {
			t.Fatal("a relative input directory must be refused")
		}
		if !strings.Contains(err.Error(), "input directory") {
			t.Fatalf("error = %v, want the input directory named", err)
		}
	})
}

// TestValidateContainerWorkspaceRootRefusesWhitespaceSymlinksAndResolution
// proves the root validator rejects a padded path, a path whose full resolution
// differs from it, and a path that cannot be resolved at all.
func TestValidateContainerWorkspaceRootRefusesWhitespaceSymlinksAndResolution(t *testing.T) {
	t.Parallel()
	home := "/home/testowner"

	t.Run("leading whitespace", func(t *testing.T) {
		t.Parallel()
		worktree, _, _ := testContainerWorkspaceDirs(t)
		_, err := validateContainerWorkspaceRoot(&containerHost{}, "worktree directory", " "+worktree, home)
		if err == nil {
			t.Fatal("a padded workspace root must be refused")
		}
		if !strings.Contains(err.Error(), "leading or trailing whitespace") {
			t.Fatalf("error = %v, want the whitespace refusal", err)
		}
	})

	t.Run("path resolves through a symlink", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		target := filepath.Join(root, "real")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatalf("create the real directory: %v", err)
		}
		child := filepath.Join(target, "child")
		if err := os.Mkdir(child, 0o755); err != nil {
			t.Fatalf("create the child directory: %v", err)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("link the real directory: %v", err)
		}
		_, err := validateContainerWorkspaceRoot(&containerHost{}, "worktree directory", filepath.Join(link, "child"), home)
		if err == nil {
			t.Fatal("a root reached through a symlink must be refused")
		}
		if !strings.Contains(err.Error(), "contains a symlink") {
			t.Fatalf("error = %v, want the symlink refusal", err)
		}
	})

	t.Run("full resolution fails", func(t *testing.T) {
		t.Parallel()
		worktree, _, _ := testContainerWorkspaceDirs(t)
		resolveFailure := errors.New("the path cannot be resolved")
		host := &containerHost{evalSymlinks: func(string) (string, error) { return "", resolveFailure }}
		_, err := validateContainerWorkspaceRoot(host, "worktree directory", worktree, home)
		if !errors.Is(err, resolveFailure) {
			t.Fatalf("error = %v, want %v", err, resolveFailure)
		}
		if !strings.Contains(err.Error(), "resolve the container workspace") {
			t.Fatalf("error = %v, want the failed resolution named", err)
		}
	})
}

// TestContainerWorkspacePathHelpersRefuseUnrepresentableInputs proves the
// low-level path predicates treat NUL bytes, empty paths, mixed absolute and
// relative paths and a missing home as refusals or non-containments.
func TestContainerWorkspacePathHelpersRefuseUnrepresentableInputs(t *testing.T) {
	t.Parallel()
	t.Run("NUL byte in a mount path", func(t *testing.T) {
		t.Parallel()
		err := validateContainerMountPath("worktree directory", "bad\x00path")
		if err == nil || !strings.Contains(err.Error(), "NUL byte") {
			t.Fatalf("error = %v, want the NUL byte refusal", err)
		}
	})
	t.Run("empty home forbids nothing", func(t *testing.T) {
		t.Parallel()
		if forbiddenContainerWorkspaceRoot("/tmp/worktree", "") {
			t.Fatal("an empty home must not forbid an otherwise valid root")
		}
		if forbiddenContainerWorkspaceRoot("/tmp/worktree", "   ") {
			t.Fatal("a whitespace-only home must not forbid an otherwise valid root")
		}
	})
	t.Run("empty paths contain nothing", func(t *testing.T) {
		t.Parallel()
		if containerPathContains("", "/worktree") {
			t.Fatal("an empty parent must not contain a child")
		}
		if containerPathContains("/worktree", "") {
			t.Fatal("an empty child must not be contained")
		}
	})
	t.Run("mixed absolute and relative paths do not contain", func(t *testing.T) {
		t.Parallel()
		if containerPathContains("/worktree", "relative/worktree") {
			t.Fatal("a mixed absolute and relative pair must not report containment")
		}
	})
}

// TestContainerWorkspaceMaskMountsRefusesAnUnboundedTree proves the directory
// cap refuses a worktree with more directories than the bound allows.
func TestContainerWorkspaceMaskMountsRefusesAnUnboundedTree(t *testing.T) {
	t.Parallel()
	worktree := filepath.Join(t.TempDir(), "worktree")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatalf("create the worktree: %v", err)
	}
	for index := 0; index < maxContainerWorkspaceDir; index++ {
		if err := os.Mkdir(filepath.Join(worktree, fmt.Sprintf("dir-%04d", index)), 0o755); err != nil {
			t.Fatalf("create scan directory %d: %v", index, err)
		}
	}
	_, err := containerWorkspaceMaskMounts(worktree)
	if err == nil {
		t.Fatal("a worktree above the directory cap must be refused")
	}
	if !strings.Contains(err.Error(), "more than") {
		t.Fatalf("error = %v, want the directory bound named", err)
	}
}

// TestContainerWorkspaceMaskMountsReportsAnUnreadableDirectory proves the scan
// reports a directory it cannot read instead of planning an incomplete mask.
func TestContainerWorkspaceMaskMountsReportsAnUnreadableDirectory(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("a readable-root user cannot produce an unreadable directory")
	}
	worktree := filepath.Join(t.TempDir(), "worktree")
	locked := filepath.Join(worktree, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatalf("create the worktree: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("lock the scan directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(locked, 0o700); err != nil {
			t.Errorf("restore the scan directory mode: %v", err)
		}
	})
	_, err := containerWorkspaceMaskMounts(worktree)
	if err == nil {
		t.Fatal("an unreadable directory must refuse the mask plan")
	}
	if !strings.Contains(err.Error(), "scan the container workspace directory") {
		t.Fatalf("error = %v, want the failed scan named", err)
	}
}

// TestContainerWorkspaceMaskMountsRefusesUnsafeOrUnboundedDependencyMasks
// proves a nested node_modules or cache path that is not a plain directory, a
// path with an unrepresentable byte, and a mask count above the bound are all
// refused.
func TestContainerWorkspaceMaskMountsRefusesUnsafeOrUnboundedDependencyMasks(t *testing.T) {
	t.Parallel()

	t.Run("nested node_modules is a file", func(t *testing.T) {
		t.Parallel()
		worktree := filepath.Join(t.TempDir(), "worktree")
		pkg := filepath.Join(worktree, "pkg")
		if err := os.MkdirAll(pkg, 0o755); err != nil {
			t.Fatalf("create the package directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(pkg, "node_modules"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write the nested node_modules file: %v", err)
		}
		_, err := containerWorkspaceMaskMounts(worktree)
		if err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("error = %v, want the non-directory mask refusal", err)
		}
	})

	t.Run("too many dependency masks", func(t *testing.T) {
		t.Parallel()
		worktree := filepath.Join(t.TempDir(), "worktree")
		if err := os.Mkdir(worktree, 0o755); err != nil {
			t.Fatalf("create the worktree: %v", err)
		}
		for index := 0; index <= maxContainerMaskCount; index++ {
			pkg := filepath.Join(worktree, fmt.Sprintf("pkg-%04d", index))
			if err := os.MkdirAll(filepath.Join(pkg, containerNodeModulesName), 0o755); err != nil {
				t.Fatalf("create nested node_modules %d: %v", index, err)
			}
		}
		_, err := containerWorkspaceMaskMounts(worktree)
		if err == nil || !strings.Contains(err.Error(), "more than") {
			t.Fatalf("error = %v, want the mask count bound named", err)
		}
	})

	t.Run("cache path has an unrepresentable byte", func(t *testing.T) {
		t.Parallel()
		worktree := filepath.Join(t.TempDir(), "worktree")
		pkg := filepath.Join(worktree, "pkg,one")
		if err := os.MkdirAll(filepath.Join(pkg, containerNXCacheName), 0o755); err != nil {
			t.Fatalf("create the cache directory: %v", err)
		}
		_, err := containerWorkspaceMaskMounts(worktree)
		if err == nil || !strings.Contains(err.Error(), "unsafe character") {
			t.Fatalf("error = %v, want the unsafe mask target named", err)
		}
	})

	t.Run("too many cache masks", func(t *testing.T) {
		t.Parallel()
		worktree := filepath.Join(t.TempDir(), "worktree")
		if err := os.Mkdir(worktree, 0o755); err != nil {
			t.Fatalf("create the worktree: %v", err)
		}
		for index := 0; index <= maxContainerMaskCount; index++ {
			pkg := filepath.Join(worktree, fmt.Sprintf("pkg-%04d", index))
			if err := os.MkdirAll(filepath.Join(pkg, containerPNPMStoreName), 0o755); err != nil {
				t.Fatalf("create cache mask %d: %v", index, err)
			}
		}
		_, err := containerWorkspaceMaskMounts(worktree)
		if err == nil || !strings.Contains(err.Error(), "more than") {
			t.Fatalf("error = %v, want the mask count bound named", err)
		}
	})

	t.Run("cache path is a symlink", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		worktree := filepath.Join(root, "worktree")
		if err := os.Mkdir(worktree, 0o755); err != nil {
			t.Fatalf("create the worktree: %v", err)
		}
		target := filepath.Join(root, "cache-target")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatalf("create the cache target: %v", err)
		}
		if err := os.Symlink(target, filepath.Join(worktree, containerNXCacheName)); err != nil {
			t.Fatalf("symlink the cache path: %v", err)
		}
		_, err := containerWorkspaceMaskMounts(worktree)
		if err == nil || !strings.Contains(err.Error(), "is a symlink") {
			t.Fatalf("error = %v, want the symlink cache refusal", err)
		}
	})
}

// TestContainerWorkspaceMaskMountsSkipsSymlinksAndPlainFiles proves a symlinked
// directory and an ordinary file are not traversed and do not become masks.
func TestContainerWorkspaceMaskMountsSkipsSymlinksAndPlainFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatalf("create the worktree: %v", err)
	}
	elsewhere := filepath.Join(root, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o755); err != nil {
		t.Fatalf("create the outside directory: %v", err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(worktree, "linked")); err != nil {
		t.Fatalf("symlink an outside directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "plain.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write a plain file: %v", err)
	}
	mounts, err := containerWorkspaceMaskMounts(worktree)
	if err != nil {
		t.Fatalf("containerWorkspaceMaskMounts: %v", err)
	}
	if len(mounts) != 1 {
		t.Fatalf("mask mounts = %#v, want only the root node_modules mask", mounts)
	}
	if mounts[0].target != filepath.Join(worktree, containerNodeModulesName) {
		t.Fatalf("mask target = %q, want the root node_modules", mounts[0].target)
	}
}

// TestValidateContainerMaskTargetRefusesUnsafeMissingAndNonDirectoryPaths
// proves the mask target validator reports an unrepresentable path, a lookup
// that is neither missing nor inspectable, and an existing non-directory.
func TestValidateContainerMaskTargetRefusesUnsafeMissingAndNonDirectoryPaths(t *testing.T) {
	t.Parallel()
	t.Run("unsafe path", func(t *testing.T) {
		t.Parallel()
		err := validateContainerMaskTarget(filepath.Join(t.TempDir(), "bad,name"))
		if err == nil || !strings.Contains(err.Error(), "unsafe character") {
			t.Fatalf("error = %v, want the unsafe path refusal", err)
		}
	})
	t.Run("lookup failure", func(t *testing.T) {
		t.Parallel()
		base := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(base, []byte("x"), 0o600); err != nil {
			t.Fatalf("write the base file: %v", err)
		}
		err := validateContainerMaskTarget(filepath.Join(base, "child"))
		if err == nil || !strings.Contains(err.Error(), "inspect the container workspace dependency mask") {
			t.Fatalf("error = %v, want the failed inspection named", err)
		}
	})
	t.Run("not a directory", func(t *testing.T) {
		t.Parallel()
		target := filepath.Join(t.TempDir(), "plain.txt")
		if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
			t.Fatalf("write the target file: %v", err)
		}
		err := validateContainerMaskTarget(target)
		if err == nil || !strings.Contains(err.Error(), "is not a directory") {
			t.Fatalf("error = %v, want the non-directory refusal", err)
		}
	})
	t.Run("missing target is allowed", func(t *testing.T) {
		t.Parallel()
		if err := validateContainerMaskTarget(filepath.Join(t.TempDir(), "absent")); err != nil {
			t.Fatalf("a missing mask target must be allowed: %v", err)
		}
	})
}

// TestVerifyContainerMountsRefusesUnreadableMismatchedAndUnplannedMounts
// proves the inspect allowlist rejects an unreadable bind record, a read-write
// mismatch, a duplicate bind and an unplanned tmpfs.
func TestVerifyContainerMountsRefusesUnreadableMismatchedAndUnplannedMounts(t *testing.T) {
	t.Parallel()

	t.Run("unreadable bind record", func(t *testing.T) {
		t.Parallel()
		document := containerInspectDocument{ID: "container"}
		document.HostConfig.Binds = []string{"unreadable"}
		err := verifyContainerMounts(document, nil)
		if err == nil || !strings.Contains(err.Error(), "unreadable bind") {
			t.Fatalf("error = %v, want the unreadable bind named", err)
		}
	})

	t.Run("bind read-write mismatch", func(t *testing.T) {
		t.Parallel()
		expected := []containerMount{{kind: containerMountBind, source: "/src", target: "/dst", writable: true}}
		document := containerInspectDocument{ID: "container"}
		document.Mounts = []containerInspectMount{{Type: string(containerMountBind), Source: "/src", Destination: "/dst", RW: true}}
		document.HostConfig.Binds = []string{"/src:/dst:ro"}
		err := verifyContainerMounts(document, expected)
		if err == nil || !strings.Contains(err.Error(), "not the planned rw") {
			t.Fatalf("error = %v, want the read-write mismatch named", err)
		}
	})

	t.Run("duplicate bind record", func(t *testing.T) {
		t.Parallel()
		expected := []containerMount{{kind: containerMountBind, source: "/src", target: "/dst", writable: true}}
		document := containerInspectDocument{ID: "container"}
		document.Mounts = []containerInspectMount{{Type: string(containerMountBind), Source: "/src", Destination: "/dst", RW: true}}
		document.HostConfig.Binds = []string{"/src:/dst:rw", "/src:/dst:rw"}
		err := verifyContainerMounts(document, expected)
		if err == nil || !strings.Contains(err.Error(), "more than once") {
			t.Fatalf("error = %v, want the duplicate bind named", err)
		}
	})

	t.Run("unplanned tmpfs", func(t *testing.T) {
		t.Parallel()
		document := containerInspectDocument{ID: "container"}
		document.HostConfig.Tmpfs = map[string]string{"/cache": "rw"}
		err := verifyContainerMounts(document, nil)
		if err == nil || !strings.Contains(err.Error(), "unplanned tmpfs") {
			t.Fatalf("error = %v, want the unplanned tmpfs named", err)
		}
	})
}

// TestParseContainerInspectBindReadsTheTwoAndThreePartForms proves Docker's
// bind records are read exactly, and that an unbounded form is refused.
func TestParseContainerInspectBindReadsTheTwoAndThreePartForms(t *testing.T) {
	t.Parallel()
	source, target, writable, err := parseContainerInspectBind("/src:/dst")
	if err != nil {
		t.Fatalf("parse a two-part bind: %v", err)
	}
	if source != "/src" || target != "/dst" || !writable {
		t.Fatalf("two-part bind = %q %q %t, want /src /dst writable", source, target, writable)
	}
	_, _, _, err = parseContainerInspectBind("/src:/dst:ro")
	if err != nil {
		t.Fatalf("parse a read-only bind: %v", err)
	}
	_, _, _, err = parseContainerInspectBind("a:b:c:d")
	if err == nil {
		t.Fatal("an unbounded bind record must be refused")
	}
	if !strings.Contains(err.Error(), "source:target") {
		t.Fatalf("error = %v, want the accepted bind form named", err)
	}
}
