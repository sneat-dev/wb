package locallink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSiblingPublicationFailuresRemoveOnlyEdgesCreatedByThisCall(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"readlink", "mkdir", "symlink"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			consumer := t.TempDir()
			names := []string{"@acme/a", "@acme/b", "@acme/c"}
			stages := map[string]string{}
			for _, name := range names {
				target := filepath.Join(consumer, "node_modules", filepath.FromSlash(name))
				stage := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+wbStageSuffix)
				stages[name] = stage
				if err := os.MkdirAll(stage, 0755); err != nil {
					t.Fatal(err)
				}
				manifest := `{"name":"` + name + `","version":"2.0.0"}`
				if name == "@acme/a" {
					manifest = `{"name":"@acme/a","dependencies":{"@acme/b":"*"}}`
				}
				if name == "@acme/b" {
					manifest = `{"name":"@acme/b","dependencies":{"@acme/c":"*"}}`
				}
				lgCovWriteFile(t, filepath.Join(stage, "package.json"), manifest)
				lgCovWriteFile(t, linkAppliedMarkerPath(consumer, name), stage+"\n")
			}
			first := filepath.Join(stages["@acme/a"], "node_modules", "@acme", "b")
			second := filepath.Join(stages["@acme/b"], "node_modules", "@acme", "c")
			access := nativeSiblingLinkIO()
			if phase == "readlink" {
				if err := os.MkdirAll(filepath.Dir(first), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(stages["@acme/b"], first); err != nil {
					t.Fatal(err)
				}
				access.lstat = func(path string) (os.FileInfo, error) {
					info, err := os.Lstat(path)
					if path == first && err == nil {
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
					}
					return info, err
				}
			}
			if phase == "mkdir" {
				calls := 0
				access.mkdirAll = func(path string, mode os.FileMode) error {
					calls++
					if calls == 2 {
						if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(path, []byte("blocked"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					return os.MkdirAll(path, mode)
				}
			}
			if phase == "symlink" {
				calls := 0
				access.symlink = func(from, to string) error {
					calls++
					if calls == 2 {
						if err := os.WriteFile(to, []byte("replacement"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					return os.Symlink(from, to)
				}
			}
			err := (ExecNode{}).linkSiblingsWithIO(context.Background(), consumer, names, access)
			want := map[string]string{"readlink": "read existing staged sibling path", "mkdir": "create staged sibling parent", "symlink": "link staged sibling"}[phase]
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error=%v", err)
			}
			if _, err := os.Lstat(first); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("first edge survived failed publication: %v", err)
			}
			if phase == "symlink" {
				raw, err := os.ReadFile(second)
				if err != nil || string(raw) != "replacement" {
					t.Fatalf("replacement changed: %q %v", raw, err)
				}
			}
			for _, name := range names {
				if _, err := os.Stat(filepath.Join(stages[name], "package.json")); err != nil {
					t.Fatalf("stage lost: %v", err)
				}
				if _, err := os.Stat(linkAppliedMarkerPath(consumer, name)); err != nil {
					t.Fatalf("recovery marker lost: %v", err)
				}
			}
		})
	}
}
