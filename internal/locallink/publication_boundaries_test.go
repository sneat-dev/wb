package locallink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestLinkPublicationFailuresRetainOriginalPackage(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"readlink", "backup write", "remove", "rename", "inspect", "symlink", "cleanup", "artifact coordinate"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			consumer := t.TempDir()
			packageName := "@acme/core"
			target := filepath.Join(consumer, "node_modules", "@acme", "core")
			published := filepath.Join(consumer, "node_modules", ".installed", "core")
			lgCovWriteFile(t, filepath.Join(published, "package.json"), `{"name":"@acme/core","version":"1.0.0"}`)
			lgCovWriteFile(t, filepath.Join(published, "keep"), "original")
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				t.Fatal(err)
			}
			if phase == "rename" {
				if err := os.Mkdir(target, 0755); err != nil {
					t.Fatal(err)
				}
				lgCovWriteFile(t, filepath.Join(target, "keep"), "installed")
			} else if err := os.Symlink(published, target); err != nil {
				t.Fatal(err)
			}
			access := nativeLinkPublicationIO()
			nativeInspect := access.inspect
			calls := 0
			retained := target + "-retained"
			failure := errors.New("publication failed")
			var inj *filewrite.Injector
			access.inspect = func(path string) (os.FileInfo, error) {
				calls++
				info, err := nativeInspect(path)
				if calls == 2 && (phase == "readlink" || phase == "inspect") {
					if phase == "readlink" {
						if err := os.Rename(path, retained); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := os.Rename(filepath.Dir(path), filepath.Dir(path)+"-retained"); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Dir(path), []byte("occupied"), 0600); err != nil {
							t.Fatal(err)
						}
						return os.Lstat(path)
					}
				}
				return info, err
			}
			if phase == "remove" {
				access.remove = func(path string) error {
					if err := os.Rename(path, retained); err != nil {
						t.Fatal(err)
					}
					return os.Remove(path)
				}
			}
			if phase == "rename" {
				access.rename = func(from, to string) error {
					if err := os.Rename(from, retained); err != nil {
						t.Fatal(err)
					}
					return os.Rename(from, to)
				}
			}
			if phase == "symlink" {
				access.symlink = func(from, to string) error {
					if err := os.WriteFile(to, []byte("replacement"), 0600); err != nil {
						t.Fatal(err)
					}
					return os.Symlink(from, to)
				}
			}
			if phase == "artifact coordinate" {
				access.relative = func(base, target string) (string, error) { return filepath.Rel(base, "relative-coordinate") }
			}
			if phase == "backup write" {
				inj = &filewrite.Injector{Step: filewrite.StepWrite, Name: target + linkSymlinkBackupSuffix, Err: failure}
			}
			if phase == "cleanup" {
				inj = &filewrite.Injector{Step: filewrite.StepWrite, Err: failure, Skip: 1, Hook: func() { lgCovWriteFile(t, linkAppliedMarkerPath(consumer, packageName), "/invalid-stage\n") }}
			}
			_, err := (ExecNode{}).linkWithPublication(context.Background(), consumer, packageName, lgCovDist(t, packageName, "2.0.0"), inj, access)
			if err == nil {
				t.Fatal("failed publication accepted")
			}
			want := map[string]string{"readlink": "read the existing link", "backup write": "publication failed", "remove": "replace the existing link", "rename": "set aside the installed", "inspect": "inspect ", "symlink": "link " + target, "cleanup": "restore the published package after the failed link", "artifact coordinate": "record generated path"}[phase]
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want %q", err, want)
			}
			raw, readErr := os.ReadFile(filepath.Join(published, "keep"))
			if readErr != nil || string(raw) != "original" {
				t.Fatalf("published package changed: %q %v", raw, readErr)
			}
			if phase == "rename" {
				raw, readErr = os.ReadFile(filepath.Join(retained, "keep"))
				if readErr != nil || string(raw) != "installed" {
					t.Fatalf("installed backup lost: %q %v", raw, readErr)
				}
			}
		})
	}
}

func TestBuiltPackageCopyPreservesNativeObservationErrors(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"entry info", "open"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			source, destination := t.TempDir(), t.TempDir()
			lgCovWriteFile(t, filepath.Join(source, "a"), "a")
			lgCovWriteFile(t, filepath.Join(source, "z"), "z")
			var inj *filewrite.Injector
			open := os.Open
			if phase == "entry info" {
				inj = &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Name: filepath.Join(destination, "a"), Hook: func() {
					if err := os.Remove(filepath.Join(source, "z")); err != nil {
						t.Fatal(err)
					}
				}}
			} else {
				open = func(path string) (*os.File, error) {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					return os.Open(path)
				}
			}
			err := copyBuiltPackageContentsWithOpen(source, destination, inj, open)
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("native error = %v", err)
			}
			if phase == "entry info" {
				raw, err := os.ReadFile(filepath.Join(destination, "a"))
				if err != nil || string(raw) != "a" {
					t.Fatalf("completed file lost: %q %v", raw, err)
				}
			}
		})
	}
}
