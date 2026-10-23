package locallink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkRestorationFailuresPreserveRecoveryArtifacts(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"readlink", "remove active", "clear restored record", "restore link", "remove backup", "superseded directory", "inspect"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			consumer, target, marker, stage := lgCovStagedConsumer(t, "@acme/core")
			published := t.TempDir()
			lgCovWriteFile(t, filepath.Join(published, "package.json"), `{"name":"@acme/core","version":"1.0.0"}`)
			lgCovWriteFile(t, filepath.Join(published, "keep"), "published")
			backup := target + linkSymlinkBackupSuffix
			lgCovWriteFile(t, backup, published+"\n")
			observe := nativeLinkRestoreObservations()
			retained := target + "-retained"
			if phase == "readlink" || phase == "remove active" {
				if err := os.Symlink(stage, target); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "clear restored record" {
				if err := os.Symlink(published, target); err != nil {
					t.Fatal(err)
				}
				observe.stat = func(path string) (os.FileInfo, error) {
					if err := os.Remove(backup); err != nil {
						t.Fatal(err)
					}
					return os.Stat(path)
				}
			}
			if phase == "inspect" {
				observe.lstat = func(path string) (os.FileInfo, error) { return os.Lstat(path + "\x00") }
			}
			if phase == "readlink" {
				observe.lstat = func(path string) (os.FileInfo, error) {
					info, err := os.Lstat(path)
					if err == nil {
						if err := os.Rename(path, retained); err != nil {
							t.Fatal(err)
						}
					}
					return info, err
				}
			}
			if phase == "remove active" {
				observe.readlink = func(path string) (string, error) {
					value, err := os.Readlink(path)
					if err == nil {
						if err := os.Rename(path, retained); err != nil {
							t.Fatal(err)
						}
					}
					return value, err
				}
			}
			if phase == "restore link" || phase == "remove backup" {
				observe.readFile = func(path string) ([]byte, error) {
					raw, err := os.ReadFile(path)
					if path == backup && err == nil {
						if phase == "restore link" {
							if err := os.WriteFile(target, []byte("replacement"), 0600); err != nil {
								t.Fatal(err)
							}
						} else {
							if err := os.Remove(backup); err != nil {
								t.Fatal(err)
							}
							if err := os.Mkdir(backup, 0755); err != nil {
								t.Fatal(err)
							}
							lgCovWriteFile(t, filepath.Join(backup, "keep"), "retained")
						}
					}
					return raw, err
				}
			}
			if phase == "superseded directory" {
				if err := os.Mkdir(target, 0755); err != nil {
					t.Fatal(err)
				}
				lgCovWriteFile(t, filepath.Join(target, "package.json"), `{"name":"@acme/core","version":"1.0.0"}`)
				dirBackup := target + linkBackupSuffix
				lgCovWriteFile(t, filepath.Join(dirBackup, "keep"), "installed")
			}
			_, err := (ExecNode{}).unlinkWithObservations(context.Background(), consumer, "@acme/core", observe)
			if err == nil {
				t.Fatal("failed restoration accepted")
			}
			want := map[string]string{"readlink": "read the active link", "remove active": "remove the link", "clear restored record": "clear the link record", "restore link": "restore the original link", "remove backup": "clear the link record", "superseded directory": "", "inspect": "inspect "}[phase]
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error=%v", err)
			}
			if phase == "readlink" || phase == "remove active" || phase == "clear restored record" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("native cause=%v", err)
				}
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("recovery marker lost: %v", err)
			}
			if _, err := os.Stat(stage); err != nil {
				t.Fatalf("staged evidence lost: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(published, "keep"))
			if err != nil || string(raw) != "published" {
				t.Fatalf("published bytes changed: %q %v", raw, err)
			}
			if phase == "restore link" {
				raw, err := os.ReadFile(target)
				if err != nil || string(raw) != "replacement" {
					t.Fatalf("replacement changed: %q %v", raw, err)
				}
			}
		})
	}
}
