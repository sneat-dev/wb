package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

func TestStaleHookCleanupRetainsOwnedDescriptorRefusals(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"rewind", "entries"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			managed := newTestManagedHooksDirectory(t, root)
			var actions []string
			err := removeStaleManagedHooksObserved(managed, nil, &actions, nil, nil, func(at string, directory *os.File) {
				if at == stage {
					if err := directory.Close(); err != nil {
						t.Fatal(err)
					}
				}
			})
			if err == nil || len(actions) != 0 {
				t.Fatalf("cleanup=%v actions=%v", err, actions)
			}
			if stage == "rewind" && !strings.Contains(err.Error(), "rewind managed hooks directory") {
				t.Fatalf("rewind context=%v", err)
			}
			if _, err := managed.directory.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("closed authority=%v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("directory changed=%v error=%v", entries, err)
			}
		})
	}
}

func TestStaleUserHookRewriteRefusesNativeSourceSubstitution(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	managed := newTestManagedHooksDirectory(t, root)
	active := filepath.Join(root, "stale")
	saved := filepath.Join(root, "retained-original")
	original := shimManagedSection("", "stale", "", "", "", false) + "echo retained user commands\n"
	if err := testenv.WriteExecutableFile(active, []byte(original), 0700); err != nil {
		t.Fatal(err)
	}
	var actions []string
	err := removeStaleManagedHooksAt(managed, nil, &actions, nil, func(name string) {
		if name != "stale" {
			t.Fatalf("authorized hook=%q", name)
		}
		if err := os.Rename(active, saved); err != nil {
			t.Fatal(err)
		}
		if err := testenv.WriteExecutableFile(active, []byte("replacement occupant"), 0700); err != nil {
			t.Fatal(err)
		}
	})
	if err == nil || len(actions) != 0 || !strings.Contains(err.Error(), "changed after inspection") {
		t.Fatalf("cleanup=%v actions=%v", err, actions)
	}
	for path, want := range map[string]string{active: "replacement occupant", saved: original} {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != want {
			t.Fatalf("retained %s=%q error=%v", path, raw, err)
		}
	}
}
