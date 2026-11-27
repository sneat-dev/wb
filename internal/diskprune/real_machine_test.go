//go:build linux

package diskprune

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// AC real-directory-dry-run: a plan against this machine's real caches,
// temporary directory and projects root completes with every category and
// never reaches a mutating operation. Each mutating seam fails the test if it
// is called at all.
func TestRealMachineDryRunNeverMutates(t *testing.T) {
	root := os.Getenv("WB_PROJECTS_ROOT")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, "projects")
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		root = t.TempDir()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	w, err := NewWorld(ctx, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	forbid := func(name string) { t.Errorf("plan called %s", name) }
	w.RemoveTree = func(string, string, Identity) (TreeOutcome, error) { forbid("RemoveTree"); return TreeOutcome{}, nil }
	w.RemoveFile = func(string, string, Identity) (int64, error) { forbid("RemoveFile"); return 0, nil }
	w.LockDirs = func([]string) (*Lock, error) { forbid("LockDirs"); return &Lock{}, nil }
	w.LockModuleVersion = func(string) (func(), error) { forbid("LockModuleVersion"); return func() {}, nil }
	w.Pnpm = func(context.Context, ...string) (string, error) { forbid("pnpm"); return "", nil }
	w.HTTP = func(context.Context, string, string) (int, []byte, error) { forbid("HTTP"); return 0, nil, nil }
	w.Admit = func(context.Context) (string, time.Duration, func()) { forbid("Admit"); return "", 0, func() {} }
	report := Run(ctx, w, Options{
		Categories: SelectableCategories, Target: Target{Kind: TargetMax}, AllowFullClean: true, Floor: 0.1,
	})
	seen := map[string]bool{}
	for _, category := range report.Categories {
		seen[category.Name] = true
	}
	for _, name := range append(append([]string{}, SelectableCategories...), categoryFullClean) {
		if !seen[name] {
			t.Errorf("category %s missing from the real plan", name)
		}
	}
	if report.Mode != "plan" || report.Lock.Held {
		t.Fatalf("report = %#v", report)
	}
}
