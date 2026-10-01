//go:build e2e && !windows

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func dirtyCaptureTestRoot(t *testing.T, directory string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func dirtyCaptureReplaceDirectory(t *testing.T, directory string) {
	t.Helper()
	if err := os.Rename(directory, directory+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
}

//nolint:paralleltest // two native Git fixtures set process-wide WB and Git environment.
func TestE2EDirtyCaptureRootAcquisitionAndQueryFaults(t *testing.T) {
	// Every callback acts on an actual filesystem handle at the named boundary;
	// it does not replace Git or os.Root's error with a fabricated one.
	tests := []struct {
		name   string
		stage  string
		setup  func(*testing.T) string
		change func(*testing.T, string, *os.Root)
		want   string
	}{
		{"root removed before open", "root-lstat", func(t *testing.T) string { return t.TempDir() }, func(t *testing.T, directory string, _ *os.Root) {
			if err := os.Remove(directory); err != nil {
				t.Fatal(err)
			}
		}, "open dirty worktree root"},
		{"different root opened", "root-lstat", func(t *testing.T) string { return t.TempDir() }, func(t *testing.T, directory string, _ *os.Root) {
			dirtyCaptureReplaceDirectory(t, directory)
		}, "changed before path inspection"},
		{"held root closed before stat", "root-open", func(t *testing.T) string { return t.TempDir() }, func(t *testing.T, _ string, root *os.Root) {
			if err := root.Close(); err != nil {
				t.Fatal(err)
			}
		}, "inspect held dirty worktree root"},
		{"root swapped after Git query", "paths", dirtyCaptureQueryFixture, func(t *testing.T, directory string, _ *os.Root) {
			dirtyCaptureReplaceDirectory(t, directory)
		}, "changed during capture"},
		{"root swapped after entry read", "entry", dirtyCaptureQueryFixture, func(t *testing.T, directory string, _ *os.Root) {
			dirtyCaptureReplaceDirectory(t, directory)
		}, "changed during capture"},
	}
	for _, test := range tests {
		//nolint:paralleltest // the query cases use native Git fixtures with t.Setenv.
		t.Run(test.name, func(t *testing.T) {
			directory := test.setup(t)
			fired := false
			_, err := collectDirtyCaptureWithBoundary(context.Background(), directory, func(stage string, root *os.Root, _ *os.File) {
				if stage == test.stage {
					fired = true
					test.change(t, directory, root)
				}
			})
			if !fired || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("boundary %s fired=%v error=%v, want %q", test.stage, fired, err, test.want)
			}
		})
	}
}

func dirtyCaptureQueryFixture(t *testing.T) string {
	t.Helper()
	fixture := newGitFixture(t)
	path := filepath.Join(fixture.canonical, "README.md")
	if err := os.WriteFile(path, []byte("# dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return fixture.canonical
}

func TestE2EDirtyCaptureLeafFaultsRemainPhaseSpecific(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, kind, stage, want string
		change                  func(*testing.T, string)
	}{
		{"regular disappeared after lstat", "file", "leaf-lstat", "read dirty path", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink disappeared before readlink", "link", "leaf-lstat", "read dirty symlink", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink replaced after readlink", "link", "readlink", "dirty symlink changed", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("other", path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			path := filepath.Join(directory, "leaf")
			if test.kind == "file" {
				if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink("target", path); err != nil {
				t.Fatal(err)
			}
			root := dirtyCaptureTestRoot(t, directory)
			fired := false
			_, _, err := readDirtyCaptureEntry(root, "leaf", 0, func(stage string, _ *os.Root, _ *os.File) {
				if stage == test.stage {
					fired = true
					test.change(t, path)
				}
			})
			if !fired || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("boundary %s fired=%v error=%v, want %q", test.stage, fired, err, test.want)
			}
		})
	}
}

func TestE2EDirtyCaptureRegularReadFaultsRemainPhaseSpecific(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, stage, want string
		change            func(*testing.T, string, *os.File)
	}{
		{"closed after open", "file-open", "closed", func(t *testing.T, _ string, file *os.File) {
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}},
		{"closed before read", "file-stat-before", "closed", func(t *testing.T, _ string, file *os.File) {
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}},
		{"closed before postread stat", "file-read", "closed", func(t *testing.T, _ string, file *os.File) {
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}},
		{"removed before postread path stat", "file-stat-after", "no such file", func(t *testing.T, path string, _ *os.File) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{"same inode changed during read", "file-stat-before", "changed while", func(t *testing.T, path string, _ *os.File) {
			if err := os.WriteFile(path, []byte("new!"), 0o600); err != nil {
				t.Fatal(err)
			}
			changed := time.Now().Add(time.Minute)
			if err := os.Chtimes(path, changed, changed); err != nil {
				t.Fatal(err)
			}
		}},
		{"mode changed during read", "file-stat-before", "changed while", func(t *testing.T, path string, _ *os.File) {
			if err := os.Chmod(path, 0o400); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			path := filepath.Join(directory, "file")
			if err := os.WriteFile(path, []byte("old!"), 0o600); err != nil {
				t.Fatal(err)
			}
			initial, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			root := dirtyCaptureTestRoot(t, directory)
			fired := false
			_, err = readDirtyCaptureRegular(root, "file", initial, func(stage string, _ *os.Root, file *os.File) {
				if stage == test.stage {
					fired = true
					test.change(t, path, file)
				}
			})
			if !fired || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("boundary %s fired=%v error=%v, want %q", test.stage, fired, err, test.want)
			}
		})
	}
}
