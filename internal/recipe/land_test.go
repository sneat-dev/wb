package recipe

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRemoteRepo creates a bare remote and a local clone tracking it, with an
// initial commit (including a README.md) on main, and returns the clone's
// path.
func newRemoteRepo(t *testing.T) string {
	t.Helper()
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare", "-b", "main")

	clone := t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", remote, clone).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
	// Land() commits inside this clone via plain `git commit` (no explicit
	// author env), relying on git config the way a real caller's environment
	// would provide it. Set it locally here so the test is hermetic and
	// doesn't depend on the CI runner having a global git identity.
	git(t, clone, "config", "user.email", "t@t")
	git(t, clone, "config", "user.name", "t")
	write(t, clone, "README.md", "# Project\n\nIntro.\n")
	git(t, clone, "add", "-A")
	git(t, clone, "commit", "-qm", "init")
	git(t, clone, "push", "-q", "origin", "main")
	return clone
}

func TestEvaluateTemplateSectionInsert(t *testing.T) {
	clone := newRemoteRepo(t)
	r := writeTemplate(t, "m", "block body")
	r.Target = "README.md"

	p, err := Evaluate(r, clone)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Changed || p.Summary != "insert" {
		t.Errorf("got %+v, want Changed=true Summary=insert", p)
	}
}

func TestEvaluateTemplateSectionNoTargetFile(t *testing.T) {
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare", "-b", "main")
	seed := t.TempDir()
	git(t, seed, "init", "-q", "-b", "main")
	write(t, seed, "other.txt", "x\n")
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-qm", "init")
	git(t, seed, "remote", "add", "origin", remote)
	git(t, seed, "push", "-q", "origin", "main")

	r := writeTemplate(t, "m", "block body")
	r.Target = "README.md"

	p, err := Evaluate(r, seed)
	if err != nil {
		t.Fatal(err)
	}
	if p.Changed || p.Summary != "no README.md" {
		t.Errorf("got %+v, want Changed=false Summary=\"no README.md\"", p)
	}
}

func TestLandTemplateSectionDirectPush(t *testing.T) {
	clone := newRemoteRepo(t)
	r := writeTemplate(t, "m", "block body")
	r.Name = "test-recipe"
	r.Target = "README.md"
	if err := r.applyDefaults(); err != nil {
		t.Fatal(err)
	}

	outcome, err := Land(r, clone, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Changed || outcome.Detail != "pushed to main" {
		t.Errorf("outcome = %+v, want Changed=true Detail=\"pushed to main\"", outcome)
	}

	// Re-landing is a noop — the section is already current.
	outcome2, err := Land(r, clone, "main")
	if err != nil {
		t.Fatal(err)
	}
	if outcome2.Changed {
		t.Errorf("second Land should be a noop, got %+v", outcome2)
	}
}

func TestLandCommandDirectPush(t *testing.T) {
	clone := newRemoteRepo(t)
	r := Recipe{Name: "touch-it", Type: KindCommand, Command: "echo hi > NOTES.md"}
	if err := r.applyDefaults(); err != nil {
		t.Fatal(err)
	}

	outcome, err := Land(r, clone, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Changed || outcome.Detail != "pushed to main" {
		t.Errorf("outcome = %+v, want Changed=true Detail=\"pushed to main\"", outcome)
	}
}
