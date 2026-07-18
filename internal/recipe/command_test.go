package recipe

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// git runs a git command in dir with a fixed test identity. Shared by every
// _test.go file in this package — do not redefine it elsewhere.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestPreviewCommandNoDryRunCommand(t *testing.T) {
	r := Recipe{Command: "true"}
	p, err := previewCommand(r, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !p.Changed || p.Summary != "run: true" {
		t.Errorf("got %+v, want Changed=true Summary=\"run: true\"", p)
	}
}

func TestPreviewCommandCleanExit(t *testing.T) {
	r := Recipe{Command: "true", DryRunCommand: "exit 0"}
	p, err := previewCommand(r, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if p.Changed || p.Summary != "clean" {
		t.Errorf("got %+v, want Changed=false Summary=clean", p)
	}
}

func TestPreviewCommandNonZeroExit(t *testing.T) {
	r := Recipe{Command: "fix-it", DryRunCommand: "exit 1"}
	p, err := previewCommand(r, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !p.Changed || p.Summary != "run: fix-it" {
		t.Errorf("got %+v, want Changed=true Summary=\"run: fix-it\"", p)
	}
}

func TestPreviewCommandCountRegex(t *testing.T) {
	r := Recipe{
		Command:       "fix-it",
		DryRunCommand: `echo "3 violation(s) found"; exit 1`,
		CountRegex:    `(\d+)\s+violation`,
	}
	p, err := previewCommand(r, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !p.Changed || p.Summary != "3 match(es)" {
		t.Errorf("got %+v, want Changed=true Summary=\"3 match(es)\"", p)
	}
}

func TestPreviewCommandLaunchFailure(t *testing.T) {
	r := Recipe{Command: "x", DryRunCommand: "this-command-does-not-exist-xyz"}
	if _, err := previewCommand(r, t.TempDir()); err == nil {
		t.Error("expected an error when the dry_run_command can't be found, got nil")
	}
}

func TestCommandMutatorChanges(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")

	r := Recipe{Name: "touch-it", Command: "echo v2 > f.txt"}
	changed, detail, err := commandMutator(r)(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || detail != "applied touch-it" {
		t.Errorf("got changed=%v detail=%q, want true, \"applied touch-it\"", changed, detail)
	}
}

func TestCommandMutatorNoChange(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")

	r := Recipe{Name: "noop", Command: "true"}
	changed, detail, err := commandMutator(r)(dir)
	if err != nil {
		t.Fatal(err)
	}
	if changed || detail != "clean" {
		t.Errorf("got changed=%v detail=%q, want false, \"clean\"", changed, detail)
	}
}
