//go:build linux

package diskprune

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// AC real-directory-dry-run: a plan against this machine's real caches,
// temporary directory and projects root completes with every category and
// never reaches a mutating operation. Each mutating seam fails the test if it
// is called at all, and wrappers on PATH record every git, go and pnpm the
// plan runs: only git worktree list is allowed.
//
//nolint:paralleltest // replaces PATH with recording wrappers
func TestRealMachineDryRunNeverMutates(t *testing.T) {
	recorder := installRecorder(t, "git", "go", "pnpm")
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
	w.RemoveFile = func(Anchor, string, string, Identity) (int64, error) { forbid("RemoveFile"); return 0, nil }
	w.LockDirs = func([]string) (*Lock, error) { forbid("LockDirs"); return &Lock{}, nil }
	w.LockModuleVersion = func(string) (func(), error) { forbid("LockModuleVersion"); return func() {}, nil }
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
	if ctx.Err() != nil {
		t.Fatalf("the real plan did not finish: %v", ctx.Err())
	}
	raw, _ := os.ReadFile(recorder)
	if len(w.Checkouts) > 0 && !strings.Contains(string(raw), "worktree list --porcelain -z") {
		t.Fatalf("the recorder saw no git call although %d checkouts were found: %q", len(w.Checkouts), raw)
	}
	for _, call := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if call != "" && !strings.HasPrefix(call, "git -C ") {
			t.Errorf("the plan ran %q", call)
		}
		if call != "" && !strings.HasSuffix(call, " worktree list --porcelain -z") {
			t.Errorf("the plan ran %q", call)
		}
	}
}

// installRecorder puts wrappers for the named tools first on PATH. Each
// appends its command line to the returned log, then runs the real tool.
func installRecorder(t *testing.T, tools ...string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "calls.log")
	for _, tool := range tools {
		real, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		script := "#!/bin/sh\necho \"" + tool + " $*\" >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}
