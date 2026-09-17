package recipe

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTailCovAppliesToReportsStatFailures pins that a path that cannot be
// inspected at all is an error, not silently "no matching file".
func TestTailCovAppliesToReportsStatFailures(t *testing.T) {
	file := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(file, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Recipe{AppliesIf: "has_file:anything"}
	ok, err := r.AppliesTo(file)
	if err == nil || ok {
		t.Fatalf("AppliesTo = (%v, %v), want the stat failure reported rather than treated as absent", ok, err)
	}
}

func TestTailCovPreviewCommandRejectsInvalidCountRegex(t *testing.T) {
	r := Recipe{Command: "fix-it", DryRunCommand: "exit 1", CountRegex: "("}
	_, err := previewCommand(r, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "invalid count_regex") {
		t.Fatalf("err = %v, want the unparsable count_regex reported", err)
	}
}

// TestTailCovCommandMutatorPropagatesFailures pins the two ways the mutator
// fails hard: the command itself is missing (shell exit 127) and the worktree
// cannot be inspected for changes.
func TestTailCovCommandMutatorPropagatesFailures(t *testing.T) {
	t.Run("command not found", func(t *testing.T) {
		r := Recipe{Name: "missing", Command: "tailcov-command-that-does-not-exist"}
		changed, detail, err := commandMutator(r)(t.TempDir())
		if err == nil || changed || detail != "" {
			t.Fatalf("commandMutator = (%v, %q, %v), want the shell's command-not-found reported", changed, detail, err)
		}
	})
	t.Run("worktree is not a repository", func(t *testing.T) {
		r := Recipe{Name: "noop", Command: "true"}
		changed, detail, err := commandMutator(r)(t.TempDir())
		if err == nil || changed || detail != "" {
			t.Fatalf("commandMutator = (%v, %q, %v), want the uninspectable worktree reported", changed, detail, err)
		}
	})
}

func TestTailCovIsCommandNotFoundRejectsNonExitErrors(t *testing.T) {
	if isCommandNotFound(errors.New("failed to start")) {
		t.Fatal("a launch failure must not be reported as command-not-found")
	}
	if isCommandNotFound(nil) {
		t.Fatal("nil must not be reported as command-not-found")
	}
}

func TestTailCovExpandPathLeavesTildeWhenHomeIsUnresolvable(t *testing.T) {
	t.Setenv("HOME", "")
	if got := expandPath("~/config.yaml"); got != "~/config.yaml" {
		t.Fatalf("expandPath(~/config.yaml) = %q, want it unchanged when the home directory cannot be resolved", got)
	}
	if got := expandPath("/absolute/config.yaml"); got != "/absolute/config.yaml" {
		t.Fatalf("expandPath(/absolute/config.yaml) = %q, want it unchanged", got)
	}
}

func TestTailCovEvaluateRejectsUnknownRecipeType(t *testing.T) {
	_, err := Evaluate(Recipe{Type: Kind("teleport")}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unknown recipe type") {
		t.Fatalf("err = %v, want the unknown type rejected", err)
	}
}

func TestTailCovEvaluateCommandRecipePreviewsDryRun(t *testing.T) {
	r := Recipe{Type: KindCommand, Command: "fix-it", DryRunCommand: "exit 1"}
	p, err := Evaluate(r, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !p.Changed || p.Summary != "run: fix-it" {
		t.Fatalf("preview = %+v, want Changed=true Summary=\"run: fix-it\"", p)
	}
}

func TestTailCovEvaluateTemplateSectionFailures(t *testing.T) {
	t.Run("template cannot be read", func(t *testing.T) {
		r := Recipe{Type: KindTemplateSection, Marker: "tailcov", Template: filepath.Join(t.TempDir(), "missing.md")}
		if _, err := Evaluate(r, t.TempDir()); err == nil || !strings.Contains(err.Error(), "read template") {
			t.Fatalf("err = %v, want the unreadable template reported", err)
		}
	})
	t.Run("fetch fails", func(t *testing.T) {
		r := writeTemplate(t, "tailcov", "block body")
		r.Target = "README.md"
		if _, err := Evaluate(r, t.TempDir()); err == nil {
			t.Fatal("a repository that cannot be fetched must fail rather than report a preview")
		}
	})
	// The default-branch lookup is only reachable once a fetch succeeds, so a
	// fake `git` answers the fetch and then fails both ways of resolving the
	// branch. This is the only way to reach that branch without a real remote.
	t.Run("default branch cannot be determined", func(t *testing.T) {
		r := writeTemplate(t, "tailcov", "block body")
		r.Target = "README.md"
		dir := t.TempDir()
		fake := filepath.Join(dir, "git")
		body := "#!/bin/sh\n" +
			"if [ \"$1\" = \"symbolic-ref\" ]; then exit 1; fi\n" +
			"if [ \"$1\" = \"remote\" ] && [ \"$2\" = \"show\" ]; then exit 1; fi\n" +
			"exit 0\n"
		if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		if _, err := Evaluate(r, t.TempDir()); err == nil || !strings.Contains(err.Error(), "remote show") {
			t.Fatalf("err = %v, want the failed default-branch lookup reported", err)
		}
	})
}

func TestTailCovLandRejectsUnknownRecipeType(t *testing.T) {
	_, err := Land(Recipe{Type: Kind("teleport")}, t.TempDir(), "main")
	if err == nil || !strings.Contains(err.Error(), "unknown recipe type") {
		t.Fatalf("err = %v, want the unknown type rejected", err)
	}
}

func TestTailCovLandReportsTemplateLoadFailure(t *testing.T) {
	r := Recipe{Type: KindTemplateSection, Marker: "tailcov", Template: filepath.Join(t.TempDir(), "missing.md")}
	_, err := Land(r, t.TempDir(), "main")
	if err == nil || !strings.Contains(err.Error(), "read template") {
		t.Fatalf("err = %v, want the unreadable template reported", err)
	}
}

func TestTailCovLandTemplateSectionMissingTargetIsASkip(t *testing.T) {
	clone := newRemoteRepo(t)
	r := writeTemplate(t, "tailcov", "block body")
	r.Name = "tailcov"
	r.Target = "ABSENT.md"
	if err := r.applyDefaults(); err != nil {
		t.Fatal(err)
	}
	outcome, err := Land(r, clone, "main")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Changed || outcome.Detail != "no ABSENT.md" {
		t.Fatalf("outcome = %+v, want an unchanged skip naming the missing target", outcome)
	}
}

func TestTailCovLoadTemplateRejectsVersionOverflow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "template.md")
	content := "<!-- tailcov:v99999999999999999999999999 -->\nbody\n<!-- /tailcov -->\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Recipe{Marker: "tailcov", Template: path}
	if _, err := r.loadTemplate(); err == nil || !strings.Contains(err.Error(), "invalid template version") {
		t.Fatalf("err = %v, want the out-of-range version reported", err)
	}
}
