package remotepublish

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/remotestate"
)

func resolvedRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func makeClone(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The git remote store is cloned inside WB's private state directory, and its
// pre-#901 canonical-clone locations are the legacy places it migrates from.
func TestRemoteStoreClonesIntoTheStateDirectory(t *testing.T) {
	t.Parallel()
	root := resolvedRoot(t)
	cfg := remotestate.Config{Provider: "git", Repo: "team/wb-state"}
	clone, err := ClonePath(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".wb", "remote-store", "team", "wb-state"); clone != want {
		t.Fatalf("ClonePath = %q, want %q", clone, want)
	}
	legacy, err := LegacyClonePaths(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(root, "github.com", "team", "wb-state"), filepath.Join(root, "team", "wb-state")}; !reflect.DeepEqual(legacy, want) {
		t.Fatalf("LegacyClonePaths = %q, want %q", legacy, want)
	}
	options, err := GitOptions(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if options.ClonePath != clone || !reflect.DeepEqual(options.LegacyClonePaths, legacy) || !options.RemoteStore || options.CloneURL != CloneURL(cfg) {
		t.Fatalf("GitOptions = %+v, want the store clone, its legacy paths and RemoteStore", options)
	}
}

// The active clone is the legacy one only while a machine has not migrated.
func TestActiveClonePathFollowsTheMigration(t *testing.T) {
	t.Parallel()
	root := resolvedRoot(t)
	cfg := remotestate.Config{Provider: "git", Repo: "team/wb-state"}
	store := filepath.Join(root, ".wb", "remote-store", "team", "wb-state")
	legacy := filepath.Join(root, "team", "wb-state")
	active := func() string {
		t.Helper()
		path, err := ActiveClonePath(root, cfg)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	if got := active(); got != store {
		t.Fatalf("with no clone anywhere, active = %q, want %q", got, store)
	}
	makeClone(t, legacy)
	if got := active(); got != legacy {
		t.Fatalf("before migration, active = %q, want the legacy clone %q", got, legacy)
	}
	makeClone(t, store)
	if got := active(); got != store {
		t.Fatalf("after migration, active = %q, want %q", got, store)
	}
}

// Every resolver refuses a missing or non-directory projects root and a
// remote.repo that is not a safe owner/name.
func TestRemoteStorePathsRejectInvalidInputs(t *testing.T) {
	t.Parallel()
	root := resolvedRoot(t)
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	good := remotestate.Config{Provider: "git", Repo: "team/wb-state"}
	for name, tc := range map[string]struct {
		root string
		cfg  remotestate.Config
		want string
	}{
		"empty root":   {"", good, "projects root is required"},
		"file root":    {file, good, "is not a directory"},
		"dotdot owner": {root, remotestate.Config{Repo: "../wb-state"}, "is not an <owner>/<name>"},
		"missing name": {root, remotestate.Config{Repo: "team"}, "is not an <owner>/<name>"},
	} {
		if _, err := ClonePath(tc.root, tc.cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: ClonePath = %v, want %q", name, err, tc.want)
		}
		if _, err := LegacyClonePaths(tc.root, tc.cfg); err == nil {
			t.Errorf("%s: LegacyClonePaths accepted it", name)
		}
		if _, err := ActiveClonePath(tc.root, tc.cfg); err == nil {
			t.Errorf("%s: ActiveClonePath accepted it", name)
		}
		if _, err := GitOptions(tc.root, tc.cfg); err == nil {
			t.Errorf("%s: GitOptions accepted it", name)
		}
	}
}

// The reader resolves the active clone on every call: built while only the
// legacy clone exists, it reads the .wb clone once that appears, and it
// reports an unresolvable store rather than an empty fleet.
func TestActiveStoreReaderFollowsTheCloneOnEveryRead(t *testing.T) {
	t.Parallel()
	root := resolvedRoot(t)
	cfg := remotestate.Config{Provider: "git", Repo: "team/wb-state"}
	plant := func(clone, login string) {
		t.Helper()
		makeClone(t, clone)
		config := "[remote \"origin\"]\n\turl = " + CloneURL(cfg) + "\n"
		if err := os.WriteFile(filepath.Join(clone, ".git", "config"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
		snapshot := filepath.Join(clone, "machines", login, "box", "snapshot.yaml")
		if err := os.MkdirAll(filepath.Dir(snapshot), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(snapshot, []byte("schema_version: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reader := ActiveStoreReader{ProjectsRoot: root, Config: cfg}
	plant(filepath.Join(root, "team", "wb-state"), "legacy")
	if entries, err := reader.ReadLocal(t.Context()); err != nil || len(entries) != 1 || entries[0].Snapshot.Login != "legacy" {
		t.Fatalf("before migration: ReadLocal = (%+v, %v), want the legacy clone's machine", entries, err)
	}
	plant(filepath.Join(root, ".wb", "remote-store", "team", "wb-state"), "migrated")
	entries, err := reader.ReadLocal(t.Context())
	if err != nil || len(entries) != 1 || entries[0].Snapshot.Login != "migrated" {
		t.Fatalf("ReadLocal = (%+v, %v), want the migrated clone's machine", entries, err)
	}
	if _, err := (ActiveStoreReader{Config: cfg}).ReadLocal(t.Context()); err == nil {
		t.Fatal("ReadLocal with no projects root succeeded")
	}
}
