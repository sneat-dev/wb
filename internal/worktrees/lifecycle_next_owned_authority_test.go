package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLifecycleNextCleanupHandleRejectsClosedTaskAuthority(t *testing.T) {
	t.Parallel()
	for _, absolute := range []bool{false, true} {
		t.Run(map[bool]string{false: "nested", true: "absolute"}[absolute], func(t *testing.T) {
			t.Parallel()
			task := newHostLevelCleanupTaskFixture(t)
			path := filepath.Join(task.taskPath, "owner", "app")
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			// The task directory remains usable for Openat. Its held root authority
			// is closed, so refusal must occur after the worktree has been opened.
			if err := task.worktrees.Close(); err != nil {
				t.Fatal(err)
			}
			var handle *cleanupWorktreeHandle
			var err error
			if absolute {
				handle, err = openAbsoluteCleanupWorktree(task, path, "relocated managed", "checkout segment")
			} else {
				handle, err = openCleanupWorktree(task, CleanupResult{ListResult: ListResult{WorktreeDir: path}})
			}
			if handle != nil || err == nil || !strings.Contains(err.Error(), "cleanup worktrees root path changed") {
				t.Fatalf("handle=%v cause=%v", handle, err)
			}
			if info, err := os.Stat(path); err != nil || !info.IsDir() {
				t.Fatalf("refusal changed checkout: %v %v", info, err)
			}
		})
	}
}

func TestLifecycleNextRelativeTaskAuthorityOpensAbsoluteCheckout(t *testing.T) {
	t.Parallel()
	task := newHostLevelCleanupTaskFixture(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, task.taskPath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(task.taskPath, "owner", "app")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	task.taskPath = relative
	if _, err := filepath.Rel(relative, path); err == nil {
		t.Fatal("relative base accepted absolute target")
	}
	handle, err := openCleanupWorktree(task, CleanupResult{ListResult: ListResult{WorktreeDir: path}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(handle.close)
	if handle.closeParent || handle.closeAncestor {
		t.Fatal("absolute fallback authorized retirement of physical ancestors")
	}
	if err := handle.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleNextParentRetirementRejectsChangedAuthority(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"task", "parent", "ancestor"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			task := newHostLevelCleanupTaskFixture(t)
			path := filepath.Join(task.taskPath, "github.com", "owner", "app")
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			handle, err := openCleanupWorktree(task, CleanupResult{ListResult: ListResult{WorktreeDir: path}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(handle.close)
			switch boundary {
			case "task":
				if err := task.worktrees.Close(); err != nil {
					t.Fatal(err)
				}
			case "parent":
				if err := handle.parent.Close(); err != nil {
					t.Fatal(err)
				}
			case "ancestor":
				if err := handle.ancestor.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if boundary == "parent" {
				if err := handle.validate(); err == nil || !strings.Contains(err.Error(), "cleanup worktree parent path changed: "+handle.parentPath) {
					t.Fatalf("closed parent admitted: %v", err)
				}
			}
			if err := handle.removeEmptyParent(nil, nil); err == nil {
				t.Fatal("changed authority permitted parent retirement")
			}
			if info, err := os.Stat(path); err != nil || !info.IsDir() {
				t.Fatalf("refusal changed checkout: %v %v", info, err)
			}
		})
	}
}

func TestLifecycleNextAdoptedRegistrationPreservesNativeRefusals(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"task", "repository_file", "pointer_directory", "occupied_repository"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			task := newHostLevelCleanupTaskFixture(t)
			owner := filepath.Join(task.taskPath, "owner")
			repository := filepath.Join(owner, "app")
			if err := os.MkdirAll(owner, 0o700); err != nil {
				t.Fatal(err)
			}
			if boundary == "repository_file" {
				if err := os.WriteFile(repository, []byte("retained repository occupant"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(repository, 0o700); err != nil {
					t.Fatal(err)
				}
				switch boundary {
				case "task":
					if err := task.worktrees.Close(); err != nil {
						t.Fatal(err)
					}
				case "pointer_directory":
					if err := os.Mkdir(filepath.Join(repository, adoptedWorktreePointerName), 0o700); err != nil {
						t.Fatal(err)
					}
				case "occupied_repository":
					if err := os.WriteFile(filepath.Join(repository, "retained"), []byte("foreign"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := removeAdoptedRegistration(task, "owner", "app"); err == nil {
				t.Fatal("native registration refusal was ignored")
			}
			if _, err := os.Lstat(repository); err != nil {
				t.Fatalf("registration was destroyed: %v", err)
			}
			if boundary == "repository_file" {
				if data, err := os.ReadFile(repository); err != nil || string(data) != "retained repository occupant" {
					t.Fatalf("occupant changed: %q %v", data, err)
				}
			}
			if boundary == "occupied_repository" {
				if data, err := os.ReadFile(filepath.Join(repository, "retained")); err != nil || string(data) != "foreign" {
					t.Fatalf("sibling changed: %q %v", data, err)
				}
			}
		})
	}
}
