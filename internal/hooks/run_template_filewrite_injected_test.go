package hooks

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR9 is task-9 PR-9's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally
// matches a different test's error by coincidence.
var errBoomPR9 = errors.New("pr9 boom")

func TestRunTemplateInjectedHonoursInjectedFailures(t *testing.T) {
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepWrite, filewrite.StepClose} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			policy := Policy{RepoRoot: initRepo(t)}
			layout := ExecutionLayout{Root: t.TempDir()}
			inj := &filewrite.Injector{Step: step, Err: errBoomPR9}
			exitCode, err := runTemplateInjected(policy, HookBlock{
				ID: "base/pre-commit", Profile: "base",
				Hook: ResolvedHook{Name: "pre-commit", Template: BuiltinPreCommit, Builtin: true},
			}, RunOptions{Hook: "pre-commit"}, eventContext{}, layout, inj)
			if exitCode != 2 || !errors.Is(err, errBoomPR9) {
				t.Fatalf("runTemplateInjected(%s failure) = (%d, %v), want (2, errBoomPR9)", step, exitCode, err)
			}
			matches, globErr := filepath.Glob(filepath.Join(tmp, "wb-hook-*.sh"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(matches) != 0 {
				t.Fatalf("leftover generated script(s) after an injected %s failure: %v", step, matches)
			}
		})
	}
}

func TestRunTemplateInjectedRunsAndCleansUpTheGeneratedScriptOnSuccess(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	policy := Policy{RepoRoot: initRepo(t)}
	layout := ExecutionLayout{Root: t.TempDir()}
	exitCode, err := runTemplateInjected(policy, HookBlock{
		ID: "base/pre-commit", Profile: "base",
		Hook: ResolvedHook{Name: "pre-commit", Template: BuiltinPreCommit, Builtin: true},
	}, RunOptions{Hook: "pre-commit"}, eventContext{}, layout, nil)
	if err != nil {
		t.Fatalf("runTemplateInjected(success) = (%d, %v), want no error", exitCode, err)
	}
	matches, globErr := filepath.Glob(filepath.Join(tmp, "wb-hook-*.sh"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover generated script(s) after a successful run: %v", matches)
	}
}
