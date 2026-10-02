package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

func TestManagedPathRetainsNativeCommonDirectoryRefusals(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"missing", "regular file"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			common := filepath.Join(t.TempDir(), "common")
			if name == "regular file" {
				if err := os.WriteFile(common, []byte("retained"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := managedPathWithCommon("repository", func(root string) (string, error) {
				if root != "repository" {
					t.Fatalf("root=%q", root)
				}
				return common, nil
			})
			if result != "" || err == nil {
				t.Fatalf("managed=%q error=%v", result, err)
			}
			if name == "missing" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("native Lstat error=%v", err)
			}
			if name == "regular file" {
				if !strings.Contains(err.Error(), "git common directory is not a directory") {
					t.Fatalf("common refusal=%v", err)
				}
				raw, err := os.ReadFile(common)
				if err != nil || string(raw) != "retained" {
					t.Fatalf("occupant=%q error=%v", raw, err)
				}
			}
		})
	}
}

func TestDurableHookExecutableRetainsPostResolutionStatFailure(t *testing.T) {
	t.Parallel()
	executable := testWBExecutable(t, "wb")
	result, err := durableWBExecutableWithStat(executable, func(path string) (os.FileInfo, error) {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		return os.Stat(path)
	})
	if result != "" || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "inspect WB executable") {
		t.Fatalf("executable=%q error=%v", result, err)
	}
}

func TestSecureHookRootOpenRetainsNativeRefusal(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent-root")
	file, err := openAbsoluteHooksDirectoryWithRoot(t.TempDir(), func(path string, flags int) (int, error) {
		if path != string(filepath.Separator) || flags != unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW {
			t.Fatalf("root open=%q flags=%d", path, flags)
		}
		return unix.Open(missing, flags, 0)
	})
	if file != nil || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "open filesystem root") {
		t.Fatalf("file=%v error=%v", file, err)
	}
}

func TestManagedHookDirectoryAdmissionRefusesNamespaceChanges(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"repository", "managed"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			repo := filepath.Join(root, "repo")
			common := filepath.Join(repo, "common")
			if err := os.MkdirAll(common, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(common, "hooks")
			var held *os.File
			result, err := openManagedHooksDirectoryObserved(repo, path, nil, func(at string, authority managedHooksDirectory) {
				if at != stage {
					return
				}
				moved := repo
				held = authority.repo
				if stage == "managed" {
					moved = path
					held = authority.directory
				}
				if err := os.Rename(moved, moved+".retained"); err != nil {
					t.Fatal(err)
				}
				if stage == "managed" {
					if err := os.Mkdir(moved, 0700); err != nil {
						t.Fatal(err)
					}
				}
			})
			if err == nil || held == nil || result.directory != nil || !strings.Contains(err.Error(), "directory path changed") {
				t.Fatalf("admission=%+v error=%v", result, err)
			}
			if _, err := held.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("failed admission leaked owned descriptor: %v", err)
			}
			retained := repo + ".retained"
			if stage == "managed" {
				retained = path + ".retained"
			}
			if info, err := os.Stat(retained); err != nil || !info.IsDir() {
				t.Fatalf("retained directory=%v error=%v", info, err)
			}
		})
	}
}
