//go:build linux

package diskprune

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// AC pnpm-store-prune-on-a-real-store: the real pnpm, against a store this
// test owns (the XDG directories decide pnpm's default store, so the user's
// store is never touched), keeps a package a project links and removes one
// no project references any more.
func TestPnpmStorePruneOnARealStore(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm is not installed")
	}
	dir := t.TempDir()
	env := append(os.Environ(), "XDG_DATA_HOME="+filepath.Join(dir, "data"), "XDG_CACHE_HOME="+filepath.Join(dir, "cache"),
		"XDG_STATE_HOME="+filepath.Join(dir, "state"), "XDG_CONFIG_HOME="+filepath.Join(dir, "config"))
	env = append(env, pnpmEnv...)
	pnpm := func(cwd string, args ...string) string {
		cmd := exec.Command("pnpm", args...)
		cmd.Dir, cmd.Env = cwd, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("pnpm %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	for name, body := range map[string]string{"kept": "module.exports='kept-package-body'", "dropped": "module.exports='dropped-package-body'"} {
		pkg := filepath.Join(dir, name)
		mustWrite(t, filepath.Join(pkg, "package.json"), `{"name":"`+name+`","version":"1.0.0"}`, 0o644)
		mustWrite(t, filepath.Join(pkg, "index.js"), body, 0o644)
		pnpm(pkg, "pack", "--pack-destination", dir)
	}
	project := filepath.Join(dir, "project")
	mustWrite(t, filepath.Join(project, "package.json"), `{"name":"p","version":"1.0.0","dependencies":{"kept":"file:../kept-1.0.0.tgz","dropped":"file:../dropped-1.0.0.tgz"}}`, 0o644)
	pnpm(project, "install", "--offline")
	mustWrite(t, filepath.Join(project, "package.json"), `{"name":"p","version":"1.0.0","dependencies":{"kept":"file:../kept-1.0.0.tgz"}}`, 0o644)
	pnpm(project, "install", "--offline")
	// pnpm keeps the dropped package in the project's virtual store, still
	// hard-linked to the store; removing that copy makes it unreferenced.
	virtual, _ := filepath.Glob(filepath.Join(project, "node_modules", ".pnpm", "dropped@*"))
	for _, path := range virtual {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	store := filepath.Join(dir, "data", "pnpm", "store")
	if !storeHolds(t, store, "dropped-package-body") {
		t.Fatal("fixture: the dropped package is not in the store before pruning")
	}

	tw := newTestWorld(t)
	tw.PnpmStores = []string{store}
	tw.Pnpm = func(ctx context.Context, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "pnpm", args...)
		cmd.Dir, cmd.Env = "/", env
		out, err := cmd.Output()
		return string(out), err
	}
	tw.categories = []category{pnpmCategory{}}
	report := tw.run(Options{Apply: true, Categories: []string{CategoryPnpm}, Floor: 0.01})
	if it := categoryByName(t, report, CategoryPnpm).Items[0]; it.Status != StatusDeleted {
		t.Fatalf("pnpm item = %#v", it)
	}
	dropped, kept := storeHolds(t, store, "dropped-package-body"), storeHolds(t, store, "kept-package-body")
	if dropped || !kept {
		t.Fatalf("after prune: dropped package present %t, kept package present %t", dropped, kept)
	}
	if got := strings.TrimSpace(pnpm("/", "store", "path")); !strings.HasPrefix(got, store) {
		t.Fatalf("store path moved to %s", got)
	}
}

func storeHolds(t *testing.T, store, needle string) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(store, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			if raw, err := os.ReadFile(path); err == nil && bytes.Contains(raw, []byte(needle)) {
				found = true
			}
		}
		return nil
	})
	return found
}
