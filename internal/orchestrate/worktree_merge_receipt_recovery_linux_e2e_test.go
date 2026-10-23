//go:build e2e && linux

package orchestrate

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
)

func TestE2EMergeReceiptPathDeletedWorkingDirectoryRetainsChildProfile(t *testing.T) {
	const marker = "WB_RECEIPT_RECOVERY_CHILD"
	if os.Getenv(marker) == t.Name() {
		fixtureMergeReceiptDeletedWorkingDirectory(t)
		return
	}
	t.Parallel()
	owned, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(owned, "child-cwd")
	if err = os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(owned, "projects")
	if err = os.Mkdir(projects, 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^" + regexp.QuoteMeta(t.Name()) + "$", "-test.v"}
	env := append(os.Environ(), marker+"="+t.Name(), "WB_RECEIPT_RECOVERY_OWNED_CWD="+cwd, "WB_RECEIPT_RECOVERY_PROJECTS="+projects)
	profile := ""
	if testing.CoverMode() != "" {
		// Keep this process's counters independent. A retention override lets
		// callers union exact block tuples; a child PASS does not cover the parent.
		directory := os.Getenv("WB_RECEIPT_RECOVERY_CHILD_PROFILE_DIR")
		if directory == "" {
			directory = filepath.Join(owned, "child-profile")
		} else if !filepath.IsAbs(directory) {
			t.Fatal("WB_RECEIPT_RECOVERY_CHILD_PROFILE_DIR must be absolute when supplied")
		}
		if err = os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		profile = filepath.Join(directory, "receipt-deleted-cwd-child.cov")
		if _, statErr := os.Lstat(profile); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("child profile must be fresh: %v", statErr)
		}
		counters := filepath.Join(owned, "child-counters")
		if err = os.Mkdir(counters, 0700); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-test.coverprofile="+profile, "-test.gocoverdir="+counters)
		env = append(env, "GOCOVERDIR="+counters)
	}
	result, err := defaultRunner.RunOpts(t.Context(), cwd, runner.RunOptions{Env: env, CaptureCombined: true}, os.Args[0], args...)
	t.Logf("owned deleted-cwd child:\n%s", result.CombinedOutput)
	if err != nil {
		t.Fatalf("child invocation: %v", err)
	}
	if !strings.Contains(result.CombinedOutput, "--- PASS: "+t.Name()) {
		t.Fatal("child assertion did not complete")
	}
	if _, statErr := os.Lstat(cwd); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("child did not remove its owned cwd: %v", statErr)
	}
	if profile != "" {
		contents, readErr := os.ReadFile(profile)
		if readErr != nil || !strings.HasPrefix(string(contents), "mode: "+testing.CoverMode()+"\n") || !strings.Contains(string(contents), "internal/orchestrate/worktree_merge.go:") {
			t.Fatalf("child exact-source profile unavailable: %v", readErr)
		}
		t.Logf("retained child coverage profile: %s", profile)
	}
}

func fixtureMergeReceiptDeletedWorkingDirectory(t *testing.T) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	owned := os.Getenv("WB_RECEIPT_RECOVERY_OWNED_CWD")
	if owned == "" || cwd != owned || filepath.Base(cwd) != "child-cwd" {
		t.Fatalf("refuse unowned cwd %q, expected %q", cwd, owned)
	}
	entries, err := os.ReadDir(cwd)
	if err != nil || len(entries) != 0 {
		t.Fatalf("child cwd not empty: %v, %v", entries, err)
	}
	projects := os.Getenv("WB_RECEIPT_RECOVERY_PROJECTS")
	if !filepath.IsAbs(projects) || filepath.Dir(projects) != filepath.Dir(cwd) {
		t.Fatalf("unowned projects root: %q", projects)
	}
	if err = os.Remove(cwd); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Getwd(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted cwd did not produce physical getwd error: %v", err)
	}
	got, err := resolveWorktreeMergeReceiptPath(projects, "relative-candidate")
	if got != "" || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("physical Abs failure = %q, %v", got, err)
	}
	if _, statErr := os.Stat(filepath.Join(projects, ".wb")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failure touched authority: %v", statErr)
	}
}
