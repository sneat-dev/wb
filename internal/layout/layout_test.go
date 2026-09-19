package layout

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

func TestMain(m *testing.M) {
	testenv.IsolateProcess()
	// This package's migrate tests read Work Log claims across every
	// wbhome-resolved home, including the retired legacy $HOME/.wb, and must
	// not see this machine's real fleet state (a fixture repository name can
	// coincidentally collide with a real claim recorded there). Redirect HOME
	// to a private, empty directory for this package's test binary only; a
	// test that deliberately exercises legacy-home behavior overrides it
	// again with its own t.Setenv("HOME", ...), which always wins for that
	// test. This is scoped to this package deliberately: internal/worktrees'
	// own tests exercise a dropped-HOME subprocess path whose expected
	// behavior depends on the real ambient HOME being genuinely absent, so
	// the same redirection must not apply there.
	//
	// os.Exit skips deferred calls, so the directory is removed explicitly
	// after m.Run; leaving it behind leaked one populated HOME per run.
	home, err := os.MkdirTemp("", "wb-layout-test-home-*")
	if err == nil {
		if resolved, err := filepath.EvalSymlinks(home); err == nil {
			home = resolved
		}
		_ = os.Setenv("HOME", home)
	}
	code := m.Run()
	if home != "" {
		_ = os.RemoveAll(home)
	}
	os.Exit(code)
}

func TestAuditDetectsTopLevelAndMisowned(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := initRemoteClone(t, root, "acme", "app", "acme/app")
	_ = canonical
	top := filepath.Join(root, "app")
	cloneFrom(t, filepath.Join(root, "acme", "app.git"), top)
	misowned := initRemoteClone(t, root, "other", "app", "acme/app")

	report, err := Audit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.TopLevel < 1 {
		t.Fatalf("summary = %+v, want top_level", report.Summary)
	}
	foundMisowned := false
	foundTop := false
	foundOK := false
	for _, finding := range report.Findings {
		switch finding.Kind {
		case KindTopLevel:
			foundTop = true
			if finding.Path != top {
				t.Fatalf("top-level path = %s", finding.Path)
			}
		case KindMisowned:
			if finding.Path == misowned {
				foundMisowned = true
			}
		case KindOK:
			if finding.PathSlug == "acme/app" {
				foundOK = true
			}
		}
	}
	if !foundTop || !foundMisowned || !foundOK {
		t.Fatalf("findings = %+v", report.Findings)
	}
	if !Failed(report) {
		t.Fatal("audit with problems must fail")
	}
}

func TestCleanRemovesSafeTopLevelDuplicate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_ = initRemoteClone(t, root, "acme", "app", "acme/app")
	top := filepath.Join(root, "app")
	cloneFrom(t, filepath.Join(root, "acme", "app.git"), top)

	planned, err := Clean(context.Background(), root, CleanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Actions) != 1 || planned.Actions[0].Status != "planned" {
		t.Fatalf("planned = %+v", planned.Actions)
	}
	if _, err := os.Stat(top); err != nil {
		t.Fatal("dry-run must not remove the top-level clone")
	}

	applied, err := Clean(context.Background(), root, CleanOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Actions) != 1 || applied.Actions[0].Status != "removed" {
		t.Fatalf("applied = %+v", applied.Actions)
	}
	if _, err := os.Stat(top); !os.IsNotExist(err) {
		t.Fatal("apply must remove the top-level clone")
	}
	if _, err := os.Stat(filepath.Join(root, "acme", "app")); err != nil {
		t.Fatal("canonical clone must remain")
	}
}

func TestCleanSkipsDirtyOrMissingCanonical(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	top := filepath.Join(root, "only")
	seedRemoteClone(t, root, "only", "acme/only", top)
	if err := os.WriteFile(filepath.Join(top, "wip.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Clean(context.Background(), root, CleanOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Actions) != 1 || report.Actions[0].Status != "skipped" {
		t.Fatalf("actions = %+v", report.Actions)
	}
	if _, err := os.Stat(top); err != nil {
		t.Fatal("dirty top-level clone must be preserved")
	}
}

func initRemoteClone(t *testing.T, root, owner, name, originSlug string) string {
	t.Helper()
	remote := filepath.Join(root, owner, name+".git")
	seed := filepath.Join(root, owner, name+"-seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "init", "-b", "main")
	run(t, seed, "git", "config", "user.email", "wb@example.test")
	run(t, seed, "git", "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte(originSlug+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-m", "init")
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, root, "git", "clone", "--bare", seed, remote)
	canonical := filepath.Join(root, owner, name)
	cloneFrom(t, remote, canonical)
	run(t, canonical, "git", "remote", "set-url", "origin", "git@github.com:"+originSlug+".git")
	return canonical
}

func seedRemoteClone(t *testing.T, root, dirName, originSlug, dest string) {
	t.Helper()
	seed := filepath.Join(root, dirName+"-seed")
	remote := filepath.Join(root, dirName+".git")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "init", "-b", "main")
	run(t, seed, "git", "config", "user.email", "wb@example.test")
	run(t, seed, "git", "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-m", "init")
	run(t, root, "git", "clone", "--bare", seed, remote)
	cloneFrom(t, remote, dest)
	run(t, dest, "git", "remote", "set-url", "origin", "git@github.com:"+originSlug+".git")
}

func cloneFrom(t *testing.T, remote, dest string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, filepath.Dir(dest), "git", "clone", remote, dest)
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
}

// gitOutput runs git in dir and returns its trimmed combined output, failing
// the test on a non-zero exit.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}
