//go:build (darwin || linux) && e2e

package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestE2ECanonicalPublicationDescriptorRemovalBoundaries(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	directory := wtLifeCovOpenDirectory(t, path)
	if directoryDescriptorWasRemovedAt(nil, path, 0) {
		t.Fatal("nil descriptor reported removed")
	}
	if directoryDescriptorWasRemovedAt(directory, path, 0) {
		t.Fatal("existing owned directory reported removed")
	}
	if directoryDescriptorWasRemovedAt(directory, path+".substitute", 0) {
		t.Fatal("different spelling reported removed")
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if directoryDescriptorWasRemovedAt(directory, path, 0) {
		t.Fatal("closed descriptor reported removed")
	}
	if _, err := directory.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("descriptor state = %v", err)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("directory evidence changed: %v, %v", info, err)
	}
}

func TestE2ECanonicalPublicationOwnedMetadataBoundary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent := wtLifeCovOpenDirectory(t, root)
	destination := filepath.Join(root, "repo")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(destination, "evidence")
	if err := os.WriteFile(evidence, []byte("retain destination"), 0600); err != nil {
		t.Fatal(err)
	}
	var observed *os.File
	read := func(directory *os.File) (os.FileInfo, error) {
		observed = directory
		if err := directory.Close(); err != nil {
			t.Fatal(err)
		}
		return directory.Stat()
	}
	got, exists, err := prepareWorktreeDestinationWithRead(root, parent, "", "repo", read)
	if got != "" || exists || !errors.Is(err, os.ErrClosed) || !strings.Contains(err.Error(), "inspect secure worktree destination") || observed == nil {
		t.Fatalf("owned metadata refusal = %q, %v, %v", got, exists, err)
	}
	if _, err := observed.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("opened destination still live: %v", err)
	}
	if contents, err := os.ReadFile(evidence); err != nil || string(contents) != "retain destination" {
		t.Fatalf("destination evidence = %q, %v", contents, err)
	}
	if _, err := parent.Stat(); err != nil {
		t.Fatalf("borrowed operation directory closed: %v", err)
	}
	stagePath := filepath.Join(root, "stage")
	if err := os.Mkdir(stagePath, 0700); err != nil {
		t.Fatal(err)
	}
	stage := wtLifeCovOpenDirectory(t, stagePath)
	readStage := func(directory *os.File) (os.FileInfo, error) {
		if directory != stage {
			t.Fatal("reader observed a different stage")
		}
		info, err := directory.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if err := directory.Close(); err != nil {
			t.Fatal(err)
		}
		return info, nil
	}
	// A real original snapshot followed by close at the observation boundary.
	// This does not claim the host spontaneously reproduced a concurrent close.
	err = quarantineMatchingStageDirectoryAtWithRead(parent, stage, readStage)
	if !errors.Is(err, syscall.EBADF) || !strings.Contains(err.Error(), "inspect held staging directory identity") {
		t.Fatalf("second native identity read = %v", err)
	}
	if _, err := stage.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("stage not closed: %v", err)
	}
	if info, err := os.Stat(stagePath); err != nil || !info.IsDir() {
		t.Fatalf("stage evidence changed: %v, %v", info, err)
	}
}

func TestE2ECanonicalPublicationDestinationPlanning(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"missing", "valid", "wrong operation", "invalid repository", "unsafe parent", "regular", "symlink", "unreadable", "substitution"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if name == "unreadable" && os.Geteuid() == 0 {
				t.Skip("requires an unprivileged process: root can bypass directory read permissions")
			}
			root := t.TempDir()
			parent := wtLifeCovOpenDirectory(t, root)
			path := filepath.Join(root, "repo")
			operationRoot := root
			relativeParent := ""
			repository := "repo"
			read := (*os.File).Stat
			want := ""
			exists := false
			switch name {
			case "missing":
			case "wrong operation":
				operationRoot = root + ".wrong"
				want = "operation path changed"
			case "invalid repository":
				repository = "../repo"
				want = "invalid worktree repository segment"
			case "unsafe parent":
				relativeParent = ".."
				want = ""
			case "regular":
				if err := os.WriteFile(path, []byte("retain regular"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "not a directory"
			case "symlink":
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
				want = "refusing symlinked worktree destination"
			default:
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				exists = true
				if name == "unreadable" {
					directoryPath := path
					if err := os.Chmod(directoryPath, 0); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := os.Chmod(directoryPath, 0700); err != nil {
							t.Error(err)
						}
					})
					want = "inspect secure worktree destination"
					exists = false
				}
				if name == "substitution" {
					read = func(directory *os.File) (os.FileInfo, error) {
						info, err := directory.Stat()
						if err != nil {
							t.Fatal(err)
						}
						if err := os.Rename(path, path+".retained"); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(path, 0700); err != nil {
							t.Fatal(err)
						}
						return info, nil
					}
					want = "destination path changed before planning"
					exists = false
				}
			}
			got, actualExists, err := prepareWorktreeDestinationWithRead(operationRoot, parent, relativeParent, repository, read)
			refused := name != "missing" && name != "valid"
			if refused {
				if err == nil || got != "" || actualExists || !strings.Contains(err.Error(), want) {
					t.Fatalf("plan %s = %q, %v, %v; want %q", name, got, actualExists, err, want)
				}
				if name == "unreadable" && !errors.Is(err, os.ErrPermission) {
					t.Fatalf("native permission identity lost: %v", err)
				}
			} else if err != nil || got != path || actualExists != exists {
				t.Fatalf("plan %s = %q, %v, %v", name, got, actualExists, err)
			}
			if _, err := parent.Stat(); err != nil {
				t.Fatalf("borrowed parent closed: %v", err)
			}
			if name == "regular" {
				if contents, err := os.ReadFile(path); err != nil || string(contents) != "retain regular" {
					t.Fatalf("regular evidence = %q, %v", contents, err)
				}
			}
			if name == "symlink" {
				if target, err := os.Readlink(path); err != nil || target == "" {
					t.Fatalf("symlink evidence = %q, %v", target, err)
				}
			}
			if name == "substitution" {
				if info, err := os.Stat(path + ".retained"); err != nil || !info.IsDir() {
					t.Fatalf("retained destination = %v, %v", info, err)
				}
			}
		})
	}
}

func TestE2ECanonicalPublicationQuarantineNativePermissionRefusal(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("requires an unprivileged process: root can bypass directory write permissions")
	}
	root := t.TempDir()
	parent := wtLifeCovOpenDirectory(t, root)
	name := ".wb-stage-permission"
	stagePath := filepath.Join(root, name)
	if err := os.Mkdir(stagePath, 0700); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(stagePath, "evidence")
	if err := os.WriteFile(evidence, []byte("retained stage"), 0600); err != nil {
		t.Fatal(err)
	}
	expected, err := secureDirectoryIdentityAt(int(parent.Fd()), name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(root, 0700); err != nil {
			t.Error(err)
		}
	})
	err = quarantineStageDirectoryAt(parent, name, expected)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("native quarantine permission refusal = %v", err)
	}
	actual, err := secureDirectoryIdentityAt(int(parent.Fd()), name)
	if err != nil || actual != expected {
		t.Fatalf("stage identity changed on refusal = %+v, %v", actual, err)
	}
	if contents, err := os.ReadFile(evidence); err != nil || string(contents) != "retained stage" {
		t.Fatalf("stage evidence = %q, %v", contents, err)
	}
	if _, err := parent.Stat(); err != nil {
		t.Fatalf("borrowed quarantine parent closed: %v", err)
	}
}
